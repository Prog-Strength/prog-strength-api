package nutrition

import (
	"strings"
	"time"
)

// PantryItem is one user-saved food entry with per-serving macros.
// "5 eggs" is represented as a log entry with quantity=5 against an
// item whose serving_size=1 and serving_unit="egg" — the math only
// cares about the per-serving macros; the size + unit are descriptive.
//
// Soft delete: DeletedAt non-nil means the item is hidden from the
// pantry list but historical log entries referencing it stay
// readable because the log carries denormalized macros.
type PantryItem struct {
	ID          string
	UserID      string
	Name        string
	Calories    float64
	ProteinG    float64
	FatG        float64
	CarbsG      float64
	ServingSize float64
	ServingUnit string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Validate checks the invariants the schema enforces server-side.
// Called by handlers before any repo write so the user gets a clean
// 400 rather than a DB CHECK violation surfaced as a 500.
func (p *PantryItem) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return ErrNameRequired
	}
	if strings.TrimSpace(p.ServingUnit) == "" {
		return ErrServingUnitRequired
	}
	if p.Calories < 0 || p.ProteinG < 0 || p.FatG < 0 || p.CarbsG < 0 {
		return ErrMacrosNegative
	}
	if p.ServingSize <= 0 {
		return ErrServingSizeNonPositive
	}
	return nil
}
