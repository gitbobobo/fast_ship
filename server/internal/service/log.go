package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

const (
	maxLogEntriesPerUpload = 500
	maxLogMessageBytes     = 4000
	maxLogMetadataBytes    = 4096
	maxLogSourceBytes      = 128
	maxLogDescriptionBytes = 500
)

var logRunIDRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type LogService struct {
	logRepo     *repository.LogRepository
	projectRepo *repository.ProjectRepository
}

func NewLogService(logRepo *repository.LogRepository, projectRepo *repository.ProjectRepository) *LogService {
	return &LogService{
		logRepo:     logRepo,
		projectRepo: projectRepo,
	}
}

type LogEntryInput struct {
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"`
	Source    string                 `json:"source"`
	Message   string                 `json:"message"`
	Metadata  map[string]interface{} `json:"metadata"`
}

type UploadLogsRequest struct {
	RunID       string          `json:"run_id"`
	Source      string          `json:"source"`
	Description string          `json:"description"`
	Entries     []LogEntryInput `json:"entries"`
}

type UploadLogsResult struct {
	ID            string     `json:"id"`
	RunID         string     `json:"run_id"`
	Source        string     `json:"source"`
	Description   string     `json:"description"`
	EntryCount    int        `json:"entry_count"`
	FirstEntryAt  *time.Time `json:"first_entry_at"`
	LastEntryAt   *time.Time `json:"last_entry_at"`
	AcceptedCount int        `json:"accepted_count"`
}

type LogEntryItem struct {
	ID          string    `json:"id"`
	BatchID     string    `json:"batch_id"`
	RunID       string    `json:"run_id"`
	BatchSource string    `json:"batch_source"`
	Timestamp   time.Time `json:"timestamp"`
	Level       string    `json:"level"`
	Source      string    `json:"source"`
	Message     string    `json:"message"`
	Metadata    string    `json:"metadata,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type LogBatchItem struct {
	ID               string     `json:"id"`
	ProjectID        string     `json:"project_id"`
	RunID            string     `json:"run_id"`
	Source           string     `json:"source"`
	Description      string     `json:"description"`
	EntryCount       int        `json:"entry_count"`
	FirstEntryAt     *time.Time `json:"first_entry_at"`
	LastEntryAt      *time.Time `json:"last_entry_at"`
	UploaderAPIKeyID *string    `json:"uploader_api_key_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type ListLogEntriesRequest struct {
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

type ListLogBatchesRequest struct {
	RunID       string
	BatchSource string
	From        *time.Time
	To          *time.Time
	Page        int
	PageSize    int
}

func (s *LogService) ensureProjectAccess(projectID, userID string) error {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

func (s *LogService) UploadLogs(projectID, userID string, uploaderAPIKeyID *string, req *UploadLogsRequest) (*UploadLogsResult, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, err
	}

	if !logRunIDRegex.MatchString(req.RunID) {
		return nil, errs.ErrInvalidParams
	}
	if len(req.Entries) == 0 || len(req.Entries) > maxLogEntriesPerUpload {
		return nil, errs.ErrInvalidParams
	}
	if len(req.Source) > maxLogSourceBytes {
		return nil, errs.ErrInvalidParams
	}
	if len(req.Description) > maxLogDescriptionBytes {
		return nil, errs.ErrInvalidParams
	}

	entries := make([]model.LogEntry, 0, len(req.Entries))
	now := time.Now()
	for _, item := range req.Entries {
		if !model.IsValidLogLevel(item.Level) {
			return nil, errs.ErrInvalidParams
		}
		if item.Message == "" || len(item.Message) > maxLogMessageBytes {
			return nil, errs.ErrInvalidParams
		}
		if len(item.Source) > maxLogSourceBytes {
			return nil, errs.ErrInvalidParams
		}
		if item.Timestamp.IsZero() {
			return nil, errs.ErrInvalidParams
		}

		metadata := ""
		if item.Metadata != nil {
			raw, err := json.Marshal(item.Metadata)
			if err != nil || len(raw) > maxLogMetadataBytes {
				return nil, errs.ErrInvalidParams
			}
			metadata = string(raw)
		}

		entries = append(entries, model.LogEntry{
			Timestamp: item.Timestamp,
			Level:     item.Level,
			Source:    item.Source,
			Message:   item.Message,
			Metadata:  metadata,
			CreatedAt: now,
		})
	}

	batch, err := s.logRepo.UploadBatchTx(projectID, req.RunID, req.Source, req.Description, uploaderAPIKeyID, entries)
	if err != nil {
		return nil, errs.ErrInternal
	}

	return &UploadLogsResult{
		ID:            batch.ID,
		RunID:         batch.RunID,
		Source:        batch.Source,
		Description:   batch.Description,
		EntryCount:    batch.EntryCount,
		FirstEntryAt:  batch.FirstEntryAt,
		LastEntryAt:   batch.LastEntryAt,
		AcceptedCount: len(entries),
	}, nil
}

func (s *LogService) ListEntries(projectID, userID string, req ListLogEntriesRequest) ([]LogEntryItem, int64, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, 0, err
	}

	entries, total, err := s.logRepo.ListEntries(repository.LogEntryFilter{
		ProjectID:   projectID,
		BatchID:     req.BatchID,
		RunID:       req.RunID,
		Level:       req.Level,
		EntrySource: req.EntrySource,
		BatchSource: req.BatchSource,
		Query:       req.Query,
		From:        req.From,
		To:          req.To,
		Page:        req.Page,
		PageSize:    req.PageSize,
		Sort:        req.Sort,
	})
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	items := make([]LogEntryItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, LogEntryItem{
			ID:          entry.ID,
			BatchID:     entry.BatchID,
			RunID:       entry.Batch.RunID,
			BatchSource: entry.Batch.Source,
			Timestamp:   entry.Timestamp,
			Level:       entry.Level,
			Source:      entry.Source,
			Message:     entry.Message,
			Metadata:    entry.Metadata,
			CreatedAt:   entry.CreatedAt,
		})
	}
	return items, total, nil
}

