package cleanedweb_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	cleanedweb "github.com/cleanedweb/cleanedweb-go"
)

// Find a published API for a site, read its contract and run it. The Example functions are compiled
// by `go test` but not run: they need a key and would be charged.
func Example() {
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
		Input:    map[string]any{"sort": "desc_stars"},
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

// Save the idempotency key before you rely on a run; after an uncertain outcome read the result with it.
func ExampleCatalogService_Result() {
	ctx := context.Background()
	client := cleanedweb.New()
	key := cleanedweb.NewIdempotencyKey() // save it with your job record first

	_, err := client.Catalog.Run(ctx, "api_id", "endpoint_id", &cleanedweb.RunParams{IdempotencyKey: key})
	if cleanedweb.IsUnsure(err) { // timeout, lost connection, a gateway error: the run may have started
		res, err := client.Catalog.Result(ctx, "api_id", "endpoint_id", key, nil) // never collects again
		fmt.Println(res, err)
	}
}

// Errors are *APIError values with typed sentinels.
func ExampleAPIError() {
	client := cleanedweb.New()
	_, err := client.Catalog.Run(context.Background(), "api_id", "endpoint_id", nil)

	var apiErr *cleanedweb.APIError
	switch {
	case errors.Is(err, cleanedweb.ErrInsufficientCredit):
		_ = errors.As(err, &apiErr)
		fmt.Println("add credit:", apiErr.Required(), "needed,", apiErr.Available(), "available")
	case errors.Is(err, cleanedweb.ErrRateLimited):
		_ = errors.As(err, &apiErr)
		fmt.Println("retry in", apiErr.RetryAfter)
	case cleanedweb.IsRunFailed(err):
		fmt.Println("the run failed; use a new idempotency key to run again")
	case err != nil:
		fmt.Println(err)
	}
}

// Iterate the records of a run while it runs, following cursors.
func ExampleRunsService_Items() {
	ctx := context.Background()
	client := cleanedweb.New()

	it := client.Runs.Items("0123456789abcdef0123456789abcdef", &cleanedweb.ItemsParams{Limit: 100, Follow: true})
	for it.Next(ctx) {
		fmt.Println(it.Value()["title"])
	}
	if err := it.Err(); err != nil {
		log.Fatal(err)
	}
}

// Schedule a catalog endpoint every day at 06:30 UTC. The idempotency key makes a retry safe.
func ExampleSchedulesService_Create() {
	ctx := context.Background()
	client := cleanedweb.New()

	schedule, err := client.Schedules.Create(ctx, &cleanedweb.ScheduleParams{
		APIID: "api_id", EndpointID: "endpoint_id", Input: map[string]any{},
		MaxItems: cleanedweb.Int(100), Every: cleanedweb.EveryDay, AtUTC: "06:30",
		IdempotencyKey: "daily-topics-1",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(schedule.ScheduleID, schedule.State, schedule.NextDueAt, schedule.Replayed)
}
