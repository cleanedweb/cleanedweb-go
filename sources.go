package cleanedweb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SourcesService runs the source APIs of your own workspace. Use the origin of your source's
// connection details as the client's base URL (today DefaultBaseURL).
type SourcesService struct{ c *Client }

// SourceRunParams are the options of Sources.Run, Start and Stream.
type SourceRunParams struct {
	// Input is the input of the source's generated contract.
	Input map[string]any
	// MaxItems stops the run after this many de-duplicated records (>= 1).
	MaxItems *int
	// TimeoutSeconds is the run's own time budget, 60 to 86400 (default 10800).
	TimeoutSeconds *int
	// WaitSeconds is how long the request waits for the run to end before answering 202 (default 90
	// for Run and Stream, 0 for Start).
	WaitSeconds *int
	// NoWait asks the server not to hold the request: Run returns the accepted run (Complete false).
	NoWait bool
	// Fields returns only these published output fields.
	Fields []string
	// Nulls sends every published field, with absent values as null.
	Nulls *bool
	// ExpectedVersion (the 64-character definition version) makes the call fail with a version
	// conflict (KindVersionConflict) if the active definition changed. Do not combine it with
	// DefinitionVersion.
	ExpectedVersion string
	// DefinitionVersion selects an accepted immutable revision explicitly.
	DefinitionVersion string
	// Endpoint is optional; the supported value is "search".
	Endpoint string
	// IdempotencyKey identifies this run; generated and returned when empty.
	IdempotencyKey string
	// PollTimeout stops waiting for a 202 after this long and returns the incomplete result.
	PollTimeout time.Duration
}

func (p *SourceRunParams) options() (*runOptions, error) {
	var sp SourceRunParams
	if p != nil {
		sp = *p
	}
	if sp.ExpectedVersion != "" && sp.DefinitionVersion != "" {
		return nil, invalidArgument("pass ExpectedVersion or DefinitionVersion, not both")
	}
	o := &runOptions{input: sp.Input, maxItems: sp.MaxItems, timeoutSeconds: sp.TimeoutSeconds, waitSeconds: sp.WaitSeconds,
		noWait: sp.NoWait, fields: sp.Fields, nulls: sp.Nulls, key: sp.IdempotencyKey, pollTimeout: sp.PollTimeout, extra: map[string]any{}}
	for k, v := range map[string]string{"expected_version": sp.ExpectedVersion, "definition_version": sp.DefinitionVersion, "endpoint": sp.Endpoint} {
		if v != "" {
			o.extra[k] = v
		}
	}
	return o, nil
}

func sourcePath(id string) string { return "/v1/sources/" + seg(id) }

// Run runs one of your source APIs (POST /v1/sources/{id}/run). It has the idempotency and waiting
// behaviour of Catalog.Run, except that a 202 is followed with GET /v1/runs/{run_id}/result, which
// needs the results scope: with a key that has only the run scope that read answers 403 (a
// permission error) while the run goes on. Read it with Runs.Get and Runs.Items, or create a key with
// the run and results scopes.
func (s *SourcesService) Run(ctx context.Context, sourceID string, p *SourceRunParams) (*RunResult, error) {
	o, err := p.options()
	if err != nil {
		return nil, err
	}
	return s.c.startRun(ctx, sourcePath(sourceID)+"/run", o, func(ctx context.Context, res *RunResult) (*RunResult, error) {
		runID := res.RunID()
		if runID == "" {
			return nil, &APIError{Kind: KindAPIStatus, StatusCode: http.StatusAccepted, Message: "the 202 answer carried no run_id to read the result with", IdempotencyKey: res.IdempotencyKey}
		}
		return s.c.readCatalogResultOnce(ctx, runPath(runID)+"/result", "")
	})
}

// Start starts a source run without waiting (POST /v1/sources/{id}/runs). It answers the run
// object: usually 202, or 200 when the run already ended. Run.Replayed is true when the key already
// named this run. Read records with Runs.Items and the final body with Runs.Result.
func (s *SourcesService) Start(ctx context.Context, sourceID string, p *SourceRunParams) (*Run, error) {
	o, err := p.options()
	if err != nil {
		return nil, err
	}
	key, err := keyOrNew(o.key)
	if err != nil {
		return nil, err
	}
	if o.waitSeconds == nil {
		zero := 0
		o.waitSeconds = &zero
	}
	body, err := o.body()
	if err != nil {
		return nil, err
	}
	resp, err := s.c.do(ctx, &request{method: http.MethodPost, url: s.c.url(sourcePath(sourceID) + "/runs"), body: body, auth: true, key: key, timeout: s.c.runTimeout(o.waitSeconds)})
	if err != nil {
		return nil, err
	}
	return decodeRun(resp)
}

