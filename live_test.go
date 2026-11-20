//go:build live

package cleanedweb

// Live smoke test against production. It reads the public catalog (no key) and sends two calls that
// must be refused (no key, and a key that does not exist), so nothing is run or charged.
//
//	go test -tags live -run TestLive -v ./...
//
// Set CLEANEDWEB_API_BASE to point it at another origin.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLiveCatalogReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Setenv("CLEANEDWEB_API_KEY", "")
	c := New()
	t.Logf("origin %s", c.BaseURL())

	apis, err := c.Catalog.List(ctx, &ListParams{Limit: 5, Sort: "name"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(apis) == 0 || apis[0].APIID == "" || apis[0].Name == "" || len(apis[0].Endpoints) == 0 {
		t.Fatalf("unexpected list: %+v", apis)
	}
	t.Logf("%d APIs on the first page, first: %s (%s)", len(apis), apis[0].Name, apis[0].APIID)

	contract, err := c.Catalog.Get(ctx, apis[0].APIID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	endpoint, err := contract.Endpoint(contract.Endpoints[0].EndpointID)
	if err != nil || endpoint.DefinitionVersion == "" || !strings.HasPrefix(endpoint.Path, "/v1/catalog/public/apis/") || len(endpoint.OutputFields()) == 0 {
		t.Fatalf("contract: %+v err %v", endpoint, err)
	}
	t.Logf("endpoint %s: %d output fields, version %.12s", endpoint.EndpointID, len(endpoint.OutputFields()), endpoint.DefinitionVersion)

	all, err := c.Catalog.Iter(&ListParams{Limit: 100}).All(ctx)
	if err != nil || len(all) < len(apis) {
		t.Fatalf("iter: %d APIs err %v", len(all), err)
	}
	found, err := c.Catalog.Find(ctx, &FindParams{Site: "github.com", NoDeep: true})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	t.Logf("%d APIs in the catalog, %d for github.com", len(all), len(found))

	_, err = c.Catalog.Contract(ctx, apis[0].APIID, endpoint.EndpointID, Int(9999))
	if !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("an unknown major: %v", err)
	}
	if _, err = c.Catalog.Get(ctx, "no-such-api-id"); !NotFound(err) {
		t.Errorf("an unknown API: %v", err)
	}
}

func TestLiveRefusedCallsRunNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Setenv("CLEANEDWEB_API_KEY", "")

	// No key: refused before any request is sent.
	_, err := New().Catalog.Run(ctx, "a", "e", &RunParams{MaxItems: Int(1)})
	if !errors.Is(err, ErrMissingAPIKey) {
		t.Errorf("no key: %v", err)
	}

	// A key that does not exist: the server answers 401 and nothing runs or is charged.
	c := New(WithAPIKey("cw_invalid_key_for_the_go_sdk_smoke_test"))
	_, err = c.Catalog.Run(ctx, "a", "e", &RunParams{MaxItems: Int(1), IdempotencyKey: "go-sdk-smoke-" + NewIdempotencyKey()})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrAuthentication) {
		t.Fatalf("a bad key: %v", err)
	}
	t.Logf("bad key answered: %v", err)
	if apiErr.RunFailed() || apiErr.Unsure() {
		t.Errorf("a 401 is neither a failed run nor unsure: %+v", apiErr)
	}
	if _, err := c.Runs.List(ctx, nil); !errors.Is(err, ErrAuthentication) {
		t.Errorf("runs list with a bad key: %v", err)
	}
}
