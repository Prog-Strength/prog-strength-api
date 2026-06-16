package user

import (
	"strconv"
	"strings"
)

// maxHandleSuffix caps how many numeric variants (base, base2, base3, …) we try
// before giving up and falling back to the id-derived handle. Bounded so a
// pathological collision storm can't loop forever.
const maxHandleSuffix = 50

// slugifyHandle converts a display name into a ValidateUsername-passing
// candidate, or "" if nothing usable remains. ASCII-folding drops bytes >= 0x80
// rather than transliterating: keeping this dependency-free is worth losing the
// occasional accented character, since GenerateHandle falls back to the id when
// the slug is empty.
func slugifyHandle(displayName string) string {
	var b strings.Builder
	b.Grow(len(displayName))

	// prevSep tracks whether the previous emitted byte was a separator, so we
	// collapse runs of separators to a single '_'.
	prevSep := false
	for i := 0; i < len(displayName); i++ {
		c := displayName[i]
		switch {
		case c >= 0x80:
			// Non-ASCII byte: drop it (no transliteration).
			continue
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
			prevSep = false
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
			prevSep = false
		default:
			// Any other char is a separator; collapse consecutive ones.
			if !prevSep && b.Len() > 0 {
				b.WriteByte('_')
				prevSep = true
			}
		}
	}

	s := strings.Trim(b.String(), "_")

	// Strip leading non-letters: the pattern requires a leading [a-z], so a
	// candidate starting with a digit (e.g. "42") is unusable until trimmed.
	s = strings.TrimLeft(s, "0123456789_")

	if len(s) > UsernameMaxLen {
		s = s[:UsernameMaxLen]
		s = strings.TrimRight(s, "_")
	}

	if _, err := ValidateUsername(s); err != nil {
		return ""
	}
	return s
}

// fallbackHandle derives a guaranteed-valid handle from the user id: a "user_"
// prefix plus the first 8 [a-z0-9] chars of the lowercased id (dashes and other
// non-alphanumerics stripped). Padded if the id yields too few usable chars so
// the result always satisfies ValidateUsername's minimum length.
func fallbackHandle(userID string) string {
	var b strings.Builder
	lower := strings.ToLower(userID)
	for i := 0; i < len(lower) && b.Len() < 8; i++ {
		c := lower[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(c)
		}
	}
	suffix := b.String()
	// Pad so "user_" + suffix clears UsernameMinLen even for an empty id.
	for len(suffix) < UsernameMinLen {
		suffix += "0"
	}
	return "user_" + suffix
}

// GenerateHandle returns a valid, unique handle for (displayName, userID).
// exists reports whether a candidate handle is already taken. The returned
// handle is canonical (already passed ValidateUsername). Any error from exists
// is propagated unchanged.
func GenerateHandle(displayName, userID string, exists func(string) (bool, error)) (string, error) {
	base := slugifyHandle(displayName)
	if base == "" {
		base = fallbackHandle(userID)
	}

	for attempt := 1; attempt <= maxHandleSuffix; attempt++ {
		candidate := base
		if attempt > 1 {
			suffix := strconv.Itoa(attempt)
			// Trim base so base+suffix never exceeds the max length.
			if limit := UsernameMaxLen - len(suffix); len(candidate) > limit {
				candidate = strings.TrimRight(candidate[:limit], "_")
			}
			candidate += suffix
		}

		valid, err := ValidateUsername(candidate)
		if err != nil {
			continue
		}
		taken, err := exists(valid)
		if err != nil {
			return "", err
		}
		if !taken {
			return valid, nil
		}
	}

	// Exhausted: the id-derived handle is unique by construction.
	return fallbackHandle(userID), nil
}
