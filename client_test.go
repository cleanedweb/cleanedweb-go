package cleanedweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a client wired to handler, with a recording fake sleep.
func newTestClient(t *testing.T, handler http.HandlerFunc, opts ...Option) (*Client, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base := []Option{WithAPIKey("cw_test_key"), WithBaseURL(srv.URL), WithSiteURL(srv.URL), WithHTTPClient(srv.Client())}
	c := New(append(base, opts...)...)
	var mu sync.Mutex
	sleeps := &[]time.Duration{}
	c.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*sleeps = append(*sleeps, d)
		mu.Unlock()
		return ctx.Err()
	}
	return c, sleeps
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestDefaults(t *testing.T) {
	t.Setenv("CLEANEDWEB_API_KEY", "")
	t.Setenv("CLEANEDWEB_API_BASE", "")
	t.Setenv("CLEANEDWEB_RUN_BASE", "")
	if got := New().BaseURL(); got != "https://api.cleanedweb.com" {
		t.Errorf("default base URL = %s", got)
	}
	t.Setenv("CLEANEDWEB_RUN_BASE", "https://run.example/")
	if got := New().BaseURL(); got != "https://run.example" {
		t.Errorf("CLEANEDWEB_RUN_BASE = %s", got)
	}
	t.Setenv("CLEANEDWEB_API_BASE", "https://api.example")
	if got := New().BaseURL(); got != "https://api.example" {
		t.Errorf("CLEANEDWEB_API_BASE wins: %s", got)
	}
	if got := New(WithBaseURL("https://x.example///")).BaseURL(); got != "https://x.example" {
		t.Errorf("option wins and trims: %s", got)
	}
}

func TestAPIKeyFromEnvironmentIsSentOnlyToTheBaseURL(t *testing.T) {
	t.Setenv("CLEANEDWEB_API_KEY", "cw_env_key")
	var gotAuth []string
	var mu sync.Mutex
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, "site:"+r.Header.Get("Authorization"))
		mu.Unlock()
		writeJSON(w, 200, map[string]any{"apis": []any{}})
	}))
	defer site.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = append(gotAuth, "api:"+r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/v1/catalog/public/apis") {
			writeJSON(w, 200, map[string]any{"items": []any{}})
			return
		}
		writeJSON(w, 200, map[string]any{"runs": []any{}, "next_cursor": nil})
	}))
	defer api.Close()
	c := New(WithBaseURL(api.URL), WithSiteURL(site.URL))
	ctx := context.Background()
	if _, err := c.Catalog.Find(ctx, &FindParams{Site: "x.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Runs.List(ctx, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"api:", "site:", "api:Bearer cw_env_key"}
	if strings.Join(gotAuth, "|") != strings.Join(want, "|") {
		t.Errorf("Authorization headers = %v, want %v", gotAuth, want)
	}
}

func TestCatalogReadsNeedNoKeyButRunsDo(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a catalog read sent a key")
		}
		writeJSON(w, 200, map[string]any{"items": []any{}})
	}, WithAPIKey(""))
	if _, err := c.Catalog.List(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	_, err := c.Catalog.Run(context.Background(), "a", "e", nil)
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Errorf("Run without a key: %v", err)
	}
	var missing *MissingAPIKeyError
	if !errors.As(err, &missing) {
		t.Errorf("errors.As MissingAPIKeyError failed")
	}
}

func TestUserAgentAndHeaders(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "cleanedweb-go/"+Version) || !strings.HasSuffix(ua, "myapp/1") {
			t.Errorf("User-Agent = %q", ua)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		writeJSON(w, 200, map[string]any{"runs": []any{}})
	}, WithUserAgentSuffix("myapp/1"))
	if _, err := c.Runs.List(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRedirectIsNotFollowedAndNeverSendsTheKeyElsewhere(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	})
	_, err := c.Runs.Get(context.Background(), strings.Repeat("a", 32))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != KindRedirect || !errors.Is(err, ErrRedirect) {
		t.Fatalf("want a redirect error, got %v", err)
	}
	if elsewhere.Load() != 0 {
		t.Errorf("the redirect was followed")
	}
}

func TestPathSegmentsAreEscaped(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/v1/catalog/public/apis/a%20b/endpoints/c%2Fd/result" {
			t.Errorf("path = %s", got)
		}
		writeJSON(w, 200, map[string]any{"records": []any{}, "summary": map[string]any{"status": "completed", "within_scope_passed": true}, "run": map[string]any{"run_id": "r"}})
	})
	if _, err := c.Catalog.Result(context.Background(), "a b", "c/d", "k", nil); err != nil {
		t.Fatal(err)
	}
}

