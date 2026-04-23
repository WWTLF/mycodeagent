package persistence

import (
	"database/sql"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

type SQLiteBadHostRepository struct {
	db *sql.DB
}

var _ repository.BadHostRepository = (*SQLiteBadHostRepository)(nil)

func NewSQLiteBadHostRepository(db *sql.DB) *SQLiteBadHostRepository {
	return &SQLiteBadHostRepository{db: db}
}

func (r *SQLiteBadHostRepository) Add(machineID int, reason string) error {
	// ON CONFLICT keeps the oldest created_at but refreshes the reason — if a
	// host fails repeatedly we want the most recent diagnosis, not the first.
	_, err := r.db.Exec(
		`INSERT INTO bad_hosts (machine_id, reason) VALUES (?, ?)
		 ON CONFLICT(machine_id) DO UPDATE SET reason = excluded.reason`,
		machineID, reason,
	)
	return err
}

func (r *SQLiteBadHostRepository) List() ([]*entity.BadHost, error) {
	rows, err := r.db.Query("SELECT machine_id, reason, created_at FROM bad_hosts ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*entity.BadHost
	for rows.Next() {
		var h entity.BadHost
		if err := rows.Scan(&h.MachineID, &h.Reason, &h.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, &h)
	}
	return result, rows.Err()
}

func (r *SQLiteBadHostRepository) IsBad(machineID int) (bool, error) {
	var n int
	err := r.db.QueryRow("SELECT 1 FROM bad_hosts WHERE machine_id = ?", machineID).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (r *SQLiteBadHostRepository) Delete(machineID int) error {
	_, err := r.db.Exec("DELETE FROM bad_hosts WHERE machine_id = ?", machineID)
	return err
}

func (r *SQLiteBadHostRepository) Clear() error {
	_, err := r.db.Exec("DELETE FROM bad_hosts")
	return err
}
