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
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/chat"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/telemetry"
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
	// Prometheus instrumentation runs after Recoverer so panics still
	// get counted (Recoverer turns them into a 500 response we observe)
	// and runs over the rest of the stack so it sees the real route
	// pattern from chi's RouteContext.
	r.Use(MetricsMiddleware)

	// CORS: only matters for cross-origin browser fetches. curl/Postman/
	// server-to-server calls are unaffected (no browser, no CORS check).
	// Empty CORSAllowedOrigin disables cross-origin browser access entirely.
	//
	// IMPORTANT: this conditional r.Use must run BEFORE any route is
	// registered. chi enforces "all middleware before any route" — if a
	// route registration intervenes, this Use panics at startup. Hidden
	// failure mode in local dev where CORS_ALLOWED_ORIGIN is unset
	// (the block is skipped, no panic); only fires in prod.
	if cfg.CORSAllowedOrigin != "" {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: []string{cfg.CORSAllowedOrigin},
			// PATCH is on the list because /chat-sessions/{id} uses it
			// for title updates. Without it the browser preflight
			// blocks the request silently — the agent /title generates
			// a title and the client's PATCH fails CORS, leaving the
			// stored title empty and the history UI showing the
			// "New chat" placeholder for every session.
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type"},
			AllowCredentials: true,
			MaxAge:           300,
		}))
		log.Printf("cors: allowing origin %s", cfg.CORSAllowedOrigin)
	}

	// --- All r.Use() calls must be above this line. ---
	// Routes follow.

	// Prometheus scrape target. Reachable only from inside the Docker
	// network — the Caddy layer refuses to proxy /metrics to the
	// public internet.
	r.Handle("/metrics", MetricsHandler())

	// Health check.
	r.Get("/health", HealthCheck)

	// Initialize repositories based on config.
	var exerciseRepo exercise.Repository
	var workoutRepo workout.Repository
	var userRepo user.Repository
	var nutritionRepo nutrition.Repository
	var bodyweightRepo bodyweight.Repository
	var chatRepo chat.Repository

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
		sqliteWorkoutRepo := workout.NewSQLiteRepository(database)
		workoutRepo = sqliteWorkoutRepo
		userRepo = user.NewSQLiteRepository(database)
		nutritionRepo = nutrition.NewSQLiteRepository(database)
		bodyweightRepo = bodyweight.NewSQLiteRepository(database)
		chatRepo = chat.NewSQLiteRepository(database)

		// Sync exercise catalog: catalog.go is the source of truth; this
		// upserts new entries and updates non-key fields on existing ones.
		if err := exerciseRepo.(*exercise.SQLiteRepository).SyncCatalog(context.Background(), exercise.Catalog); err != nil {
			return nil, err
		}

		// Backfill the 1RM history table for any workouts that existed
		// before this feature shipped. No-op when the table is already
		// populated, so it stays cheap on every subsequent startup.
		if err := sqliteWorkoutRepo.BackfillOneRepMaxHistory(context.Background()); err != nil {
			return nil, err
		}

		// Same pattern for the personal records and event tables. Both
		// derived from `workouts`; both gated on count > 0.
		if err := sqliteWorkoutRepo.BackfillPersonalRecords(context.Background()); err != nil {
			return nil, err
		}

		// Telemetry uses its own SQLite file so high-volume agent
		// writes don't share locks or Litestream backups with the
		// application data. Same migration pattern as app.db, just
		// pointed at a different embed.FS.
		if cfg.TelemetryDatabaseURL != "" {
			log.Printf("using telemetry SQLite database at %s", cfg.TelemetryDatabaseURL)
			telemetryDB, err := db.Open(cfg.TelemetryDatabaseURL)
			if err != nil {
				return nil, err
			}
			if err := db.MigrateTelemetry(telemetryDB); err != nil {
				return nil, err
			}
			telemetryRepo := telemetry.NewSQLiteRepository(telemetryDB)
			telemetry.NewHandlerWithIntentSink(telemetryRepo, chatRepo).Mount(r)
			// Daily TTL: NULLs content/arguments_json/result_summary
			// after 90 days. Metadata (token counts, latencies, tool
			// names, timestamps) is kept indefinitely. Background
			// goroutine; survives until process exit.
			telemetryRepo.StartContentTTL(context.Background(), telemetry.ContentRetention)
			log.Println("telemetry: agent event recording enabled")
		} else {
			log.Println("telemetry: disabled (TELEMETRY_DATABASE_URL unset)")
		}
	} else {
		// In-memory mode (default for local dev without DATABASE_URL).
		log.Println("using in-memory repositories")

		exerciseRepo = exercise.NewMemoryRepository(exercise.Catalog)
		workoutRepo = workout.NewMemoryRepository()
		userRepo = user.NewMemoryRepository()
		nutritionRepo = nutrition.NewMemoryRepository()
		bodyweightRepo = bodyweight.NewMemoryRepository()
		chatRepo = chat.NewMemoryRepository()
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
	// /exercises public. The progression endpoint needs the exercise
	// catalog to resolve a muscle_group filter to its member exercises.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireUser(jwtSecret))
		workout.NewHandler(workoutRepo, exerciseRepo).Mount(r)
		// Nutrition + pantry routes share the JWT-gated group with
		// workouts. Phase 1 mounts pantry items and the nutrition
		// log + daily-macros aggregate; recipes and bodyweight ship
		// in later phases under the same auth middleware.
		nutrition.NewHandler(nutritionRepo).Mount(r)
		// Bodyweight lives in its own package — independent concept,
		// independent read paths — and shares the same JWT-gated
		// router group. Needs the user repository to default unit
		// from the user's preferred WeightUnit when omitted.
		bodyweight.NewHandler(bodyweightRepo, userRepo).Mount(r)
		// Chat session persistence. Agent stays stateless; this
		// surface is just CRUD for sessions + a turn-append endpoint
		// the clients write to after each completed stream. See
		// prog-strength-docs/sows/persistent-chat-sessions.md.
		chat.NewHandler(chatRepo).Mount(r)
		// User self route — exposes the authed user (incl. weight_unit)
		// for user-scoped frontend reads. Shares the JWT-gated group;
		// getMe reads the user ID from context.
		user.NewHandler(userRepo).Mount(r)
	})

	// Internal chat routes (read-only intent lookup for the agent).
	// Lives OUTSIDE the JWT-gated group — auth boundary is the docker
	// network, identical to /internal/telemetry/*.
	chat.NewHandler(chatRepo).MountInternal(r)

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
