package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
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
func createS3Client(
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

	// The SDK bounds connecting and the TLS handshake, but nothing after that:
	// a destination that accepts the upload and then never answers would hang
	// forever. This bounds only the wait for response headers, which starts
	// once the request body has been sent, so a slow but progressing upload is
	// unaffected however long it takes.
	httpClient := awshttp.NewBuildableClient().WithTransportOptions(
		func(tr *http.Transport) {
			tr.ResponseHeaderTimeout = responseHeaderTimeout
		},
	)

	//nolint:all
	conf, err := config.LoadDefaultConfig(
		context.TODO(),
		config.WithRegion(region),
		config.WithEndpointResolver(endpointResolver),
		config.WithCredentialsProvider(credentialsProvider),
		config.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("error initializing storage config: %w", err)
	}

	s3Client := s3.NewFromConfig(conf)
	return s3Client, nil
}

// S3Test tests the connection to S3
func (Client) S3Test(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName string,
) error {
	s3Client, err := createS3Client(
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

const (
	// abortTimeout bounds the cleanup of a failed multipart upload.
	abortTimeout = 30 * time.Second

	// responseHeaderTimeout bounds how long a destination may take to start
	// answering once a request body has been sent. It is generous because
	// completing a large multipart upload legitimately takes a while.
	responseHeaderTimeout = 5 * time.Minute
)

// abortMultipartUpload removes the parts left behind by a failed multipart
// upload.
//
// The SDK already tries this on failure, but it reuses the context the upload
// was given and discards the result. When the upload failed *because* that
// context was cancelled — which is how a stalled backup is unwound — the
// SDK's attempt fails instantly and silently, and the parts already uploaded
// stay on the bucket, billable, until a lifecycle rule removes them.
//
// So the abort is retried here on a context that is deliberately detached from
// the caller's.
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
func (Client) S3Upload(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName, key string,
	fileReader io.Reader,
) (int64, error) {
	s3Client, err := createS3Client(
		accessKey, secretKey, region, endpoint,
	)
	if err != nil {
		return 0, err
	}

	key = strutil.RemoveLeadingSlash(key)
	contentType := strutil.GetContentTypeFromFileName(key)

	uploader := manager.NewUploader(s3Client)
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
func (Client) S3Delete(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName, key string,
) error {
	s3Client, err := createS3Client(
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
func (Client) S3GetDownloadLink(
	ctx context.Context,
	accessKey, secretKey, region, endpoint, bucketName, key string,
	expiration time.Duration,
) (string, error) {
	s3Client, err := createS3Client(
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
