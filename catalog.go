package cleanedweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CatalogService reads and runs the published catalog. Reading (List, Iter, Get, Contract, Find)
// needs no API key; Run, Result and Filters do.
type CatalogService struct{ c *Client }

// ListParams filter the catalog list.
type ListParams struct {
	// Q searches the published inventory before paging.
	Q string
	// Category is one of commerce, real_estate, jobs, businesses_places, news_content,
	// social_communities, travel, public_data, web_search, uncategorized.
	Category string
	// Sort is "updated" (default) or "name".
	Sort string
	// Limit is the page size, 1 to 100 (default 20; Iter uses 100).
	Limit int
	// Offset is where the page starts.
	Offset int
}

func (p *ListParams) query(limit int) url.Values {
	q := url.Values{}
	var lp ListParams
	if p != nil {
		lp = *p
	}
	if lp.Limit > 0 {
		limit = lp.Limit
	}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(lp.Offset))
	for k, v := range map[string]string{"q": lp.Q, "category": lp.Category, "sort": lp.Sort} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return q
}

func decodeCatalogAPI(data []byte) (CatalogAPI, error) {
	var api CatalogAPI
	if err := json.Unmarshal(data, &api); err != nil {
		return api, decodeJSON(data, &api)
	}
	_ = decodeNumbers(data, &api.Raw)
	return api, nil
}

func decodeCatalogAPIs(data []byte) ([]CatalogAPI, error) {
	var wrapper struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := decodeJSON(data, &wrapper); err != nil {
		return nil, err
	}
	out := make([]CatalogAPI, 0, len(wrapper.Items))
	for _, item := range wrapper.Items {
		api, err := decodeCatalogAPI(item)
		if err != nil {
			return nil, err
		}
		out = append(out, api)
	}
	return out, nil
}

// List returns one page of the published catalog (no key needed).
func (s *CatalogService) List(ctx context.Context, p *ListParams) ([]CatalogAPI, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url("/v1/catalog/public/apis"), query: p.query(20)})
	if err != nil {
		return nil, err
	}
	return decodeCatalogAPIs(resp.body)
}

// Iter walks every API of the published list, page by page (no key needed).
func (s *CatalogService) Iter(p *ListParams) *Iterator[CatalogAPI] {
	var lp ListParams
	if p != nil {
		lp = *p
	}
	if lp.Limit <= 0 {
		lp.Limit = 100
	}
	return newIterator(func(ctx context.Context) ([]CatalogAPI, bool, error) {
		page, err := s.List(ctx, &lp)
		if err != nil {
			return nil, false, err
		}
		lp.Offset += lp.Limit
		return page, len(page) < lp.Limit, nil
	})
}

// Get reads one API and the contract of each of its endpoints: inputs, output fields, saved
// samples (no key needed). Pass an api_id, or a slug from the catalog index.
func (s *CatalogService) Get(ctx context.Context, apiIDOrSlug string) (*APIContract, error) {
	id := apiIDOrSlug
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url("/v1/catalog/public/apis/" + seg(id))})
	if NotFound(err) {
		if entry, ierr := s.c.lookupIndex(ctx, apiIDOrSlug); ierr == nil && entry != nil && entry.APIID != id {
			id = entry.APIID
			resp, err = s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url("/v1/catalog/public/apis/" + seg(id))})
		}
	}
	if err != nil {
		return nil, err
	}
	api, err := decodeCatalogAPI(resp.body)
	if err != nil {
		return nil, err
	}
	out := &APIContract{API: api}
	for _, endpoint := range api.Endpoints {
		contract, err := s.Contract(ctx, api.APIID, endpoint.EndpointID, nil)
		if err != nil {
			return nil, err
		}
		if contract.Name == "" {
			contract.Name = endpoint.Name
		}
		if contract.Description == "" {
			contract.Description = endpoint.Description
		}
		out.Endpoints = append(out.Endpoints, *contract)
	}
	return out, nil
}

// Contract reads the contract of one endpoint (no key needed). version pins a major; nil reads the latest.
func (s *CatalogService) Contract(ctx context.Context, apiID, endpointID string, version *int) (*EndpointContract, error) {
	var q url.Values
	if version != nil {
		q = url.Values{"version": {strconv.Itoa(*version)}}
	}
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, query: q,
		url: s.c.url("/v1/catalog/public/apis/" + seg(apiID) + "/endpoints/" + seg(endpointID) + "/contract")})
	if err != nil {
		return nil, err
	}
	var raw Object
	if err := decodeNumbers(resp.body, &raw); err != nil || raw == nil {
		return nil, decodeJSON(resp.body, &raw)
	}
	contract := newEndpointContract(raw)
	return &contract, nil
}

