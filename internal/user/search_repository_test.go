package user

import (
	"context"
	"testing"
)

// makeNamedUser inserts a user with the given display name and (optional)
// username, returning it. A nil username leaves the handle unset.
func makeNamedUser(t *testing.T, repo Repository, email, displayName string, username *string) *User {
	t.Helper()
	u := &User{
		Email:        email,
		DisplayName:  displayName,
		Username:     username,
		WeightUnit:   WeightUnitPounds,
		DistanceUnit: DistanceUnitMiles,
	}
	if err := repo.Create(context.Background(), u); err != nil {
		t.Fatalf("Create(%s): %v", email, err)
	}
	return u
}

// searchIDs runs a search and returns the ordered result ids.
func searchIDs(t *testing.T, repo Repository, q string, limit int, after *SearchCursor) ([]string, *SearchCursor) {
	t.Helper()
	users, next, err := repo.SearchProfiles(context.Background(), q, limit, after)
	if err != nil {
		t.Fatalf("SearchProfiles(%q): %v", q, err)
	}
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	return ids, next
}

// TestSearch_RankingOrder verifies exact-username > prefix-username >
// substring-display-name bucketing across both backends.
func TestSearch_RankingOrder(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			// exact username "jim"
			exact := makeNamedUser(t, b.repo, "a@example.com", "Zed", strPtr("jim"))
			// prefix username "jimmy"
			prefix := makeNamedUser(t, b.repo, "b@example.com", "Aaron", strPtr("jimmy"))
			// substring display name "Benjimin", username unrelated
			substr := makeNamedUser(t, b.repo, "c@example.com", "Benjim Smith", strPtr("zzz_other"))
			// non-match
			makeNamedUser(t, b.repo, "d@example.com", "Nobody", strPtr("nope"))

			ids, _ := searchIDs(t, b.repo, "jim", 10, nil)
			want := []string{exact.ID, prefix.ID, substr.ID}
			if len(ids) != len(want) {
				t.Fatalf("got %v ids, want %v", ids, want)
			}
			for i := range want {
				if ids[i] != want[i] {
					t.Fatalf("rank order[%d] = %s, want %s (full: %v want %v)", i, ids[i], want[i], ids, want)
				}
			}
		})
	}
}

// TestSearch_CaseInsensitive verifies the query and stored values are folded.
func TestSearch_CaseInsensitive(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			u := makeNamedUser(t, b.repo, "a@example.com", "Casey Strong", strPtr("casey"))

			// Uppercase query against lowercased stored handle.
			ids, _ := searchIDs(t, b.repo, "CASEY", 10, nil)
			if len(ids) != 1 || ids[0] != u.ID {
				t.Fatalf("exact uppercase query: got %v, want [%s]", ids, u.ID)
			}
			// Mixed-case substring against display name.
			ids, _ = searchIDs(t, b.repo, "StRoNg", 10, nil)
			if len(ids) != 1 || ids[0] != u.ID {
				t.Fatalf("substring mixed-case: got %v, want [%s]", ids, u.ID)
			}
		})
	}
}

// TestSearch_EmptyQuery verifies an empty/whitespace query yields no results
// and no error.
func TestSearch_EmptyQuery(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			makeNamedUser(t, b.repo, "a@example.com", "Someone", strPtr("someone"))
			for _, q := range []string{"", "   "} {
				ids, next := searchIDs(t, b.repo, q, 10, nil)
				if len(ids) != 0 || next != nil {
					t.Fatalf("empty query %q: ids=%v next=%v, want empty/nil", q, ids, next)
				}
			}
		})
	}
}

// TestSearch_NullUsernameExcludedFromUsernamePredicates verifies a NULL-username
// user never matches the username predicates but is still matchable by display
// name.
func TestSearch_NullUsernameExcludedFromUsernamePredicates(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			// No username; display name contains "alpha".
			noHandle := makeNamedUser(t, b.repo, "a@example.com", "Alpha Person", nil)
			// Has username "alpha" (exact).
			withHandle := makeNamedUser(t, b.repo, "b@example.com", "Different", strPtr("alpha"))

			ids, _ := searchIDs(t, b.repo, "alpha", 10, nil)
			// Both match: withHandle via exact username (bucket 0), noHandle via
			// display-name substring (bucket 2). Order: withHandle first.
			if len(ids) != 2 {
				t.Fatalf("got %v, want both users", ids)
			}
			if ids[0] != withHandle.ID {
				t.Fatalf("first = %s, want exact-username %s", ids[0], withHandle.ID)
			}
			if ids[1] != noHandle.ID {
				t.Fatalf("second = %s, want display-name match %s", ids[1], noHandle.ID)
			}
		})
	}
}