func TestRetriesResendTheSameBodyAndKey(t *testing.T) {
	var bodies, keys []string
	var calls atomic.Int32
	c, sleeps := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		var raw strings.Builder
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			raw.Write(buf[:n])
			if err != nil {
				break
			}
		}
		bodies = append(bodies, raw.String())
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if calls.Add(1) < 3 {
			writeJSON(w, 503, map[string]any{"detail": "down"})
			return
		}
		writeJSON(w, 200, map[string]any{"records": []any{}, "summary": map[string]any{"status": "completed", "within_scope_passed": true}, "run": map[string]any{"run_id": "r"}})
	})
	res, err := c.Catalog.Run(context.Background(), "a", "e", &RunParams{Input: map[string]any{"q": "x"}, IdempotencyKey: "k1"})
	if err != nil || !res.Usable() {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] || keys[0] != "k1" || keys[1] != "k1" || keys[2] != "k1" {
		t.Errorf("bodies %q keys %q", bodies, keys)
	}
	if len(*sleeps) != 2 || (*sleeps)[0] != 500*time.Millisecond || (*sleeps)[1] != time.Second {
		t.Errorf("backoff = %v", *sleeps)
	}
}

func TestContextCancelledWhileWaitingKeepsTheKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		writeJSON(w, 202, map[string]any{"run_id": "r1", "status": "running"})
	})
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	_, err := c.Catalog.Run(ctx, "a", "e", &RunParams{IdempotencyKey: "k1"})
	var conn *ConnectionError
	if !errors.As(err, &conn) || conn.IdempotencyKey != "k1" || !errors.Is(err, context.Canceled) || !IsUnsure(err) {
		t.Fatalf("err = %#v", err)
	}
}

