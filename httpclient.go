package main

import (
	"context"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// Simple HTTP client with retries. No TLS fiddling—Nginx terminates HTTPS/mTLS.
func buildHTTPClient() *retryablehttp.Client {
	std := &http.Client{
		Timeout: 10 * time.Second,
	}

	rc := retryablehttp.NewClient()
	rc.RetryMax = 3
	rc.RetryWaitMin = 500 * time.Millisecond
	rc.RetryWaitMax = 2 * time.Second
	rc.Backoff = retryablehttp.DefaultBackoff
	rc.Logger = nil
	rc.HTTPClient = std
	rc.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if err != nil {
			return true, nil
		}
		if resp == nil {
			return false, nil
		}
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusInternalServerError,
			http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true, nil
		default:
			return false, nil
		}
	}
	return rc
}
