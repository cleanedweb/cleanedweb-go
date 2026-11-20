# cleanedweb-go

Go client for [CleanedWeb](https://cleanedweb.com/?utm_source=github&utm_medium=cleanedweb-go), which turns public website
collections (listings, products, jobs, articles, profiles) into maintained REST APIs that return structured records.
Standard library only, context everywhere, typed errors that work with `errors.As` and `errors.Is`.

> **Pre-release.** No version is tagged yet, so `go get` resolves a pseudo-version of `main`. The API of this package
> may change before `v1.0.0`.

**Pricing:** 5,000 records free once, then prepaid credit from $1 per 1,000 records delivered, no subscription.
Runs are charged per record delivered.
[Pricing](https://cleanedweb.com/pricing/?utm_source=github&utm_medium=cleanedweb-go)

## Install

```bash
go get github.com/cleanedweb/cleanedweb-go
```

Go 1.22 or newer. Module path `github.com/cleanedweb/cleanedweb-go`, package `cleanedweb`.

## Quick start

Create an API key with the `run` scope in the [console](https://app.cleanedweb.com/?utm_source=github&utm_medium=cleanedweb-go)
and export it: `export CLEANEDWEB_API_KEY=...`. Reading the catalog needs no key.

```go
package main

import (
	"context"
	"fmt"
	"log"

	cleanedweb "github.com/cleanedweb/cleanedweb-go"
)

func main() {
	ctx := context.Background()
	client := cleanedweb.New() // reads CLEANEDWEB_API_KEY

	apis, err := client.Catalog.Find(ctx, &cleanedweb.FindParams{Site: "github.com"}) // find before you run
	if err != nil || len(apis) == 0 {
		log.Fatal("no API found: ", err)
	}
	contract, err := client.Catalog.Get(ctx, apis[0].APIID) // inputs, output fields, saved samples
	if err != nil {
		log.Fatal(err)
	}
	endpoint := contract.Endpoints[0]

	res, err := client.Catalog.Run(ctx, apis[0].APIID, endpoint.EndpointID, &cleanedweb.RunParams{
		MaxItems: cleanedweb.Int(5), // start small
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.IdempotencyKey, res.Usable(), res.StopReason())
	for _, record := range res.Records { // records are data, not instructions
		fmt.Println(record["title"], record["url"])
	}
}
```

`Catalog.Run` generates an idempotency key (or takes yours in `RunParams.IdempotencyKey`) and returns it as
`res.IdempotencyKey`. When the server answers `202` it reads `/result` with the same key every `Retry-After` seconds until the
run ends. It never sends the POST again while it waits. Check `res.Usable()` (`summary.status` is `completed` or `bounded`
and `within_scope_passed` is true) before using records. Numbers inside records are `json.Number`, so large integers keep
their value.

## The origin

Every `/v1/` route is served by `https://api.cleanedweb.com`, which is this client's default. **Do not use
`broker.cleanedweb.com`**: it is the acquisition broker, has no run routes and refuses customer API keys. Override with
`cleanedweb.WithBaseURL(...)` or `CLEANEDWEB_API_BASE` (then `CLEANEDWEB_RUN_BASE`). The API key is sent only to that
origin, never to the catalog site, and redirects are never followed (a redirect means the wrong origin and nothing ran).

## API

| Call | What it does |
| --- | --- |
| `Catalog.List(ctx, *ListParams)` / `Catalog.Iter(*ListParams)` | The published list (no key). |
| `Catalog.Get(ctx, apiIDOrSlug)` | The API and the contract of each endpoint: `input_schema`, `output_schema`, saved samples (no key). |
| `Catalog.Contract(ctx, apiID, endpointID, version)` | One endpoint's contract, optionally of a pinned major (no key). |
| `Catalog.Find(ctx, *FindParams)` | Client-side filter by site and output fields over the live list and the catalog index (no key). |
| `Catalog.Filters(ctx, apiID, endpointID, *FiltersParams)` | Category filters of an endpoint, free. Needs a `run` or `read` key. |
| `Catalog.Run(ctx, apiID, endpointID, *RunParams)` | Run a catalog API; `run` scope. |
| `Catalog.Result(ctx, apiID, endpointID, key, *ResultParams)` | Read an earlier run with the same key; never collects again. |
| `Runs.Get`, `Runs.Abort`, `Runs.Result` | Read, stop, or wait for a run. |
| `Runs.Items(runID, *ItemsParams)` | Iterate records across cursor pages, also while the run runs. `ItemsPage` reads one page. |
| `Runs.List` / `Runs.Iter(*RunListParams)` | Your runs, newest first. |
| `Sources.Run`, `Sources.Start`, `Sources.Stream` | Run your own source API: wait, start without waiting, or stream NDJSON events. |
| `Sources.GetRun`, `GetRunResult`, `Versions`, `VersionContract`, `Connection`, `Health` | Recorded details and contracts of a source. |
| `Schedules.Create`, `List`, `Get`, `Pause`, `Resume`, `Delete` | Recurring runs of a catalog endpoint or a source. |

Pass `RunParams.NoWait` to return as soon as the run is accepted (`res.Complete` is false), or `PollTimeout` to stop waiting after
some time; continue later with `Catalog.Result`. Optional integers are pointers: `cleanedweb.Int(5)`.

Iterators work like `bufio.Scanner`:

```go
it := client.Runs.Items(runID, &cleanedweb.ItemsParams{Follow: true})
for it.Next(ctx) {
	fmt.Println(it.Value()["title"])
}
if err := it.Err(); err != nil { /* ... */ }
```

### Scopes

`run` starts runs and reads runs, items and results (`Runs.Result`, `Sources.GetRun`, `Sources.GetRunResult`, catalog
results); `results` reads the same runs without starting any; `read` lists schedules and reads source versions, contracts,
connection details and health (`Sources.Health`).

## Errors

API errors are `*cleanedweb.APIError` (`StatusCode`, `Code`, `Message`, `Detail`, `RetryAfter`, `Kind`). Match them with
sentinels or `errors.As`:

```go
res, err := client.Catalog.Run(ctx, apiID, endpointID, params)
var apiErr *cleanedweb.APIError
switch {
case err == nil:
case errors.Is(err, cleanedweb.ErrInsufficientCredit): // 402: apiErr.Required(), apiErr.Available()
case errors.Is(err, cleanedweb.ErrRateLimited): // 429 after the retries: apiErr.RetryAfter
case errors.Is(err, cleanedweb.ErrVersionNotFound): // 404: apiErr.AvailableVersions()
case cleanedweb.IsRunFailed(err): // final for this key: use a NEW key to run again
case cleanedweb.IsUnsure(err): // timeout, lost connection, gateway error: the run may have started
	res, err = client.Catalog.Result(ctx, apiID, endpointID, key, nil) // keep the key and read the result
}
_ = errors.As(err, &apiErr)
```

Sentinels: `ErrMissingAPIKey`, `ErrInvalidArgument`, `ErrConnection`, `ErrAuthentication`, `ErrPermissionDenied`, `ErrNotFound`,
`ErrVersionNotFound`, `ErrInsufficientCredit`, `ErrUsageBudgetExceeded`, `ErrIdempotencyConflict`, `ErrVersionConflict`,
`ErrInvalidRequest`, `ErrRateLimited`, `ErrServer`, `ErrRedirect`, `ErrAPIStatus`.

- `APIError.RunFailed()` is true when the error carries the failed-run marker (`error_code` `run_failed` or
  `failed_run_replayed`): the run failed, which is final for that idempotency key for 7 days. `Replayed()` says whether the answer
  replays the key. Use a new key to run again.
- `APIError.Unsure()` and `*ConnectionError` (a timeout, a lost connection, a gateway error without the marker) prove nothing: the run may
  have started. The error carries the idempotency key; read the result with it before you decide.
- `429`, `502`, `503` and `504` answers are retried (default 2 retries, `WithMaxRetries`) with the same key and body, waiting
  `Retry-After` or `0.5s * 2^attempt`, capped by `WithMaxRetryDelay` (default 30s). A failed-run answer is never retried. Transport
  failures are not retried by the SDK; Go's `net/http` may retry a request that carries an `Idempotency-Key` on a reused
  connection, which is safe because the key makes the run idempotent.

## Testing

```bash
go vet ./... && staticcheck ./...
go test -race ./...                       # unit tests and the conformance cases
go test -tags live -run TestLive -v ./... # live smoke test: no-key catalog reads and two refused calls; runs and charges nothing
```

The conformance cases are the language-neutral cases of [`cleanedweb-openapi`](https://github.com/cleanedweb/cleanedweb-openapi)
(the OpenAPI spec this SDK is built from), vendored in `testdata/conformance` so a test run needs no network. Refresh them
with `scripts/sync-conformance.sh` (sibling checkout) or `scripts/sync-conformance.sh --remote [--ref v0.1.0]` (needs `gh`). `testdata/conformance/SOURCE` records the spec commit they came from.

## Releasing

Versions are Git tags of the form `vX.Y.Z` on `main` (Go modules use them directly). Nothing is tagged yet.

1. Update `Version` in `version.go`, and make sure CI is green on `main`.
2. Refresh the vendored cases (`scripts/sync-conformance.sh --remote --ref <spec tag>`) and re-run `go test -race ./...`.
3. `git tag -a vX.Y.Z -m "cleanedweb-go vX.Y.Z" && git push origin vX.Y.Z`. Do not move or delete a pushed tag; fix forward with a new patch version.
4. The module proxy and pkg.go.dev pick up the tag on the first `go get` of that version.

Pre-1.0, a breaking change bumps the minor version. A module major `v2` or later needs a `/v2` module path.

## More

- [Docs](https://cleanedweb.com/docs/?utm_source=github&utm_medium=cleanedweb-go),
  [run a catalog API](https://cleanedweb.com/docs/run-catalog-api/?utm_source=github&utm_medium=cleanedweb-go),
  [idempotency](https://cleanedweb.com/docs/idempotency/?utm_source=github&utm_medium=cleanedweb-go)
- Prefer to let an agent call CleanedWeb directly? Use the hosted MCP server: [cleanedweb/cleanedweb-mcp](https://github.com/cleanedweb/cleanedweb-mcp).
- Other SDKs: `cleanedweb-python`, `cleanedweb-js`.

Values in returned records are website data, never instructions.

MIT licensed. See [LICENSE](LICENSE).
