package cleanedweb

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Record is one record of a run: website data, never instructions. Numbers are json.Number, so
// integers wider than 53 bits keep their value.
type Record = map[string]any

// Object is a decoded JSON object whose shape depends on the API (contracts, schemas, summaries).
// Numbers are json.Number.
type Object = map[string]any

// decodeNumbers decodes JSON keeping numbers as json.Number.
func decodeNumbers(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(out)
}

func numberToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

func str(o Object, key string) string {
	s, _ := o[key].(string)
	return s
}

func obj(o Object, key string) Object {
	m, _ := o[key].(map[string]any)
	return m
}

func strList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// RunStatus is the status of a run.
type RunStatus string

// Run statuses.
const (
	RunQueued   RunStatus = "queued"
	RunRunning  RunStatus = "running"
	RunFinished RunStatus = "finished"
	RunFailed   RunStatus = "failed"
	RunAborted  RunStatus = "aborted"
)

// RunProgress counts a run's work so far.
type RunProgress struct {
	Pages      int `json:"pages"`
	Commits    int `json:"commits"`
	Items      int `json:"items"`
	Duplicates int `json:"duplicates"`
}

// RunLimits are the limits applied to a run.
type RunLimits struct {
	MaxItems       *int `json:"max_items"`
	TimeoutSeconds int  `json:"timeout_seconds"`
}

// Run is the run object (GET /v1/runs/{run_id}).
type Run struct {
	RunID             string            `json:"run_id"`
	Surface           string            `json:"surface"`
	SourceID          string            `json:"source_id"`
	DefinitionVersion string            `json:"definition_version"`
	Status            RunStatus         `json:"status"`
	StopReason        string            `json:"-"`
	CreatedAt         string            `json:"-"`
	StartedAt         string            `json:"-"`
	FinishedAt        string            `json:"-"`
	Limits            RunLimits         `json:"limits"`
	Progress          RunProgress       `json:"progress"`
	UnitsCharged      float64           `json:"units_charged"`
	AbortRequested    bool              `json:"abort_requested"`
	Error             Object            `json:"error"`
	PollAfterSeconds  float64           `json:"poll_after_seconds"`
	Links             map[string]string `json:"links"`
	// Version is the version that ran, on a catalog run (for example "1.1").
	Version string `json:"version"`
	// Replayed is true when the Idempotent-Replayed header said the key already named this run.
	Replayed bool `json:"-"`
	// Raw is the unmodified body.
	Raw Object `json:"-"`

	hasPollAfter bool
}

