package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

type Server struct {
	httpServer *http.Server
}

func New(cfg config.Config) (*Server, error) {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Health check.
	r.Get("/health", HealthCheck)

	// Initialize repositories based on config.
	var exerciseRepo exercise.Repository
	var workoutRepo workout.Repository
	var userRepo user.Repository

	if cfg.DatabaseURL != "" {
		// SQLite mode.
		log.Printf("using SQLite database at %s", cfg.DatabaseURL)

		database, err := db.Open(cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}

		// Run migrations.
		if err := db.Migrate(database); err != nil {
			return nil, err
		}

		// Create SQLite repositories.
		exerciseRepo = exercise.NewSQLiteRepository(database)
		workoutRepo = workout.NewSQLiteRepository(database)
		userRepo = user.NewSQLiteRepository(database)

		// Seed exercise catalog if empty.
		if err := exerciseRepo.(*exercise.SQLiteRepository).SeedCatalog(context.Background(), exercise.Catalog); err != nil {
			return nil, err
		}
	} else {
		// In-memory mode (default for local dev without DATABASE_URL).
		log.Println("using in-memory repositories")

		exerciseRepo = exercise.NewMemoryRepository(exercise.Catalog)
		workoutRepo = workout.NewMemoryRepository()
		userRepo = user.NewMemoryRepository()
	}

	// Exercise routes.
	exerciseHandler := exercise.NewHandler(exerciseRepo)
	exerciseHandler.Mount(r)

	// Workout routes.
	workoutHandler := workout.NewHandler(workoutRepo)
	workoutHandler.Mount(r)

	// User repo is available but no handler yet (OAuth login will use it).
	_ = userRepo

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.ServerAddr,
			Handler:           r,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}, nil
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		log.Printf("server listening on %s", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	}
}
