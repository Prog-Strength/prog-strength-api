package server

import (
	"context"
	"errors"
	"log"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/follow"
	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/user"
)

// followProfileProvider adapts the user domain to follow.ProfileProvider so the
// follow package can resolve usernames, check existence, and render profile
// summaries without importing the user package. It mirrors the user handler's
// avatar resolution (presigned uploaded avatar, OAuth fallback, or none) and
// nil-guards the avatar store the same way.
type followProfileProvider struct {
	userRepo    user.Repository
	avatarStore user.AvatarStore
}

var _ follow.ProfileProvider = (*followProfileProvider)(nil)

// newFollowProfileProvider builds the provider over the user repository and the
// (possibly nil) avatar store.
func newFollowProfileProvider(userRepo user.Repository, avatarStore user.AvatarStore) *followProfileProvider {
	return &followProfileProvider{userRepo: userRepo, avatarStore: avatarStore}
}

// ResolveUsername maps a username to its user id, translating user.ErrNotFound
// into follow.ErrNotFound so the follow handler's not-found branches fire.
func (p *followProfileProvider) ResolveUsername(ctx context.Context, username string) (string, error) {
	u, err := p.userRepo.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return "", follow.ErrNotFound
		}
		return "", err
	}
	return u.ID, nil
}

// UserExists reports whether userID refers to a live user. A not-found user is
// (false, nil), not an error.
func (p *followProfileProvider) UserExists(ctx context.Context, userID string) (bool, error) {
	_, err := p.userRepo.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, user.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ProfileSummaries batch-loads summaries by looping GetByID (the user repo has
// no batch read). Missing users are skipped — absent from the returned map.
func (p *followProfileProvider) ProfileSummaries(ctx context.Context, userIDs []string) (map[string]follow.ProfileSummary, error) {
	out := make(map[string]follow.ProfileSummary, len(userIDs))
	for _, uid := range userIDs {
		if _, seen := out[uid]; seen {
			continue
		}
		u, err := p.userRepo.GetByID(ctx, uid)
		if err != nil {
			if errors.Is(err, user.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out[uid] = follow.ProfileSummary{
			UserID:      u.ID,
			DisplayName: u.DisplayName,
			Username:    u.Username,
			AvatarURL:   p.avatarURL(ctx, u),
		}
	}
	return out, nil
}

// avatarURL resolves a user's avatar URL: a presigned GET of the uploaded
// avatar, the OAuth fallback, or nil — mirroring user.Handler.resolveMe and
// nil-guarding the store.
func (p *followProfileProvider) avatarURL(ctx context.Context, u *user.User) *string {
	switch {
	case u.AvatarKey != nil && p.avatarStore != nil:
		url, err := p.avatarStore.PresignGet(ctx, *u.AvatarKey)
		if err != nil {
			log.Printf("follow: avatar presign user_id=%s key=%s err=%v", u.ID, *u.AvatarKey, err)
			return u.OAuthAvatarURL // graceful fallback
		}
		return &url
	case u.OAuthAvatarURL != nil:
		return u.OAuthAvatarURL
	default:
		return nil
	}
}
