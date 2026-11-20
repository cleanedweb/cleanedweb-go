package cleanedweb

// Runs the language-neutral conformance cases of cleanedweb-openapi (vendored in
// testdata/conformance, refreshed by scripts/sync-conformance.sh) against a mock server. The format is
// described in testdata/conformance/README.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type generic = map[string]any

func readJSON(t *testing.T, path string) generic {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out generic
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

func TestConformance(t *testing.T) {
	files, err := filepath.Glob("testdata/conformance/cases/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no conformance cases found: %v", err)
	}
	total := 0
	for _, file := range files {
		doc := readJSON(t, file)
		for _, raw := range doc["cases"].([]any) {
			c := raw.(generic)
			total++
			t.Run(fmt.Sprintf("%s/%s", doc["group"], c["id"]), func(t *testing.T) { runCase(t, c) })
		}
	}
	t.Logf("%d conformance cases", total)
}

type harness struct {
	t         *testing.T
	exchanges []any
	mu        sync.Mutex
	next      int
	key       string // the bound $key
	failures  []string
}

func (h *harness) fail(format string, args ...any) {
	h.mu.Lock()
	h.failures = append(h.failures, fmt.Sprintf(format, args...))
	h.mu.Unlock()
}

var keyGrammar = regexp.MustCompile(`^[A-Za-z0-9._:\-]{1,200}$`)

