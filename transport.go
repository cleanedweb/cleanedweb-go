package cleanedweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxErrorBody   = 64 << 10
	minPoll        = time.Second
	maxPoll        = 60 * time.Second
	defaultPoll    = 30 * time.Second
	defaultWait    = 90 // seconds a run request is held by the server
	waitHeadroom   = 30 * time.Second
	resultReadTime = 120 * time.Second
)

// request describes one HTTP call.
type request struct {
	method string
	url    string
	query  url.Values
	body   any // JSON-encoded when not nil
	auth   bool
	key    string // Idempotency-Key header
	// timeout bounds one attempt; zero means the client default, negative means none (a stream).
	timeout time.Duration
}

// response is a completed (2xx) answer.
type response struct {
	status int
	header http.Header
	body   []byte
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

func (c *Client) newHTTPRequest(ctx context.Context, r *request, payload []byte) (*http.Request, error) {
	u := r.url
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		return nil, invalidArgument("bad request URL: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.auth {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if r.key != "" {
		req.Header.Set("Idempotency-Key", r.key)
	}
	return req, nil
}

// open sends the request, retrying 429, 502, 503 and 504 answers with the same idempotency key and
// body, and returns the open response of a 2xx answer (the caller closes the body and calls cancel).
// Any other answer is returned as an *APIError, a transport failure as a *ConnectionError.
func (c *Client) open(ctx context.Context, r *request) (*http.Response, context.CancelFunc, error) {
	if r.auth && c.apiKey == "" {
		return nil, nil, &MissingAPIKeyError{}
	}
	var payload []byte
	if r.body != nil {
		var err error
		if payload, err = json.Marshal(r.body); err != nil {
			return nil, nil, invalidArgument("cannot encode the request body: %v", err)
		}
	}
	timeout := r.timeout
	if timeout == 0 {
		timeout = c.requestTimeout
	}
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		req, err := c.newHTTPRequest(attemptCtx, r, payload)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, &ConnectionError{Err: ctxErr, IdempotencyKey: r.key}
			}
			return nil, nil, &ConnectionError{Err: err, IdempotencyKey: r.key}
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, cancel, nil
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		resp.Body.Close()
		cancel()
		if readErr != nil && len(data) == 0 {
			return nil, nil, &ConnectionError{Err: readErr, IdempotencyKey: r.key}
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			e := &APIError{Kind: KindRedirect, StatusCode: resp.StatusCode, Header: resp.Header, Body: data, IdempotencyKey: r.key}
			return nil, nil, e
		}
		apiErr := newAPIError(resp.StatusCode, data, resp.Header, r.key)
		if retryableStatus(resp.StatusCode) && attempt < c.maxRetries && !apiErr.RunFailed() {
			if delay, ok := c.retryDelay(attempt, apiErr); ok {
				if err := c.sleep(ctx, delay); err != nil {
					return nil, nil, &ConnectionError{Err: err, IdempotencyKey: r.key}
				}
				continue
			}
		}
		return nil, nil, apiErr
	}
}

// retryDelay is how long to wait before retrying, or false when the server asks for longer than
// the cap.
func (c *Client) retryDelay(attempt int, e *APIError) (time.Duration, bool) {
	if e.hasRetryAfter {
		return e.RetryAfter, e.RetryAfter <= c.maxRetryDelay
	}
	d := 500 * time.Millisecond << uint(attempt)
	if d > c.maxRetryDelay {
		d = c.maxRetryDelay
	}
	return d, true
}

// do sends the request and reads the whole answer.
func (c *Client) do(ctx context.Context, r *request) (*response, error) {
	resp, cancel, err := c.open(ctx, r)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &ConnectionError{Err: err, IdempotencyKey: r.key}
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func decodeJSON(data []byte, out any) error {
	if err := json.Unmarshal(data, out); err != nil {
		return &APIError{Kind: KindAPIStatus, StatusCode: http.StatusOK, Message: "the answer is not the expected JSON: " + err.Error(), Body: truncateBytes(data, 1024)}
	}
	return nil
}

func truncateBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

func (c *Client) url(path string) string { return c.baseURL + path }

// seg percent-encodes one path segment.
func seg(v string) string { return strings.ReplaceAll(url.PathEscape(v), "/", "%2F") }

// runTimeout leaves room for the server holding a run request for its wait_seconds.
func (c *Client) runTimeout(waitSeconds *int) time.Duration {
	held := defaultWait
	if waitSeconds != nil {
		held = *waitSeconds
	}
	t := time.Duration(held)*time.Second + waitHeadroom
	if t < c.requestTimeout {
		t = c.requestTimeout
	}
	return t
}

// pollDelay is the wait before reading a running run again: Retry-After, else the run's
// poll_after_seconds, else 30s, clamped to 1s..60s.
func pollDelay(retryAfter time.Duration, hasRetryAfter bool, pollAfter float64, hasPollAfter bool) time.Duration {
	d := defaultPoll
	switch {
	case hasRetryAfter:
		d = retryAfter
	case hasPollAfter:
		d = seconds(pollAfter)
	}
	if d < minPoll {
		return minPoll
	}
	if d > maxPoll {
		return maxPoll
	}
	return d
}
