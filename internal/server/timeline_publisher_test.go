package server

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/timeline"
)

// fakeTimelineRepo is a timeline.Repository whose EnsurePost behavior is
// driven by the test. Only EnsurePost is exercised here; the rest are
// inherited from the embedded nil interface and panic if called (they
// shouldn't be).
type fakeTimelineRepo struct {
	timeline.Repository
	ensureErr error
	calls     []timeline.PostRef
}

func (f *fakeTimelineRepo) EnsurePost(_ context.Context, ref timeline.PostRef) (timeline.Post, error) {
	f.calls = append(f.calls, ref)
	if f.ensureErr != nil {
		return timeline.Post{}, f.ensureErr
	}
	return timeline.Post{ID: "p1", UserID: ref.UserID, SourceType: ref.SourceType, SourceID: ref.SourceID}, nil
}

func TestTimelinePublisher_Success(t *testing.T) {
	repo := &fakeTimelineRepo{}
	pub := newTimelinePublisher(repo)
	ref := timeline.PostRef{UserID: "u1", SourceType: timeline.SourceWorkout, SourceID: "w1", OccurredAt: time.Now()}

	if err := pub.EnsurePost(context.Background(), ref); err != nil {
		t.Fatalf("EnsurePost: unexpected error %v", err)
	}
	if len(repo.calls) != 1 || repo.calls[0] != ref {
		t.Fatalf("repo not called with ref; calls=%v", repo.calls)
	}
}

func TestTimelinePublisher_FailureLoggedAndReturned(t *testing.T) {
	sentinel := errors.New("boom")
	repo := &fakeTimelineRepo{ensureErr: sentinel}
	pub := newTimelinePublisher(repo)
	ref := timeline.PostRef{UserID: "u1", SourceType: timeline.SourceRun, SourceID: "a1", OccurredAt: time.Now()}

	// Capture the log output to assert the failure is logged.
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	err := pub.EnsurePost(context.Background(), ref)
	if !errors.Is(err, sentinel) {
		t.Fatalf("EnsurePost err = %v, want sentinel", err)
	}
	if !strings.Contains(buf.String(), "publish failed") {
		t.Errorf("expected publish failure log, got %q", buf.String())
	}
	// Metering of the failure (the counter increment) is covered in the
	// timeline package's metrics test, where the unexported counter is in
	// scope; here we lock in the log + error-return contract.
}
