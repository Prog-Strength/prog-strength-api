// Command weather-backfill captures the historical conditions for outdoor GPS
// activities that have no activity_weather row yet — a five-year history on
// first run, and thereafter whatever the import path missed (a timeout, an
// exhausted budget, an activity retagged indoor → outdoor).
//
//	weather-backfill --dry-run            # count and cost, zero provider calls
//	weather-backfill --limit 200          # cap this run's provider calls
//	weather-backfill --rate 2             # calls per second, default 1
//	weather-backfill --retry-unavailable  # clear terminal 'unavailable' rows and retry
//
// --dry-run composes with all of them and stays a pure read: combined with
// --retry-unavailable it COUNTS the rows the retry would clear rather than
// clearing them.
//
// why a command and not a worker: an automatic paced background drain was
// considered and rejected. This is the one operation in this project that
// spends money in bulk, against a metered vendor, over a history whose size is
// not known until it is counted. A command puts a human between the count and
// the spend — --dry-run prints exactly how many calls the run will make BEFORE
// any of them happen, and the operator decides. A worker would make that
// decision at boot, and the first time anyone learned the real number would be
// from the ledger. See sows/activity-weather-conditions.md, Algorithms § "Why
// the backfill is a command and not a worker".
//
// It is re-runnable rather than one-shot, and resumable by construction: the
// presence of a row IS the checkpoint, so there is no separate progress state
// to corrupt. A crash, a SIGINT, or an exhausted budget all leave a consistent
// database and re-running resumes exactly where it stopped, never re-spending
// on an activity it has already resolved.
//
// Scale boundary: the design assumes a history in the low thousands. Above
// roughly 800 eligible activities the run spans more than one UTC day and
// becomes a multi-day operation (hence the "resume tomorrow" exit); above
// ~10,000 it would want batched writes and a resume cursor rather than a
// per-activity anti-join. Neither is close for a single-user product.
//
// It invents no second budget: every call goes through the same
// weather.ActivityCapturer, the same BudgetLedger, and the same ceiling the
// import path and the dashboard tile use.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata"

	progstrength "github.com/Prog-Strength/prog-strength-api"
	"github.com/Prog-Strength/prog-strength-api/internal/config"
	"github.com/Prog-Strength/prog-strength-api/internal/db"
	"github.com/Prog-Strength/prog-strength-api/internal/logging"
	"github.com/Prog-Strength/prog-strength-api/internal/weather"
)

// providerHTTPTimeout mirrors the dashboard tile's client in
// internal/server/server.go: the provider sets no timeout of its own, and a
// stalled OpenWeather connection must not hang a run that holds no lock but
// does hold a reservation the ledger already counted.
const providerHTTPTimeout = 8 * time.Second

// options is the parsed command line, kept as one value so backfill's
// signature does not grow a flag at a time.
type options struct {
	dryRun           bool
	limit            int
	rate             float64
	retryUnavailable bool
}

func main() {
	// SIGINT and SIGTERM cancel the context the loop checks between
	// activities, so Ctrl-C stops after the in-flight capture rather than in
	// the middle of one — the database is consistent at every boundary and the
	// next run resumes from it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var opts options
	flag.BoolVar(&opts.dryRun, "dry-run", false, "print the eligible/no-GPS/already-captured split and stop; zero provider calls, zero writes")
	flag.IntVar(&opts.limit, "limit", 0, "cap this run's provider calls (0 = unlimited); activities with no GPS are free and do not count")
	flag.Float64Var(&opts.rate, "rate", 1, "provider calls per second")
	flag.BoolVar(&opts.retryUnavailable, "retry-unavailable", false, "clear terminal 'unavailable' rows first so they are retried; counted rather than cleared under --dry-run")
	flag.Parse()

	if err := run(ctx, opts); err != nil {
		log.Fatalf("weather-backfill: %v", err)
	}
}

