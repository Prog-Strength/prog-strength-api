package workout

// HeadlineExercises is the curated default list of exercise slugs
// surfaced on the Personal Records view. Lives in backend Go (rather
// than in any frontend) so the web and future mobile clients see the
// same defaults without having to be kept in sync. Used in two places:
//
//  1. The personalRecords handler iterates over a user's selection
//     from user_headline_exercises, falling back to this list when
//     the user has no rows yet.
//  2. The /headline-exercises/defaults endpoint serves this list so
//     the customization modal can label which catalog entries are
//     part of the curated default set and implement "Reset to
//     defaults" without baking slugs into the frontend.
//
// Slugs must exist in the exercise catalog;
// TestHeadlineExercises_AllInCatalog verifies that so a typo doesn't
// ship silently.
//
// Renamed from HeadlineLifts in the custom-headline-lifts SOW so the
// shared "headline" framing extends to a future user_headline_runs
// (or similar) when cardio support lands. See
// prog-strength-docs/sows/custom-headline-lifts.md.
var HeadlineExercises = []string{
	"barbell-bench-press",
	"barbell-high-bar-back-squat",
	"barbell-deadlift",
}
