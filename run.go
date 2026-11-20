package cleanedweb

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// runOptions are the options common to every run request.
type runOptions struct {
	input          map[string]any
	maxItems       *int
	timeoutSeconds *int
	waitSeconds    *int
	noWait         bool
	fields         []string
	nulls          *bool
	key            string
	pollTimeout    time.Duration
	extra          map[string]any
}

// effectiveWait: NoWait asks the server not to hold the request, so it answers 202 right away.
func (o *runOptions) effectiveWait() *int {
	if o.noWait && o.waitSeconds == nil {
		zero := 0
		return &zero
	}
	return o.waitSeconds
}

func (o *runOptions) body() (map[string]any, error) {
	if o.maxItems != nil && *o.maxItems < 1 {
		return nil, invalidArgument("MaxItems must be an integer >= 1")
	}
	input := o.input
	if input == nil {
		input = map[string]any{}
	}
	body := map[string]any{"input": input}
	if o.maxItems != nil {
		body["max_items"] = *o.maxItems
	}
	if o.timeoutSeconds != nil {
		body["timeout_seconds"] = *o.timeoutSeconds
	}
	if w := o.effectiveWait(); w != nil {
		body["wait_seconds"] = *w
	}
	if o.fields != nil {
		body["fields"] = o.fields
	}
	if o.nulls != nil {
		body["nulls"] = *o.nulls
	}
	for k, v := range o.extra {
		body[k] = v
	}
	return body, nil
}

// ResultParams control how a result is read.
type ResultParams struct {
	// NoWait returns the first answer even if the run is still going (an incomplete RunResult).
	NoWait bool
	// PollTimeout stops waiting after this long and returns the incomplete result. Zero waits
	// until the run ends or ctx is done.
	PollTimeout time.Duration
	// Nulls sends every published field, with absent values as null (?nulls=true).
	Nulls bool
}

func (p *ResultParams) get() ResultParams {
	if p == nil {
		return ResultParams{}
	}
	return *p
}

func (p ResultParams) query() url.Values {
	if p.Nulls {
		return url.Values{"nulls": {"true"}}
	}
	return nil
}

// startRun posts a run request and, unless noWait, follows a 202 with fetch until the run ends.
// It never sends the POST again.
func (c *Client) startRun(ctx context.Context, path string, o *runOptions, fetch func(ctx context.Context, res *RunResult) (*RunResult, error)) (*RunResult, error) {
	key, err := keyOrNew(o.key)
	if err != nil {
		return nil, err
	}
	body, err := o.body()
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, &request{method: http.MethodPost, url: c.url(path), body: body, auth: true, key: key, timeout: c.runTimeout(o.effectiveWait())})
	if err != nil {
		return nil, err
	}
	res, err := newRunResult(resp.status, resp.body, resp.header, key)
	if err != nil {
		return nil, err
	}
	if res.Complete || o.noWait {
		return res, nil
	}
	return c.poll(ctx, res, func(ctx context.Context) (*RunResult, error) {
		next, err := fetch(ctx, res)
		if next != nil {
			next.IdempotencyKey = key // every answer of this run carries its key
		}
		return next, err
	}, o.pollTimeout)
}

// readResult reads a result route once, then polls it unless NoWait.
func (c *Client) readResult(ctx context.Context, path, key string, p ResultParams) (*RunResult, error) {
	fetch := func(ctx context.Context) (*RunResult, error) {
		resp, err := c.do(ctx, &request{method: http.MethodGet, url: c.url(path), query: p.query(), auth: true, key: key, timeout: resultReadTime})
		if err != nil {
			return nil, err
		}
		return newRunResult(resp.status, resp.body, resp.header, key)
	}
	res, err := fetch(ctx)
	if err != nil {
		return nil, err
	}
	if res.Complete || p.NoWait {
		return res, nil
	}
	return c.poll(ctx, res, fetch, p.PollTimeout)
}

// poll reads fetch until the run ends. It honours Retry-After and never resends a run request. A
// 409 result_not_available, result_not_complete or result_not_running means "not ready yet".
func (c *Client) poll(ctx context.Context, res *RunResult, fetch func(ctx context.Context) (*RunResult, error), timeout time.Duration) (*RunResult, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for !res.Complete {
		delay := res.pollDelay()
		if !deadline.IsZero() && time.Now().Add(delay).After(deadline) {
			return res, nil
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, &ConnectionError{Err: err, IdempotencyKey: res.IdempotencyKey}
		}
		next, err := fetch(ctx)
		if err != nil {
			if apiErr, ok := err.(*APIError); ok && apiErr.pendingResult() {
				res = &RunResult{Complete: false, StatusCode: http.StatusAccepted, IdempotencyKey: res.IdempotencyKey, Run: res.Run,
					RetryAfter: apiErr.RetryAfter, hasRetryAfter: apiErr.hasRetryAfter}
				continue
			}
			return nil, err
		}
		res = next
	}
	return res, nil
}
