package storage

import "time"

type Client struct {
	// writeTimeout bounds how long a single write to a destination may block.
	// It is refreshed on every write, so it limits lack of progress rather than
	// total duration.
	writeTimeout time.Duration

	// responseTimeout bounds how long a destination may take to start answering
	// once a request has been sent.
	responseTimeout time.Duration
}

func New(writeTimeout, responseTimeout time.Duration) *Client {
	return &Client{
		writeTimeout:    writeTimeout,
		responseTimeout: responseTimeout,
	}
}
