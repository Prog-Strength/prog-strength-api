package timeline

import "context"

// Author is a timeline-local view of a user summary, resolved via
// ProfileResolver. It is the identity the timeline embeds on each post and
// comment so a client can render who authored a card without a follow-up read.
// Keeping it package-local (rather than importing the user/follow shape) is what
// lets the timeline package stay import-clean — the wiring layer adapts the
// cross-domain summary into this struct.
type Author struct {
	UserID      string
	Username    *string
	DisplayName string
	AvatarURL   *string
}

// ProfileResolver batch-resolves user summaries; missing IDs are simply absent
// from the returned map. The timeline calls it ONCE per page (the distinct set
// of author ids) so author hydration never fans out into an N+1.
type ProfileResolver interface {
	Authors(ctx context.Context, userIDs []string) (map[string]Author, error)
}

// authorDTO is the wire shape of an embedded author identity.
type authorDTO struct {
	UserID      string  `json:"user_id"`
	Username    *string `json:"username"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
}

// toAuthorDTO shapes a resolved Author for the wire. Author and authorDTO have
// identical fields (the DTO only adds json tags), so a direct conversion is
// exact and the simplest expression of the mapping.
func toAuthorDTO(a Author) authorDTO {
	return authorDTO(a)
}
