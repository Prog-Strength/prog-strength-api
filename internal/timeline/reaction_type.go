package timeline

// ReactionType is the closed set of reactions a user can apply to a post.
// It mirrors the CHECK constraint on timeline_reaction.type; the handler
// rejects an unknown type with ErrValidation rather than letting the insert
// fail at the DB. The four v1 types are surfaced in both clients as 👍 Like,
// 💪 Strong, 🔥 Fire, and 🎉 Celebrate.
type ReactionType string

const (
	ReactionLike      ReactionType = "like"
	ReactionStrong    ReactionType = "strong"
	ReactionFire      ReactionType = "fire"
	ReactionCelebrate ReactionType = "celebrate"
)

// Valid reports whether t is one of the known reaction types. Used by the
// handler to reject unknown {type} path segments before touching storage.
func (t ReactionType) Valid() bool {
	switch t {
	case ReactionLike, ReactionStrong, ReactionFire, ReactionCelebrate:
		return true
	}
	return false
}
