package cleanedweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// RunsService reads, waits for, aborts and lists runs. Reading a run never collects the website
// again. Run and items reads work with the run or results scope; reading a result needs the
// results scope; abort needs run.
type RunsService struct{ c *Client }

func runPath(runID string) string { return "/v1/runs/" + seg(runID) }

// Get reads the run object.
func (s *RunsService) Get(ctx context.Context, runID string) (*Run, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url(runPath(runID)), auth: true})
	if err != nil {
		return nil, err
	}
	return decodeRun(resp)
}

func decodeRun(resp *response) (*Run, error) {
	var run Run
	if err := decodeJSON(resp.body, &run); err != nil {
		return nil, err
	}
	run.Replayed = resp.header.Get("Idempotent-Replayed") == "true"
	return &run, nil
}

// ItemsParams control Runs.Items and Runs.ItemsPage.
type ItemsParams struct {
	// Cursor is where to start; empty starts at the first record.
	Cursor string
	// Limit is the page size (default 100).
	Limit int
	// Follow keeps waiting for new records until the run ended and every record was read.
	// Otherwise Items stops at the first empty page.
	Follow bool
	// PollInterval is the wait between reads when Follow is set (default 2s).
	PollInterval time.Duration
	// Nulls sends every published field, with absent values as null.
	Nulls bool
}

func (p *ItemsParams) get() ItemsParams {
	var ip ItemsParams
	if p != nil {
		ip = *p
	}
	if ip.Cursor == "" {
		ip.Cursor = "0"
	}
	if ip.Limit <= 0 {
		ip.Limit = 100
	}
	if ip.PollInterval <= 0 {
		ip.PollInterval = 2 * time.Second
	}
	return ip
}

// ItemsPage reads one page of the records committed so far. It is readable while the run runs.
func (s *RunsService) ItemsPage(ctx context.Context, runID string, p *ItemsParams) (*RunItemsPage, error) {
	ip := p.get()
	q := url.Values{"cursor": {ip.Cursor}, "limit": {strconv.Itoa(ip.Limit)}}
	if ip.Nulls {
		q.Set("nulls", "true")
	}
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url(runPath(runID) + "/items"), query: q, auth: true})
	if err != nil {
		return nil, err
	}
	return newRunItemsPage(resp.body)
}

// Items iterates the records committed so far, following next_cursor page by page. It is
// readable while the run runs.
func (s *RunsService) Items(runID string, p *ItemsParams) *Iterator[Record] {
	ip := p.get()
	return newIterator(func(ctx context.Context) ([]Record, bool, error) {
		for {
			page, err := s.ItemsPage(ctx, runID, &ip)
			if err != nil {
				return nil, false, err
			}
			if page.NextCursor == "" {
				return page.Items, true, nil
			}
			ip.Cursor = page.NextCursor
			if len(page.Items) > 0 {
				return page.Items, false, nil
			}
			if !ip.Follow {
				return nil, true, nil
			}
			if err := s.c.sleep(ctx, ip.PollInterval); err != nil {
				return nil, false, err
			}
		}
	})
}

// Result reads the final body of a run: complete once the run ended, or, while it runs, polled
// every Retry-After seconds until it ends (unless NoWait). A failed run returns its error. Needs
// the results scope.
func (s *RunsService) Result(ctx context.Context, runID string, p *ResultParams) (*RunResult, error) {
	return s.c.readResult(ctx, runPath(runID)+"/result", "", p.get())
}

// Abort stops a run: a queued run ends now, a running run at its next page boundary. Records
// committed so far stay readable and charged. Needs the run scope.
func (s *RunsService) Abort(ctx context.Context, runID string) (*Run, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodPost, url: s.c.url(runPath(runID) + "/abort"), auth: true})
	if err != nil {
		return nil, err
	}
	return decodeRun(resp)
}

// RunListParams filter the run list.
type RunListParams struct {
	SourceID string
	Status   RunStatus
	// Cursor is the NextCursor of the previous page.
	Cursor string
	// Limit is the page size (default 50).
	Limit int
}

func (p *RunListParams) query() url.Values {
	var rp RunListParams
	if p != nil {
		rp = *p
	}
	if rp.Limit <= 0 {
		rp.Limit = 50
	}
	q := url.Values{"limit": {strconv.Itoa(rp.Limit)}}
	if rp.SourceID != "" {
		q.Set("source_id", rp.SourceID)
	}
	if rp.Status != "" {
		q.Set("status", string(rp.Status))
	}
	if rp.Cursor != "" {
		q.Set("cursor", rp.Cursor)
	}
	return q
}

// List reads one page of your runs, newest first.
func (s *RunsService) List(ctx context.Context, p *RunListParams) (*RunList, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url("/v1/runs"), query: p.query(), auth: true})
	if err != nil {
		return nil, err
	}
	var page struct {
		Runs       []json.RawMessage `json:"runs"`
		NextCursor *string           `json:"next_cursor"`
	}
	if err := decodeJSON(resp.body, &page); err != nil {
		return nil, err
	}
	out := &RunList{}
	_ = decodeNumbers(resp.body, &out.Raw)
	for _, raw := range page.Runs {
		var run Run
		if err := decodeJSON(raw, &run); err != nil {
			return nil, err
		}
		out.Runs = append(out.Runs, run)
	}
	if page.NextCursor != nil {
		out.NextCursor = *page.NextCursor
	}
	return out, nil
}

// Iter walks every run, following next_cursor.
func (s *RunsService) Iter(p *RunListParams) *Iterator[Run] {
	var rp RunListParams
	if p != nil {
		rp = *p
	}
	return newIterator(func(ctx context.Context) ([]Run, bool, error) {
		page, err := s.List(ctx, &rp)
		if err != nil {
			return nil, false, err
		}
		rp.Cursor = page.NextCursor
		return page.Runs, page.NextCursor == "" || len(page.Runs) == 0, nil
	})
}