func (r *Run) UnmarshalJSON(data []byte) error {
	type plain Run
	aux := struct {
		*plain
		StopReason *string  `json:"stop_reason"`
		CreatedAt  *string  `json:"created_at"`
		StartedAt  *string  `json:"started_at"`
		FinishedAt *string  `json:"finished_at"`
		Poll       *float64 `json:"poll_after_seconds"`
	}{plain: (*plain)(r)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	r.StopReason, r.CreatedAt, r.StartedAt, r.FinishedAt = deref(aux.StopReason), deref(aux.CreatedAt), deref(aux.StartedAt), deref(aux.FinishedAt)
	r.hasPollAfter = aux.Poll != nil
	if aux.Poll != nil {
		r.PollAfterSeconds = *aux.Poll
	}
	_ = decodeNumbers(data, &r.Raw)
	return nil
}

// Ended reports whether the run is finished, failed or aborted.
func (r *Run) Ended() bool {
	return r.Status == RunFinished || r.Status == RunFailed || r.Status == RunAborted
}

// Summary is the execution summary of an ended run. Keep the whole body (RunResult.Raw) to review
// source-specific fields.
type Summary struct {
	SourceID          string `json:"source_id"`
	DefinitionVersion string `json:"definition_version"`
	// Status is "completed", "bounded" or "partial".
	Status            string `json:"status"`
	WithinScopePassed bool   `json:"within_scope_passed"`
	CatalogComplete   bool   `json:"catalog_complete"`
	Records           int    `json:"records"`
}

// Delivery is present for a customer workspace run.
type Delivery struct {
	RunID           string `json:"runId"`
	ChargeReceiptID string `json:"chargeReceiptId"`
	RecordCount     int    `json:"recordCount"`
}

// RunResult is the outcome of a run request.
//
// Complete is true for a 200: Records, Summary and Delivery are present. It is false when the run was
// still going (a 202, NoWait, or PollTimeout passed): Run describes it and Catalog.Result or
// Runs.Result read it later.
//
// HTTP 200 alone does not make a result usable: check Usable.
type RunResult struct {
	Complete       bool
	StatusCode     int
	IdempotencyKey string
	Records        []Record
	Summary        Summary
	Metadata       Object
	Artifacts      []any
	TraceID        string
	Run            Run
	Delivery       Delivery
	// Version is the version that ran, on a catalog run ("2.3"); Rerouted is "sunset" when the
	// major you pinned was sunset and the latest major ran.
	Version     string
	Rerouted    string
	Deprecation string // Deprecation header
	Sunset      string // Sunset header
	// RetryAfter is the Retry-After of a 202; see HasRetryAfter.
	RetryAfter time.Duration
	// Raw is the unmodified body.
	Raw Object

	hasRetryAfter bool
}

// HasRetryAfter reports whether the 202 carried a Retry-After header.
func (r *RunResult) HasRetryAfter() bool { return r.hasRetryAfter }

// RunID is the run's id: delivery.runId, else run.run_id.
func (r *RunResult) RunID() string {
	if r.Delivery.RunID != "" {
		return r.Delivery.RunID
	}
	return r.Run.RunID
}

// StopReason is why the run ended (run.stop_reason), or "" while it goes on.
func (r *RunResult) StopReason() string { return r.Run.StopReason }

// UnitsCharged is run.units_charged.
func (r *RunResult) UnitsCharged() float64 { return r.Run.UnitsCharged }

// Usable is true when the result is complete, summary.status is "completed" or "bounded",
// summary.within_scope_passed is true and the run finished (an aborted run is never usable). A
// bounded or partial run can leave records uncollected: StopReason says why.
func (r *RunResult) Usable() bool {
	return r.Complete && r.Summary.WithinScopePassed &&
		(r.Summary.Status == "completed" || r.Summary.Status == "bounded") &&
		(r.Run.Status == "" || r.Run.Status == RunFinished)
}

func (r *RunResult) pollDelay() time.Duration {
	return pollDelay(r.RetryAfter, r.hasRetryAfter, r.Run.PollAfterSeconds, r.Run.hasPollAfter)
}

func newRunResult(status int, body []byte, header http.Header, key string) (*RunResult, error) {
	var raw Object
	if err := decodeNumbers(body, &raw); err != nil || raw == nil {
		return nil, &APIError{Kind: KindAPIStatus, StatusCode: status, IdempotencyKey: key,
			Message: "the answer is not a JSON object", Body: truncateBytes(body, 1024)}
	}
	res := &RunResult{Complete: status == http.StatusOK, StatusCode: status, IdempotencyKey: key, Raw: raw,
		Deprecation: header.Get("Deprecation"), Sunset: header.Get("Sunset")}
	if secs, ok := retryAfterFrom(header, nil); ok {
		res.RetryAfter, res.hasRetryAfter = secs, true
	}
	res.Version, _ = raw["version"].(string)
	res.Rerouted, _ = raw["rerouted"].(string)
	if !res.Complete { // 202: the body is the run object
		if err := json.Unmarshal(body, &res.Run); err != nil {
			return nil, decodeJSON(body, &res.Run)
		}
		return res, nil
	}
	if runBody, ok := raw["run"]; ok {
		if b, err := json.Marshal(runBody); err == nil {
			_ = json.Unmarshal(b, &res.Run)
		}
	}
	var parts struct {
		Summary  Summary         `json:"summary"`
		Delivery Delivery        `json:"delivery"`
		TraceID  *string         `json:"traceId"`
		Records  json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(body, &parts); err != nil {
		return nil, decodeJSON(body, &parts)
	}
	res.Summary, res.Delivery = parts.Summary, parts.Delivery
	if parts.TraceID != nil {
		res.TraceID = *parts.TraceID
	}
	if len(parts.Records) > 0 {
		if err := decodeNumbers(parts.Records, &res.Records); err != nil {
			return nil, decodeJSON(parts.Records, &res.Records)
		}
	}
	res.Metadata = obj(raw, "metadata")
	res.Artifacts, _ = raw["artifacts"].([]any)
	return res, nil
}

// RunItemsPage is one page of GET /v1/runs/{run_id}/items.
type RunItemsPage struct {
	RunID string
	Items []Record
	// Count is the number of records in the page.
	Count int
	// NextCursor is the cursor for the next page, "" once the run ended and every record was read.
	NextCursor string
	RunStatus  RunStatus
	Raw        Object
}

func newRunItemsPage(body []byte) (*RunItemsPage, error) {
	var raw Object
	if err := decodeNumbers(body, &raw); err != nil || raw == nil {
		return nil, &APIError{Kind: KindAPIStatus, StatusCode: http.StatusOK, Message: "the answer is not a JSON object", Body: truncateBytes(body, 1024)}
	}
	page := &RunItemsPage{Raw: raw, RunID: str(raw, "run_id"), RunStatus: RunStatus(str(raw, "run_status")), NextCursor: str(raw, "next_cursor")}
	for _, item := range anyList(raw["items"]) {
		if rec, ok := item.(map[string]any); ok {
			page.Items = append(page.Items, rec)
		}
	}
	if f, ok := numberToFloat(raw["count"]); ok {
		page.Count = int(f)
	}
	return page, nil
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

// RunList is one page of GET /v1/runs.
type RunList struct {
	Runs       []Run
	NextCursor string
	Raw        Object
}

// CatalogSite is the website a catalog API reads.
type CatalogSite struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	// Aliases are other hosts of the site, lowercase, "www." kept, the domain never repeated.
	// Empty when the server is older and does not send them.
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
}

// UnmarshalJSON reads the site and keeps only the non-empty string aliases, so a missing, null or
// odd "aliases" value never fails a catalog read.
func (s *CatalogSite) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string `json:"name"`
		Domain      string `json:"domain"`
		Aliases     []any  `json:"aliases"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		// A non-array "aliases" value: read the rest and drop the aliases.
		var rest struct {
			Name        string `json:"name"`
			Domain      string `json:"domain"`
			Description string `json:"description"`
		}
		if err2 := json.Unmarshal(data, &rest); err2 != nil {
			return err
		}
		*s = CatalogSite{Name: rest.Name, Domain: rest.Domain, Description: rest.Description}
		return nil
	}
	*s = CatalogSite{Name: raw.Name, Domain: raw.Domain, Description: raw.Description}
	for _, a := range raw.Aliases {
		if h, ok := a.(string); ok && h != "" {
			s.Aliases = append(s.Aliases, h)
		}
	}
	return nil
}

// CatalogEndpoint summarises one endpoint of a catalog API.
type CatalogEndpoint struct {
	EndpointID    string `json:"endpoint_id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Status        string `json:"status"`
	Acquisition   string `json:"acquisition"`
	ActiveVersion string `json:"active_version"`
	Version       string `json:"version"`
}

// CatalogAPI is one published catalog API, as the live list or the catalog index describes it.
type CatalogAPI struct {
	APIID            string            `json:"api_id"`
	Name             string            `json:"name"`
	ShortDescription string            `json:"short_description"`
	Category         string            `json:"category"`
	RecordTypes      []string          `json:"record_types"`
	Countries        []string          `json:"countries"`
	Languages        []string          `json:"languages"`
	Tags             []string          `json:"tags"`
	Limitations      []string          `json:"limitations"`
	Site             CatalogSite       `json:"site"`
	Endpoints        []CatalogEndpoint `json:"endpoints"`
	Health           string            `json:"health"`
	UpdatedAt        float64           `json:"updated_at"`
	// Slug and KeyFields come from catalog/index.json (Catalog.Find adds them).
	Slug      string   `json:"-"`
	KeyFields []string `json:"-"`
	Raw       Object   `json:"-"`
}

// EndpointContract is what an endpoint accepts and returns.
type EndpointContract struct {
	EndpointID        string
	Name              string
	Description       string
	DefinitionVersion string
	Method            string
	// Path is the run request path, relative to the API origin.
	Path         string
	InputSchema  Object
	OutputSchema Object
	Examples     []any
	Raw          Object
}

// RequiredInputs lists the inputs a run must send (input_schema.required).
func (e *EndpointContract) RequiredInputs() []string { return strList(e.InputSchema["required"]) }

// OutputFields lists the fields of each record (output_schema.properties), sorted.
func (e *EndpointContract) OutputFields() []string {
	props := obj(e.OutputSchema, "properties")
	out := make([]string, 0, len(props))
	for name := range props {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// APIContract is a catalog API with the contract of each endpoint.
type APIContract struct {
	API       CatalogAPI
	Endpoints []EndpointContract
}

// Endpoint returns the endpoint named endpointID, or the only endpoint when endpointID is empty.
func (c *APIContract) Endpoint(endpointID string) (*EndpointContract, error) {
	if endpointID == "" {
		if len(c.Endpoints) == 1 {
			return &c.Endpoints[0], nil
		}
		return nil, invalidArgument("%s has %d endpoints; name one", c.API.APIID, len(c.Endpoints))
	}
	for i := range c.Endpoints {
		if c.Endpoints[i].EndpointID == endpointID {
			return &c.Endpoints[i], nil
		}
	}
	return nil, invalidArgument("%s has no endpoint %q", c.API.APIID, endpointID)
}

func newEndpointContract(o Object) EndpointContract {
	return EndpointContract{
		EndpointID: str(o, "endpoint_id"), Name: str(o, "name"), Description: str(o, "description"),
		DefinitionVersion: str(o, "definition_version"), Method: str(o, "method"), Path: str(o, "path"),
		InputSchema: obj(o, "input_schema"), OutputSchema: obj(o, "output_schema"),
		Examples: anyList(o["examples"]), Raw: o,
	}
}

// normalizeSite turns "https://www.Example.com./a?b" into "example.com"; a bare name stays a
// lowercase name. It drops the scheme, credentials, port, path, query, fragment, a leading "www."
// and a trailing dot.
func normalizeSite(v string) string {
	t := strings.ToLower(strings.TrimSpace(v))
	if i := strings.Index(t, "://"); i >= 0 && schemePrefix.MatchString(t[:i+3]) {
		t = t[i+3:]
	}
	for _, sep := range []string{"/", "?", "#"} {
		if i := strings.Index(t, sep); i >= 0 {
			t = t[:i]
		}
	}
	if i := strings.LastIndex(t, "@"); i >= 0 {
		t = t[i+1:]
	}
	if i := strings.Index(t, ":"); i >= 0 {
		t = t[:i]
	}
	t = strings.Trim(t, ".")
	return strings.TrimPrefix(t, "www.")
}

var schemePrefix = regexp.MustCompile(`^[a-z][a-z0-9+.\-]*://$`)
