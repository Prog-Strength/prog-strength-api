package weather

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Prog-Strength/prog-strength-api/internal/db/dbtest"
)

// fixLedgerClock pins the ledger's clock to a settable instant so tests can
// cross UTC midnight deliberately instead of flaking near real midnight.
func fixLedgerClock(l *BudgetLedger, at time.Time) *time.Time {
	current := at
	l.now = func() time.Time { return current }
	return &current
}

// seedUsed burns exactly `used` calls so a test can start at a known count.
func seedUsed(t *testing.T, l *BudgetLedger, used int) {
	t.Helper()
	if used == 0 {
		return
	}
	if err := l.Reserve(context.Background(), used, used); err != nil {
		t.Fatalf("seed %d used calls: %v", used, err)
	}
}

// TestBudgetLedgerReserveBoundary walks the ceiling boundary: landing exactly
// on the ceiling is allowed, crossing it in any way is not, and a refused
// reserve consumes nothing.
func TestBudgetLedgerReserveBoundary(t *testing.T) {
	const ceiling = 5
	cases := []struct {
		name      string
		used      int // calls already reserved today
		n         int
		wantErr   error
		wantAfter int
	}{
		{"at N-1 landing on N succeeds", ceiling - 1, 1, nil, ceiling},
		{"at N fails", ceiling, 1, ErrBudgetExhausted, ceiling},
		{"reserve that would land past N fails", ceiling - 1, 2, ErrBudgetExhausted, ceiling - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger := NewBudgetLedger(dbtest.New(t))
			fixLedgerClock(ledger, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
			seedUsed(t, ledger, tc.used)

			err := ledger.Reserve(context.Background(), tc.n, ceiling)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Reserve(%d, %d) with %d used: err = %v, want %v", tc.n, ceiling, tc.used, err, tc.wantErr)
			}
			got, err := ledger.UsedToday(context.Background())
			if err != nil {
				t.Fatalf("UsedToday: %v", err)
			}
			if got != tc.wantAfter {
				t.Errorf("UsedToday after reserve = %d, want %d", got, tc.wantAfter)
			}
		})
	}
}

