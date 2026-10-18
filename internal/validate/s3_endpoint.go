package validate

import (
	"net/url"
	"strings"
)

// S3Endpoint validates if endpoint is an http or https URL with a host. The S3
// client uses the endpoint as given, so one without a scheme can never connect.
func S3Endpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}

	return u.Host != ""
}
