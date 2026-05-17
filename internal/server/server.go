package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
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

	// CORS: only matters for cross-origin browser fetches. curl/Postman/
	// server-to-server calls are unaffected (no browser, no CORS check).
	// Empty CORSAllowedOrigin disables cross-origin browser access entirely.
	if cfg.CORSAllowedOrigin != "" {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   []string{cfg.CORSAllowedOrigin},
			AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: true,
			MaxAge:           300,
		}))
		log.Printf("cors: allowing origin %s", cfg.CORSAllowedOrigin)
	}

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

		// Sync exercise catalog: catalog.go is the source of truth; this
		// upserts new entries and updates non-key fields on existing ones.
		if err := exerciseRepo.(*exercise.SQLiteRepository).SyncCatalog(context.Background(), exercise.Catalog); err != nil {
			return nil, err
		}
	} else {
		// In-memory mode (default for local dev without DATABASE_URL).
		log.Println("using in-memory repositories")

		exerciseRepo = exercise.NewMemoryRepository(exercise.Catalog)
		workoutRepo = workout.NewMemoryRepository()
		userRepo = user.NewMemoryRepository()
	}

	// Auth: mounts /auth/google/* when Google OAuth is configured and
	// /auth/dev/token when DEV_AUTH=true. Always mounted so that login
	// failures surface as 404 (route absent) rather than mysterious 500s.
	jwtSecret := []byte(cfg.JWTSigningKey)
	authHandler := auth.NewHandler(auth.Config{
		JWTSecret:              jwtSecret,
		GoogleClientID:         cfg.GoogleClientID,
		GoogleClientSecret:     cfg.GoogleClientSecret,
		GoogleRedirectURL:      cfg.GoogleRedirectURL,
		DevAuth:                cfg.DevAuth,
		ReturnToAllowedOrigins: cfg.ReturnToAllowedOrigins,
		BetaAllowedEmails:      cfg.BetaAllowedEmails,
	}, userRepo)
	authHandler.Mount(r)
	log.Printf("auth: google=%v dev_token=%v", authHandler.HasGoogle(), cfg.DevAuth)

	// Exercise routes — public read of the shared catalog.
	exerciseHandler := exercise.NewHandler(exerciseRepo)
	exerciseHandler.Mount(r)

	// Workout routes — require a valid JWT. Group ensures the middleware
	// only applies to routes mounted inside it, leaving /health and
	// /exercises public.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireUser(jwtSecret))
		workout.NewHandler(workoutRepo).Mount(r)
	})

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
