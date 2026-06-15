package timeline

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestObservePublishFailure verifies the exported metering seam increments
// the publish-failure counter for the labeled source_type. This is the
// counter the server-package publisher bumps on a swallowed EnsurePost error.
func TestObservePublishFailure(t *testing.T) {
	before := testutil.ToFloat64(publishFailuresTotal.WithLabelValues(string(SourceBestEffort)))

	ObservePublishFailure(SourceBestEffort)
	ObservePublishFailure(SourceBestEffort)

	after := testutil.ToFloat64(publishFailuresTotal.WithLabelValues(string(SourceBestEffort)))
	if after-before != 2 {
		t.Fatalf("counter delta = %v, want 2", after-before)
	}
}
