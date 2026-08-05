package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

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
