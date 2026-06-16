package user

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/jwallace145/progressive-overload-fitness-tracker/internal/id"
)

// Compile-time check that *SQLiteRepository satisfies Repository.
var _ Repository = (*SQLiteRepository)(nil)

// SQLiteRepository is a SQLite-backed implementation of Repository.
type SQLiteRepository struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{
		db:  db,
		now: time.Now,
	}
}

func (r *SQLiteRepository) Create(ctx context.Context, u *User) error {
	// Default the calendar prefs before validation so a newly-built user
	// without them set passes Validate and matches the DB defaults.
	if u.Timezone == "" {
		u.Timezone = "UTC"
	}
	if u.CalendarDefaultDetail == "" {
		u.CalendarDefaultDetail = "time_block"
	}

	if err := u.Validate(); err != nil {
		return err
	}

	now := r.now().UTC()
	u.ID = id.New()
	u.Email = normalizeEmail(u.Email)
	u.CreatedAt = now
	u.UpdatedAt = now

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, username, weight_unit, distance_unit, height_cm, bio, avatar_key, oauth_avatar_url, timezone, calendar_default_detail, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.Email, u.DisplayName, u.Username, u.WeightUnit, u.DistanceUnit, u.HeightCm, u.Bio, u.AvatarKey, u.OAuthAvatarURL, u.Timezone, u.CalendarDefaultDetail, u.CreatedAt, u.UpdatedAt)

	if err != nil {
		// Check for UNIQUE constraint violation on email.
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			return ErrEmailExists
		}
		return err
	}

	return nil
}

func (r *SQLiteRepository) GetByID(ctx context.Context, id string) (*User, error) {
	var u User
	err := r.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, username, weight_unit, distance_unit, height_cm, bio, avatar_key, oauth_avatar_url, timezone, calendar_default_detail, created_at, updated_at, deleted_at
		FROM users
		WHERE id = ? AND deleted_at IS NULL
	`, id).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Username, &u.WeightUnit, &u.DistanceUnit, &u.HeightCm, &u.Bio, &u.AvatarKey, &u.OAuthAvatarURL, &u.Timezone, &u.CalendarDefaultDetail, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &u, nil
}

func (r *SQLiteRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	email = normalizeEmail(email)

	var u User
	err := r.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, username, weight_unit, distance_unit, height_cm, bio, avatar_key, oauth_avatar_url, timezone, calendar_default_detail, created_at, updated_at, deleted_at
		FROM users
		WHERE email = ? AND deleted_at IS NULL
	`, email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Username, &u.WeightUnit, &u.DistanceUnit, &u.HeightCm, &u.Bio, &u.AvatarKey, &u.OAuthAvatarURL, &u.Timezone, &u.CalendarDefaultDetail, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &u, nil
}

func (r *SQLiteRepository) GetByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := r.db.QueryRowContext(ctx, `
		SELECT id, email, display_name, username, weight_unit, distance_unit, height_cm, bio, avatar_key, oauth_avatar_url, timezone, calendar_default_detail, created_at, updated_at, deleted_at
		FROM users
		WHERE username = ? AND deleted_at IS NULL
	`, username).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Username, &u.WeightUnit, &u.DistanceUnit, &u.HeightCm, &u.Bio, &u.AvatarKey, &u.OAuthAvatarURL, &u.Timezone, &u.CalendarDefaultDetail, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &u, nil
}