func (h *harness) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	idx := h.next
	h.next++
	h.mu.Unlock()
	if idx >= len(h.exchanges) {
		h.fail("unexpected request %d: %s %s", idx, r.Method, r.URL.RequestURI())
		http.Error(w, "unexpected", http.StatusTeapot)
		return
	}
	ex := h.exchanges[idx].(generic)
	h.checkRequest(idx, r, ex["request"].(generic))
	if kind, ok := ex["network_error"].(string); ok {
		_ = kind
		hj, ok := w.(http.Hijacker)
		if !ok {
			h.fail("cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	resp := ex["response"].(generic)
	if headers, ok := resp["headers"].(generic); ok {
		for k, v := range headers {
			w.Header().Set(k, v.(string))
		}
	}
	status, _ := resp["status"].(json.Number).Int64()
	if text, ok := resp["text"].(string); ok {
		w.WriteHeader(int(status))
		_, _ = w.Write([]byte(text))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(int(status))
	if body, ok := resp["json"]; ok {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (h *harness) checkRequest(idx int, r *http.Request, want generic) {
	where := fmt.Sprintf("request %d (%s %s)", idx, r.Method, r.URL.RequestURI())
	if want["method"] != r.Method {
		h.fail("%s: method want %v", where, want["method"])
	}
	if want["path"] != r.URL.Path {
		h.fail("%s: path want %v", where, want["path"])
	}
	wantQuery := map[string]string{}
	if q, ok := want["query"].(generic); ok {
		for k, v := range q {
			wantQuery[k] = v.(string)
		}
	}
	gotQuery := map[string]string{}
	for k, v := range r.URL.Query() {
		gotQuery[k] = strings.Join(v, ",")
	}
	if !reflect.DeepEqual(wantQuery, gotQuery) {
		h.fail("%s: query want %v got %v", where, wantQuery, gotQuery)
	}
	if headers, ok := want["headers"].(generic); ok {
		for name, v := range headers {
			got := r.Header.Get(name)
			switch exp := v.(type) {
			case nil:
				if _, present := r.Header[http.CanonicalHeaderKey(name)]; present {
					h.fail("%s: header %s must be absent, got %q", where, name, got)
				}
			case string:
				if exp == "$key" {
					h.mu.Lock()
					if h.key == "" {
						h.key = got
						if !keyGrammar.MatchString(got) {
							h.failures = append(h.failures, fmt.Sprintf("%s: generated key %q breaks the key grammar", where, got))
						}
					}
					exp = h.key
					h.mu.Unlock()
				}
				if got != exp {
					h.fail("%s: header %s want %q got %q", where, name, exp, got)
				}
			}
		}
	}
	if wantBody, ok := want["body"]; ok {
		raw, _ := readAll(r)
		var got any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&got); err != nil {
			h.fail("%s: body is not JSON: %q", where, raw)
		} else if !reflect.DeepEqual(normalize(wantBody), normalize(got)) {
			h.fail("%s: body want %s got %s", where, mustJSON(wantBody), raw)
		}
	} else if r.Method == http.MethodGet {
		if raw, _ := readAll(r); len(raw) > 0 {
			h.fail("%s: a GET must not send a body", where)
		}
	}
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// normalize makes numbers comparable: every number becomes a float64.
func normalize(v any) any {
	switch x := v.(type) {
	case json.Number:
		f, _ := x.Float64()
		return f
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	case time.Duration:
		return x.Seconds()
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = normalize(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normalize(val)
		}
		return out
	}
	return v
}

// subset reports whether every key of want is in got with an equal value, recursively.
func subset(want, got any, path string, problems *[]string) {
	want, got = normalize(want), normalize(got)
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: want an object, got %T", path, got))
			return
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			gv, present := g[k]
			if !present {
				*problems = append(*problems, fmt.Sprintf("%s.%s: missing (want %s)", path, k, mustJSON(w[k])))
				continue
			}
			subset(w[k], gv, path+"."+k, problems)
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			*problems = append(*problems, fmt.Sprintf("%s: want %d items %s, got %s", path, len(w), mustJSON(want), mustJSON(got)))
			return
		}
		for i := range w {
			subset(w[i], g[i], fmt.Sprintf("%s[%d]", path, i), problems)
		}
	default:
		if !reflect.DeepEqual(want, got) {
			*problems = append(*problems, fmt.Sprintf("%s: want %v got %v", path, want, got))
		}
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	case float64:
		return int(n), true
	}
	return 0, false
}

func intPtr(args generic, key string) *int {
	if v, ok := args[key]; ok {
		if i, ok := asInt(v); ok {
			return &i
		}
	}
	return nil
}

func argStr(args generic, key string) string { s, _ := args[key].(string); return s }

func argBool(args generic, key string) (bool, bool) { b, ok := args[key].(bool); return b, ok }

func argSeconds(args generic, key string) time.Duration {
	if f, ok := normalize(args[key]).(float64); ok {
		return time.Duration(f * float64(time.Second))
	}
	return 0
}

func argMap(args generic, key string) map[string]any {
	m, _ := args[key].(map[string]any)
	return m
}

func argStrings(args generic, key string) []string {
	list, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

func resultParams(args generic) *ResultParams {
	p := &ResultParams{PollTimeout: argSeconds(args, "poll_timeout_seconds")}
	if wait, ok := argBool(args, "wait"); ok && !wait {
		p.NoWait = true
	}
	p.Nulls, _ = argBool(args, "nulls")
	return p
}

func runCase(t *testing.T, c generic) {
	h := &harness{t: t, exchanges: c["exchanges"].([]any)}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cfg := c["client"].(generic)
	retries, _ := asInt(cfg["max_retries"])
	maxDelay, _ := normalize(cfg["max_retry_delay_seconds"]).(float64)
	apiKey, _ := cfg["api_key"].(string)
	client := New(WithAPIKey(apiKey), WithBaseURL(srv.URL), WithSiteURL(srv.URL), WithHTTPClient(srv.Client()),
		WithMaxRetries(retries), WithMaxRetryDelay(time.Duration(maxDelay*float64(time.Second))))
	var sleeps []float64
	client.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d.Seconds())
		return ctx.Err()
	}
	args := c["args"].(generic)
	h.key = argStr(args, "idempotency_key")

	outcome, callErr := callOperation(context.Background(), client, c["operation"].(string), args)

	h.mu.Lock()
	failures := append([]string(nil), h.failures...)
	sent := h.next
	h.mu.Unlock()
	if sent < len(h.exchanges) {
		failures = append(failures, fmt.Sprintf("only %d of %d expected requests were sent", sent, len(h.exchanges)))
	}
	expect := c["expect"].(generic)
	var problems []string
	subst := func(v any) any { return substituteKey(v, h.key) }

	if want, ok := expect["error"].(generic); ok {
		if callErr == nil {
			problems = append(problems, "want an error, got none")
		} else {
			subset(subst(want), errorFields(callErr), "error", &problems)
		}
	} else if callErr != nil {
		problems = append(problems, "unexpected error: "+callErr.Error())
	} else {
		for _, key := range []string{"result", "run", "items", "run_ids", "schedule", "deleted", "data", "events"} {
			if want, ok := expect[key]; ok {
				subset(subst(want), outcome, key, &problems)
			}
		}
		if want, ok := expect["schedules"]; ok { // Schedules.List answers {schedules, limit}
			list := outcome.(generic)
			subset(want, list["schedules"], "schedules", &problems)
			subset(expect["limit"], list["limit"], "limit", &problems)
		}
	}
	if generated, _ := expect["generated_key"].(bool); generated {
		if h.key == "" || !keyGrammar.MatchString(h.key) {
			problems = append(problems, fmt.Sprintf("no generated key was sent (%q)", h.key))
		}
	}
	wantSleeps := []any{}
	if s, ok := expect["sleeps"].([]any); ok {
		wantSleeps = s
	}
	gotSleeps := make([]any, len(sleeps))
	for i, s := range sleeps {
		gotSleeps[i] = s
	}
	subset(wantSleeps, gotSleeps, "sleeps", &problems)

	for _, f := range append(failures, problems...) {
		t.Error(f)
	}
}

func substituteKey(v any, key string) any {
	switch x := v.(type) {
	case string:
		if x == "$key" {
			return key
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = substituteKey(val, key)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = substituteKey(val, key)
		}
		return out
	}
	return v
}

func asGeneric(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&out)
	return out
}

func resultOutcome(r *RunResult) generic {
	return generic{
		"complete": r.Complete, "usable": r.Usable(), "status_code": r.StatusCode, "idempotency_key": r.IdempotencyKey,
		"run_id": r.RunID(), "record_count": len(r.Records), "stop_reason": r.StopReason(), "units_charged": r.UnitsCharged(),
		"version": r.Version, "rerouted": r.Rerouted, "deprecation": r.Deprecation, "sunset": r.Sunset,
	}
}

func runOutcome(r *Run) generic {
	return generic{
		"run_id": r.RunID, "status": string(r.Status), "stop_reason": r.StopReason, "ended": r.Ended(),
		"items": r.Progress.Items, "units_charged": r.UnitsCharged, "abort_requested": r.AbortRequested, "replayed": r.Replayed,
	}
}

func scheduleOutcome(s *Schedule) generic {
	return generic{
		"schedule_id": s.ScheduleID, "state": s.State, "pause_reason": s.PauseReason, "replayed": s.Replayed,
		"inflight_run_id": s.InflightRunID, "cadence": generic{"every": s.Cadence.Every, "at_utc": s.Cadence.AtUTC},
	}
}

func callOperation(ctx context.Context, c *Client, op string, args generic) (any, error) {
	switch op {
	case "catalog.run":
		p := &RunParams{Input: argMap(args, "input"), MaxItems: intPtr(args, "max_items"), TimeoutSeconds: intPtr(args, "timeout_seconds"),
			WaitSeconds: intPtr(args, "wait_seconds"), Fields: argStrings(args, "fields"), Version: intPtr(args, "version"),
			IdempotencyKey: argStr(args, "idempotency_key"), PollTimeout: argSeconds(args, "poll_timeout_seconds")}
		if wait, ok := argBool(args, "wait"); ok && !wait {
			p.NoWait = true
		}
		if n, ok := argBool(args, "nulls"); ok {
			p.Nulls = Bool(n)
		}
		res, err := c.Catalog.Run(ctx, argStr(args, "api_id"), argStr(args, "endpoint_id"), p)
		return resultOrNil(res, err)
	case "catalog.result":
		res, err := c.Catalog.Result(ctx, argStr(args, "api_id"), argStr(args, "endpoint_id"), argStr(args, "idempotency_key"), resultParams(args))
		return resultOrNil(res, err)
	case "runs.get":
		run, err := c.Runs.Get(ctx, argStr(args, "run_id"))
		return runOrNil(run, err)
	case "runs.abort":
		run, err := c.Runs.Abort(ctx, argStr(args, "run_id"))
		return runOrNil(run, err)
	case "runs.result":
		res, err := c.Runs.Result(ctx, argStr(args, "run_id"), resultParams(args))
		return resultOrNil(res, err)
	case "runs.items":
		limit, _ := asInt(args["limit"])
		follow, _ := argBool(args, "follow")
		it := c.Runs.Items(argStr(args, "run_id"), &ItemsParams{Limit: limit, Follow: follow, PollInterval: argSeconds(args, "poll_interval_seconds")})
		items, err := it.All(ctx)
		if err != nil {
			return nil, err
		}
		return asGeneric(items), nil
	case "runs.list":
		limit, _ := asInt(args["limit"])
		page, err := c.Runs.List(ctx, &RunListParams{SourceID: argStr(args, "source_id"), Status: RunStatus(argStr(args, "status")), Limit: limit})
		if err != nil {
			return nil, err
		}
		return idsOf(page.Runs), nil
	case "runs.iter":
		limit, _ := asInt(args["limit"])
		runs, err := c.Runs.Iter(&RunListParams{SourceID: argStr(args, "source_id"), Status: RunStatus(argStr(args, "status")), Limit: limit}).All(ctx)
		if err != nil {
			return nil, err
		}
		return idsOf(runs), nil
	case "sources.run", "sources.start", "sources.stream":
		p := &SourceRunParams{Input: argMap(args, "input"), MaxItems: intPtr(args, "max_items"), ExpectedVersion: argStr(args, "expected_version"),
			DefinitionVersion: argStr(args, "definition_version"), IdempotencyKey: argStr(args, "idempotency_key")}
		switch op {
		case "sources.run":
			res, err := c.Sources.Run(ctx, argStr(args, "source_id"), p)
			return resultOrNil(res, err)
		case "sources.start":
			run, err := c.Sources.Start(ctx, argStr(args, "source_id"), p)
			return runOrNil(run, err)
		}
		st, err := c.Sources.Stream(ctx, argStr(args, "source_id"), p)
		if err != nil {
			return nil, err
		}
		defer st.Close()
		var events []any
		for st.Next() {
			events = append(events, asGeneric(st.Event().Raw))
		}
		return events, st.Err()
	case "sources.get_run":
		d, err := c.Sources.GetRun(ctx, argStr(args, "source_id"), argStr(args, "run_id"))
		return rawOrNil(d, err, func() Object { return d.Raw })
	case "sources.get_run_result":
		res, err := c.Sources.GetRunResult(ctx, argStr(args, "source_id"), argStr(args, "run_id"), resultParams(args))
		return resultOrNil(res, err)
	case "sources.versions":
		limit, _ := asInt(args["limit"])
		v, err := c.Sources.Versions(ctx, argStr(args, "source_id"), limit, "")
		return rawOrNil(v, err, func() Object { return v.Raw })
	case "sources.version_contract":
		v, err := c.Sources.VersionContract(ctx, argStr(args, "source_id"), argStr(args, "definition_version"))
		return rawOrNil(v, err, func() Object { return v.Raw })
	case "sources.connection":
		v, err := c.Sources.Connection(ctx, argStr(args, "source_id"))
		return rawOrNil(v, err, func() Object { return v.Raw })
	case "sources.health":
		v, err := c.Sources.Health(ctx, argStr(args, "source_id"))
		return rawOrNil(v, err, func() Object { return v.Raw })
	case "schedules.create":
		p := &ScheduleParams{APIID: argStr(args, "api_id"), EndpointID: argStr(args, "endpoint_id"), SourceID: argStr(args, "source_id"),
			Input: argMap(args, "input"), MaxItems: intPtr(args, "max_items"), Every: argStr(args, "every"), AtUTC: argStr(args, "at_utc"),
			Name: argStr(args, "name"), IdempotencyKey: argStr(args, "idempotency_key")}
		s, err := c.Schedules.Create(ctx, p)
		return scheduleOrNil(s, err)
	case "schedules.get":
		s, err := c.Schedules.Get(ctx, argStr(args, "schedule_id"))
		return scheduleOrNil(s, err)
	case "schedules.pause":
		s, err := c.Schedules.Pause(ctx, argStr(args, "schedule_id"))
		return scheduleOrNil(s, err)
	case "schedules.resume":
		s, err := c.Schedules.Resume(ctx, argStr(args, "schedule_id"))
		return scheduleOrNil(s, err)
	case "schedules.list":
		l, err := c.Schedules.List(ctx)
		if err != nil {
			return nil, err
		}
		out := generic{"limit": l.Limit}
		var list []any
		for i := range l.Schedules {
			list = append(list, scheduleOutcome(&l.Schedules[i]))
		}
		out["schedules"] = list
		return out, nil
	case "schedules.delete":
		d, err := c.Schedules.Delete(ctx, argStr(args, "schedule_id"))
		if err != nil {
			return nil, err
		}
		return generic{"schedule_id": d.ScheduleID, "deleted": d.Deleted}, nil
	}
	return nil, fmt.Errorf("conformance harness: unknown operation %q", op)
}

func idsOf(runs []Run) []any {
	out := make([]any, len(runs))
	for i := range runs {
		out[i] = runs[i].RunID
	}
	return out
}

func resultOrNil(r *RunResult, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return resultOutcome(r), nil
}

func runOrNil(r *Run, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return runOutcome(r), nil
}

func scheduleOrNil(s *Schedule, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return scheduleOutcome(s), nil
}

func rawOrNil(v any, err error, raw func() Object) (any, error) {
	if err != nil {
		return nil, err
	}
	return raw(), nil
}

// errorFields describes err in the neutral vocabulary of the conformance cases.
func errorFields(err error) generic {
	out := generic{"kind": "unknown: " + err.Error()}
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		out = generic{"kind": string(apiErr.Kind), "status_code": apiErr.StatusCode, "code": apiErr.Code, "run_failed": apiErr.RunFailed(),
			"replayed": apiErr.Replayed(), "unsure": apiErr.Unsure(), "run_id": apiErr.RunID(), "error_code": apiErr.ErrorCode(),
			"idempotency_key": apiErr.IdempotencyKey}
		if apiErr.HasRetryAfter() {
			out["retry_after"] = apiErr.RetryAfter.Seconds()
		}
		if apiErr.Kind == KindInsufficientCredit {
			out["required"], out["available"] = apiErr.Required(), apiErr.Available()
		}
		if apiErr.Kind == KindVersionNotFound {
			versions := make([]any, 0)
			for _, v := range apiErr.AvailableVersions() {
				versions = append(versions, v)
			}
			out["available_versions"] = versions
		}
	case errors.Is(err, ErrConnection):
		var conn *ConnectionError
		errors.As(err, &conn)
		out = generic{"kind": string(KindConnection), "unsure": IsUnsure(err), "idempotency_key": conn.IdempotencyKey}
	case errors.Is(err, ErrMissingAPIKey):
		out = generic{"kind": string(KindMissingAPIKey)}
	case errors.Is(err, ErrInvalidArgument):
		out = generic{"kind": string(KindInvalidArgument)}
	}
	return out
}

