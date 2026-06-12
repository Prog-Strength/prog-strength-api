package nutritionlookup

import "context"

// Provider is one external nutrition data source. Implementations must
// be safe to call concurrently and must return errors (not swallow
// them) on HTTP failures — the service layer decides how to degrade.
// This interface is the vendor-swap seam: if FatSecret's pricing or
// USDA's coverage ever changes, a replacement provider slots in behind
// env config with no service changes.
type Provider interface {
	// Source is the short identifier stamped onto candidates ("fatsecret",
	// "usda") and used in degraded-mode error details.
	Source() string

	// Configured reports whether the provider has the credentials it
	// needs. Unconfigured providers are skipped, not errored — the
	// system stays fully functional with no external nutrition keys.
	Configured() bool

	// Search returns up to limit per-serving candidates for query.
	Search(ctx context.Context, query string, limit int) ([]Candidate, error)
}
