package streamutil

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// blockingReader blocks inside Read until it is released, standing in for a
// pg_dump that is slow to produce data.
type blockingReader struct {
	release chan struct{}
}

func (r *blockingReader) Read(p []byte) (int, error) {
	<-r.release
	return 0, io.EOF
}

func TestStallReaderPassesDataThrough(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))

	s := NewStallReader(src, time.Minute, func() { t.Error("should not stall") })
	defer s.Stop()

	out, err := io.ReadAll(s)
	require.NoError(t, err)
	require.Equal(t, "hello world", string(out))
	require.False(t, s.Stalled())
}

// TestStallReaderFiresWhenConsumerGoesQuiet covers the backup whose upload hangs:
// the consumer stops reading, so pg_dump would otherwise block forever on a full
// pipe, holding a transaction open on the source database.
func TestStallReaderFiresWhenConsumerGoesQuiet(t *testing.T) {
	src := bytes.NewReader(bytes.Repeat([]byte("x"), 1024))

	var wg sync.WaitGroup
	wg.Add(1)
	s := NewStallReader(src, 200*time.Millisecond, wg.Done)
	defer s.Stop()

	// Read once, then walk away, exactly as a stuck upload would.
	buf := make([]byte, 8)
	_, err := s.Read(buf)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("onStall was never called for an idle consumer")
	}

	require.True(t, s.Stalled())

	_, err = s.Read(buf)
	require.ErrorContains(t, err, "stream stalled")
}

// TestStallReaderIgnoresSlowProducer is the false positive that would abort
// healthy backups: a consumer blocked inside Read is waiting on a slow dump,
// which is not a stall.
func TestStallReaderIgnoresSlowProducer(t *testing.T) {
	blocking := &blockingReader{release: make(chan struct{})}

	stalled := make(chan struct{})
	s := NewStallReader(blocking, 200*time.Millisecond, func() { close(stalled) })
	defer s.Stop()

	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 8)
		_, _ = s.Read(buf)
		close(readDone)
	}()

	// Hold the read open well past the timeout.
	select {
	case <-stalled:
		t.Fatal("a consumer blocked inside Read must not count as stalled")
	case <-time.After(1 * time.Second):
	}

	close(blocking.release)
	<-readDone
	require.False(t, s.Stalled())
}

func TestStallReaderStopEndsWatchdog(t *testing.T) {
	src := bytes.NewReader([]byte("data"))

	s := NewStallReader(src, 100*time.Millisecond, func() {
		t.Error("onStall must not fire after Stop")
	})

	s.Stop()
	s.Stop() // idempotent

	time.Sleep(500 * time.Millisecond)
	require.False(t, s.Stalled())
}

// TestStallReaderStopsWatchingAtEOF is the false positive that would break real
// backups. An S3 uploader buffers roughly 50 MiB (5 MiB parts, concurrency 5,
// plus a channel of the same depth), so it reads any modest dump to the end long
// before it finishes transmitting, and then issues no further reads while the
// queued parts go out.
//
// Watching past EOF would cancel those healthy transfers. It is also pointless:
// EOF means the producer already exited, so nothing remains to protect.
func TestStallReaderStopsWatchingAtEOF(t *testing.T) {
	src := bytes.NewReader([]byte("a whole small dump"))

	stalled := make(chan struct{})
	s := NewStallReader(src, 100*time.Millisecond, func() { close(stalled) })
	defer s.Stop()

	out, err := io.ReadAll(s) // drains to EOF, like the uploader buffering
	require.NoError(t, err)
	require.Equal(t, "a whole small dump", string(out))

	// Now spend far longer than the timeout "transmitting" without reading.
	select {
	case <-stalled:
		t.Fatal("a finished stream must not be reported as stalled")
	case <-time.After(1 * time.Second):
	}

	require.False(t, s.Stalled())
}

// errReader fails after its first read, standing in for a dump that dies.
type errReader struct{ reads int }

func (r *errReader) Read(p []byte) (int, error) {
	r.reads++
	if r.reads == 1 {
		p[0] = 'x'
		return 1, nil
	}
	return 0, errors.New("pg_dump failed")
}

// TestStallReaderStopsWatchingAfterError applies the same reasoning to a failed
// dump: the error is already travelling to the caller, so the watchdog has
// nothing left to add.
func TestStallReaderStopsWatchingAfterError(t *testing.T) {
	s := NewStallReader(&errReader{}, 100*time.Millisecond, func() {
		t.Error("a failed stream must not also be reported as stalled")
	})
	defer s.Stop()

	_, err := io.ReadAll(s)
	require.ErrorContains(t, err, "pg_dump failed")

	time.Sleep(1 * time.Second)
	require.False(t, s.Stalled())
}