func run(ctx context.Context, opts options) error {
	if opts.rate <= 0 {
		return fmt.Errorf("--rate must be positive, got %v", opts.rate)
	}

	cfg, err := config.Load(progstrength.DefaultConfigTOML)
	if err != nil {
		return err
	}

	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	if err := db.Migrate(database); err != nil {
		return err
	}

	repo := weather.NewSQLiteActivityWeatherRepository(database)
	capturer := weather.NewActivityCapturer(
		cfg.Weather,
		repo,
		weather.NewBudgetLedger(database),
		weather.NewOpenWeatherProvider(&http.Client{Timeout: providerHTTPTimeout}, cfg.Weather.APIKey),
		logging.NewLogger(os.Stdout, cfg.LogLevel),
	)

	pacer := newTickerPacer(opts.rate)
	defer pacer.stop()

	return backfill(ctx, deps{
		repo:     repo,
		capturer: capturer,
		pacer:    pacer,
		logger:   log.New(os.Stdout, "weather-backfill: ", log.LstdFlags),
	}, opts)
}

// capturer is the narrow seam onto weather.ActivityCapturer. The loop decides
// WHICH activities to resolve and in what order; the capturer owns the budget
// reservation, the provider call, and every write — so the backfill and the
// import path share one code path to trust.
type capturer interface {
	Enabled() bool
	Capture(ctx context.Context, activityID string, lat, lon float64, at time.Time) error
	RecordNoCoordinates(ctx context.Context, activityID string) error
	RecordUnavailable(ctx context.Context, activityID string, lat, lon float64) error
}

// pacer gates each provider call. It is a seam rather than a bare *time.Ticker
// so the tests exercise the real loop without sleeping through --rate.
type pacer interface {
	wait(ctx context.Context) error
	stop()
}

// tickerPacer is the production pacer: --rate calls per second, so a long
// backfill degrades the dashboard tile gradually instead of draining the day's
// allowance in the first ninety seconds. The first tick arrives one interval
// in, which costs a second on a one-activity run and is the safe direction.
type tickerPacer struct{ t *time.Ticker }

func newTickerPacer(rate float64) *tickerPacer {
	return &tickerPacer{t: time.NewTicker(paceInterval(rate))}
}

// paceInterval converts calls-per-second to the gap between them. Clamped to a
// positive duration because time.NewTicker panics on a non-positive interval
// and a sub-nanosecond gap is "as fast as possible" anyway.
func paceInterval(rate float64) time.Duration {
	d := time.Duration(float64(time.Second) / rate)
	if d < 1 {
		return 1
	}
	return d
}

func (p *tickerPacer) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.t.C:
		return nil
	}
}

func (p *tickerPacer) stop() { p.t.Stop() }

type deps struct {
	repo     weather.ActivityWeatherRepository
	capturer capturer
	pacer    pacer
	logger   *log.Logger
}

