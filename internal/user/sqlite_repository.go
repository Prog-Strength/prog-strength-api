package user

import (
	"context"
	"database/sql"
	"errors"
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
	if err := u.Validate(); err != nil {
		return err
	}

	now := r.now().UTC()
	u.ID = id.New()
	u.Email = normalizeEmail(u.Email)
	u.CreatedAt = now
	u.UpdatedAt = now

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, weight_unit, distance_unit, height_cm, avatar_key, oauth_avatar_url, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.Email, u.DisplayName, u.WeightUnit, u.DistanceUnit, u.HeightCm, u.AvatarKey, u.OAuthAvatarURL, u.CreatedAt, u.UpdatedAt)

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
		SELECT id, email, display_name, weight_unit, distance_unit, height_cm, avatar_key, oauth_avatar_url, created_at, updated_at, deleted_at
		FROM users
		WHERE id = ? AND deleted_at IS NULL
	`, id).Scan(&u.ID, &u.Email, &u.DisplayName, &u.WeightUnit, &u.DistanceUnit, &u.HeightCm, &u.AvatarKey, &u.OAuthAvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)

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
		SELECT id, email, display_name, weight_unit, distance_unit, height_cm, avatar_key, oauth_avatar_url, created_at, updated_at, deleted_at
		FROM users
		WHERE email = ? AND deleted_at IS NULL
	`, email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.WeightUnit, &u.DistanceUnit, &u.HeightCm, &u.AvatarKey, &u.OAuthAvatarURL, &u.CreatedAt, &u.UpdatedAt, &u.DeletedAt)

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
		SET display_name = ?, weight_unit = ?, distance_unit = ?, height_cm = ?, avatar_key = ?, oauth_avatar_url = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL
	`, u.DisplayName, u.WeightUnit, u.DistanceUnit, u.HeightCm, u.AvatarKey, u.OAuthAvatarURL, u.UpdatedAt, u.ID)

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