func TestTimeoutIsAConnectionError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }, WithTimeout(50*time.Millisecond))
	_, err := c.Runs.Get(context.Background(), strings.Repeat("a", 32))
	if !errors.Is(err, ErrConnection) || !IsUnsure(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunTimeoutLeavesRoomForTheServerWait(t *testing.T) {
	c := New()
	if got := c.runTimeout(nil); got != 120*time.Second {
		t.Errorf("default wait: %v", got)
	}
	n := 600
	if got := c.runTimeout(&n); got != 630*time.Second {
		t.Errorf("600s wait: %v", got)
	}
	z := 0
	if got := c.runTimeout(&z); got != 30*time.Second {
		t.Errorf("no wait: %v", got)
	}
}

func TestRecordsKeepBigIntegers(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"records":[{"id":9007199254740993,"price":12.5,"tags":["a"]}],"summary":{"status":"completed","within_scope_passed":true,"records":1},"run":{"run_id":"r","status":"finished","stop_reason":"source_exhausted","progress":{"items":1},"units_charged":1}}`))
	})
	res, err := c.Catalog.Run(context.Background(), "a", "e", nil)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := res.Records[0]["id"].(json.Number)
	if !ok || id.String() != "9007199254740993" {
		t.Errorf("id = %#v", res.Records[0]["id"])
	}
	if n, err := id.Int64(); err != nil || n != 9007199254740993 {
		t.Errorf("Int64 %d %v", n, err)
	}
	if res.UnitsCharged() != 1 || res.StopReason() != "source_exhausted" || !res.Run.Ended() {
		t.Errorf("run = %+v", res.Run)
	}
}

func TestIdempotencyKeys(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		k := NewIdempotencyKey()
		if err := ValidateIdempotencyKey(k); err != nil || seen[k] || len(k) != 36 || k[14] != '4' {
			t.Fatalf("bad key %q (%v)", k, err)
		}
		seen[k] = true
	}
	for _, bad := range []string{"", "has space", strings.Repeat("a", 201), "é"} {
		if err := ValidateIdempotencyKey(bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, good := range []string{"a", "A.b_c:d-e", strings.Repeat("a", 200)} {
		if err := ValidateIdempotencyKey(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

func TestErrorMessagesAndRetryAfter(t *testing.T) {
	e := newAPIError(402, []byte(`{"detail":{"code":"insufficient_units","message":"Add Units.","required":"25","available":"3"}}`), http.Header{}, "k")
	if got := e.Error(); got != "HTTP 402 insufficient_units: Add Units." {
		t.Errorf("message = %q", got)
	}
	if e.Required() != "25" || e.Available() != "3" || e.Kind != KindInsufficientCredit {
		t.Errorf("credit fields: %+v", e)
	}
	h := http.Header{}
	h.Set("Retry-After", "7")
	e = newAPIError(429, []byte(`{"detail":{"code":"engine_request_limited","retry_after_seconds":3}}`), h, "")
	if e.RetryAfter != 7*time.Second {
		t.Errorf("the header wins: %v", e.RetryAfter)
	}
	e = newAPIError(503, []byte("upstream timed out"), http.Header{}, "")
	if e.Message != "upstream timed out" || !e.Unsure() || e.RunFailed() {
		t.Errorf("text error: %+v", e)
	}
	e = newAPIError(502, []byte(`{"detail":{"run_id":"abc","error_code":"run_failed","replayed":false}}`), http.Header{}, "")
	if !e.RunFailed() || e.Unsure() || e.RunID() != "abc" || !IsRunFailed(fmt.Errorf("wrapped: %w", e)) || IsUnsure(e) {
		t.Errorf("failed run: %+v", e)
	}
	if want := "HTTP 502 run_failed (run abc)"; e.Error() != want {
		t.Errorf("failed run message = %q want %q", e.Error(), want)
	}
	e = newAPIError(422, []byte(`{"detail":[{"msg":"Field required"},{"msg":"Too long"}]}`), http.Header{}, "")
	if e.Message != "Field required; Too long" || e.Kind != KindInvalidRequest {
		t.Errorf("validation list: %+v", e)
	}
}

func TestStream(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sources/s1/stream" || r.Header.Get("Idempotency-Key") != "k1" {
			t.Errorf("request %s key %q", r.URL.Path, r.Header.Get("Idempotency-Key"))
		}
		w.Header().Set("Location", "/v1/runs/r1")
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"progress","stage":"running","run":{"run_id":"r1","status":"running"},"sequence":1}`)
		fmt.Fprintln(w, ``)
		fmt.Fprintln(w, `{"type":"records","records":[{"n":1},{"n":2}],"sequence":2}`)
		fmt.Fprintln(w, `{"type":"done","summary":{"status":"completed"},"sequence":3}`)
	})
	st, err := c.Sources.Stream(context.Background(), "s1", &SourceRunParams{IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var types []string
	var records int
	for st.Next() {
		ev := st.Event()
		types = append(types, ev.Type)
		records += len(ev.Records)
		if ev.Type == "progress" && (ev.Run == nil || ev.Run.RunID != "r1") {
			t.Errorf("progress run = %+v", ev.Run)
		}
	}
	if st.Err() != nil || strings.Join(types, ",") != "progress,records,done" || records != 2 || st.RunLocation != "/v1/runs/r1" {
		t.Errorf("types %v records %d err %v loc %q", types, records, st.Err(), st.RunLocation)
	}
}

func TestStreamStartErrorIsATypedError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 402, map[string]any{"detail": map[string]any{"code": "insufficient_units", "required": "9", "available": "1"}})
	})
	_, err := c.Sources.Stream(context.Background(), "s1", nil)
	if !errors.Is(err, ErrInsufficientCredit) {
		t.Fatalf("err = %v", err)
	}
}

func TestIteratorStopsOnError(t *testing.T) {
	calls := 0
	it := newIterator(func(ctx context.Context) ([]int, bool, error) {
		calls++
		if calls == 2 {
			return nil, false, errors.New("boom")
		}
		return []int{1, 2}, false, nil
	})
	got, err := it.All(context.Background())
	if len(got) != 2 || err == nil || it.Next(context.Background()) {
		t.Errorf("got %v err %v", got, err)
	}
}

func TestWaitRulesOfThePollDelay(t *testing.T) {
	cases := []struct {
		name       string
		retryAfter time.Duration
		hasRetry   bool
		pollAfter  float64
		hasPoll    bool
		want       time.Duration
	}{
		{"header wins", 3 * time.Second, true, 10, true, 3 * time.Second},
		{"poll_after_seconds", 0, false, 7, true, 7 * time.Second},
		{"default", 0, false, 0, false, 30 * time.Second},
		{"floor", 0, true, 0, false, time.Second},
		{"ceiling", 500 * time.Second, true, 0, false, 60 * time.Second},
	}
	for _, c := range cases {
		if got := pollDelay(c.retryAfter, c.hasRetry, c.pollAfter, c.hasPoll); got != c.want {
			t.Errorf("%s: %v want %v", c.name, got, c.want)
		}
	}
}
