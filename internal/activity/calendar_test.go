package activity

import (
	"reflect"
	"testing"
)

func TestRenderManifest_UsesDescriptorCalendarEventWhenPresent(t *testing.T) {
	want := CalendarManifest{
		Title:    "✓ Threshold Run",
		Headline: "5.2 mi · 41:12",
		Sections: []ManifestSection{{Heading: "Heart rate", Lines: []string{"Avg 162 bpm"}}},
	}
	reg := NewRegistry(&Descriptor{
		Type:          ActivityRunning,
		CalendarEvent: func(Activity, any) CalendarManifest { return want },
		// A Summarize is also registered to prove CalendarEvent wins.
		Summarize: func(Activity, any) Summary { return Summary{Title: "card title"} },
	})

	got, ok := RenderManifest(reg, Activity{ID: "act_1", ActivityType: ActivityRunning}, nil)
	if !ok {
		t.Fatal("RenderManifest ok = false, want true")
	}
	if got.Title != want.Title || got.Headline != want.Headline {
		t.Fatalf("RenderManifest = %+v, want the descriptor's CalendarEvent output", got)
	}
	if !reflect.DeepEqual(got.Sections, want.Sections) {
		t.Fatalf("Sections = %+v, want %+v", got.Sections, want.Sections)
	}
}

// The fallback is the whole point of the design: a type that never writes a
// calendar renderer still syncs, so registering a new activity type needs no
// calendar work at all.
func TestRenderManifest_FallsBackToSummarizeWhenNoCalendarEvent(t *testing.T) {
	reg := NewRegistry(&Descriptor{
		Type: ActivityCycling,
		Summarize: func(Activity, any) Summary {
			return Summary{Title: "Evening Ride", Subtitle: "12.4 mi · 44:02", Metrics: []string{"12.4 mi", "44:02"}}
		},
	})

	got, ok := RenderManifest(reg, Activity{ID: "act_2", ActivityType: ActivityCycling}, nil)
	if !ok {
		t.Fatal("RenderManifest ok = false, want true")
	}
	if got.Title != "Evening Ride" {
		t.Fatalf("Title = %q, want %q", got.Title, "Evening Ride")
	}
	if got.Headline != "12.4 mi · 44:02" {
		t.Fatalf("Headline = %q, want the metrics joined", got.Headline)
	}
}

// Every endurance Summarize builds Subtitle by joining its own Metrics, so a
// naive adapter would print that line twice in the event body.
func TestRenderManifest_FallbackDropsSubtitleThatDuplicatesMetrics(t *testing.T) {
	reg := NewRegistry(&Descriptor{
		Type: ActivityWalking,
		Summarize: func(Activity, any) Summary {
			return Summary{Title: "Walk", Subtitle: "2.0 mi · 30:00", Metrics: []string{"2.0 mi", "30:00"}}
		},
	})

	got, _ := RenderManifest(reg, Activity{ID: "act_3", ActivityType: ActivityWalking}, nil)
	if len(got.Sections) != 0 {
		t.Fatalf("Sections = %+v, want none (subtitle duplicated the headline)", got.Sections)
	}
}

func TestRenderManifest_FallbackKeepsDistinctSubtitleAsASection(t *testing.T) {
	reg := NewRegistry(&Descriptor{
		Type: ActivityOther,
		Summarize: func(Activity, any) Summary {
			return Summary{Title: "Rowing", Subtitle: "Steady state, zone 2", Metrics: []string{"30:00"}}
		},
	})

	got, _ := RenderManifest(reg, Activity{ID: "act_4", ActivityType: ActivityOther}, nil)
	if len(got.Sections) != 1 || len(got.Sections[0].Lines) != 1 {
		t.Fatalf("Sections = %+v, want one bare section carrying the subtitle", got.Sections)
	}
	if got.Sections[0].Lines[0] != "Steady state, zone 2" {
		t.Fatalf("section line = %q, want the subtitle", got.Sections[0].Lines[0])
	}
}

// A metric-less type must not produce a body whose first line is blank.
func TestRenderManifest_FallbackPromotesSubtitleWhenThereAreNoMetrics(t *testing.T) {
	reg := NewRegistry(&Descriptor{
		Type: ActivityOther,
		Summarize: func(Activity, any) Summary {
			return Summary{Title: "Mobility", Subtitle: "20 minutes"}
		},
	})

	got, _ := RenderManifest(reg, Activity{ID: "act_5", ActivityType: ActivityOther}, nil)
	if got.Headline != "20 minutes" {
		t.Fatalf("Headline = %q, want the subtitle promoted", got.Headline)
	}
	if len(got.Sections) != 0 {
		t.Fatalf("Sections = %+v, want none (subtitle was promoted)", got.Sections)
	}
}

func TestRenderManifest_DefaultsLinkPathToTheCanonicalRedirector(t *testing.T) {
	reg := NewRegistry(&Descriptor{
		Type:          ActivityRunning,
		CalendarEvent: func(Activity, any) CalendarManifest { return CalendarManifest{Title: "Run"} },
	})

	got, _ := RenderManifest(reg, Activity{ID: "act_01H8X", ActivityType: ActivityRunning}, nil)
	if got.LinkPath != "/activities/act_01H8X" {
		t.Fatalf("LinkPath = %q, want the canonical /activities/{id} redirector", got.LinkPath)
	}
}

func TestRenderManifest_PreservesAnExplicitLinkPath(t *testing.T) {
	reg := NewRegistry(&Descriptor{
		Type: ActivityRunning,
		CalendarEvent: func(Activity, any) CalendarManifest {
			return CalendarManifest{Title: "Run", LinkPath: "/somewhere/else"}
		},
	})

	got, _ := RenderManifest(reg, Activity{ID: "act_6", ActivityType: ActivityRunning}, nil)
	if got.LinkPath != "/somewhere/else" {
		t.Fatalf("LinkPath = %q, want the descriptor's override preserved", got.LinkPath)
	}
}

func TestRenderManifest_NotRenderableWithoutRegistryTypeOrRenderer(t *testing.T) {
	t.Run("nil registry", func(t *testing.T) {
		if _, ok := RenderManifest(nil, Activity{ActivityType: ActivityRunning}, nil); ok {
			t.Fatal("ok = true for a nil registry, want false")
		}
	})
	t.Run("unregistered type", func(t *testing.T) {
		reg := NewRegistry(&Descriptor{Type: ActivityRunning})
		if _, ok := RenderManifest(reg, Activity{ActivityType: ActivityCycling}, nil); ok {
			t.Fatal("ok = true for an unregistered type, want false")
		}
	})
	t.Run("no renderer at all", func(t *testing.T) {
		reg := NewRegistry(&Descriptor{Type: ActivityRunning})
		if _, ok := RenderManifest(reg, Activity{ActivityType: ActivityRunning}, nil); ok {
			t.Fatal("ok = true for a descriptor with neither renderer, want false")
		}
	})
}
