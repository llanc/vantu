package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/llanc/vantu/internal/store"
)

// Databases created by v0.1.x lack the basic auth columns and must be upgraded on open.
func TestOpenUpgradesLegacyRoutesTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"basic_auth_user", "basic_auth_hash"} {
		if _, err := raw.Exec("ALTER TABLE routes DROP COLUMN " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	_ = raw.Close()

	st, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen legacy db: %v", err)
	}
	defer st.Close()
	if _, err := st.ListRoutes(t.Context()); err != nil {
		t.Fatalf("list routes after upgrade: %v", err)
	}
}