// StreamEvent is one line of a source run's stream.
type StreamEvent struct {
	// Type is "progress", "records", "done" or "error".
	Type     string
	Sequence int
	// Stage is "running" or "heartbeat" on a progress event.
	Stage string
	// Records are the records of a records event.
	Records []Record
	// Run is the run object of the first progress event.
	Run *Run
	// StatusCode and ErrorCode describe an error event (a failed run ends the stream with one,
	// not with an HTTP error). Replayed and RunID come from its detail.
	StatusCode int
	ErrorCode  string
	// Raw is the whole line.
	Raw Object
}

// Stream is an open NDJSON stream of a run. Close it when done. Closing leaves the run running.
type Stream struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	r      *bufio.Reader
	cur    StreamEvent
	err    error
	// IdempotencyKey is the key of the run.
	IdempotencyKey string
	// RunLocation is the Location header: /v1/runs/{run_id}.
	RunLocation string
}

// Stream runs a source API and streams its records as they are committed
// (POST /v1/sources/{id}/stream). A failed run ends the stream with an error event.
func (s *SourcesService) Stream(ctx context.Context, sourceID string, p *SourceRunParams) (*Stream, error) {
	o, err := p.options()
	if err != nil {
		return nil, err
	}
	key, err := keyOrNew(o.key)
	if err != nil {
		return nil, err
	}
	body, err := o.body()
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	resp, cancelAttempt, err := s.c.open(streamCtx, &request{method: http.MethodPost, url: s.c.url(sourcePath(sourceID) + "/stream"), body: body, auth: true, key: key, timeout: -1})
	if err != nil {
		cancel()
		return nil, err
	}
	_ = cancelAttempt
	return &Stream{body: resp.Body, cancel: cancel, r: bufio.NewReaderSize(resp.Body, 64<<10), IdempotencyKey: key, RunLocation: resp.Header.Get("Location")}, nil
}

// Next reads the next event. It returns false at the end of the stream or on an error; check Err.
func (st *Stream) Next() bool {
	if st.err != nil {
		return false
	}
	for {
		line, err := st.r.ReadBytes('\n')
		if len(bytesTrim(line)) > 0 {
			var raw Object
			if derr := decodeNumbers(line, &raw); derr != nil {
				st.err = &APIError{Kind: KindAPIStatus, StatusCode: http.StatusOK, Message: "a stream line is not JSON: " + derr.Error(), Body: truncateBytes(line, 1024)}
				return false
			}
			st.cur = newStreamEvent(raw)
			return true
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				st.err = &ConnectionError{Err: err, IdempotencyKey: st.IdempotencyKey}
			}
			return false
		}
	}
}

// Event is the current event, valid after Next returned true.
func (st *Stream) Event() StreamEvent { return st.cur }

// Err is the error that ended the stream, if any.
func (st *Stream) Err() error { return st.err }

// Close releases the stream. The run keeps running; read it with Runs.Get.
func (st *Stream) Close() error {
	st.cancel()
	return st.body.Close()
}

func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}

func newStreamEvent(raw Object) StreamEvent {
	ev := StreamEvent{Raw: raw, Type: str(raw, "type"), Stage: str(raw, "stage"), ErrorCode: str(raw, "error_code")}
	if f, ok := numberToFloat(raw["sequence"]); ok {
		ev.Sequence = int(f)
	}
	if f, ok := numberToFloat(raw["statusCode"]); ok {
		ev.StatusCode = int(f)
	}
	for _, item := range anyList(raw["records"]) {
		if rec, ok := item.(map[string]any); ok {
			ev.Records = append(ev.Records, rec)
		}
	}
	if runBody, ok := raw["run"].(map[string]any); ok {
		if b, err := json.Marshal(runBody); err == nil {
			var run Run
			if json.Unmarshal(b, &run) == nil {
				ev.Run = &run
			}
		}
	}
	return ev
}

// SourceRunDetail is the recorded detail of a source run: the run, its results state, billing and
// recovery context. Needs the results scope.
type SourceRunDetail struct {
	ContractVersion string
	Run, Results    Object
	Billing         Object
	Recovery        Object
	Raw             Object
}

