// Package version exposes the running build's semantic version.
//
// Version is set at link time by the release pipeline via:
//
//	go build -ldflags="-X github.com/Prog-Strength/prog-strength-api/internal/version.Version=v1.2.3"
//
// Local builds without ldflags get the literal "dev". The value surfaces
// in every API response's "version" field so operators can tell which
// build produced any given response.
package version

// Version is the semver of the running build.
var Version = "dev"