func (s *LogService) ListBatches(projectID, userID string, req ListLogBatchesRequest) ([]LogBatchItem, int64, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, 0, err
	}

	batches, total, err := s.logRepo.ListBatches(repository.LogBatchFilter{
		ProjectID:   projectID,
		RunID:       req.RunID,
		BatchSource: req.BatchSource,
		From:        req.From,
		To:          req.To,
		Page:        req.Page,
		PageSize:    req.PageSize,
	})
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	items := make([]LogBatchItem, 0, len(batches))
	for _, batch := range batches {
		items = append(items, toLogBatchItem(batch))
	}
	return items, total, nil
}

func (s *LogService) GetBatch(batchID, userID string) (*LogBatchItem, error) {
	batch, err := s.logRepo.FindBatchByID(batchID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrLogBatchNotFound
		}
		return nil, errs.ErrInternal
	}

	if err := s.ensureProjectAccess(batch.ProjectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return nil, errs.ErrLogBatchNotFound
		}
		return nil, err
	}

	item := toLogBatchItem(*batch)
	return &item, nil
}

func toLogBatchItem(batch model.LogBatch) LogBatchItem {
	return LogBatchItem{
		ID:               batch.ID,
		ProjectID:        batch.ProjectID,
		RunID:            batch.RunID,
		Source:           batch.Source,
		Description:      batch.Description,
		EntryCount:       batch.EntryCount,
		FirstEntryAt:     batch.FirstEntryAt,
		LastEntryAt:      batch.LastEntryAt,
		UploaderAPIKeyID: batch.UploaderAPIKeyID,
		CreatedAt:        batch.CreatedAt,
		UpdatedAt:        batch.UpdatedAt,
	}
}

func (s *LogService) DeleteBatch(batchID, userID string) error {
	batch, err := s.logRepo.FindBatchByID(batchID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrLogBatchNotFound
		}
		return errs.ErrInternal
	}

	if err := s.ensureProjectAccess(batch.ProjectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return errs.ErrLogBatchNotFound
		}
		return err
	}

	if err := s.logRepo.DeleteBatch(batchID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrLogBatchNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

func (s *LogService) DeleteByProject(projectID, userID string) error {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return err
	}
	if err := s.logRepo.DeleteByProject(projectID); err != nil {
		return errs.ErrInternal
	}
	return nil
}
