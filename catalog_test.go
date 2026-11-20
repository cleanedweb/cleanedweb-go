package cleanedweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	apiID      = "generated_047a8ebb25911ccf62f4a212"
	endpointID = "generated_047a8ebb25911ccf62f4a212_search"
)

func catalogItem(id, name, domain string) map[string]any {
	return map[string]any{"api_id": id, "name": name, "short_description": name + " records", "category": "uncategorized",
		"site":      map[string]any{"name": name, "domain": domain},
		"endpoints": []any{map[string]any{"endpoint_id": id + "_search", "name": "Extract records", "status": "available"}}}
}

func TestListSendsLimitOffsetAndFilters(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		want := url.Values{"limit": {"5"}, "offset": {"10"}, "q": {"shoes"}, "category": {"commerce"}, "sort": {"name"}}
		if r.URL.Path != "/v1/catalog/public/apis" || !reflect.DeepEqual(r.URL.Query(), want) {
			t.Errorf("request %s", r.URL.RequestURI())
		}
		writeJSON(w, 200, map[string]any{"items": []any{catalogItem("a1", "Shoes", "shoes.example")}})
	}, WithAPIKey(""))
	apis, err := c.Catalog.List(context.Background(), &ListParams{Q: "shoes", Category: "commerce", Sort: "name", Limit: 5, Offset: 10})
	if err != nil || len(apis) != 1 || apis[0].APIID != "a1" || apis[0].Site.Domain != "shoes.example" || apis[0].Endpoints[0].Status != "available" {
		t.Fatalf("apis %+v err %v", apis, err)
	}
	if apis[0].Raw["api_id"] != "a1" {
		t.Errorf("Raw = %v", apis[0].Raw)
	}
}

func TestIterPagesUntilAShortPage(t *testing.T) {
	var offsets []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset+"/"+r.URL.Query().Get("limit"))
		var items []any
		switch offset {
		case "0":
			items = []any{catalogItem("a1", "One", "one.example"), catalogItem("a2", "Two", "two.example")}
		case "2":
			items = []any{catalogItem("a3", "Three", "three.example")}
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	apis, err := c.Catalog.Iter(&ListParams{Limit: 2}).All(context.Background())
	if err != nil || len(apis) != 3 || strings.Join(offsets, ",") != "0/2,2/2" {
		t.Fatalf("apis %d offsets %v err %v", len(apis), offsets, err)
	}
}

func contractBody() map[string]any {
	return map[string]any{
		"endpoint_id": endpointID, "definition_version": strings.Repeat("e", 64), "method": "POST",
		"path":          "/v1/catalog/public/apis/" + apiID + "/endpoints/" + endpointID + "/run",
		"input_schema":  map[string]any{"type": "object", "required": []any{"topic"}, "properties": map[string]any{"topic": map[string]any{"type": "string"}}},
		"output_schema": map[string]any{"properties": map[string]any{"url": map[string]any{}, "title": map[string]any{}, "stars": map[string]any{}}},
		"examples":      []any{map[string]any{"kind": "saved_validation_records"}},
	}
}

func TestGetReadsDetailAndEachContract(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/public/apis/" + apiID:
			item := catalogItem(apiID, "GitHub Topics Repositories", "github.com")
			item["endpoints"].([]any)[0].(map[string]any)["endpoint_id"] = endpointID
			writeJSON(w, 200, item)
		case "/v1/catalog/public/apis/" + apiID + "/endpoints/" + endpointID + "/contract":
			writeJSON(w, 200, contractBody())
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}, WithAPIKey(""))
	contract, err := c.Catalog.Get(context.Background(), apiID)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := contract.Endpoint("")
	if err != nil || ep.Name != "Extract records" || !reflect.DeepEqual(ep.RequiredInputs(), []string{"topic"}) ||
		!reflect.DeepEqual(ep.OutputFields(), []string{"stars", "title", "url"}) || ep.DefinitionVersion != strings.Repeat("e", 64) {
		t.Errorf("endpoint %+v", ep)
	}
	if _, err := contract.Endpoint("nope"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("unknown endpoint: %v", err)
	}
}

func TestGetResolvesASlugThroughTheIndex(t *testing.T) {
	var detailCalls []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/catalog/index.json":
			writeJSON(w, 200, map[string]any{"apis": []any{map[string]any{"api_id": apiID, "slug": "github-topics-repositories-api", "key_fields": []any{"title", "url"}}}})
		case r.URL.Path == "/v1/catalog/public/apis/github-topics-repositories-api":
			detailCalls = append(detailCalls, "slug")
			writeJSON(w, 404, map[string]any{"detail": map[string]any{"code": "catalog_not_found"}})
		case r.URL.Path == "/v1/catalog/public/apis/"+apiID:
			detailCalls = append(detailCalls, "id")
			item := catalogItem(apiID, "GitHub", "github.com")
			item["endpoints"] = []any{}
			writeJSON(w, 200, item)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	contract, err := c.Catalog.Get(context.Background(), "github-topics-repositories-api")
	if err != nil || contract.API.APIID != apiID || strings.Join(detailCalls, ",") != "slug,id" {
		t.Fatalf("contract %+v calls %v err %v", contract, detailCalls, err)
	}
}

func TestContractPinsAMajor(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("version") != "2" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		writeJSON(w, 404, map[string]any{"detail": map[string]any{"code": "catalog_version_not_found", "available": []any{1}}})
	})
	_, err := c.Catalog.Contract(context.Background(), apiID, endpointID, Int(2))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !reflect.DeepEqual(apiErr.AvailableVersions(), []int{1}) {
		t.Fatalf("err %v", err)
	}
}

