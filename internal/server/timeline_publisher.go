package server

import (
	"context"
	"log"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
)

// timelinePublisher adapts the timeline Repository to the timeline.Publisher
// seam the workout/activity handlers depend on. It exists so those domains
// push events into the feed index through a narrow interface rather than the
// full repository, and so the one place publish failures are logged + metered
// lives in the wiring layer.
//
// Publishing is best-effort by contract: EnsurePost returns the error so the
// interface stays honest and testable, but the calling handlers treat it as
// fire-and-forget — a feed-index hiccup must never fail a workout/run write.
type timelinePublisher struct {
	repo timeline.Repository
}

// newTimelinePublisher builds a Publisher over the given timeline repository.
func newTimelinePublisher(repo timeline.Repository) *timelinePublisher {
	return &timelinePublisher{repo: repo}
}

var _ timeline.Publisher = (*timelinePublisher)(nil)

// EnsurePost idempotently records ref in the feed index. On failure it logs
// and increments the publish-failure counter (labelled by source_type) so a
// silently-degrading feed index is observable, then returns the error.
// Callers ignore the return value; it is surfaced only so the publisher is
// unit-testable and the interface contract is truthful.
func (p *timelinePublisher) EnsurePost(ctx context.Context, ref timeline.PostRef) error {
	if _, err := p.repo.EnsurePost(ctx, ref); err != nil {
		log.Printf("timeline: publish failed source_type=%s source_id=%s user_id=%s: %v",
			ref.SourceType, ref.SourceID, ref.UserID, err)
		timeline.ObservePublishFailure(ref.SourceType)
		return err
	}
	return nil
}
