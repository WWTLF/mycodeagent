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
			volume_id   INTEGER NOT NULL DEFAULT 0,
			num_gpus    INTEGER NOT NULL DEFAULT 0,
			volume_name TEXT    NOT NULL DEFAULT ''
		);
		CREATE TABLE IF NOT EXISTS volumes (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			vastai_id   INTEGER NOT NULL,
			volume_name TEXT    NOT NULL,
			size_gb     INTEGER NOT NULL,
			mount_path  TEXT    NOT NULL DEFAULT '/root/.cache/huggingface',
			machine_id  INTEGER NOT NULL DEFAULT 0,
			created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
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
	// Migration: add volume_id column if missing (existing DBs)
	db.Exec("ALTER TABLE instances ADD COLUMN volume_id INTEGER NOT NULL DEFAULT 0")
	// Migration: add num_gpus column so restart can re-emit the right --tensor-parallel-size
	db.Exec("ALTER TABLE instances ADD COLUMN num_gpus INTEGER NOT NULL DEFAULT 0")
	// Migration: add volume_name column so ps can render volume info without
	// querying vast.ai (Sync populates it from the remote ExtraEnv data).
	db.Exec("ALTER TABLE instances ADD COLUMN volume_name TEXT NOT NULL DEFAULT ''")
	return nil
}