func TestFindFiltersBySiteAndFields(t *testing.T) {
	var contractReads atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/catalog/public/apis":
			writeJSON(w, 200, map[string]any{"items": []any{
				catalogItem("gh1", "GitHub Topics", "github.com"),
				catalogItem("gh2", "GitHub Issues", "www.github.com"),
				catalogItem("gh3", "GitHub Users", "github.com"),
				catalogItem("sh1", "Shoes", "shoes.example"),
			}})
		case "/catalog/index.json":
			writeJSON(w, 200, map[string]any{"apis": []any{
				map[string]any{"api_id": "gh1", "slug": "github-topics", "key_fields": []any{"title", "stars"}},
				map[string]any{"api_id": "gh2", "slug": "github-issues", "key_fields": []any{"title"}},
			}})
		case "/v1/catalog/public/apis/gh2", "/v1/catalog/public/apis/gh3":
			item := catalogItem(strings.TrimPrefix(r.URL.Path, "/v1/catalog/public/apis/"), "x", "github.com")
			writeJSON(w, 200, item)
		case "/v1/catalog/public/apis/gh2/endpoints/gh2_search/contract":
			contractReads.Add(1)
			writeJSON(w, 200, map[string]any{"endpoint_id": "gh2_search", "output_schema": map[string]any{"properties": map[string]any{"title": map[string]any{}, "STARS": map[string]any{}}}})
		case "/v1/catalog/public/apis/gh3/endpoints/gh3_search/contract":
			contractReads.Add(1)
			writeJSON(w, 200, map[string]any{"endpoint_id": "gh3_search", "output_schema": map[string]any{"properties": map[string]any{"login": map[string]any{}}}})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	apis, err := c.Catalog.Find(ctx, &FindParams{Site: "https://www.GitHub.com/topics"})
	if err != nil || len(apis) != 3 || apis[0].Slug != "github-topics" || !reflect.DeepEqual(apis[0].KeyFields, []string{"title", "stars"}) {
		t.Fatalf("site filter: %d %v", len(apis), err)
	}
	apis, err = c.Catalog.Find(ctx, &FindParams{Site: "github.com", Fields: []string{"title", "stars"}})
	ids := []string{}
	for _, a := range apis {
		ids = append(ids, a.APIID)
	}
	if err != nil || strings.Join(ids, ",") != "gh1,gh2" || contractReads.Load() != 2 {
		t.Fatalf("fields: %v reads %d err %v", ids, contractReads.Load(), err)
	}
	apis, _ = c.Catalog.Find(ctx, &FindParams{Site: "github.com", Fields: []string{"title", "stars"}, NoDeep: true})
	if len(apis) != 1 || apis[0].APIID != "gh1" {
		t.Errorf("NoDeep: %v", apis)
	}
}

func TestFiltersSendsAJSONObject(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var filters map[string]any
		_ = json.Unmarshal([]byte(q.Get("filters")), &filters)
		if q.Get("search") != "leica" || filters["brand"] != "Leica" || r.Header.Get("Authorization") != "Bearer cw_test_key" {
			t.Errorf("request %s auth %q", r.URL.RawQuery, r.Header.Get("Authorization"))
		}
		writeJSON(w, 200, map[string]any{"categories": []any{"cameras"}})
	})
	out, err := c.Catalog.Filters(context.Background(), apiID, endpointID, &FiltersParams{Search: "leica", Filters: map[string]any{"brand": "Leica"}})
	if err != nil || fmt.Sprint(out["categories"]) != "[cameras]" {
		t.Fatalf("out %v err %v", out, err)
	}
}

func TestFiltersUnavailableKeepsTheErrorEnvelopeCode(t *testing.T) {
	// The live /filters 404 wraps its error in "error", not "detail" (cleanedweb-openapi FINDINGS 12).
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "category_filters_unavailable",
			"details": map[string]any{"reason": "no_category_filters_evidence"},
			"message": "This API has no category filters to describe."}})
	})
	_, err := c.Catalog.Filters(context.Background(), apiID, endpointID, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "category_filters_unavailable" ||
		apiErr.Message != "This API has no category filters to describe." || !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %#v", err)
	}
}

func TestRunsItemsFollowsCursorsWithNulls(t *testing.T) {
	var queries []string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("cursor") == "0" {
			writeJSON(w, 200, map[string]any{"run_id": "r", "items": []any{map[string]any{"n": 1}}, "count": 1, "next_cursor": "1", "run_status": "running"})
			return
		}
		writeJSON(w, 200, map[string]any{"run_id": "r", "items": []any{map[string]any{"n": 2}}, "count": 1, "next_cursor": nil, "run_status": "finished"})
	})
	items, err := c.Runs.Items(strings.Repeat("a", 32), &ItemsParams{Limit: 1, Nulls: true}).All(context.Background())
	if err != nil || len(items) != 2 || strings.Join(queries, "|") != "cursor=0&limit=1&nulls=true|cursor=1&limit=1&nulls=true" {
		t.Fatalf("items %v queries %v err %v", items, queries, err)
	}
}

func TestScheduleParamsAreValidatedBeforeSending(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("a request was sent") })
	for name, p := range map[string]*ScheduleParams{
		"nil":          nil,
		"no target":    {Every: EveryDay},
		"both targets": {APIID: "a", EndpointID: "e", SourceID: "s", Every: EveryDay},
		"half catalog": {APIID: "a", Every: EveryDay},
		"no cadence":   {SourceID: "s"},
		"bad max":      {SourceID: "s", Every: EveryDay, MaxItems: Int(0)},
	} {
		if _, err := c.Schedules.Create(context.Background(), p); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: err %v", name, err)
		}
	}
}
