package config

import "os"

// Config holds application configuration loaded from environment variables.
type Config struct {
	// DatabaseURL is the path to the SQLite database file.
	// If empty, the application uses in-memory repositories.
	// Example: "/data/app.db" or "./data/app.db"
	DatabaseURL string

	// ServerAddr is the address the HTTP server listens on.
	// Defaults to ":8080" if not specified.
	ServerAddr string
}

// Load reads configuration from environment variables.
func Load() Config {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		ServerAddr:  os.Getenv("SERVER_ADDR"),
	}

	if cfg.ServerAddr == "" {
		cfg.ServerAddr = ":8080"
	}

	return cfg
}
