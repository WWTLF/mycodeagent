package persistence

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/WWTLF/mycodeagent/internal/infrastructure/config"
)

func OpenDB() (*sql.DB, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "mycodeagent.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS instances (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			vastai_id   INTEGER NOT NULL,
			model_name  TEXT    NOT NULL,
			status      TEXT    NOT NULL DEFAULT 'starting',
			local_port  INTEGER NOT NULL,
			ssh_host    TEXT,
			ssh_port    INTEGER,
			tunnel_pid  INTEGER,
			hourly_rate REAL,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			num_gpus    INTEGER NOT NULL DEFAULT 0,
			context_length INTEGER NOT NULL DEFAULT 0,
			sync_pid       INTEGER NOT NULL DEFAULT 0,
			sync_root      TEXT    NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS bad_hosts (
			machine_id INTEGER PRIMARY KEY,
			reason     TEXT    NOT NULL DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return err
	}
	// Migrations, all idempotent by error: each statement fails harmlessly once
	// it has already been applied, which is why the errors are ignored. Nothing
	// here may drop data a later version still needs.
	//
	// Additive — both are re-emitted by `restart`, which runs after the offer is
	// gone and so cannot recompute them:
	//   num_gpus       — the GPU split the instance was deployed with.
	//   context_length — the *scaled* window, not the catalog baseline. 0 on
	//                    legacy rows means "fall back to the model definition".
	//   sync_root      — the local directory the rsync loop targets. Empty on
	//                    legacy rows means "the cwd-derived default", which is
	//                    what their running loops already use.
	//
	// Subtractive — leftovers from the removed persistent-volume support. Harmless
	// (reads use an explicit column list) but they make the schema lie about what
	// the tool does. DROP COLUMN needs SQLite >= 3.35; the driver ships 3.53.
	for _, stmt := range []string{
		"ALTER TABLE instances ADD COLUMN num_gpus INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE instances ADD COLUMN context_length INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE instances ADD COLUMN sync_pid INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE instances ADD COLUMN sync_root TEXT NOT NULL DEFAULT ''",
		"DROP TABLE IF EXISTS volumes",
		"ALTER TABLE instances DROP COLUMN volume_id",
		"ALTER TABLE instances DROP COLUMN volume_name",
	} {
		db.Exec(stmt)
	}
	return nil
}
