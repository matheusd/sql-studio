package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// openTempDB creates a real temp-file SQLite database with a single seeded table
// and returns the open *sql.DB plus its on-disk path. The caller owns the handle
// (it is intentionally NOT registered for cleanup-close here so tests can assert
// that NewSQLite leaves it usable); the temp dir is cleaned up by t.TempDir.
func openTempDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite3", path+"?mode=rwc")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Keep access serialized and deterministic, mirroring OpenSQLite.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO widgets (id, name) VALUES (1, 'alpha')`); err != nil {
		t.Fatalf("seeding row: %v", err)
	}

	return db, path
}

// TestNewSQLiteUsesInjectedHandle proves that a backend built with NewSQLite
// reads through the externally-owned *sql.DB and derives file metadata from the
// supplied path: Overview and Tables must see the seeded table/row and report
// the path's base name and a non-empty size.
func TestNewSQLiteUsesInjectedHandle(t *testing.T) {
	db, path := openTempDB(t)
	defer requireClose(t, db)

	d := NewSQLite(db, path, 5*time.Second)
	ctx := context.Background()

	overview, err := d.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	// File metadata comes from os.Stat(path), not from the handle.
	if overview.FileName != "test.db" {
		t.Fatalf("Overview.FileName = %q, want %q", overview.FileName, "test.db")
	}
	if overview.DBSize == "" {
		t.Fatal("Overview.DBSize is empty, want non-empty")
	}
	if overview.SQLiteVersion == nil {
		t.Fatal("Overview.SQLiteVersion is nil, want non-nil")
	}
	if *overview.SQLiteVersion == "" {
		t.Fatal("Overview.SQLiteVersion is empty, want non-empty")
	}
	if overview.Tables != 1 {
		t.Fatalf("Overview.Tables = %d, want %d", overview.Tables, 1)
	}
	// The seeded table's row is visible through the injected handle.
	wantCounts := []Count{{Name: "widgets", Count: 1}}
	if !reflect.DeepEqual(overview.RowCounts, wantCounts) {
		t.Fatalf("Overview.RowCounts = %+v, want %+v", overview.RowCounts, wantCounts)
	}

	tables, err := d.Tables(ctx)
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if !reflect.DeepEqual(tables.Tables, wantCounts) {
		t.Fatalf("Tables.Tables = %+v, want %+v", tables.Tables, wantCounts)
	}

	// TableData should round-trip the seeded row through the data path.
	data, err := d.TableData(ctx, "widgets", 1)
	if err != nil {
		t.Fatalf("TableData: %v", err)
	}
	wantColumns := []string{"id", "name"}
	if !reflect.DeepEqual(data.Columns, wantColumns) {
		t.Fatalf("TableData.Columns = %+v, want %+v", data.Columns, wantColumns)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("TableData.Rows length = %d, want %d", len(data.Rows), 1)
	}
	if got := data.Rows[0][0]; got != int64(1) {
		t.Fatalf("TableData.Rows[0][0] = %v, want %v", got, int64(1))
	}
	if got := data.Rows[0][1]; got != "alpha" {
		t.Fatalf("TableData.Rows[0][1] = %v, want %v", got, "alpha")
	}
}

// TestNewSQLiteDoesNotOwnHandle proves NewSQLite never closes the injected
// handle and does not mutate the caller's pool tuning: after fully exercising
// the backend the *sql.DB is still usable for a direct query, and the
// MaxOpenConns the caller set is unchanged.
func TestNewSQLiteDoesNotOwnHandle(t *testing.T) {
	db, path := openTempDB(t)
	defer requireClose(t, db)

	before := db.Stats().MaxOpenConnections

	d := NewSQLite(db, path, 5*time.Second)
	ctx := context.Background()

	// Drive every read path that touches the handle.
	if _, err := d.Overview(ctx); err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if _, err := d.Tables(ctx); err != nil {
		t.Fatalf("Tables: %v", err)
	}
	if _, err := d.Table(ctx, "widgets"); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if _, err := d.TablesWithColumns(ctx); err != nil {
		t.Fatalf("TablesWithColumns: %v", err)
	}
	if _, err := d.Erd(ctx); err != nil {
		t.Fatalf("Erd: %v", err)
	}
	if _, err := d.Query(ctx, "SELECT 1"); err != nil {
		t.Fatalf("Query: %v", err)
	}

	// Pool tuning the caller set must be untouched.
	if after := db.Stats().MaxOpenConnections; after != before {
		t.Fatalf("MaxOpenConnections changed: before = %d, after = %d", before, after)
	}

	// The caller still owns an open, usable handle.
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM widgets").Scan(&n); err != nil {
		t.Fatalf("direct query: %v", err)
	}
	if n != 1 {
		t.Fatalf("widgets count = %d, want %d", n, 1)
	}

	// And a fresh ping confirms the handle was not closed underneath us.
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("PingContext: %v", err)
	}
}

// TestNewSQLiteTableMetadata checks that Table-level metadata resolves through
// both the path (os.Stat for size gating) and the injected handle (schema/row
// count), covering the same injected-handle contract at the single-table level.
func TestNewSQLiteTableMetadata(t *testing.T) {
	db, path := openTempDB(t)
	defer requireClose(t, db)

	// Sanity: the path used for os.Stat actually exists on disk.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("os.Stat(%q): %v", path, err)
	}

	d := NewSQLite(db, path, 5*time.Second)

	table, err := d.Table(context.Background(), "widgets")
	if err != nil {
		t.Fatalf("Table: %v", err)
	}
	if table.Name != "widgets" {
		t.Fatalf("Table.Name = %q, want %q", table.Name, "widgets")
	}
	if table.SQL == nil {
		t.Fatal("Table.SQL is nil, want non-nil")
	}
	if !strings.Contains(*table.SQL, "CREATE TABLE") {
		t.Fatalf("Table.SQL = %q, want it to contain %q", *table.SQL, "CREATE TABLE")
	}
	if table.RowCount != 1 {
		t.Fatalf("Table.RowCount = %d, want %d", table.RowCount, 1)
	}
	if table.ColumnCount != 2 {
		t.Fatalf("Table.ColumnCount = %d, want %d", table.ColumnCount, 2)
	}
}

// requireClose closes db and fails the test if Close returns an error.
func requireClose(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Errorf("closing db: %v", err)
	}
}
