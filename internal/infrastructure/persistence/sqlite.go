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
			context_length INTEGER NOT NULL DEFAULT 0
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
	// Additive migrations. ALTER fails harmlessly on a DB that already has the
	// column, which is why the errors are ignored.
	//
	//   num_gpus       — restart re-emits the same GPU split (--split-mode).
	//   context_length — restart re-emits the *scaled* window rather than the
	//                    catalog baseline; the offer is gone by then, so it can't
	//                    be recomputed. 0 on legacy rows means "fall back to the
	//                    model definition".
	//
	// (Older DBs may also carry now-unused volume_id / volume_name columns; the instance
	// repo selects columns explicitly so leftover columns are harmless.)
	db.Exec("ALTER TABLE instances ADD COLUMN num_gpus INTEGER NOT NULL DEFAULT 0")
	db.Exec("ALTER TABLE instances ADD COLUMN context_length INTEGER NOT NULL DEFAULT 0")
	return nil
}
