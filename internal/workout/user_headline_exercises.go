package workout

import "time"

// MaxHeadlineExercises caps a user's selection size. The frontend
// modal enforces the same limit; the handler re-validates on the
// server side so a misbehaving client can't blow up the row set.
//
// Picked so the Personal Records grid stays scannable: 12 fits a 3-
// column tablet grid and a 2-column phone grid without crowding.
// Easy to bump — pure handler-side validation, no schema change.
const MaxHeadlineExercises = 12

// UserHeadlineExercise is one row in user_headline_exercises:
// "user U has exercise slug E pinned at display position P." The
// presence of any row for a user marks them as having customized
// their selection — the read path falls back to the curated
// HeadlineExercises slice when there are zero rows.
type UserHeadlineExercise struct {
	UserID     string
	ExerciseID string
	Position   int
	CreatedAt  time.Time
}