// TestSearch_SoftDeletedExcluded verifies a soft-deleted user never appears.
func TestSearch_SoftDeletedExcluded(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			ctx := context.Background()
			gone := makeNamedUser(t, b.repo, "a@example.com", "Ghost", strPtr("ghosthandle"))
			if err := b.repo.Delete(ctx, gone.ID); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			ids, _ := searchIDs(t, b.repo, "ghost", 10, nil)
			if len(ids) != 0 {
				t.Fatalf("soft-deleted matched: %v", ids)
			}
		})
	}
}

// TestSearch_PaginationStability walks every page with limit=1 and asserts the
// concatenation reproduces the single-page order with no gaps or repeats,
// including same-bucket ties (multiple prefix matches sharing bucket 1).
func TestSearch_PaginationStability(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			// Five prefix-username matches (all bucket 1) plus distinct sort keys
			// so the (bucket, sortkey, id) order is well-defined and ties on
			// bucket are exercised.
			handles := []string{"team_a", "team_b", "team_c", "team_d", "team_e"}
			for i, hname := range handles {
				makeNamedUser(t, b.repo, string(rune('a'+i))+"@example.com", "Member "+hname, strPtr(hname))
			}

			// Single page (oversized limit) is the source of truth.
			full, next := searchIDs(t, b.repo, "team", 100, nil)
			if next != nil {
				t.Fatalf("oversized page returned a cursor: %v", next)
			}
			if len(full) != len(handles) {
				t.Fatalf("full page got %d, want %d (%v)", len(full), len(handles), full)
			}

			// Walk page-by-page with limit 1.
			var paged []string
			var cursor *SearchCursor
			for {
				ids, nx := searchIDs(t, b.repo, "team", 1, cursor)
				if len(ids) == 0 {
					break
				}
				if len(ids) != 1 {
					t.Fatalf("limit=1 returned %d rows", len(ids))
				}
				paged = append(paged, ids[0])
				if nx == nil {
					break
				}
				cursor = nx
				if len(paged) > len(handles) {
					t.Fatalf("pagination did not terminate: %v", paged)
				}
			}

			if len(paged) != len(full) {
				t.Fatalf("paged %v != full %v", paged, full)
			}
			for i := range full {
				if paged[i] != full[i] {
					t.Fatalf("page order diverges at %d: paged=%v full=%v", i, paged, full)
				}
			}
		})
	}
}

// TestSearch_NextCursorNilWhenExhausted verifies the last page yields a nil
// cursor when the result count is an exact multiple of the page size.
func TestSearch_NextCursorNilWhenExhausted(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			makeNamedUser(t, b.repo, "a@example.com", "One", strPtr("uniq_one"))
			makeNamedUser(t, b.repo, "b@example.com", "Two", strPtr("uniq_two"))

			// Exactly two results, limit 2 → no next page.
			_, next := searchIDs(t, b.repo, "uniq", 2, nil)
			if next != nil {
				t.Fatalf("expected nil cursor on exhausting page, got %v", next)
			}
		})
	}
}

// TestSearch_WildcardInjectionEscaped verifies LIKE wildcards in user input are
// treated literally (a query of "%" must not match every user).
func TestSearch_WildcardInjectionEscaped(t *testing.T) {
	for _, b := range repoBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			makeNamedUser(t, b.repo, "a@example.com", "Plain", strPtr("plain"))
			lit := makeNamedUser(t, b.repo, "b@example.com", "Has % Percent", strPtr("pctuser"))

			ids, _ := searchIDs(t, b.repo, "%", 10, nil)
			// "%" should match only the user whose display name literally
			// contains "%", not every user.
			if len(ids) != 1 || ids[0] != lit.ID {
				t.Fatalf("wildcard %% query: got %v, want only literal-%% user %s", ids, lit.ID)
			}
		})
	}
}
