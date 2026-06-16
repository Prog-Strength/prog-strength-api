-- Short, plain-text user bio shown on the public profile. Nullable; validated
-- (<=160 runes) at the API write edge. Empty input clears it to NULL.
ALTER TABLE users ADD COLUMN bio TEXT;
