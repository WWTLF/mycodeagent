package persistence

import (
	"database/sql"

	"github.com/WWTLF/mycodeagent/internal/domain/entity"
	"github.com/WWTLF/mycodeagent/internal/domain/repository"
)

type SQLiteVolumeRepository struct {
	db *sql.DB
}

var _ repository.VolumeRepository = (*SQLiteVolumeRepository)(nil)

func NewSQLiteVolumeRepository(db *sql.DB) *SQLiteVolumeRepository {
	return &SQLiteVolumeRepository{db: db}
}

func (r *SQLiteVolumeRepository) Save(vol *entity.Volume) error {
	result, err := r.db.Exec(
		`INSERT INTO volumes (vastai_id, volume_name, size_gb, mount_path, machine_id)
		 VALUES (?, ?, ?, ?, ?)`,
		vol.VastaiID, vol.VolumeName, vol.SizeGB, vol.MountPath, vol.MachineID,
	)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	vol.ID = id
	return nil
}

func (r *SQLiteVolumeRepository) FindAll() ([]*entity.Volume, error) {
	rows, err := r.db.Query("SELECT * FROM volumes ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*entity.Volume
	for rows.Next() {
		var vol entity.Volume
		err := rows.Scan(
			&vol.ID, &vol.VastaiID, &vol.VolumeName, &vol.SizeGB,
			&vol.MountPath, &vol.MachineID, &vol.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, &vol)
	}
	return result, rows.Err()
}

func (r *SQLiteVolumeRepository) Delete(id int64) error {
	_, err := r.db.Exec("DELETE FROM volumes WHERE id = ?", id)
	return err
}