func TestKindSentinels(t *testing.T) {
	for _, kind := range []struct {
		err  *APIError
		want error
	}{
		{newAPIError(401, nil, http.Header{}, ""), ErrAuthentication},
		{newAPIError(429, []byte(`{"detail":{"code":"engine_request_limited","retry_after_seconds":3}}`), http.Header{}, ""), ErrRateLimited},
		{newAPIError(409, []byte(`{"detail":{"code":"delivery_identity_conflict"}}`), http.Header{}, ""), ErrIdempotencyConflict},
		{newAPIError(404, []byte(`{"detail":{"code":"catalog_version_not_found","available":[1,2]}}`), http.Header{}, ""), ErrVersionNotFound},
	} {
		if !errors.Is(kind.err, kind.want) {
			t.Errorf("%v is not %v", kind.err, kind.want)
		}
	}
	e := newAPIError(404, []byte(`{"detail":{"code":"catalog_version_not_found","available":[1,2]}}`), http.Header{}, "")
	if got := e.AvailableVersions(); !reflect.DeepEqual(got, []int{1, 2}) || !NotFound(e) {
		t.Errorf("versions %v", got)
	}
	if d := newAPIError(429, []byte(`{"detail":{"retry_after_seconds":3}}`), http.Header{}, ""); !d.HasRetryAfter() || math.Abs(d.RetryAfter.Seconds()-3) > 1e-9 {
		t.Errorf("retry after %v", d.RetryAfter)
	}
}
