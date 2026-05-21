package workout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/auth"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/exercise"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/httpresp"
)

// headlineExerciseDTO is one row in the user's effective selection.
// `is_default` lets the modal annotate which exercises are part of
// the curated default set without making a second request.
type headlineExerciseDTO struct {
	ExerciseID   string `json:"exercise_id"`
	ExerciseName string `json:"exercise_name"`
	Position     int    `json:"position"`
	IsDefault    bool   `json:"is_default"`
}

// defaultHeadlineExerciseDTO is the slimmer shape for the defaults
// endpoint: no position (the curated list has its own canonical
// order) and no is_default flag (everything returned here is one).
type defaultHeadlineExerciseDTO struct {
	ExerciseID   string `json:"exercise_id"`
	ExerciseName string `json:"exercise_name"`
}

// effectiveHeadlineExerciseSlugs returns the slug list to drive the
// Personal Records page for the given user. Reads
// user_headline_exercises first; falls back to the curated
// HeadlineExercises default when the user has no rows. See
// prog-strength-docs/sows/custom-headline-lifts.md.
func (h *Handler) effectiveHeadlineExerciseSlugs(
	ctx context.Context,
	userID string,
) ([]string, error) {
	custom, err := h.repo.ListUserHeadlineExercises(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(custom) == 0 {
		// Defensive copy so callers can't mutate the package-level slice.
		out := make([]string, len(HeadlineExercises))
		copy(out, HeadlineExercises)
		return out, nil
	}
	out := make([]string, len(custom))
	for i, c := range custom {
		out[i] = c.ExerciseID
	}
	return out, nil
}

// buildHeadlineExerciseDTOs resolves the given slug list against the
// exercise catalog for display names and against HeadlineExercises
// for the is_default flag. A slug missing from the catalog still
// renders (with the slug as the name) rather than dropping the row,
// matching the existing personalRecords handler's tolerance.
func (h *Handler) buildHeadlineExerciseDTOs(
	ctx context.Context,
	slugs []string,
) []headlineExerciseDTO {
	defaults := make(map[string]bool, len(HeadlineExercises))
	for _, s := range HeadlineExercises {
		defaults[s] = true
	}
	out := make([]headlineExerciseDTO, 0, len(slugs))
	for i, slug := range slugs {
		name := slug
		if ex, err := h.exerciseRepo.GetByID(ctx, slug); err == nil {
			name = ex.Name
		}
		out = append(out, headlineExerciseDTO{
			ExerciseID:   slug,
			ExerciseName: name,
			Position:     i,
			IsDefault:    defaults[slug],
		})
	}
	return out
}

// getMyHeadlineExercises handles GET /me/headline-exercises.
//
// Returns the user's selection in display order, falling back to the
// curated defaults when they haven't customized. The modal uses this
// to pre-check the right boxes when it opens.
func (h *Handler) getMyHeadlineExercises(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}
	slugs, err := h.effectiveHeadlineExerciseSlugs(r.Context(), userID)
	if err != nil {
		httpresp.ServerError(w, r.Context(), "list user headline exercises", err)
		return
	}
	out := h.buildHeadlineExerciseDTOs(r.Context(), slugs)
	httpresp.OK(w, "listed headline exercises", out)
}

// putHeadlineExercisesRequest is the body shape for the replace
// endpoint. Ordered: the array index becomes the row position.
type putHeadlineExercisesRequest struct {
	ExerciseIDs []string `json:"exercise_ids"`
}

// putMyHeadlineExercises handles PUT /me/headline-exercises.
//
// Replaces the user's selection wholesale. The repo does the
// delete+insert in one transaction. Validation enforced here (not
// in the repo) since it needs the exercise catalog: every slug must
// exist, be non-empty, and not appear twice; the list itself must be
// non-empty and within MaxHeadlineExercises.
func (h *Handler) putMyHeadlineExercises(w http.ResponseWriter, r *http.Request) {
	userID, ok := auth.UserIDFrom(r.Context())
	if !ok {
		httpresp.ServerError(w, r.Context(), "missing user in context", errors.New("auth middleware not applied"))
		return
	}

	var req putHeadlineExercisesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpresp.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.ExerciseIDs) == 0 {
		httpresp.Error(w, http.StatusBadRequest, "at least one exercise is required")
		return
	}
	if len(req.ExerciseIDs) > MaxHeadlineExercises {
		httpresp.Error(w, http.StatusBadRequest,
			fmt.Sprintf("at most %d exercises allowed", MaxHeadlineExercises))
		return
	}
	seen := make(map[string]bool, len(req.ExerciseIDs))
	for _, slug := range req.ExerciseIDs {
		if slug == "" {
			httpresp.Error(w, http.StatusBadRequest, "empty exercise id in selection")
			return
		}
		if seen[slug] {
			httpresp.Error(w, http.StatusBadRequest,
				fmt.Sprintf("duplicate exercise in selection: %s", slug))
			return
		}
		seen[slug] = true
		if _, err := h.exerciseRepo.GetByID(r.Context(), slug); err != nil {
			if errors.Is(err, exercise.ErrNotFound) {
				httpresp.Error(w, http.StatusBadRequest,
					fmt.Sprintf("unknown exercise: %s", slug))
				return
			}
			httpresp.ServerError(w, r.Context(), "validate exercise slug", err)
			return
		}
	}

	if err := h.repo.ReplaceUserHeadlineExercises(
		r.Context(), userID, req.ExerciseIDs, time.Now().UTC(),
	); err != nil {
		httpresp.ServerError(w, r.Context(), "replace user headline exercises", err)
		return
	}

	// Respond with the saved list in the same shape as GET so the
	// frontend can splice it into state without a second request.
	// Source from the request itself rather than re-reading the DB —
	// we just wrote it; rereading would be redundant.
	out := h.buildHeadlineExerciseDTOs(r.Context(), req.ExerciseIDs)
	httpresp.OK(w, "saved headline exercises", out)
}

// getHeadlineExercisesDefaults handles GET /headline-exercises/defaults.
//
// Returns the curated HeadlineExercises slice resolved against the
// catalog. Used by the modal so it can render "(default)" annotations
// across the full catalog and offer "Reset to defaults" without
// baking slugs into the frontend.
func (h *Handler) getHeadlineExercisesDefaults(w http.ResponseWriter, r *http.Request) {
	out := make([]defaultHeadlineExerciseDTO, 0, len(HeadlineExercises))
	for _, slug := range HeadlineExercises {
		name := slug
		if ex, err := h.exerciseRepo.GetByID(r.Context(), slug); err == nil {
			name = ex.Name
		}
		out = append(out, defaultHeadlineExerciseDTO{
			ExerciseID:   slug,
			ExerciseName: name,
		})
	}
	httpresp.OK(w, "listed default headline exercises", out)
}
