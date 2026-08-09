package weather

import (
	"context"
	"database/sql"
	"errors"
)

var _ ActivityWeatherRepository = (*SQLiteActivityWeatherRepository)(nil)

// outdoorDetailsUnion projects the environment column out of all five
// endurance detail tables. environment lives on the per-type detail row, not
// on activities (migration 042), so "is this outdoor?" is a five-way union
// rather than a column read. It is one const because ListPending and
// CountPending must agree on what eligible means — a table added to one and
// forgotten in the other would make --dry-run quote a number the run then
// exceeds.
const outdoorDetailsUnion = `
	          SELECT activity_id, environment FROM activity_run_details
	UNION ALL SELECT activity_id, environment FROM activity_walk_details
	UNION ALL SELECT activity_id, environment FROM activity_cycle_details
	UNION ALL SELECT activity_id, environment FROM activity_hike_details
	UNION ALL SELECT activity_id, environment FROM activity_other_details
`

// pendingActivitiesSelect is the anti-join that defines "still owed a
// capture": a live outdoor activity with no activity_weather row, carrying the
// first positioned trackpoint as its coordinate. It deliberately stops before
// ORDER BY and LIMIT — those are the caller's paging concern, and leaving them
// out lets CountPending wrap this exact text instead of restating it.
const pendingActivitiesSelect = `
	SELECT a.id AS activity_id,
	       a.start_time AS start_time,
	       (SELECT tp.latitude  FROM activity_trackpoints tp
	         WHERE tp.activity_id = a.id AND tp.latitude IS NOT NULL AND tp.longitude IS NOT NULL
	         ORDER BY tp.sequence LIMIT 1) AS lat,
	       (SELECT tp.longitude FROM activity_trackpoints tp
	         WHERE tp.activity_id = a.id AND tp.latitude IS NOT NULL AND tp.longitude IS NOT NULL
	         ORDER BY tp.sequence LIMIT 1) AS lon
	  FROM activities a
	  JOIN (` + outdoorDetailsUnion + `) d ON d.activity_id = a.id
	 WHERE a.deleted_at IS NULL
	   AND d.environment = 'outdoor'
	   AND NOT EXISTS (SELECT 1 FROM activity_weather w WHERE w.activity_id = a.id)
`

type SQLiteActivityWeatherRepository struct {
	db *sql.DB
}

func NewSQLiteActivityWeatherRepository(db *sql.DB) *SQLiteActivityWeatherRepository {
	return &SQLiteActivityWeatherRepository{db: db}
}

func (r *SQLiteActivityWeatherRepository) Get(ctx context.Context, activityID string) (*ActivityWeather, error) {
	var (
		row                                   ActivityWeather
		lat, lon                              sql.NullFloat64
		observedAt                            sql.NullTime
		temp, feelsLike, dewPoint, wind, prec sql.NullFloat64
		humidity, windDeg                     sql.NullInt64
		condition, icon                       sql.NullString
	)
	err := r.db.QueryRowContext(ctx, `
		SELECT activity_id, status, lat, lon, observed_at, temp_c, feels_like_c,
		       dew_point_c, humidity, wind_kmh, wind_deg, precip_mm, condition,
		       icon, fetched_at
		FROM activity_weather
		WHERE activity_id = ?
	`, activityID).Scan(
		&row.ActivityID, &row.Status, &lat, &lon, &observedAt, &temp, &feelsLike,
		&dewPoint, &humidity, &wind, &windDeg, &prec, &condition, &icon, &row.FetchedAt,
	)
	// No row means the activity was never attempted, which is a normal answer
	// on every read path: the DTO omits the key and the backfill picks it up.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if lat.Valid {
		row.Lat = &lat.Float64
	}
	if lon.Valid {
		row.Lon = &lon.Float64
	}
	if row.Status == ActivityStatusOK {
		row.Observation = &Observation{
			ObservedAt: observedAt.Time.UTC(),
			TempC:      temp.Float64,
			FeelsLikeC: feelsLike.Float64,
			DewPointC:  dewPoint.Float64,
			Humidity:   int(humidity.Int64),
			WindKMH:    wind.Float64,
			WindDeg:    int(windDeg.Int64),
			PrecipMM:   prec.Float64,
			Condition:  condition.String,
			Icon:       icon.String,
		}
	}
	row.FetchedAt = row.FetchedAt.UTC()
	return &row, nil
}

