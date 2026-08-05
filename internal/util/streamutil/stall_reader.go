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
// A stall means "the consumer is not asking for data". Two cases look like that
// without being one, and neither is treated as a stall:
//
//   - A consumer blocked *inside* Read is waiting on the producer, which is a
//     slow dump rather than a stuck upload.
//   - A consumer that has read to the end has nothing left to ask for. Watching
//     past that point is not just useless but harmful: an S3 uploader buffers
//     tens of megabytes, so it reaches the end of a modest dump long before it
//     finishes transmitting, and cancelling then would kill a healthy backup.
//
// The second case is also why watching can stop at the end of the stream: once
// the producer has signalled EOF it has exited, so the resources this exists to
// protect are already released.
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

	// The stream is finished, so the producer is gone and there is nothing left
	// to guard. Stopping here is what keeps a long, healthy transfer of what
	// was already buffered from being mistaken for a stall.
	if err != nil {
		s.Stop()
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
