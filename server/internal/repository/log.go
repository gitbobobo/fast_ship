package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type LogRepository struct {
	db *gorm.DB
}

func NewLogRepository(db *gorm.DB) *LogRepository {
	return &LogRepository{db: db}
}

type LogEntryFilter struct {
	ProjectID   string
	BatchID     string
	RunID       string
	Level       string
	EntrySource string
	BatchSource string
	Query       string
	From        *time.Time
	To          *time.Time
	Page        int
	PageSize    int
	Sort        string
}

type LogBatchFilter struct {
	ProjectID   string
	RunID       string
	BatchSource string
	From        *time.Time
	To          *time.Time
	Page        int
	PageSize    int
}

func (r *LogRepository) FindBatchByID(id string) (*model.LogBatch, error) {
	var batch model.LogBatch
	if err := r.db.Where("id = ?", id).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *LogRepository) UploadBatchTx(
	projectID, runID, source, description string,
	uploaderAPIKeyID *string,
	entries []model.LogEntry,
) (*model.LogBatch, error) {
	var batch model.LogBatch
	err := r.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("project_id = ? AND run_id = ?", projectID, runID).First(&batch).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			now := time.Now()
			batch = model.LogBatch{
				ID:               uuid.New().String(),
				ProjectID:        projectID,
				RunID:            runID,
				Source:           source,
				Description:      description,
				UploaderAPIKeyID: uploaderAPIKeyID,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if createErr := tx.Create(&batch).Error; createErr != nil {
				// First into a fresh struct: dest with PK set would AND on that id.
				var existing model.LogBatch
				if findErr := tx.Where("project_id = ? AND run_id = ?", projectID, runID).First(&existing).Error; findErr != nil {
					return errors.Join(createErr, findErr)
				}
				batch = existing
			}
		} else if err != nil {
			return err
		}

		minTs := entries[0].Timestamp
		maxTs := entries[0].Timestamp
		for i := range entries {
			entries[i].ID = uuid.New().String()
			entries[i].BatchID = batch.ID
			if entries[i].Timestamp.Before(minTs) {
				minTs = entries[i].Timestamp
			}
			if entries[i].Timestamp.After(maxTs) {
				maxTs = entries[i].Timestamp
			}
		}

		if err := tx.CreateInBatches(&entries, 100).Error; err != nil {
			return err
		}

		return tx.Model(&model.LogBatch{}).Where("id = ?", batch.ID).Updates(map[string]interface{}{
			"entry_count":    gorm.Expr("entry_count + ?", len(entries)),
			"first_entry_at": gorm.Expr("CASE WHEN first_entry_at IS NULL OR first_entry_at > ? THEN ? ELSE first_entry_at END", minTs, minTs),
			"last_entry_at":  gorm.Expr("CASE WHEN last_entry_at IS NULL OR last_entry_at < ? THEN ? ELSE last_entry_at END", maxTs, maxTs),
			"updated_at":     time.Now(),
		}).Error
	})
	if err != nil {
		return nil, err
	}

	if err := r.db.Where("id = ?", batch.ID).First(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func (r *LogRepository) applyEntryFilter(query *gorm.DB, filter LogEntryFilter) *gorm.DB {
	query = query.Joins("JOIN log_batches ON log_batches.id = log_entries.batch_id").
		Where("log_batches.project_id = ?", filter.ProjectID)

	if filter.BatchID != "" {
		query = query.Where("log_entries.batch_id = ?", filter.BatchID)
	}
	if filter.RunID != "" {
		query = query.Where("log_batches.run_id = ?", filter.RunID)
	}
	if filter.Level != "" {
		query = query.Where("log_entries.level = ?", filter.Level)
	}
	if filter.EntrySource != "" {
		query = query.Where("log_entries.source = ?", filter.EntrySource)
	}
	if filter.BatchSource != "" {
		query = query.Where("log_batches.source = ?", filter.BatchSource)
	}
	if filter.Query != "" {
		query = query.Where("log_entries.message LIKE ?", "%"+filter.Query+"%")
	}
	if filter.From != nil {
		query = query.Where("log_entries.timestamp >= ?", *filter.From)
	}
	if filter.To != nil {
		query = query.Where("log_entries.timestamp <= ?", *filter.To)
	}
	return query
}

func (r *LogRepository) ListEntries(filter LogEntryFilter) ([]model.LogEntry, int64, error) {
	base := r.db.Model(&model.LogEntry{})
	base = r.applyEntryFilter(base, filter)

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	order := "log_entries.timestamp DESC"
	if filter.Sort == "timestamp_asc" {
		order = "log_entries.timestamp ASC"
	}

	offset := (filter.Page - 1) * filter.PageSize
	var entries []model.LogEntry
	err := base.Preload("Batch").
		Order(order).
		Offset(offset).
		Limit(filter.PageSize).
		Find(&entries).Error
	return entries, total, err
}

func (r *LogRepository) ListBatches(filter LogBatchFilter) ([]model.LogBatch, int64, error) {
	query := r.db.Model(&model.LogBatch{}).Where("project_id = ?", filter.ProjectID)
	if filter.RunID != "" {
		query = query.Where("run_id = ?", filter.RunID)
	}
	if filter.BatchSource != "" {
		query = query.Where("source = ?", filter.BatchSource)
	}
	if filter.From != nil {
		query = query.Where("last_entry_at >= ?", *filter.From)
	}
	if filter.To != nil {
		query = query.Where("last_entry_at <= ?", *filter.To)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (filter.Page - 1) * filter.PageSize
	var batches []model.LogBatch
	err := query.Order("last_entry_at DESC, created_at DESC").
		Offset(offset).
		Limit(filter.PageSize).
		Find(&batches).Error
	return batches, total, err
}

func (r *LogRepository) DeleteBatch(id string) error {
	result := r.db.Delete(&model.LogBatch{}, "id = ?", id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *LogRepository) DeleteByProject(projectID string) error {
	return r.db.Where("project_id = ?", projectID).Delete(&model.LogBatch{}).Error
}