// backfill is the testable orchestration: clear retries, count, print, and
// then resolve each pending activity in turn.
func backfill(ctx context.Context, d deps, opts options) error {
	// no_coordinates rows are deliberately left alone on both paths: a position
	// that was never recorded cannot be re-derived, so retrying one would spend
	// a call to learn nothing.
	switch {
	case opts.retryUnavailable && opts.dryRun:
		// Counted, never cleared. --dry-run's contract is zero writes, and a
		// preview that deleted the rows it only promised to describe would be
		// the most destructive thing this command can do.
		n, err := d.repo.CountUnavailable(ctx)
		if err != nil {
			return err
		}
		// Reported separately from the eligible line below, which cannot see
		// these activities: they still have a row, so the anti-join excludes
		// them until the real run clears them.
		d.logger.Printf("would clear %d unavailable rows (%d more provider calls on the real run)", n, n)
	case opts.retryUnavailable:
		cleared, err := d.repo.ClearUnavailable(ctx)
		if err != nil {
			return err
		}
		d.logger.Printf("cleared %d unavailable rows for retry", cleared)
	}

	counts, err := d.repo.CountPending(ctx)
	if err != nil {
		return err
	}
	captured, err := d.repo.CountOK(ctx)
	if err != nil {
		return err
	}
	// The number the operator approves before any money is spent.
	d.logger.Printf("%d activities eligible (%d provider calls), %d with no GPS (free), %d already captured",
		counts.Eligible, counts.Eligible, counts.NoCoordinates, captured)

	if opts.dryRun {
		d.logger.Printf("dry run: stopping before the first provider call; nothing was written")
		return nil
	}

	// Checked once, before the loop rather than per activity: with capture
	// switched off every eligible activity would fail identically, and a run
	// that logged that a few thousand times would still look like it tried.
	if !d.capturer.Enabled() {
		return errors.New("capture is disabled: needs [weather] enabled = true, capture_activity_weather = true, and OPENWEATHER_API_KEY set")
	}

	// The whole queue, not ListPending(limit): --limit caps provider CALLS,
	// and activities with no coordinate are resolved for free, so a database
	// LIMIT would cap the wrong quantity. Bounded by the scale boundary in the
	// package comment.
	pending, err := d.repo.ListPending(ctx, 0)
	if err != nil {
		return err
	}

	var res results
	for _, p := range pending {
		// Between activities, never inside one: an interrupted run leaves a
		// consistent database because the row that was in flight either
		// committed or was never written.
		if ctx.Err() != nil {
			d.logger.Printf("interrupted: stopped between activities; re-run to resume")
			break
		}

		if p.Lat == nil || p.Lon == nil {
			// Free: no reservation, no call, and not counted against --limit.
			if err := d.capturer.RecordNoCoordinates(ctx, p.ActivityID); err != nil {
				d.logger.Printf("recording no_coordinates failed (activity=%s): %v", p.ActivityID, err)
				continue
			}
			res.noCoordinates++
			continue
		}

		if opts.limit > 0 && res.calls() >= opts.limit {
			d.logger.Printf("--limit %d reached; %d eligible activities remain", opts.limit, counts.Eligible-res.resolved())
			break
		}

		if err := d.pacer.wait(ctx); err != nil {
			d.logger.Printf("interrupted: stopped between activities; re-run to resume")
			break
		}

		err := d.capturer.Capture(ctx, p.ActivityID, *p.Lat, *p.Lon, p.StartTime)
		switch {
		case errors.Is(err, weather.ErrBudgetExhausted):
			// A budget stop is the clean, expected end of a backfill day, not
			// a failure: exit 0 so a cron-style re-run or a shell `&&` chain
			// does not treat "come back tomorrow" as breakage.
			d.logger.Printf("daily call budget exhausted; %d eligible activities remain — resume tomorrow",
				counts.Eligible-res.resolved())
			d.logger.Print(res.summary())
			return nil
		case errors.Is(err, weather.ErrNoHistoricalData):
			// The provider's definitive negative, and the only failure worth
			// recording as terminal. The capturer returns it unwritten because
			// this command owns that decision.
			if writeErr := d.capturer.RecordUnavailable(ctx, p.ActivityID, *p.Lat, *p.Lon); writeErr != nil {
				d.logger.Printf("recording unavailable failed (activity=%s): %v", p.ActivityID, writeErr)
				continue
			}
			res.unavailable++
		case err != nil:
			// Transient: no row, so the activity stays eligible and the next
			// run picks it up. One bad activity must not end the run.
			d.logger.Printf("capture failed, leaving no row (activity=%s): %v", p.ActivityID, err)
			res.failed++
		default:
			res.ok++
		}
	}

	d.logger.Print(res.summary())
	return nil
}

// results is the run's tally. failed captures are deliberately excluded from
// resolved(): they wrote no row and are still eligible next run.
type results struct {
	ok            int
	unavailable   int
	noCoordinates int
	failed        int
}

func (r results) calls() int    { return r.ok + r.unavailable + r.failed }
func (r results) resolved() int { return r.ok + r.unavailable }

func (r results) summary() string {
	return fmt.Sprintf("done: captured=%d unavailable=%d no_coordinates=%d failed=%d provider_calls=%d",
		r.ok, r.unavailable, r.noCoordinates, r.failed, r.calls())
}
