package timeline

// Visibility is the closed set of post visibilities. It exists as
// forward-scaffolding for the friends/followers SOW: present and
// CHECK-constrained from day one, but always VisibilityPrivate in v1. That
// SOW flips defaults and gives 'friends'/'public' real semantics by changing
// only the canView authorization rule — no migration of existing rows.
type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityFriends Visibility = "friends"
	VisibilityPublic  Visibility = "public"
)

// Valid reports whether v is one of the known visibilities. Mirrors the
// CHECK on timeline_post.visibility.
func (v Visibility) Valid() bool {
	switch v {
	case VisibilityPrivate, VisibilityFriends, VisibilityPublic:
		return true
	}
	return false
}
