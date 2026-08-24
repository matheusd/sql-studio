package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestPostgresUsesInjectedPool verifies the PostgreSQL backend is catalog-safe,
// keeps the caller-owned pool open, and supports overview/table/query/ERD paths.
func TestPostgresUsesInjectedPool(t *testing.T) {
	url := os.Getenv("PRESSAGIO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires PRESSAGIO_TEST_DATABASE_URL")
	}
	var token [6]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	schema := "sqlstudio_" + hex.EncodeToString(token[:])
	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
	if _, err := admin.Exec(`CREATE TABLE "` + schema + `".widgets (id BIGINT PRIMARY KEY, name TEXT NOT NULL, parent_id BIGINT REFERENCES "` + schema + `".widgets(id)); INSERT INTO "` + schema + `".widgets(id,name) VALUES (1,'alpha')`); err != nil {
		t.Fatal(err)
	}
	backend, err := NewPostgres(admin, schema, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, operation := range []func() error{
		func() error { _, err := backend.Overview(ctx); return err },
		func() error { _, err := backend.Tables(ctx); return err },
		func() error { _, err := backend.Table(ctx, "widgets"); return err },
		func() error { _, err := backend.TableData(ctx, "widgets", 1); return err },
		func() error { _, err := backend.TablesWithColumns(ctx); return err },
		func() error { _, err := backend.Query(ctx, `SELECT id,name FROM "`+schema+`".widgets`); return err },
		func() error { _, err := backend.Erd(ctx); return err },
	} {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := backend.TableData(ctx, "widgets", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 1 || data.Rows[0][1] != "alpha" {
		t.Fatalf("TableData = %#v, want alpha row", data)
	}
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("backend closed caller pool: %v", err)
	}
}
