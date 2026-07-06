package user

import (
	"context"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/running/estimate"
)

// EstimateDemographicsLoader adapts the user repository for max-effort
// estimation in the activity handler.
type EstimateDemographicsLoader struct {
	Repo Repository
}

func (l EstimateDemographicsLoader) LoadEstimateDemographics(ctx context.Context, userID string, now time.Time) estimate.Demographics {
	u, err := l.Repo.GetByID(ctx, userID)
	if err != nil {
		return estimate.Demographics{}
	}
	return estimate.DemographicsFromProfile(u.HeightCm, u.Birthdate, u.Sex, now)
}
