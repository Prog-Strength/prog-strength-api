package db

import (
	"path/filepath"
	"testing"
)

// TestSQLiteVecExtension exercises the full sqlite-vec round trip on a database
// opened via Open, proving the auto-extension registered in init() is present on
// real connections: version probe, vec0 virtual table creation, inserts, and a
// per-user KNN query.
func TestSQLiteVecExtension(t *testing.T) {
	t.Parallel()

	conn, openErr := Open(filepath.Join(t.TempDir(), "app.db"))
	if openErr != nil {
		t.Fatalf("open db: %v", openErr)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var version string
	if err := conn.QueryRow("SELECT vec_version()").Scan(&version); err != nil {
		t.Fatalf("vec_version: %v", err)
	}
	if version == "" {
		t.Fatal("vec_version returned an empty string")
	}

	if _, err := conn.Exec(
		"CREATE VIRTUAL TABLE v USING vec0(memory_id INTEGER PRIMARY KEY, user_id TEXT, embedding float[4] distance_metric=cosine)",
	); err != nil {
		t.Fatalf("create virtual table: %v", err)
	}

	if _, err := conn.Exec(
		"INSERT INTO v(memory_id, user_id, embedding) VALUES (1,'u1','[1,0,0,0]'),(2,'u1','[0,1,0,0]'),(3,'u2','[1,0,0,0]')",
	); err != nil {
		t.Fatalf("insert rows: %v", err)
	}

	rows, queryErr := conn.Query(
		"SELECT memory_id FROM v WHERE user_id=? AND embedding MATCH ? AND k=? ORDER BY distance",
		"u1", "[1,0,0,0]", 5,
	)
	if queryErr != nil {
		t.Fatalf("knn query: %v", queryErr)
	}
	defer func() { _ = rows.Close() }()

	var got []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}

	want := []int{1, 2}
	if len(got) != len(want) {
		t.Fatalf("got memory_ids %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got memory_ids %v, want %v", got, want)
		}
	}
}
