-- migrations/001_initial_schema.sql
-- Initial schema for progressive overload fitness tracker

-- Schema migrations tracking table
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Users table
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE COLLATE NOCASE,  -- Case-insensitive uniqueness
    display_name TEXT NOT NULL,
    weight_unit TEXT NOT NULL CHECK(weight_unit IN ('lb', 'kg')),
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

CREATE INDEX idx_users_email ON users(email) WHERE deleted_at IS NULL;
CREATE INDEX idx_users_deleted_at ON users(deleted_at);

-- Exercises table
CREATE TABLE IF NOT EXISTS exercises (
    id TEXT PRIMARY KEY,  -- Slug-based ID like "back-squat"
    name TEXT NOT NULL,
    description TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

CREATE INDEX idx_exercises_deleted_at ON exercises(deleted_at);

-- Exercise muscle groups (many-to-many via join table)
CREATE TABLE IF NOT EXISTS exercise_muscle_groups (
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    muscle_group TEXT NOT NULL,
    PRIMARY KEY (exercise_id, muscle_group)
);

-- Exercise equipment (many-to-many via join table)
CREATE TABLE IF NOT EXISTS exercise_equipment (
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    equipment TEXT NOT NULL,
    PRIMARY KEY (exercise_id, equipment)
);

-- Workouts table
CREATE TABLE IF NOT EXISTS workouts (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,  -- No FK constraint yet (users created via OAuth, not in DB initially)
    name TEXT,
    performed_at DATETIME NOT NULL,
    notes TEXT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
);

CREATE INDEX idx_workouts_user_id ON workouts(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_workouts_performed_at ON workouts(performed_at) WHERE deleted_at IS NULL;
CREATE INDEX idx_workouts_deleted_at ON workouts(deleted_at);

-- Workout exercises (exercises within a workout)
CREATE TABLE IF NOT EXISTS workout_exercises (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workout_id TEXT NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    exercise_id TEXT NOT NULL REFERENCES exercises(id),
    exercise_order INTEGER NOT NULL,  -- "order" is a reserved word
    notes TEXT
);

CREATE INDEX idx_workout_exercises_workout_id ON workout_exercises(workout_id);

-- Sets within a workout exercise
CREATE TABLE IF NOT EXISTS sets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workout_exercise_id INTEGER NOT NULL REFERENCES workout_exercises(id) ON DELETE CASCADE,
    reps INTEGER NOT NULL CHECK(reps > 0),
    weight REAL NOT NULL CHECK(weight >= 0),
    unit TEXT NOT NULL CHECK(unit IN ('lb', 'kg')),
    set_order INTEGER NOT NULL  -- Order within the workout exercise
);

CREATE INDEX idx_sets_workout_exercise_id ON sets(workout_exercise_id);
