package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	// time.LoadLocation needs the IANA tzdata; importing the embedded
	// copy makes the binary self-contained across the AL2023 base image
	// (which does ship tzdata, but we don't want to depend on it) and a
	// developer's macOS laptop.
	_ "time/tzdata"

	progstrength "github.com/jwallace145/progressive-overload-fitness-tracker"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/server"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(progstrength.DefaultConfigTOML)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("server setup failed: %v", err)
	}

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
