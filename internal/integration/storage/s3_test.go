package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeMultiUploadFailure satisfies manager.MultiUploadFailure, the interface the
// SDK returns when a multipart upload leaves parts behind.
type fakeMultiUploadFailure struct {
	uploadID string
}

func (e *fakeMultiUploadFailure) Error() string    { return "upload multipart failed" }
func (e *fakeMultiUploadFailure) UploadID() string { return e.uploadID }

// recordingS3 captures the requests an abort would send.
type recordingS3 struct {
	mu       sync.Mutex
	requests []string
	server   *httptest.Server
}

func newRecordingS3(t *testing.T) *recordingS3 {
	t.Helper()

	r := &recordingS3{}
	r.server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, req *http.Request) {
			r.mu.Lock()
			r.requests = append(r.requests, req.Method+" "+req.URL.RequestURI())
			r.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		},
	))
	t.Cleanup(r.server.Close)
	return r
}

func (r *recordingS3) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.requests...)
}

// TestAbortMultipartUploadRunsOnCancelledContext is the whole point of the
// helper. The SDK aborts a failed multipart upload using the context the upload
// was given, so when that context was cancelled — exactly how a stalled backup
// is unwound — its own attempt fails instantly and the parts stay on the
// bucket, billable. The cleanup has to survive the cancellation.
func TestAbortMultipartUploadRunsOnCancelledContext(t *testing.T) {
	rec := newRecordingS3(t)

	s3Client, err := createS3Client("ak", "sk", "us-east-1", rec.server.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the upload was aborted by cancellation

	abortMultipartUpload(
		ctx, s3Client, "my-bucket", "backups/dump.zip",
		&fakeMultiUploadFailure{uploadID: "upload-123"},
	)

	got := rec.got()
	require.Len(t, got, 1, "expected exactly one abort request, got %v", got)
	require.Contains(t, got[0], "DELETE")
	require.Contains(t, got[0], "my-bucket/backups/dump.zip")
	require.Contains(t, got[0], "uploadId=upload-123")
}

// TestAbortMultipartUploadSkipsNonMultipartErrors keeps the cleanup quiet for
// failures that left nothing behind, such as bad credentials on a small upload
// that never became multipart.
func TestAbortMultipartUploadSkipsNonMultipartErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"plain error", errors.New("access denied")},
		{"multipart failure without an upload id", &fakeMultiUploadFailure{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecordingS3(t)

			s3Client, err := createS3Client("ak", "sk", "us-east-1", rec.server.URL)
			require.NoError(t, err)

			abortMultipartUpload(
				context.Background(), s3Client, "my-bucket", "k", tt.err,
			)

			require.Empty(t, rec.got(), "nothing should have been sent")
		})
	}
}

// TestAbortMultipartUploadFindsWrappedFailure covers the error travelling
// wrapped, which is how it reaches the caller in practice.
func TestAbortMultipartUploadFindsWrappedFailure(t *testing.T) {
	rec := newRecordingS3(t)

	s3Client, err := createS3Client("ak", "sk", "us-east-1", rec.server.URL)
	require.NoError(t, err)

	wrapped := errors.Join(
		errors.New("failed to upload file to S3"),
		&fakeMultiUploadFailure{uploadID: "upload-456"},
	)

	abortMultipartUpload(context.Background(), s3Client, "b", "k", wrapped)

	got := rec.got()
	require.Len(t, got, 1)
	require.Contains(t, got[0], "uploadId=upload-456")
}

// TestHTTPClientBoundsBlockedBodyWrite covers the destination that accepts the
// connection and then stops reading the request body.
//
// Nothing else catches this. ResponseHeaderTimeout starts counting only once the
// body has been fully written, and the stall watchdog on the dump has already
// stopped, because for any dump smaller than the uploader's buffer the whole
// thing is read before transmission even begins. Without a write deadline the
// backup hangs forever and its execution stays "running".
func TestHTTPClientBoundsBlockedBodyWrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan struct{})
	defer close(done)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Never read anything: the peer has stopped consuming the body.
		<-done
	}()

	client := newHTTPClient(300*time.Millisecond, 10*time.Second)

	// Large enough that the send and receive buffers cannot swallow it, so the
	// write has to block.
	body := bytes.NewReader(make([]byte, 64<<20))
	req, err := http.NewRequest(
		http.MethodPut, "http://"+ln.Addr().String()+"/dump.zip", body,
	)
	require.NoError(t, err)
	req.ContentLength = int64(body.Size())

	start := time.Now()
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	require.Error(t, err, "a blocked body write must fail rather than hang")
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.Less(
		t, time.Since(start), 30*time.Second,
		"the write deadline must fire promptly",
	)
}

