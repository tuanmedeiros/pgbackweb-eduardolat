package streamutil

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// StallReader wraps a reader and gives up when the consumer stops asking for
// data altogether.
//
// It exists for backups whose upload hangs instead of failing. A pg_dump feeds
// its output through a pipe, and a consumer stuck writing to a remote endpoint
// never drains it, so pg_dump blocks forever holding an open transaction on the
// source database. Nothing downstream ever returns, so no amount of deferred
// cleanup runs.
//
// A stall means "the consumer is not asking for data". A consumer that is
// blocked *inside* Read is waiting on the producer instead, which is a slow
// dump rather than a stuck upload, and is deliberately not treated as a stall.
type StallReader struct {
	reader  io.Reader
	timeout time.Duration
	onStall func()

	mu       sync.Mutex
	inFlight int
	lastRead time.Time
	stalled  bool

	stopOnce sync.Once
	stop     chan struct{}
}

// NewStallReader returns a reader that calls onStall, once, if the consumer
// goes timeout without asking for data, and fails every read from then on.
//
// Call Stop when done to release the watchdog.
func NewStallReader(
	reader io.Reader, timeout time.Duration, onStall func(),
) *StallReader {
	s := &StallReader{
		reader:   reader,
		timeout:  timeout,
		onStall:  onStall,
		lastRead: time.Now(),
		stop:     make(chan struct{}),
	}

	go s.watch()
	return s
}

// watch polls for a consumer that has gone quiet. It polls rather than resetting
// a timer on every Read so that a fast dump doesn't pay for timer churn.
func (s *StallReader) watch() {
	// Check often enough to detect a stall promptly without busy work.
	interval := s.timeout / 10
	if interval < time.Second {
		interval = time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			if s.checkStalled() {
				s.onStall()
				return
			}
		}
	}
}

// checkStalled reports whether the consumer has gone quiet for too long, and
// latches the stalled state if so.
func (s *StallReader) checkStalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stalled {
		return false
	}
	// A read in flight means the consumer is waiting on the producer, not the
	// other way around.
	if s.inFlight > 0 || time.Since(s.lastRead) < s.timeout {
		return false
	}

	s.stalled = true
	return true
}

func (s *StallReader) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.stalled {
		s.mu.Unlock()
		return 0, s.err()
	}
	s.inFlight++
	s.mu.Unlock()

	n, err := s.reader.Read(p)

	s.mu.Lock()
	s.inFlight--
	s.lastRead = time.Now()
	stalled := s.stalled
	s.mu.Unlock()

	if stalled {
		return 0, s.err()
	}
	return n, err
}

func (s *StallReader) err() error {
	return fmt.Errorf(
		"stream stalled: no data was consumed for %s", s.timeout,
	)
}

// Stalled reports whether the reader gave up.
func (s *StallReader) Stalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stalled
}

// Stop releases the watchdog. It is safe to call more than once.
func (s *StallReader) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}