// Put upserts rather than plain-inserting. A second write for the same
// activity is legitimate, not a bug to surface: --retry-unavailable clears a
// terminal row so the next run can re-resolve it, and a re-imported activity
// reuses its id. A primary-key collision on a best-effort capture path would
// turn either of those into a hard error for no gain.
func (r *SQLiteActivityWeatherRepository) Put(ctx context.Context, row ActivityWeather) error {
	var (
		lat, lon                              any
		observedAt, temp, feelsLike, dewPoint any
		humidity, wind, windDeg, prec         any
		condition, icon                       any
	)
	if row.Lat != nil {
		lat = *row.Lat
	}
	if row.Lon != nil {
		lon = *row.Lon
	}
	if row.Observation != nil {
		o := row.Observation
		observedAt = o.ObservedAt.UTC()
		temp, feelsLike, dewPoint = o.TempC, o.FeelsLikeC, o.DewPointC
		humidity, wind, windDeg, prec = o.Humidity, o.WindKMH, o.WindDeg, o.PrecipMM
		condition, icon = o.Condition, o.Icon
	}

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO activity_weather (
			activity_id, status, lat, lon, observed_at, temp_c, feels_like_c,
			dew_point_c, humidity, wind_kmh, wind_deg, precip_mm, condition,
			icon, fetched_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(activity_id) DO UPDATE SET
			status       = excluded.status,
			lat          = excluded.lat,
			lon          = excluded.lon,
			observed_at  = excluded.observed_at,
			temp_c       = excluded.temp_c,
			feels_like_c = excluded.feels_like_c,
			dew_point_c  = excluded.dew_point_c,
			humidity     = excluded.humidity,
			wind_kmh     = excluded.wind_kmh,
			wind_deg     = excluded.wind_deg,
			precip_mm    = excluded.precip_mm,
			condition    = excluded.condition,
			icon         = excluded.icon,
			fetched_at   = excluded.fetched_at
	`, row.ActivityID, string(row.Status), lat, lon, observedAt, temp, feelsLike,
		dewPoint, humidity, wind, windDeg, prec, condition, icon, row.FetchedAt.UTC())
	return err
}

// CountOK joins activities so a soft-deleted activity's retained row stops
// counting, keeping the gauge consistent with CountPending's denominator.
func (r *SQLiteActivityWeatherRepository) CountOK(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM activity_weather w
		JOIN activities a ON a.id = w.activity_id
		WHERE w.status = 'ok' AND a.deleted_at IS NULL
	`).Scan(&n)
	return n, err
}

func (r *SQLiteActivityWeatherRepository) CountPending(ctx context.Context) (PendingCounts, error) {
	// One pass over the same select ListPending walks: total pending, and how
	// many of those carry no coordinate. Eligible is the difference, so the
	// two numbers can never sum to something other than the work queued.
	var total, noCoords int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN lat IS NULL THEN 1 ELSE 0 END), 0)
		FROM (`+pendingActivitiesSelect+`) p
	`).Scan(&total, &noCoords)
	if err != nil {
		return PendingCounts{}, err
	}
	return PendingCounts{Eligible: total - noCoords, NoCoordinates: noCoords}, nil
}

func (r *SQLiteActivityWeatherRepository) ListPending(ctx context.Context, limit int) ([]PendingActivity, error) {
	query := pendingActivitiesSelect + ` ORDER BY start_time DESC`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]PendingActivity, 0)
	for rows.Next() {
		var (
			pa       PendingActivity
			lat, lon sql.NullFloat64
		)
		if err := rows.Scan(&pa.ActivityID, &pa.StartTime, &lat, &lon); err != nil {
			return nil, err
		}
		if lat.Valid && lon.Valid {
			pa.Lat, pa.Lon = &lat.Float64, &lon.Float64
		}
		pa.StartTime = pa.StartTime.UTC()
		out = append(out, pa)
	}
	return out, rows.Err()
}

// ClearUnavailable is scoped to live activities so the count it returns is
// exactly what the next run will retry — a soft-deleted activity's row can
// never become eligible again, so clearing it would inflate the number the
// operator reads.
func (r *SQLiteActivityWeatherRepository) ClearUnavailable(ctx context.Context) (int, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM activity_weather
		WHERE status = 'unavailable'
		  AND activity_id IN (SELECT id FROM activities WHERE deleted_at IS NULL)
	`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
