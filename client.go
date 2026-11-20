package cleanedweb

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client talks to the CleanedWeb API. It is safe for concurrent use.
//
// The API key (default: CLEANEDWEB_API_KEY) is sent only to the base URL, never to the catalog
// site. Reading the catalog needs no key; running does.
type Client struct {
	// Catalog finds, reads and runs published catalog APIs.
	Catalog *CatalogService
	// Runs reads, waits for, aborts and lists runs.
	Runs *RunsService
	// Sources runs your own source APIs.
	Sources *SourcesService
	// Schedules creates and manages recurring runs.
	Schedules *SchedulesService

	apiKey         string
	baseURL        string
	siteURL        string
	httpClient     *http.Client
	maxRetries     int
	maxRetryDelay  time.Duration
	requestTimeout time.Duration
	userAgent      string

	// sleep waits between retries and polls; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
	// index caches catalog/index.json.
	index indexCache
}

// Option configures a Client.
type Option func(*Client)

// WithAPIKey sets the API key. The default is the CLEANEDWEB_API_KEY environment variable.
func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }

// WithBaseURL sets the API origin (no path). The default is DefaultBaseURL, or the
// CLEANEDWEB_API_BASE (then CLEANEDWEB_RUN_BASE) environment variable. For a source API, use
// the origin of your source's connection details.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") } }

// WithSiteURL sets the origin of the static catalog files (default DefaultSiteURL).
func WithSiteURL(u string) Option { return func(c *Client) { c.siteURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client. The SDK uses a copy that never follows redirects: a
// redirect means the request went to the wrong origin and nothing ran.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithMaxRetries sets how many times a 429, 502, 503 or 504 answer is retried with the same
// idempotency key (default 2, 0 disables retries).
func WithMaxRetries(n int) Option { return func(c *Client) { c.maxRetries = n } }

// WithMaxRetryDelay caps the wait before a retry (default 30s). A server-asked wait longer than
// the cap is not waited for: the error is returned with its RetryAfter.
func WithMaxRetryDelay(d time.Duration) Option { return func(c *Client) { c.maxRetryDelay = d } }

// WithTimeout sets the timeout of one request (default 30s). A run request is held by the server
// for up to its wait_seconds, so the timeout is raised to wait_seconds plus 30s for it.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.requestTimeout = d } }

// WithUserAgentSuffix appends a product token to the User-Agent header.
func WithUserAgentSuffix(s string) Option {
	return func(c *Client) { c.userAgent = userAgent + " " + strings.TrimSpace(s) }
}

// New builds a Client. Without options it reads CLEANEDWEB_API_KEY and, optionally,
// CLEANEDWEB_API_BASE.
func New(opts ...Option) *Client {
	c := &Client{
		apiKey:         os.Getenv("CLEANEDWEB_API_KEY"),
		baseURL:        DefaultBaseURL,
		siteURL:        DefaultSiteURL,
		maxRetries:     2,
		maxRetryDelay:  30 * time.Second,
		requestTimeout: 30 * time.Second,
		userAgent:      userAgent,
		sleep:          sleepContext,
	}
	if v := os.Getenv("CLEANEDWEB_API_BASE"); v != "" {
		c.baseURL = strings.TrimRight(v, "/")
	} else if v := os.Getenv("CLEANEDWEB_RUN_BASE"); v != "" {
		c.baseURL = strings.TrimRight(v, "/")
	}
	for _, opt := range opts {
		opt(c)
	}
	base := c.httpClient
	if base == nil {
		base = &http.Client{}
	}
	copied := *base // never follow redirects: a redirect means the wrong origin
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.httpClient = &copied
	c.Catalog = &CatalogService{c: c}
	c.Runs = &RunsService{c: c}
	c.Sources = &SourcesService{c: c}
	c.Schedules = &SchedulesService{c: c}
	return c
}

// BaseURL returns the API origin in use.
func (c *Client) BaseURL() string { return c.baseURL }

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Int returns a pointer to n, for optional integer parameters such as RunParams.MaxItems.
func Int(n int) *int { return &n }

// Bool returns a pointer to b, for optional boolean parameters.
func Bool(b bool) *bool { return &b }
