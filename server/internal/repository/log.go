package repository

import (
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const MaxLogEntriesPerRun = 50000

var ErrLogRunEntryLimitExceeded = errors.New("log run entry limit exceeded")

type LogRepository struct {
	db *gorm.DB
}

func NewLogRepository(db *gorm.DB) *LogRepository {
	return &LogRepository{db: db}
}

type LogEntryFilter struct {
	ProjectID   string
	RunID       string
	Level       string
	EntrySource string
	Query       string
	From        *time.Time
	To          *time.Time
	Page        int
	PageSize    int
	Sort        string
}

type LogRunFilter struct {
	ProjectID string
	RunID     string
	Source    string
	From      *time.Time
	To        *time.Time
	Page      int
	PageSize  int
}

type UploadRunTxResult struct {
	Run           *model.LogRun
	Duplicate     bool
	AcceptedCount int
}

func (r *LogRepository) FindRunByProjectAndRunID(projectID, runID string) (*model.LogRun, error) {
	var run model.LogRun
	if err := r.db.Where("project_id = ? AND run_id = ?", projectID, runID).First(&run).Error; err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *LogRepository) UploadRunTx(
	projectID, runID, chunkID, source, description string,
	uploaderAPIKeyID *string,
	entries []model.LogEntry,
) (*UploadRunTxResult, error) {
	result := &UploadRunTxResult{}
	err := r.db.Transaction(func(tx *gorm.DB) error {
		run, err := getOrCreateLogRun(tx, projectID, runID, source, description, uploaderAPIKeyID)
		if err != nil {
			return err
		}

		chunk := model.LogRunChunk{
			LogRunID:  run.ID,
			ChunkID:   chunkID,
			CreatedAt: time.Now(),
		}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			result.Duplicate = true
			result.Run = run
			return nil
		}

		minTs := entries[0].Timestamp
		maxTs := entries[0].Timestamp
		for i := range entries {
			entries[i].ID = uuid.New().String()
			entries[i].LogRunID = run.ID
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

		n := len(entries)
		updateResult := tx.Model(&model.LogRun{}).
			Where("id = ? AND entry_count + ? <= ?", run.ID, n, MaxLogEntriesPerRun).
			Updates(map[string]interface{}{
				"entry_count":    gorm.Expr("entry_count + ?", n),
				"first_entry_at": gorm.Expr("CASE WHEN first_entry_at IS NULL OR first_entry_at > ? THEN ? ELSE first_entry_at END", minTs, minTs),
				"last_entry_at":  gorm.Expr("CASE WHEN last_entry_at IS NULL OR last_entry_at < ? THEN ? ELSE last_entry_at END", maxTs, maxTs),
				"updated_at":     time.Now(),
			})
		if updateResult.Error != nil {
			return updateResult.Error
		}
		if updateResult.RowsAffected == 0 {
			return ErrLogRunEntryLimitExceeded
		}

		if err := tx.Where("id = ?", run.ID).First(run).Error; err != nil {
			return err
		}
		result.Run = run
		result.AcceptedCount = n
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func getOrCreateLogRun(
	tx *gorm.DB,
	projectID, runID, source, description string,
	uploaderAPIKeyID *string,
) (*model.LogRun, error) {
	var run model.LogRun
	err := tx.Where("project_id = ? AND run_id = ?", projectID, runID).First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		now := time.Now()
		run = model.LogRun{
			ID:               uuid.New().String(),
			ProjectID:        projectID,
			RunID:            runID,
			Source:           source,
			Description:      description,
			UploaderAPIKeyID: uploaderAPIKeyID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if createErr := tx.Create(&run).Error; createErr != nil {
			var existing model.LogRun
			if findErr := tx.Where("project_id = ? AND run_id = ?", projectID, runID).First(&existing).Error; findErr != nil {
				return nil, errors.Join(createErr, findErr)
			}
			run = existing
		}
		return &run, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *LogRepository) applyEntryFilter(query *gorm.DB, filter LogEntryFilter) *gorm.DB {
	query = query.Joins("JOIN log_runs ON log_runs.id = log_entries.log_run_id").
		Where("log_runs.project_id = ?", filter.ProjectID)

	if filter.RunID != "" {
		query = query.Where("log_runs.run_id = ?", filter.RunID)
	}
	if filter.Level != "" {
		query = query.Where("log_entries.level = ?", filter.Level)
	}
	if filter.EntrySource != "" {
		query = query.Where("log_entries.source = ?", filter.EntrySource)
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
	err := base.Preload("LogRun").
		Order(order).
		Offset(offset).
		Limit(filter.PageSize).
		Find(&entries).Error
	return entries, total, err
}

func (r *LogRepository) ListRuns(filter LogRunFilter) ([]model.LogRun, int64, error) {
	query := r.db.Model(&model.LogRun{}).Where("project_id = ?", filter.ProjectID)
	if filter.RunID != "" {
		query = query.Where("run_id = ?", filter.RunID)
	}
	if filter.Source != "" {
		query = query.Where("source = ?", filter.Source)
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
	var runs []model.LogRun
	err := query.Order("last_entry_at DESC, created_at DESC").
		Offset(offset).
		Limit(filter.PageSize).
		Find(&runs).Error
	return runs, total, err
}

func (r *LogRepository) DeleteRun(projectID, runID string) error {
	result := r.db.Delete(&model.LogRun{}, "project_id = ? AND run_id = ?", projectID, runID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *LogRepository) DeleteByProject(projectID string) error {
	return r.db.Where("project_id = ?", projectID).Delete(&model.LogRun{}).Error
}
