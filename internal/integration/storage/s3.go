package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/eduardolat/pgbackweb/internal/logger"
	"github.com/eduardolat/pgbackweb/internal/util/strutil"
)

// createS3Client creates a new S3 client
func (c *Client) createS3Client(
	accessKey, secretKey, region, endpoint string,
) (*s3.Client, error) {
	credentialsProvider := credentials.NewStaticCredentialsProvider(
		accessKey, secretKey, "",
	)

	//nolint:all
	endpointResolver := aws.EndpointResolverFunc(func(
		_ string, _ string,
	) (aws.Endpoint, error) {
		return aws.Endpoint{
			HostnameImmutable: true,
			URL:               endpoint,
		}, nil
	})

	//nolint:all
	conf, err := config.LoadDefaultConfig(
		context.TODO(),
		config.WithRegion(region),
		config.WithEndpointResolver(endpointResolver),
		config.WithCredentialsProvider(credentialsProvider),
		config.WithHTTPClient(
			newHTTPClient(c.writeTimeout, c.responseTimeout),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("error initializing storage config: %w", err)
	}

	s3Client := s3.NewFromConfig(conf)
	return s3Client, nil
}

// S3Test tests the connection to S3
func (c *Client) S3Test(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName string,
) error {
	s3Client, err := c.createS3Client(
		accessKey, secretKey, region, endpoint,
	)
	if err != nil {
		return err
	}

	_, err = s3Client.HeadBucket(
		ctx,
		&s3.HeadBucketInput{
			Bucket: aws.String(bucketName),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to test S3 bucket: %w", err)
	}

	return nil
}

// abortTimeout bounds the cleanup of a failed multipart upload. It is not
// configurable: it guards a single small request, made on a context detached
// from the caller's, so there is nothing for an operator to tune.
const abortTimeout = 30 * time.Second

// writeDeadlineConn bounds how long any single write to the peer may block.
//
// This is the only thing that catches a destination which stops reading the
// request body: the socket buffers fill, the write blocks, and no other timeout
// applies. ResponseHeaderTimeout does not, because it starts counting only once
// the body has been fully written, and the stall watchdog on the dump does not
// either, because by then the uploader may have buffered the whole dump and
// stopped reading from it.
type writeDeadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *writeDeadlineConn) Write(b []byte) (int, error) {
	// Refreshed per write, so this measures stalled progress, not total time.
	if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

// newHTTPClient builds the HTTP client used for every S3 request.
//
// The SDK bounds connecting and the TLS handshake, but nothing after that, and
// it sets no overall request timeout. Left alone, a destination that accepts
// the connection and then stops making progress hangs the backup forever.
func newHTTPClient(
	writeTimeout, respHeaderTimeout time.Duration,
) *awshttp.BuildableClient {
	return awshttp.NewBuildableClient().WithTransportOptions(
		func(tr *http.Transport) {
			tr.ResponseHeaderTimeout = respHeaderTimeout

			// Wrap the SDK's own dialer rather than replacing it, so its
			// tracing and dial timeouts are kept.
			dial := tr.DialContext
			tr.DialContext = func(
				ctx context.Context, network, addr string,
			) (net.Conn, error) {
				conn, err := dial(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &writeDeadlineConn{Conn: conn, timeout: writeTimeout}, nil
			}
		},
	)
}

// abortMultipartUpload removes the parts left behind by a failed multipart
// upload. It is the only thing that does so — the uploader is configured with
// LeavePartsOnError, which turns off the SDK's own attempt.
//
// That attempt cannot be relied on: it reuses the context the upload was given,
// so when the failure *was* a cancelled context — which is how a stalled backup
// is unwound — it fails instantly, and its error is discarded. The parts would
// stay on the bucket, billable, until a lifecycle rule removed them.
//
// Doing it here instead means one abort, always on a context that still works,
// and a real error when it fails.
func abortMultipartUpload(
	ctx context.Context, s3Client *s3.Client, bucketName, key string,
	uploadErr error,
) {
	// Only a multipart upload leaves anything behind, and only it knows the
	// upload ID needed to clean up.
	var failure manager.MultiUploadFailure
	if !errors.As(uploadErr, &failure) || failure.UploadID() == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()

	_, err := s3Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucketName),
		Key:      aws.String(key),
		UploadId: aws.String(failure.UploadID()),
	})
	if err != nil {
		logger.Error("failed to abort incomplete multipart upload", logger.KV{
			"bucket":    bucketName,
			"key":       key,
			"upload_id": failure.UploadID(),
			"error":     err.Error(),
		})
	}
}

// S3Upload uploads a file to S3 from a reader.
//
// Returns the file size, in bytes.
func (c *Client) S3Upload(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName, key string,
	fileReader io.Reader,
) (int64, error) {
	s3Client, err := c.createS3Client(
		accessKey, secretKey, region, endpoint,
	)
	if err != nil {
		return 0, err
	}

	key = strutil.RemoveLeadingSlash(key)
	contentType := strutil.GetContentTypeFromFileName(key)

	uploader := manager.NewUploader(s3Client, func(u *manager.Uploader) {
		// Take sole ownership of cleaning up a failed multipart upload. The
		// SDK's own attempt reuses the upload's context, so it silently does
		// nothing when the failure *was* a cancelled context — and when it does
		// work, a second abort here would draw a NoSuchUpload and be logged as a
		// cleanup failure that never happened.
		u.LeavePartsOnError = true
	})
	_, err = uploader.Upload(
		ctx,
		&s3.PutObjectInput{
			Bucket:      aws.String(bucketName),
			Key:         aws.String(key),
			Body:        fileReader,
			ContentType: aws.String(contentType),
		},
	)
	if err != nil {
		abortMultipartUpload(ctx, s3Client, bucketName, key, err)
		return 0, fmt.Errorf("failed to upload file to S3: %w", err)
	}

	fileHead, err := s3Client.HeadObject(
		ctx,
		&s3.HeadObjectInput{
			Bucket: aws.String(bucketName),
			Key:    aws.String(key),
		},
	)
	if err != nil {
		return 0, fmt.Errorf("failed to get uploaded file info from S3: %w", err)
	}

	var fileSize int64
	if fileHead.ContentLength != nil {
		fileSize = *fileHead.ContentLength
	}

	return fileSize, nil
}

// S3Delete deletes a file from S3
func (c *Client) S3Delete(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName, key string,
) error {
	s3Client, err := c.createS3Client(
		accessKey, secretKey, region, endpoint,
	)
	if err != nil {
		return err
	}

	key = strutil.RemoveLeadingSlash(key)

	_, err = s3Client.DeleteObject(
		ctx,
		&s3.DeleteObjectInput{
			Bucket: aws.String(bucketName),
			Key:    aws.String(key),
		},
	)
	if err != nil {
		return fmt.Errorf("failed to delete file from S3: %w", err)
	}

	return nil
}

// S3GetDownloadLink generates a presigned URL for downloading a file from S3
func (c *Client) S3GetDownloadLink(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName, key string,
	expiration time.Duration,
) (string, error) {
	s3Client, err := c.createS3Client(
		accessKey, secretKey, region, endpoint,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create S3 client: %w", err)
	}

	presigned, err := s3.NewPresignClient(s3Client).PresignGetObject(
		ctx,
		&s3.GetObjectInput{
			Bucket: aws.String(bucketName),
			Key:    aws.String(key),
		},
		s3.WithPresignExpires(expiration),
	)
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presigned.URL, nil
}