// TestBudgetLedgerCeilingIsPerCall proves the ceiling is the caller's
// parameter, not ledger state: the same day's row is judged against whatever
// ceiling each Reserve passes.
func TestBudgetLedgerCeilingIsPerCall(t *testing.T) {
	ledger := NewBudgetLedger(dbtest.New(t))
	fixLedgerClock(ledger, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()

	if err := ledger.Reserve(ctx, 1, 1); err != nil {
		t.Fatalf("Reserve(1, ceiling 1) on empty day: %v", err)
	}
	if err := ledger.Reserve(ctx, 1, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Reserve(1, ceiling 1) at ceiling: err = %v, want ErrBudgetExhausted", err)
	}
	// A more generous ceiling on the very next call must be honored.
	if err := ledger.Reserve(ctx, 1, 5); err != nil {
		t.Fatalf("Reserve(1, ceiling 5) after ceiling-1 exhaustion: %v", err)
	}
	got, err := ledger.UsedToday(ctx)
	if err != nil {
		t.Fatalf("UsedToday: %v", err)
	}
	if got != 2 {
		t.Errorf("UsedToday = %d, want 2", got)
	}
}

// TestBudgetLedgerUTCDateRollover crosses midnight UTC with the injected
// clock: yesterday's spend must not count against today.
func TestBudgetLedgerUTCDateRollover(t *testing.T) {
	database := dbtest.New(t)
	ledger := NewBudgetLedger(database)
	clock := fixLedgerClock(ledger, time.Date(2026, 8, 9, 23, 59, 0, 0, time.UTC))
	ctx := context.Background()

	if err := ledger.Reserve(ctx, 2, 10); err != nil {
		t.Fatalf("Reserve before midnight: %v", err)
	}

	*clock = time.Date(2026, 8, 10, 0, 1, 0, 0, time.UTC)
	got, err := ledger.UsedToday(ctx)
	if err != nil {
		t.Fatalf("UsedToday after rollover: %v", err)
	}
	if got != 0 {
		t.Fatalf("UsedToday after rollover = %d, want 0 (fresh day)", got)
	}

	if err := ledger.Reserve(ctx, 1, 10); err != nil {
		t.Fatalf("Reserve after rollover: %v", err)
	}
	got, err = ledger.UsedToday(ctx)
	if err != nil {
		t.Fatalf("UsedToday: %v", err)
	}
	if got != 1 {
		t.Errorf("UsedToday = %d, want 1 (only today's reservations)", got)
	}

	// Yesterday's row is the audit trail; rollover must not touch it.
	var yesterday int
	if err := database.QueryRow(
		`SELECT calls_used FROM weather_call_budget WHERE usage_date = '2026-08-09'`,
	).Scan(&yesterday); err != nil {
		t.Fatalf("read yesterday's row: %v", err)
	}
	if yesterday != 2 {
		t.Errorf("yesterday's calls_used = %d, want 2", yesterday)
	}
}

// TestBudgetLedgerSurvivesRestart exists specifically to prevent regressing
// to the WHOOP counter failure: the spend count lived in process memory, so
// every deploy handed the integration a fresh allowance. A NEW BudgetLedger
// over the same database must see the calls the old one reserved.
func TestBudgetLedgerSurvivesRestart(t *testing.T) {
	database := dbtest.New(t)
	day := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	first := NewBudgetLedger(database)
	fixLedgerClock(first, day)
	if err := first.Reserve(ctx, 3, 10); err != nil {
		t.Fatalf("Reserve on first ledger: %v", err)
	}

	// "Restart": a fresh ledger instance, same durable store.
	second := NewBudgetLedger(database)
	fixLedgerClock(second, day)
	got, err := second.UsedToday(ctx)
	if err != nil {
		t.Fatalf("UsedToday on second ledger: %v", err)
	}
	if got != 3 {
		t.Fatalf("UsedToday after restart = %d, want 3 (spend must be durable)", got)
	}
	// And the restarted ledger must respect the prior spend when reserving.
	if err := second.Reserve(ctx, 1, 3); !errors.Is(err, ErrBudgetExhausted) {
		t.Errorf("Reserve past prior spend after restart: err = %v, want ErrBudgetExhausted", err)
	}
}

// TestBudgetLedgerConcurrentReserves hammers one day's row from 100
// goroutines with a ceiling of 50: exactly 50 must win, exactly 50 must get
// ErrBudgetExhausted, and the durable count must be exactly 50. Run under
// -race; this is the test that keeps the reserve atomic.
func TestBudgetLedgerConcurrentReserves(t *testing.T) {
	database := dbtest.New(t)
	ledger := NewBudgetLedger(database)
	fixLedgerClock(ledger, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()

	const goroutines = 100
	const ceiling = 50
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ledger.Reserve(ctx, 1, ceiling)
		}()
	}
	wg.Wait()
	close(errs)

	var ok, exhausted int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrBudgetExhausted):
			exhausted++
		default:
			t.Fatalf("unexpected Reserve error: %v", err)
		}
	}
	if ok != ceiling || exhausted != goroutines-ceiling {
		t.Errorf("got %d successes and %d exhausted, want %d and %d", ok, exhausted, ceiling, goroutines-ceiling)
	}

	var used int
	if err := database.QueryRow(
		`SELECT calls_used FROM weather_call_budget WHERE usage_date = '2026-08-09'`,
	).Scan(&used); err != nil {
		t.Fatalf("read calls_used: %v", err)
	}
	if used != ceiling {
		t.Errorf("calls_used = %d, want exactly %d", used, ceiling)
	}
}

// TestBudgetLedgerMultiCallReserveIsAtomic pins the all-or-nothing contract:
// a refresh needing 3 calls when only 2 remain must fail entirely and leave
// the remaining 2 available.
func TestBudgetLedgerMultiCallReserveIsAtomic(t *testing.T) {
	ledger := NewBudgetLedger(dbtest.New(t))
	fixLedgerClock(ledger, time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC))
	ctx := context.Background()
	const ceiling = 4

	if err := ledger.Reserve(ctx, 2, ceiling); err != nil {
		t.Fatalf("Reserve(2): %v", err)
	}
	if err := ledger.Reserve(ctx, 3, ceiling); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("Reserve(3) with 2 remaining: err = %v, want ErrBudgetExhausted", err)
	}
	got, err := ledger.UsedToday(ctx)
	if err != nil {
		t.Fatalf("UsedToday: %v", err)
	}
	if got != 2 {
		t.Fatalf("UsedToday after failed Reserve(3) = %d, want 2 (nothing consumed)", got)
	}
	// The 2 remaining calls are still reservable — the failure was clean.
	if err := ledger.Reserve(ctx, 2, ceiling); err != nil {
		t.Errorf("Reserve(2) after failed Reserve(3): %v", err)
	}
}

// TestBudgetLedgerUsedTodayEmptyDay: a day with no reservations reads as 0,
// not sql.ErrNoRows.
func TestBudgetLedgerUsedTodayEmptyDay(t *testing.T) {
	ledger := NewBudgetLedger(dbtest.New(t))
	got, err := ledger.UsedToday(context.Background())
	if err != nil {
		t.Fatalf("UsedToday on empty ledger: %v", err)
	}
	if got != 0 {
		t.Errorf("UsedToday = %d, want 0", got)
	}
}