func (r *SQLiteRepository) Update(ctx context.Context, u *User) error {
	if err := u.Validate(); err != nil {
		return err
	}

	// Fetch existing user to preserve Email and CreatedAt.
	existing, err := r.GetByID(ctx, u.ID)
	if err != nil {
		return err
	}

	u.Email = existing.Email
	u.CreatedAt = existing.CreatedAt
	u.UpdatedAt = r.now().UTC()

	result, err := r.db.ExecContext(ctx, `
		UPDATE users
		SET display_name = ?, username = ?, weight_unit = ?, distance_unit = ?, height_cm = ?, bio = ?, avatar_key = ?, oauth_avatar_url = ?, timezone = ?, calendar_default_detail = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, u.DisplayName, u.Username, u.WeightUnit, u.DistanceUnit, u.HeightCm, u.Bio, u.AvatarKey, u.OAuthAvatarURL, u.Timezone, u.CalendarDefaultDetail, u.UpdatedAt, u.ID)

	if err != nil {
		// A unique-index violation on Update can only come from the username
		// index (email is preserved from the existing row above); surface it
		// as a domain-level taken error so the handler can map it to 409.
		var sqliteErr sqlite3.Error
		if errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique {
			return ErrUsernameTaken
		}
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}

// escapeLike escapes the LIKE wildcards (% and _) and the escape char itself
// in user-supplied input so the query can use it as a literal prefix/substring
// under an `ESCAPE '\'` clause — a user typing "a_b" or "50%" can't smuggle in
// wildcard semantics that would broaden the match.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// SearchProfiles implements the ranked, keyset-paginated profile search. The
// CASE-expression bucket and SortKey are computed in an inner SELECT so the
// outer keyset WHERE can reference them by name; the whole query is fully
// parameterized (LIKE input escaped via escapeLike + ESCAPE '\'). See the
// Repository interface doc for the follower_count tiebreak deferral.
func (r *SQLiteRepository) SearchProfiles(ctx context.Context, query string, limit int, after *SearchCursor) ([]*User, *SearchCursor, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []*User{}, nil, nil
	}
	if limit < 1 {
		limit = 1
	}
	esc := escapeLike(q)
	prefix := esc + "%"         // username LIKE q%
	contains := "%" + esc + "%" // lower(display_name) LIKE %q%

	// The inner query computes, per matching non-deleted user, the best bucket
	// and the stable sort key; the outer query applies the keyset filter and
	// the total order. Fetch limit+1 to detect a next page.
	args := []any{
		q, prefix, // bucket CASE
		q, prefix, contains, // WHERE match predicate
	}
	keysetClause := ""
	if after != nil {
		// Strictly-greater tuple over (bucket, sortkey, id).
		keysetClause = `
		WHERE bucket > ?
		   OR (bucket = ? AND sortkey > ?)
		   OR (bucket = ? AND sortkey = ? AND id > ?)`
		args = append(args, after.Bucket, after.Bucket, after.SortKey, after.Bucket, after.SortKey, after.ID)
	}
	args = append(args, limit+1)

	sqlStr := `
	SELECT id, email, display_name, username, weight_unit, distance_unit, height_cm, avatar_key, oauth_avatar_url, created_at, updated_at, deleted_at, bucket, sortkey
	FROM (
		SELECT id, email, display_name, username, weight_unit, distance_unit, height_cm, avatar_key, oauth_avatar_url, created_at, updated_at, deleted_at,
			CASE
				WHEN username IS NOT NULL AND lower(username) = ? THEN 0
				WHEN username IS NOT NULL AND lower(username) LIKE ? ESCAPE '\' THEN 1
				ELSE 2
			END AS bucket,
			COALESCE(lower(username), lower(display_name)) AS sortkey
		FROM users
		WHERE deleted_at IS NULL
		  AND (
			(username IS NOT NULL AND lower(username) = ?)
			OR (username IS NOT NULL AND lower(username) LIKE ? ESCAPE '\')
			OR (lower(display_name) LIKE ? ESCAPE '\')
		  )
	)` + keysetClause + `
	ORDER BY bucket ASC, sortkey ASC, id ASC
	LIMIT ?`

	rows, err := r.db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var (
		users   []*User
		buckets []int
		keys    []string
	)
	for rows.Next() {
		var u User
		var bucket int
		var sortkey string
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Username, &u.WeightUnit, &u.DistanceUnit, &u.HeightCm, &u.AvatarKey, &u.OAuthAvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt, &bucket, &sortkey); err != nil {
			return nil, nil, err
		}
		uu := u
		users = append(users, &uu)
		buckets = append(buckets, bucket)
		keys = append(keys, sortkey)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var next *SearchCursor
	if len(users) > limit {
		last := users[limit-1]
		next = &SearchCursor{Bucket: buckets[limit-1], SortKey: keys[limit-1], ID: last.ID}
		users = users[:limit]
	}
	if users == nil {
		users = []*User{}
	}
	return users, next, nil
}

func (r *SQLiteRepository) Delete(ctx context.Context, id string) error {
	now := r.now().UTC()

	result, err := r.db.ExecContext(ctx, `
		UPDATE users
		SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, now, now, id)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}

	return nil
}
