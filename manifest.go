// Package progstrength is the module-root package. It exists solely to
// embed the canonical config.toml manifest, which lives at the repository
// root so it is the first thing an operator sees. go:embed cannot reach a
// parent directory, so the directive has to live in a package at the repo
// root rather than inside internal/config; the decoded bytes are handed to
// config.Load.
package progstrength

import _ "embed"

// DefaultConfigTOML is the embedded contents of the repo-root config.toml —
// the default manifest the API ships with. cmd/api passes it to
// config.Load; an external CONFIG_FILE overrides it wholesale at runtime.
//
//go:embed config.toml
var DefaultConfigTOML []byte