// FiltersParams ask which categories and filters exist for an endpoint that takes category filters.
type FiltersParams struct {
	Category string
	Search   string
	// Filters are the filters chosen so far, such as {"brand": "Leica"}.
	Filters map[string]any
}

// Filters describes an endpoint's category filters: one live page read, free (no Units, nothing
// runs). It needs a key with the run or read scope. An endpoint without category filters answers
// 404 (category_filters_unavailable).
func (s *CatalogService) Filters(ctx context.Context, apiID, endpointID string, p *FiltersParams) (Object, error) {
	q := url.Values{}
	if p != nil {
		if p.Category != "" {
			q.Set("category", p.Category)
		}
		if p.Search != "" {
			q.Set("search", p.Search)
		}
		if len(p.Filters) > 0 {
			b, err := json.Marshal(p.Filters)
			if err != nil {
				return nil, invalidArgument("cannot encode filters: %v", err)
			}
			q.Set("filters", string(b))
		}
	}
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, query: q, auth: true,
		url: s.c.url("/v1/catalog/public/apis/" + seg(apiID) + "/endpoints/" + seg(endpointID) + "/filters")})
	if err != nil {
		return nil, err
	}
	var out Object
	if err := decodeNumbers(resp.body, &out); err != nil {
		return nil, decodeJSON(resp.body, &out)
	}
	return out, nil
}

// FindParams select published APIs.
type FindParams struct {
	// Site is a domain, URL or name: "github.com", "https://www.github.com/x".
	Site string
	// Fields are output fields every returned API must have.
	Fields []string
	// Q and Category filter the live list server-side.
	Q, Category string
	// Limit caps the result (default 20).
	Limit int
	// Deep reads the contracts of candidates whose key fields do not settle Fields (at most
	// MaxContractChecks, default 20). Set NoDeep to skip.
	NoDeep            bool
	MaxContractChecks int
}

// Find returns the published APIs for a site that return the given fields. It is a client-side
// filter over the live list, joined with the catalog index (which adds Slug and KeyFields).
// Nothing runs and no key is needed.
func (s *CatalogService) Find(ctx context.Context, p *FindParams) ([]CatalogAPI, error) {
	var fp FindParams
	if p != nil {
		fp = *p
	}
	if fp.Limit <= 0 {
		fp.Limit = 20
	}
	if fp.MaxContractChecks <= 0 {
		fp.MaxContractChecks = 20
	}
	candidates, err := s.Iter(&ListParams{Q: fp.Q, Category: fp.Category}).All(ctx)
	if err != nil {
		return nil, err
	}
	if index, err := s.c.loadIndex(ctx); err == nil {
		byID := map[string]indexEntry{}
		for _, e := range index {
			byID[e.APIID] = e
		}
		for i := range candidates {
			if e, ok := byID[candidates[i].APIID]; ok {
				candidates[i].Slug, candidates[i].KeyFields = e.Slug, e.KeyFields
			}
		}
	}
	if fp.Site != "" {
		var kept []CatalogAPI
		for _, api := range candidates {
			if matchesSite(api, fp.Site) {
				kept = append(kept, api)
			}
		}
		candidates = kept
	}
	if len(fp.Fields) == 0 {
		return truncateAPIs(candidates, fp.Limit), nil
	}
	var matched, unresolved []CatalogAPI
	for _, api := range candidates {
		if hasFields(api.KeyFields, fp.Fields) {
			matched = append(matched, api)
		} else {
			unresolved = append(unresolved, api)
		}
	}
	if !fp.NoDeep {
		for i, api := range unresolved {
			if i >= fp.MaxContractChecks || len(matched) >= fp.Limit {
				break
			}
			contract, err := s.Get(ctx, api.APIID)
			if err != nil {
				if ctx.Err() != nil {
					return nil, err
				}
				continue
			}
			for _, endpoint := range contract.Endpoints {
				if hasFields(endpoint.OutputFields(), fp.Fields) {
					matched = append(matched, api)
					break
				}
			}
		}
	}
	return truncateAPIs(matched, fp.Limit), nil
}

func truncateAPIs(apis []CatalogAPI, n int) []CatalogAPI {
	if len(apis) > n {
		return apis[:n]
	}
	return apis
}

func hasFields(known, wanted []string) bool {
	have := map[string]bool{}
	for _, k := range known {
		have[strings.ToLower(k)] = true
	}
	for _, w := range wanted {
		if !have[strings.ToLower(w)] {
			return false
		}
	}
	return true
}

