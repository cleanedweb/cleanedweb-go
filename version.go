package cleanedweb

// Version is the version of this SDK. It is sent in the User-Agent header.
const Version = "0.1.0"

const (
	// DefaultBaseURL is the origin of every /v1/ route: catalog reads, runs, results,
	// schedules and source APIs. Not broker.cleanedweb.com, which is the acquisition broker
	// and refuses customer keys.
	DefaultBaseURL = "https://api.cleanedweb.com"

	// DefaultSiteURL serves the static catalog index (catalog/index.json) that Catalog.Find and
	// Catalog.Get use to add slugs and key fields. The API key is never sent to it.
	DefaultSiteURL = "https://cleanedweb.com"

	userAgent = "cleanedweb-go/" + Version
)
