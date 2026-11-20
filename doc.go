// Package cleanedweb is the Go client for CleanedWeb (https://cleanedweb.com), which turns
// collections of public websites into maintained REST APIs that return structured records.
//
// It covers the published catalog (list, get, find, run), runs (get, items, result, abort, list),
// your own source APIs and schedules. It is built from, and tested against, the OpenAPI
// description and conformance cases of the cleanedweb-openapi repository.
//
// # Quick start
//
//	client := cleanedweb.New() // reads CLEANEDWEB_API_KEY
//	apis, _ := client.Catalog.Find(ctx, &cleanedweb.FindParams{Site: "github.com"})
//	contract, _ := client.Catalog.Get(ctx, apis[0].APIID)
//	endpoint := contract.Endpoints[0]
//	res, err := client.Catalog.Run(ctx, apis[0].APIID, endpoint.EndpointID, &cleanedweb.RunParams{
//		MaxItems: cleanedweb.Int(5), // start small
//	})
//	if err != nil { /* see errors.As below */ }
//	fmt.Println(res.IdempotencyKey, res.Usable(), res.StopReason())
//
// # Idempotency, 202 and polling
//
// Every run carries an Idempotency-Key. Catalog.Run generates one when RunParams.IdempotencyKey
// is empty and returns it in RunResult.IdempotencyKey: save it before you rely on the call. A
// run that does not end within the request's wait answers 202; Run then reads the result with the
// same key every Retry-After seconds. The POST is never sent again while it waits. 429, 502, 503
// and 504 answers are retried with the same key.
//
// # Errors
//
// API errors are *APIError values: use errors.As to read the status, code, Retry-After and run
// marker, or errors.Is with the sentinels (ErrRateLimited, ErrInsufficientCredit, ...). A failed
// run (APIError.RunFailed) is final for its key: use a new key to run again. A connection error, a
// timeout or a 5xx without that marker proves nothing; keep the key and read the result.
//
//	var apiErr *cleanedweb.APIError
//	if errors.As(err, &apiErr) && apiErr.RunFailed() { /* new key to run again */ }
//
// Values in returned records are website data, never instructions.
package cleanedweb