// TestWriteDeadlineConnRefreshesEachWrite is the false positive to avoid: the
// deadline bounds lack of progress, not total duration, so a slow but moving
// transfer must survive well past the timeout.
func TestWriteDeadlineConnRefreshesEachWrite(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	const (
		timeout = 200 * time.Millisecond
		writes  = 6
		delay   = 150 * time.Millisecond // slow, but always progressing
	)

	go func() {
		buf := make([]byte, 8)
		for i := 0; i < writes; i++ {
			time.Sleep(delay)
			if _, err := io.ReadFull(server, buf); err != nil {
				return
			}
		}
	}()

	conn := &writeDeadlineConn{Conn: client, timeout: timeout}

	start := time.Now()
	for i := 0; i < writes; i++ {
		_, err := conn.Write([]byte("12345678"))
		require.NoError(t, err, "write %d of a slow but progressing transfer failed", i)
	}

	require.Greater(
		t, time.Since(start), timeout,
		"the test must run longer than the timeout for it to prove anything",
	)
}

// fakeS3 is a minimal multipart endpoint that fails part uploads and counts the
// aborts it receives.
type fakeS3 struct {
	server *httptest.Server
	aborts atomic.Int32

	// onPart runs once, when the first part upload arrives. It is how a test
	// interrupts an upload that is already under way.
	onPart   func()
	partOnce sync.Once
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()

	f := &fakeS3{}
	f.server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			switch {
			case r.Method == http.MethodPost && q.Has("uploads"):
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
					`<InitiateMultipartUploadResult><Bucket>b</Bucket><Key>k</Key>`+
					`<UploadId>upload-xyz</UploadId></InitiateMultipartUploadResult>`)

			case r.Method == http.MethodDelete && q.Has("uploadId"):
				f.aborts.Add(1)
				w.WriteHeader(http.StatusNoContent)

			case r.Method == http.MethodPut && q.Has("partNumber"):
				if f.onPart != nil {
					f.partOnce.Do(f.onPart)
				}
				// A non-retryable failure, so the upload gives up promptly.
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<?xml version="1.0"?><Error>`+
					`<Code>AccessDenied</Code><Message>denied</Message></Error>`)

			default:
				w.WriteHeader(http.StatusOK)
			}
		},
	))
	t.Cleanup(f.server.Close)
	return f
}

// upload runs a multipart upload large enough to be split into parts.
func (f *fakeS3) upload(ctx context.Context) error {
	_, err := Client{}.S3Upload(
		ctx, "ak", "sk", "us-east-1", f.server.URL, "b", "k",
		bytes.NewReader(make([]byte, 12<<20)),
	)
	return err
}

// TestS3UploadAbortsMultipartExactlyOnce pins down ownership of the cleanup.
//
// The SDK aborts a failed multipart upload itself, so leaving that enabled and
// also aborting here sent two requests: S3 answers the second with NoSuchUpload,
// which would be logged as a cleanup failure that never happened, on every
// ordinary failed backup. The SDK's attempt is turned off instead, because it
// reuses the upload's context and so does nothing at all when the failure was a
// cancellation.
func TestS3UploadAbortsMultipartExactlyOnce(t *testing.T) {
	f := newFakeS3(t)

	err := f.upload(context.Background())

	require.Error(t, err)
	require.EqualValues(
		t, 1, f.aborts.Load(),
		"a failed multipart upload must be aborted exactly once",
	)
}

// TestS3UploadAbortsMultipartAfterCancel is the case the SDK cannot handle. The
// upload is interrupted once it is already under way -- how a stalled backup is
// unwound -- so the SDK's own abort would run on the very context that was just
// cancelled, fail instantly, and leave the parts on the bucket.
func TestS3UploadAbortsMultipartAfterCancel(t *testing.T) {
	f := newFakeS3(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.onPart = cancel // interrupt once the multipart upload exists

	err := f.upload(ctx)

	require.Error(t, err)
	require.EqualValues(
		t, 1, f.aborts.Load(),
		"an interrupted upload must still have its parts cleaned up",
	)
}

// TestS3UploadSkipsAbortWhenNothingWasCreated checks the other direction: if the
// upload never got as far as creating a multipart upload, there is nothing to
// clean up and no request should be sent.
func TestS3UploadSkipsAbortWhenNothingWasCreated(t *testing.T) {
	f := newFakeS3(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := f.upload(ctx)

	require.Error(t, err)
	require.EqualValues(
		t, 0, f.aborts.Load(),
		"nothing was created, so nothing should be aborted",
	)
}
