package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/activity"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/beta"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/bodyweight"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/calendarconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/calendarsync"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/chat"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/config"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/dashboard"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/db"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/follow"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/hrzones"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/logging"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutrition"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/nutritionlookup"
	plannedworkout "github.com/jwallace145/progressive-overload-fitness-tracker/internal/planned_workout"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/requestid"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/snapshot"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/steps"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/telemetry"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/tokencrypt"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/usage"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/vectormemory"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whoopconn"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/whooprecovery"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/workout"
)

type Server struct {
	httpServer *http.Server
}

func New(cfg config.Config) (*Server, error) {
	r := chi.NewRouter()

	// requestid.Middleware replaces chi's middleware.RequestID so the id
	// it mints is the same value echoed on the X-Request-ID response
	// header and embedded in every httpresp envelope. chi's version only
	// seeded context — the frontend never saw the id, and CloudWatch
	// reverse-search by id was impossible. Format is a 32-char hex string
	// (internal/id.New), not chi's "hostname/prefix-counter".
	r.Use(requestid.Middleware)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	// Prometheus instrumentation runs after Recoverer so panics still
	// get counted (Recoverer turns them into a 500 response we observe)
	// and runs over the rest of the stack so it sees the real route
	// pattern from chi's RouteContext.
	r.Use(MetricsMiddleware)

	// CORS: only matters for cross-origin browser fetches. curl/Postman/
	// server-to-server calls are unaffected (no browser, no CORS check).
	// An empty CORSAllowedOrigins disables cross-origin browser access
	// entirely. Each entry may carry a single "*" wildcard — go-chi matches
	// the pattern and reflects the concrete origin, so Vercel preview URLs
	// work via e.g. "https://prog-strength-web-*-<scope>.vercel.app" while
	// credentialed requests stay valid (see config.CORSAllowedOrigins).
	//
	// IMPORTANT: this conditional r.Use must run BEFORE any route is
	// registered. chi enforces "all middleware before any route" — if a
	// route registration intervenes, this Use panics at startup. Hidden
	// failure mode in local dev where CORS_ALLOWED_ORIGIN is unset
	// (the block is skipped, no panic); only fires in prod.
	if len(cfg.CORSAllowedOrigins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins: cfg.CORSAllowedOrigins,
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
		log.Printf("cors: allowing origins %v", cfg.CORSAllowedOrigins)
	}

	// --- All r.Use() calls must be above this line. ---
	// Routes follow.

	// Prometheus scrape target. Reachable only from inside the Docker
	// network — the Caddy layer refuses to proxy /metrics to the
	// public internet.
	r.Handle("/metrics", MetricsHandler())

	// Health check.
	r.Get("/health", HealthCheck)

	// TCX archiver for imported running files. When the TCX bucket is set
	// we archive to S3 (prod); otherwise an in-memory archiver keeps the
	// dev/test path working without object storage. NewS3Archiver only does
	// a one-time AWS config/client init here, so context.Background() is the
	// right scope (server.New has no request-lifetime ctx to thread). A
	// configured-but-broken bucket is a startup error — fail loudly rather
	// than silently dropping uploads.
	var activityArchiver activity.Archiver
	if bucket := cfg.TCXBucketName; bucket != "" {
		s3Archiver, err := activity.NewS3Archiver(context.Background(), bucket)
		if err != nil {
			return nil, err
		}
		activityArchiver = s3Archiver
		log.Printf("activity: archiving TCX uploads to s3 bucket %s", bucket)
	} else {
		activityArchiver = activity.NewMemoryArchiver()
		log.Println("activity: TCX uploads use an in-memory archiver (dev only, not durable)")
	}

	// Avatar store. When AVATAR_BUCKET_NAME is set we presign/put to S3 (prod);
	// otherwise the store is nil and the user handler nil-guards: GET/PATCH /me
	// still serve name/height/oauth-fallback, while POST/DELETE /me/avatar
	// return a clear 503. Same one-time AWS init scope as the TCX archiver; a
	// configured-but-broken bucket is a loud startup error.
	var avatarStore user.AvatarStore
	if cfg.AvatarBucketName != "" {
		s3AvatarStore, err := user.NewS3AvatarStore(context.Background(), cfg.AvatarBucketName)
		if err != nil {
			return nil, err
		}
		avatarStore = s3AvatarStore
		log.Printf("user: storing avatars in s3 bucket %s", cfg.AvatarBucketName)
	} else {
		log.Println("user: avatar storage disabled (AVATAR_BUCKET_NAME unset); /me/avatar returns 503")
	}

	// Initialize repositories based on config.
	var exerciseRepo exercise.Repository
	var workoutRepo workout.Repository
	var userRepo user.Repository
	var nutritionRepo nutrition.Repository
	var bodyweightRepo bodyweight.Repository
	var plannedWorkoutRepo plannedworkout.Repository
	var stepsRepo steps.Repository
	var chatRepo chat.Repository
	var activityRepo activity.Repository
	var nutritionLookupRepo nutritionlookup.Repository
	var timelineRepo timeline.Repository
	var calendarConnRepo calendarconn.Repository
	var followRepo follow.Repository
	var betaRepo beta.Repository

	// usageLedger is non-nil only when telemetry is enabled (the ledger
	// reads telemetry.db). The usage handler is mounted in the JWT-gated
	// group below only when it exists. Parse the price table once here;
	// the env var is an optional override of usage.DefaultPriceTable. A
	// bad override logs and falls back to the defaults rather than the
	// previous empty-table behavior — silently shipping zero prices is
	// the most dangerous failure mode because it disables capping.
	var usageLedger *usage.Ledger
	priceTable, err := usage.LoadPriceTable(cfg.UsagePriceTableJSON)
	if err != nil {
		log.Printf("usage: failed to parse USAGE_PRICE_TABLE_JSON, falling back to default price table: %v", err)
		priceTable = usage.DefaultPriceTable()
	}

	// DATABASE_URL is required: the in-memory repositories were dev-only
	// scaffolding and have been removed, so a SQLite file path is the only
	// supported persistence backend. Fail fast with an actionable message
	// rather than silently booting against non-durable storage.
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required; set it to a SQLite file path, e.g. DATABASE_URL=./dev.db")
	}

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
	plannedWorkoutRepo = plannedworkout.NewSQLiteRepository(database)
	stepsRepo = steps.NewSQLiteRepository(database)
	chatSQLiteRepo := chat.NewSQLiteRepository(database)
	chatRepo = chatSQLiteRepo
	activityRepo = activity.NewSQLiteRepository(database, activityArchiver)
	nutritionLookupRepo = nutritionlookup.NewSQLiteRepository(database)
	timelineRepo = timeline.NewSQLiteRepository(database)
	calendarConnRepo = calendarconn.NewSQLiteRepository(database)
	followRepo = follow.NewSQLiteRepository(database)
	betaRepo = beta.NewSQLiteRepository(database)
	// Whoop connection + recovery read repos. Harmless without the OAuth
	// wiring (Task 10): the dashboard reads them defensively and shows the
	// recovery card only for a connected user, so an empty table is a no-op.
	whoopConnRepo := whoopconn.NewSQLiteRepository(database)
	whoopRecoveryRepo := whooprecovery.NewSQLiteRepository(database)

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

	// Backfill running best efforts from each live running activity's
	// archived TCX. Gated on activity_best_efforts being empty, so it
	// runs once after migration 016 ships and is a no-op thereafter.
	if err := activityRepo.(*activity.SQLiteRepository).BackfillActivityBestEfforts(context.Background()); err != nil {
		return nil, err
	}
	if err := activityRepo.(*activity.SQLiteRepository).BackfillBestEffortWindowBounds(context.Background()); err != nil {
		return nil, err
	}

	// TEMPORARY — route geometry backfill (sows/sow-trail-map-backfill.md).
	// Remove this call and BackfillActivityRoutes once prod outdoor history has
	// maps (or you no longer care about pre-trail-map uploads). Safe to delete:
	// new ingest already writes route_geojson; this only repairs the past.
	if err := activityRepo.(*activity.SQLiteRepository).BackfillActivityRoutes(context.Background()); err != nil {
		log.Printf("backfill activity routes: %v", err)
	}

	// Seed the timeline feed index from existing workouts, runs, PR
	// events, and best efforts. Gated on timeline_post being empty, so it
	// runs once after migration 019 ships and is a no-op thereafter.
	if err := backfillTimeline(context.Background(), database, timelineRepo); err != nil {
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
		// Usage ledger reads the same telemetry.db handle to price
		// per-user daily spend for GET /me/usage (mounted below in
		// the JWT-gated group).
		usageLedger = usage.NewLedger(telemetryDB, priceTable)
		// Daily TTL: NULLs content/arguments_json/result_summary
		// after 90 days. Metadata (token counts, latencies, tool
		// names, timestamps) is kept indefinitely. Background
		// goroutine; survives until process exit.
		telemetryRepo.StartContentTTL(context.Background(), telemetry.ContentRetention)
		log.Println("telemetry: agent event recording enabled")
	} else {
		log.Println("telemetry: disabled (TELEMETRY_DATABASE_URL unset)")
	}

	// Agent vector memory: per-user durable recollections distilled from
	// idle chat sessions (Anthropic) and embedded (OpenAI) into sqlite-vec
	// for semantic retrieval by the agent. Everything is gated on Enabled —
	// when off we mount nothing and start no background job, so an
	// unconfigured deploy never spends against the paid providers. The
	// handler is declared in the outer scope so the route-group blocks below
	// can mount it (admin search + internal retrieve); it stays nil when the
	// feature is disabled and the mounts nil-guard.
	var vmHandler *vectormemory.Handler
	if cfg.VectorMemory.Enabled {
		vmClient := &http.Client{Timeout: 15 * time.Second} // embedding + distillation round-trips (job/backfill); the agent bounds its own retrieve timeout
		vmLogger := logging.NewLogger(os.Stdout, cfg.LogLevel)
		vmRepo := vectormemory.NewSQLiteRepository(database)
		vmEmbedder := vectormemory.NewOpenAIEmbedder(vmClient, cfg.VectorMemory.OpenAIAPIKey, cfg.VectorMemory.EmbedModel)
		vmDistiller := vectormemory.NewAnthropicDistiller(vmClient, cfg.VectorMemory.AnthropicAPIKey, cfg.VectorMemory.DistillModel)
		vmService := vectormemory.NewService(vmRepo, vmEmbedder, vmDistiller, cfg.VectorMemory, vmLogger)
		vmHandler = vectormemory.NewHandler(vmService, vmLogger)
		vmSources := BuildMemorySources(database, chatSQLiteRepo, cfg.VectorMemory)
		vmService.StartDistillation(context.Background(), vmSources)
		log.Println("vectormemory: enabled (distillation + retrieval)")
	} else {
		log.Println("vectormemory: disabled (enabled=false)")
	}

	// Nutrition lookup service: FatSecret first (restaurant + branded),
	// USDA FDC as fallback, both behind the durable cache repository.
	// One shared http.Client with a tight timeout — a slow third party
	// must not stall the agent's tool loop indefinitely. Unconfigured
	// providers are skipped; with no keys at all the endpoint reports
	// 503 lookup_unavailable and the agent falls back to estimating.
	// See prog-strength-docs/sows/custom-meal-macro-accuracy.md.
	//
	// The lookup path logs through a request-id-aware JSON slog logger
	// (LOG_LEVEL-gated) so CloudWatch Logs Insights can reconstruct a
	// request end-to-end via `filter request_id = "…"`.
	lookupClient := &http.Client{Timeout: 8 * time.Second}
	lookupLogger := nutritionlookup.NewLogger(os.Stdout, cfg.LogLevel)
	nutritionLookupSvc := nutritionlookup.NewService(
		nutritionLookupRepo,
		lookupLogger,
		nutritionlookup.NewFatSecretProvider(lookupClient, cfg.FatSecretClientID, cfg.FatSecretClientSecret, lookupLogger),
		nutritionlookup.NewUSDAProvider(lookupClient, cfg.USDAFDCAPIKey, lookupLogger),
	)

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
	}, userRepo, betaRepo)
	authHandler.Mount(r)
	log.Printf("auth: google=%v dev_token=%v", authHandler.HasGoogle(), cfg.DevAuth)

	// Calendar sync: the incremental Google OAuth flow (calendar.events scope,
	// offline access) plus connection-status endpoints. It stays DORMANT unless
	// BOTH a valid token-encryption key (CALENDAR_TOKEN_ENC_KEY) AND a calendar
	// redirect URL (GOOGLE_CALENDAR_REDIRECT_URL) are configured, mirroring how
	// avatar storage is optional. An empty or invalid key logs and skips the
	// mount rather than failing boot — the rest of the API is unaffected. The
	// authed half is mounted inside the JWT-gated group below.
	var calendarSyncHandler *calendarsync.Handler
	// calendarScheduler is the event-writing service injected into the planned
	// workout handler. It is non-nil only when calendar sync is fully
	// configured; otherwise the handler nil-guards its /schedule + /resync
	// routes to a 503 (mirrors the avatar-store-nil pattern).
	var calendarScheduler *calendarsync.Service
	if cfg.CalendarTokenEncKey != "" && cfg.GoogleCalendarRedirectURL != "" && cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" {
		key, keyErr := tokencrypt.KeyFromEnv(cfg.CalendarTokenEncKey)
		if keyErr != nil {
			log.Printf("calendar-sync: disabled (invalid CALENDAR_TOKEN_ENC_KEY): %v", keyErr)
		} else if cipher, cipherErr := tokencrypt.NewCipher(key); cipherErr != nil {
			log.Printf("calendar-sync: disabled (cipher init failed): %v", cipherErr)
		} else {
			calendarOAuthConfig := calendarsync.NewCalendarConfig(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleCalendarRedirectURL)
			// Dedicated client with a timeout so a slow Google token/revoke
			// call can't stall a request indefinitely (mirrors lookupClient).
			calendarClient := &http.Client{Timeout: 8 * time.Second}
			calendarSyncHandler = calendarsync.NewHandler(calendarOAuthConfig, calendarConnRepo, cipher, calendarClient, cfg.ReturnToAllowedOrigins, jwtSecret)
			// Public callback — Google redirects here; the user id rides in the
			// OAuth state, not our auth cookie, so it can't sit behind RequireUser.
			calendarSyncHandler.MountPublic(r)

			// Event-writing service: mints access tokens from the stored refresh
			// token (TokenSource shares the bounded calendarClient as its oauth2
			// HTTP client), writes events via the Google Calendar v3 REST client,
			// and persists sync status back onto the plan. appLinkBase links
			// events back to the frontend; we reuse the first allowed return-to
			// origin (the web app) and tolerate it being empty.
			var appLinkBase string
			if len(cfg.ReturnToAllowedOrigins) > 0 {
				appLinkBase = cfg.ReturnToAllowedOrigins[0]
			}
			tokenSource := calendarsync.NewTokenSource(calendarOAuthConfig, calendarClient, nil)
			calendarEventClient := calendarsync.NewGoogleCalendarClient(calendarClient)
			calendarScheduler = calendarsync.NewService(
				calendarConnRepo,
				cipher,
				tokenSource,
				calendarEventClient,
				plannedWorkoutRepo,
				userRepo,
				appLinkBase,
				nil,
			)
			log.Println("calendar-sync: enabled (google calendar oauth + connection + event writing)")
		}
	} else {
		log.Println("calendar-sync: disabled (CALENDAR_TOKEN_ENC_KEY / GOOGLE_CALENDAR_REDIRECT_URL / google client not configured)")
	}

	// Exercise routes — public read of the shared catalog.
	exerciseHandler := exercise.NewHandler(exerciseRepo)
	exerciseHandler.Mount(r)

	// Workout routes — require a valid JWT. Group ensures the middleware
	// only applies to routes mounted inside it, leaving /health and
	// /exercises public. The progression endpoint needs the exercise
	// catalog to resolve a muscle_group filter to its member exercises.
	// Timeline wiring: a publisher (best-effort feed-index writes injected
	// into the workout/activity handlers) and a hydrator (renders post
	// content from the live workout/activity repos at read time). Both adapt
	// the cross-domain repos so the timeline package stays import-clean.
	timelinePublisher := newTimelinePublisher(timelineRepo)
	timelineHydrator := newTimelineHydrator(workoutRepo, activityRepo)

	r.Group(func(r chi.Router) {
		r.Use(auth.RequireUser(jwtSecret))
		// Capture the workout + activity handlers so the timeline publisher
		// can be injected before mounting — best-effort publishing of
		// workouts/PRs (workout) and runs/best efforts (activity).
		workoutHandler := workout.NewHandler(workoutRepo, exerciseRepo, activityRepo)
		workoutHandler.SetPublisher(timelinePublisher)
		workoutHandler.Mount(r)
		// Nutrition + pantry routes share the JWT-gated group with
		// workouts. Phase 1 mounts pantry items and the nutrition
		// log + daily-macros aggregate; recipes and bodyweight ship
		// in later phases under the same auth middleware.
		nutrition.NewHandler(nutritionRepo).Mount(r)
		// Nutrition lookup — external food-database search behind the
		// durable cache. Auth-gated alongside nutrition: public food
		// data, but the endpoint spends shared provider quota.
		nutritionlookup.NewHandler(nutritionLookupSvc, lookupLogger).Mount(r)
		// Bodyweight lives in its own package — independent concept,
		// independent read paths — and shares the same JWT-gated
		// router group. Needs the user repository to default unit
		// from the user's preferred WeightUnit when omitted.
		bodyweight.NewHandler(bodyweightRepo, userRepo).Mount(r)
		// Planned workouts — forward-looking scheduled training entries with
		// an optional lift agenda and Google Calendar sync. Shares the JWT-gated
		// group; needs the user repository to default a plan's timezone from the
		// user's Timezone when omitted. The calendar scheduler is injected before
		// mounting so /schedule + /resync (and the best-effort push on
		// create/update/delete) work; left nil when calendar sync isn't
		// configured, in which case those routes return 503.
		plannedWorkoutHandler := plannedworkout.NewHandler(plannedWorkoutRepo, userRepo)
		if calendarScheduler != nil {
			plannedWorkoutHandler.SetCalendarSync(calendarScheduler)
		}
		// Request-id-stamping JSON logger gated at LOG_LEVEL — info for the
		// list summary, debug for the per-plan detail that diagnoses a
		// timezone-window clip.
		plannedWorkoutHandler.SetLogger(logging.NewLogger(os.Stdout, cfg.LogLevel))
		plannedWorkoutHandler.Mount(r)
		// Steps lives in its own package — daily totals upserted by
		// calendar date, unitless and hard-deleted — and shares the same
		// JWT-gated router group. No user repository needed since there's
		// no unit to default.
		steps.NewHandler(stepsRepo).Mount(r)
		// Activity import + CRUD + running-specific metrics. Shares the
		// JWT-gated group; the import handler reads the user ID from
		// context and archives the raw TCX through activityArchiver.
		// Generalized from the prior running-only domain — see migration
		// 015 and prog-strength-docs/sows/running-tracking-via-tcx-import.md.
		activityHandler := activity.NewHandler(activityRepo)
		activityHandler.SetPublisher(timelinePublisher)
		// Heart-rate-zone engine: tunables come from the [hr_zones] config
		// section; the recency window for the reference-max-HR estimate is
		// derived from recency_window_days.
		hrEngine := hrzones.New(hrzones.Config{
			PopulationDefaultMaxHR: cfg.HRZones.PopulationDefaultMaxHR,
			CalibratedRunThreshold: cfg.HRZones.CalibratedRunThreshold,
			RecencyWindowDays:      cfg.HRZones.RecencyWindowDays,
			MinReferenceBpm:        cfg.HRZones.MinReferenceBpm,
			MaxReferenceBpm:        cfg.HRZones.MaxReferenceBpm,
			ZoneUpperBounds:        cfg.HRZones.ZoneUpperBounds,
			ZoneNames:              cfg.HRZones.ZoneNames,
		})
		activityHandler.SetHRZonesEngine(hrEngine, time.Duration(cfg.HRZones.RecencyWindowDays)*24*time.Hour)
		activityHandler.SetDemographicsLoader(user.EstimateDemographicsLoader{Repo: userRepo})
		activityHandler.Mount(r)
		// Dashboard "command center" — the read-only aggregate that composes
		// every domain's tile into one GET /dashboard/summary. Shares the
		// JWT-gated group; reads from every domain repo, owns no writes.
		dashboard.NewHandler(activityRepo, workoutRepo, exerciseRepo, stepsRepo, nutritionRepo, bodyweightRepo, userRepo, whoopConnRepo, whoopRecoveryRepo).Mount(r)
		// Training snapshot — the agent-facing holistic read across every
		// domain (GET /training-snapshot). Separate surface from the web
		// dashboard; composes the same domain repos defensively. Arg order
		// follows snapshot.NewService: workout, exercise, activity, steps,
		// bodyweight, nutrition, user.
		snapshot.NewHandler(snapshot.NewService(
			workoutRepo, exerciseRepo, activityRepo, stepsRepo, bodyweightRepo, nutritionRepo, userRepo,
		)).Mount(r)
		// Wire the shared planned-workout service into the activity + workout
		// plan-matcher seams so a logged run/lift best-effort completes a
		// matching planned workout. One service instance backs both adapters;
		// SetCalendarSync above (if configured) already set the calendar on the
		// same struct, so plan-completion calendar pushes flow through it.
		planService := plannedWorkoutHandler.Service()
		activityHandler.SetPlanMatcher(&activityPlanMatcher{svc: planService})
		workoutHandler.SetPlanMatcher(&workoutPlanMatcher{svc: planService})
		// Chat session persistence. Agent stays stateless; this
		// surface is just CRUD for sessions + a turn-append endpoint
		// the clients write to after each completed stream. See
		// prog-strength-docs/sows/persistent-chat-sessions.md.
		chat.NewHandler(chatRepo).Mount(r)
		// User self route — exposes the authed user (incl. weight_unit)
		// for user-scoped frontend reads. Shares the JWT-gated group;
		// getMe reads the user ID from context.
		user.NewHandler(userRepo, avatarStore).Mount(r)
		// Usage self route — GET /me/usage reports the authed user's
		// daily-spend percentage against the configured cap. Only mounted
		// when telemetry is enabled (the ledger needs telemetry.db); in
		// in-memory mode there is no spend source so the route is absent.
		if usageLedger != nil {
			usage.NewHandler(usageLedger, cfg.DailyUsageCapUSD).Mount(r)
		}
		// The follow profile provider adapts the user domain for both the follow
		// handler (ProfileProvider) and the timeline scoped-feed username
		// resolution (timeline.UserResolver — it has ResolveUsername, which
		// returns follow.ErrNotFound for an unknown username; the timeline
		// handler masks any resolve error as a 404).
		followProvider := newFollowProfileProvider(userRepo, avatarStore)
		// Timeline — the reverse-chronological social feed: the viewer's own
		// training events plus their accepted-followees' non-private posts, with
		// comments + reactions, and a ?user=<username> scoped feed. Shares the
		// JWT-gated group; the handler reads the viewer's id from context,
		// hydrates post content from the live source repos via timelineHydrator,
		// fans out over the follow graph via followRepo (it satisfies
		// timeline.AcceptedFollowees), and resolves usernames via followProvider.
		var _ timeline.AcceptedFollowees = followRepo
		var _ timeline.UserResolver = followProvider
		// The author resolver embeds each post/comment author's identity,
		// batch-resolved over the follow profile provider (reusing its avatar
		// presigning + OAuth fallback) so the feed has no N+1 over authors.
		timelineProfiles := newTimelineProfileResolver(followProvider)
		timeline.NewHandler(timelineRepo, timelineHydrator, followRepo, followProvider, timelineProfiles).Mount(r)
		// Follow graph — the request/accept state machine, teardown verbs, and
		// the requests inbox. Shares the JWT-gated group; the handler reads the
		// actor's id from context and renders profile summaries via the user
		// domain through the follow.ProfileProvider seam.
		follow.NewHandler(followRepo, followProvider).Mount(r)
		// Discovery — public profile, followers/following lists, and ranked
		// profile search. Lives in the user package as a handler separate from
		// /me; consumes the follow repo's read methods through the user-side
		// FollowReader seam (followRepo satisfies it directly). Mounted after
		// the follow handler in the same JWT-gated group.
		// The profile-stats sources adapt the workout + activity repos to the
		// user package's narrow LiftSessionSource/RunningSampleSource seams so
		// GET /users/{username}/stats can read weekly training data without the
		// user package importing workout/activity.
		user.NewDiscoveryHandler(
			userRepo,
			followRepo,
			avatarStore,
			newLiftSessionSource(workoutRepo),
			newRunningSampleSource(activityRepo),
		).Mount(r)

		// Admin beta-allowlist surface — manage the closed-beta allowlist at
		// runtime (GET/POST/DELETE /admin/beta-emails). Wrapped in its own
		// RequireAdmin group (inside the JWT-gated group) so the admin gate
		// applies only to these routes. An empty ADMIN_EMAILS makes the whole
		// surface inert (every route 403s, fail-closed). The middleware lives
		// in auth (not beta) to avoid an import cycle — beta exposes Checker
		// to auth, so beta must not import auth.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin(userRepo, cfg.AdminEmails))
			beta.NewHandler(betaRepo, userRepo).Mount(r)
			// Admin vector-memory surface — GET /admin/memories +
			// POST /admin/memories/search, gated by the RequireAdmin
			// middleware on this group (MountAdmin itself is ungated).
			// Only present when the feature is enabled.
			if vmHandler != nil {
				vmHandler.MountAdmin(r)
			}
		})
		// Calendar sync (authed half): GET /auth/google/calendar/connect plus
		// GET/DELETE /me/calendar/connection. Only present when calendar sync
		// is enabled (cipher + redirect URL configured above). /connect reads
		// the user id from context and encodes it into the OAuth state so the
		// public callback can recover it.
		if calendarSyncHandler != nil {
			calendarSyncHandler.MountAuthed(r)
		}
	})

	// Internal chat routes (read-only intent lookup for the agent).
	// Lives OUTSIDE the JWT-gated group — auth boundary is the docker
	// network, identical to /internal/telemetry/*.
	chat.NewHandler(chatRepo).MountInternal(r)

	// Internal vector-memory retrieval (POST /internal/memory/retrieve) for
	// the agent. Lives OUTSIDE the JWT-gated group — same docker-network auth
	// boundary as /internal/chat/* and /internal/telemetry/*. Only present
	// when the feature is enabled.
	if vmHandler != nil {
		vmHandler.MountInternal(r)
	}

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
