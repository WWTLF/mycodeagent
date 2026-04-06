package persistence

import (
	"database/sql"
	"fmt"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

type SQLiteInstanceRepository struct {
	db *sql.DB
}

var _ repository.InstanceRepository = (*SQLiteInstanceRepository)(nil)

func NewSQLiteInstanceRepository(db *sql.DB) *SQLiteInstanceRepository {
	return &SQLiteInstanceRepository{db: db}
}

func (r *SQLiteInstanceRepository) Save(inst *entity.Instance) error {
	result, err := r.db.Exec(
		`INSERT INTO instances (vastai_id, model_name, status, local_port, ssh_host, ssh_port, tunnel_pid, hourly_rate, volume_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inst.VastaiID, inst.ModelName, inst.Status, inst.LocalPort,
		inst.SSHHost, inst.SSHPort, inst.TunnelPID, inst.HourlyRate, inst.VolumeID,
	)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	inst.ID = id
	return nil
}

func (r *SQLiteInstanceRepository) FindByID(id int64) (*entity.Instance, error) {
	return r.scanOne("SELECT * FROM instances WHERE id = ?", id)
}

func (r *SQLiteInstanceRepository) FindByVastaiID(vastaiID int64) (*entity.Instance, error) {
	return r.scanOne("SELECT * FROM instances WHERE vastai_id = ?", vastaiID)
}

func (r *SQLiteInstanceRepository) FindAll() ([]*entity.Instance, error) {
	return r.scanMany("SELECT * FROM instances ORDER BY created_at DESC")
}

func (r *SQLiteInstanceRepository) FindRunning() ([]*entity.Instance, error) {
	return r.scanMany("SELECT * FROM instances WHERE status LIKE 'starting%' OR status LIKE 'running%' ORDER BY created_at DESC")
}

func (r *SQLiteInstanceRepository) Update(inst *entity.Instance) error {
	_, err := r.db.Exec(
		`UPDATE instances SET vastai_id=?, model_name=?, status=?, local_port=?,
		 ssh_host=?, ssh_port=?, tunnel_pid=?, hourly_rate=?, volume_id=? WHERE id=?`,
		inst.VastaiID, inst.ModelName, inst.Status, inst.LocalPort,
		inst.SSHHost, inst.SSHPort, inst.TunnelPID, inst.HourlyRate, inst.VolumeID, inst.ID,
	)
	return err
}

func (r *SQLiteInstanceRepository) Delete(id int64) error {
	_, err := r.db.Exec("DELETE FROM instances WHERE id = ?", id)
	return err
}

func (r *SQLiteInstanceRepository) scanOne(query string, args ...any) (*entity.Instance, error) {
	row := r.db.QueryRow(query, args...)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("instance not found")
	}
	return inst, err
}

func (r *SQLiteInstanceRepository) scanMany(query string, args ...any) ([]*entity.Instance, error) {
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*entity.Instance
	for rows.Next() {
		var inst entity.Instance
		err := rows.Scan(
			&inst.ID, &inst.VastaiID, &inst.ModelName, &inst.Status,
			&inst.LocalPort, &inst.SSHHost, &inst.SSHPort, &inst.TunnelPID,
			&inst.HourlyRate, &inst.CreatedAt, &inst.VolumeID,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, &inst)
	}
	return result, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanInstance(row scannable) (*entity.Instance, error) {
	var inst entity.Instance
	err := row.Scan(
		&inst.ID, &inst.VastaiID, &inst.ModelName, &inst.Status,
		&inst.LocalPort, &inst.SSHHost, &inst.SSHPort, &inst.TunnelPID,
		&inst.HourlyRate, &inst.CreatedAt, &inst.VolumeID,
	)
	return &inst, err
}
