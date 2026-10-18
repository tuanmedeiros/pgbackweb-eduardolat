package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestS3Endpoint(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		wantValid bool
	}{
		{
			name:      "https endpoint",
			endpoint:  "https://s3.us-west-1.amazonaws.com",
			wantValid: true,
		},
		{
			name:      "http endpoint with port",
			endpoint:  "http://minio.local:9000",
			wantValid: true,
		},
		{
			name:      "uppercase scheme",
			endpoint:  "HTTPS://s3.us-east-005.backblazeb2.com",
			wantValid: true,
		},
		{
			name:      "host without scheme",
			endpoint:  "s3-us-west-1.amazonaws.com",
			wantValid: false,
		},
		{
			name:      "host and port without scheme",
			endpoint:  "minio.local:9000",
			wantValid: false,
		},
		{
			name:      "ip and port without scheme",
			endpoint:  "127.0.0.1:9000",
			wantValid: false,
		},
		{
			name:      "scheme without host",
			endpoint:  "https://",
			wantValid: false,
		},
		{
			name:      "other scheme",
			endpoint:  "ftp://s3.us-west-1.amazonaws.com",
			wantValid: false,
		},
		{
			name:      "leading space",
			endpoint:  " https://s3.us-west-1.amazonaws.com",
			wantValid: false,
		},
		{
			name:      "empty",
			endpoint:  "",
			wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := S3Endpoint(tt.endpoint)
			assert.Equal(t, tt.wantValid, got, "S3Endpoint(%q)", tt.endpoint)
		})
	}
}