// GetRun reads the recorded details of a source run (GET /v1/sources/{id}/runs/{run_id}).
func (s *SourcesService) GetRun(ctx context.Context, sourceID, runID string) (*SourceRunDetail, error) {
	raw, err := s.getObject(ctx, sourcePath(sourceID)+"/runs/"+seg(runID), nil)
	if err != nil {
		return nil, err
	}
	return &SourceRunDetail{ContractVersion: str(raw, "contract_version"), Run: obj(raw, "run"), Results: obj(raw, "results"),
		Billing: obj(raw, "billing"), Recovery: obj(raw, "recovery"), Raw: raw}, nil
}

// GetRunResult reads the result of a run of this source, polling like Runs.Result.
func (s *SourcesService) GetRunResult(ctx context.Context, sourceID, runID string, p *ResultParams) (*RunResult, error) {
	return s.c.readResult(ctx, sourcePath(sourceID)+"/runs/"+seg(runID)+"/result", "", p.get())
}

// SourceVersions is a page of definition versions.
type SourceVersions struct {
	Items      []Object
	NextCursor string
	Raw        Object
}

// Versions lists the definition versions of a source, newest first (read scope).
func (s *SourcesService) Versions(ctx context.Context, sourceID string, limit int, cursor string) (*SourceVersions, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	raw, err := s.getObject(ctx, sourcePath(sourceID)+"/versions", q)
	if err != nil {
		return nil, err
	}
	out := &SourceVersions{Raw: raw, NextCursor: str(raw, "next_cursor")}
	for _, item := range anyList(raw["items"]) {
		if m, ok := item.(map[string]any); ok {
			out.Items = append(out.Items, m)
		}
	}
	return out, nil
}

// SourceVersionContract is the published contract of one definition version.
type SourceVersionContract struct {
	SourceID          string
	DefinitionVersion string
	// OpenAPI is an OpenAPI document for this version's run request and records.
	OpenAPI Object
	// Request is a ready-made run request: method, path, headers and body.
	Request Object
	Raw     Object
}

// VersionContract reads the contract of an accepted definition version (read scope).
func (s *SourcesService) VersionContract(ctx context.Context, sourceID, definitionVersion string) (*SourceVersionContract, error) {
	raw, err := s.getObject(ctx, sourcePath(sourceID)+"/versions/"+seg(definitionVersion)+"/contract", nil)
	if err != nil {
		return nil, err
	}
	return &SourceVersionContract{SourceID: str(raw, "source_id"), DefinitionVersion: str(raw, "definition_version"),
		OpenAPI: obj(raw, "openapi"), Request: obj(raw, "request"), Raw: raw}, nil
}

// SourceConnection is the run request for a source's active definition. It carries paths only,
// never an origin.
type SourceConnection struct {
	SourceID          string
	DefinitionVersion string
	// REST is the run request: method, path, body (with expected_version) and an example key.
	REST Object
	Raw  Object
}

// Connection reads a source's connection details (read scope). Fetching again creates new
// example idempotency keys, so keep the one you use.
func (s *SourcesService) Connection(ctx context.Context, sourceID string) (*SourceConnection, error) {
	raw, err := s.getObject(ctx, sourcePath(sourceID)+"/connection", nil)
	if err != nil {
		return nil, err
	}
	return &SourceConnection{SourceID: str(raw, "source_id"), DefinitionVersion: str(raw, "definition_version"), REST: obj(raw, "rest"), Raw: raw}, nil
}

// SourceHealth is the monitoring evidence of a source's active definition.
type SourceHealth struct {
	SourceID          string
	DefinitionVersion string
	// Status is healthy, degraded, unhealthy, unknown, stale or never_checked.
	Status string
	Raw    Object
}

// Health reads a source's health. No customer scope opens this route today: a scoped key answers
// a permission error. It works with credentials the engine treats as unscoped.
func (s *SourcesService) Health(ctx context.Context, sourceID string) (*SourceHealth, error) {
	raw, err := s.getObject(ctx, sourcePath(sourceID)+"/health", nil)
	if err != nil {
		return nil, err
	}
	return &SourceHealth{SourceID: str(raw, "source_id"), DefinitionVersion: str(raw, "definition_version"), Status: str(raw, "status"), Raw: raw}, nil
}

func (s *SourcesService) getObject(ctx context.Context, path string, q url.Values) (Object, error) {
	resp, err := s.c.do(ctx, &request{method: http.MethodGet, url: s.c.url(path), query: q, auth: true})
	if err != nil {
		return nil, err
	}
	var raw Object
	if err := decodeNumbers(resp.body, &raw); err != nil || raw == nil {
		return nil, decodeJSON(resp.body, &raw)
	}
	return raw, nil
}