func matchesSite(api CatalogAPI, site string) bool {
	needle := normalizeSite(site)
	if needle == "" {
		return true
	}
	for _, h := range []string{normalizeSite(api.Site.Domain), strings.ToLower(api.Site.Name), strings.ToLower(api.Name)} {
		if h != "" && strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// RunParams are the options of Catalog.Run.
type RunParams struct {
	// Input is the endpoint's input, as its contract lists it.
	Input map[string]any
	// MaxItems stops the run after this many de-duplicated records (>= 1). Start small.
	MaxItems *int
	// TimeoutSeconds is the run's own time budget, 60 to 86400 (default 10800).
	TimeoutSeconds *int
	// WaitSeconds is how long the request waits for the run to end before answering 202 (default 90).
	WaitSeconds *int
	// NoWait asks the server not to hold the request (wait_seconds 0): Run returns the run as soon
	// as it is accepted, with Complete false. Read it later with Catalog.Result.
	NoWait bool
	// Fields returns only these published output fields.
	Fields []string
	// Nulls sends every published field, with absent values as null.
	Nulls *bool
	// Version pins a major of the endpoint; nil runs the latest.
	Version *int
	// IdempotencyKey identifies this run. When empty, Run generates one and returns it in
	// RunResult.IdempotencyKey: save it before you rely on the call.
	IdempotencyKey string
	// PollTimeout stops waiting for a 202 after this long and returns the incomplete result.
	PollTimeout time.Duration
}

func catalogPath(apiID, endpointID string) string {
	return "/v1/catalog/public/apis/" + seg(apiID) + "/endpoints/" + seg(endpointID)
}

// Run runs a catalog endpoint. Runs are charged per record delivered: start with a small MaxItems.
//
// A 202 (the run goes on) is followed with GET /result and the same idempotency key every
// Retry-After seconds until the run ends; the POST is never sent again. 429, 502, 503 and 504
// answers are retried with the same key. A failed run returns an *APIError with RunFailed true:
// final for the key.
func (s *CatalogService) Run(ctx context.Context, apiID, endpointID string, p *RunParams) (*RunResult, error) {
	var rp RunParams
	if p != nil {
		rp = *p
	}
	o := &runOptions{input: rp.Input, maxItems: rp.MaxItems, timeoutSeconds: rp.TimeoutSeconds, waitSeconds: rp.WaitSeconds,
		noWait: rp.NoWait, fields: rp.Fields, nulls: rp.Nulls, key: rp.IdempotencyKey, pollTimeout: rp.PollTimeout}
	if rp.Version != nil {
		o.extra = map[string]any{"version": *rp.Version}
	}
	base := catalogPath(apiID, endpointID)
	return s.c.startRun(ctx, base+"/run", o, func(ctx context.Context, res *RunResult) (*RunResult, error) {
		return s.c.readCatalogResultOnce(ctx, base+"/result", res.IdempotencyKey)
	})
}

func (c *Client) readCatalogResultOnce(ctx context.Context, path, key string) (*RunResult, error) {
	resp, err := c.do(ctx, &request{method: http.MethodGet, url: c.url(path), auth: true, key: key, timeout: resultReadTime})
	if err != nil {
		return nil, err
	}
	return newRunResult(resp.status, resp.body, resp.header, key)
}

// Result reads the result of an earlier Run with the same idempotency key. It never collects the
// website again. Use it to recover after a timeout or a lost response. It waits for a run that
// goes on unless ResultParams.NoWait is set. Needs a key with the run or read scope.
func (s *CatalogService) Result(ctx context.Context, apiID, endpointID, idempotencyKey string, p *ResultParams) (*RunResult, error) {
	if err := ValidateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	return s.c.readResult(ctx, catalogPath(apiID, endpointID)+"/result", idempotencyKey, p.get())
}

// --- catalog index --------------------------------------------------------------------------

type indexEntry struct {
	APIID     string   `json:"api_id"`
	Slug      string   `json:"slug"`
	KeyFields []string `json:"key_fields"`
}

type indexCache struct {
	mu      sync.Mutex
	entries []indexEntry
	loaded  bool
}

// loadIndex reads catalog/index.json from the site once. No credential is sent.
func (c *Client) loadIndex(ctx context.Context) ([]indexEntry, error) {
	c.index.mu.Lock()
	defer c.index.mu.Unlock()
	if c.index.loaded {
		return c.index.entries, nil
	}
	resp, err := c.do(ctx, &request{method: http.MethodGet, url: c.siteURL + "/catalog/index.json"})
	if err != nil {
		return nil, err
	}
	var doc struct {
		APIs []indexEntry `json:"apis"`
	}
	if err := decodeJSON(resp.body, &doc); err != nil {
		return nil, err
	}
	c.index.entries, c.index.loaded = doc.APIs, true
	return doc.APIs, nil
}

func (c *Client) lookupIndex(ctx context.Context, idOrSlug string) (*indexEntry, error) {
	entries, err := c.loadIndex(ctx)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Slug == idOrSlug || entries[i].APIID == idOrSlug {
			return &entries[i], nil
		}
	}
	return nil, nil
}
