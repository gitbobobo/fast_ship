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
	ChunkID     string          `json:"chunk_id"`
	Source      string          `json:"source"`
	Description string          `json:"description"`
	Entries     []LogEntryInput `json:"entries"`
}

type UploadLogsResult struct {
	RunID         string     `json:"run_id"`
	Source        string     `json:"source"`
	Description   string     `json:"description"`
	EntryCount    int        `json:"entry_count"`
	FirstEntryAt  *time.Time `json:"first_entry_at"`
	LastEntryAt   *time.Time `json:"last_entry_at"`
	AcceptedCount int        `json:"accepted_count"`
	Duplicate     bool       `json:"duplicate"`
}

type LogEntryItem struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
	Metadata  string    `json:"metadata,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type LogRunItem struct {
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

type ListLogRunsRequest struct {
	RunID    string
	Source   string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
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

	if !logRunIDRegex.MatchString(req.RunID) || !logRunIDRegex.MatchString(req.ChunkID) {
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

	txResult, err := s.logRepo.UploadRunTx(projectID, req.RunID, req.ChunkID, req.Source, req.Description, uploaderAPIKeyID, entries)
	if err != nil {
		if errors.Is(err, repository.ErrLogRunEntryLimitExceeded) {
			return nil, errs.ErrLogRunEntryLimitExceeded
		}
		return nil, errs.ErrInternal
	}

	run := txResult.Run
	return &UploadLogsResult{
		RunID:         run.RunID,
		Source:        run.Source,
		Description:   run.Description,
		EntryCount:    run.EntryCount,
		FirstEntryAt:  run.FirstEntryAt,
		LastEntryAt:   run.LastEntryAt,
		AcceptedCount: txResult.AcceptedCount,
		Duplicate:     txResult.Duplicate,
	}, nil
}

func (s *LogService) ListEntries(projectID, userID string, req ListLogEntriesRequest) ([]LogEntryItem, int64, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, 0, err
	}

	entries, total, err := s.logRepo.ListEntries(repository.LogEntryFilter{
		ProjectID:   projectID,
		RunID:       req.RunID,
		Level:       req.Level,
		EntrySource: req.EntrySource,
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
			ID:        entry.ID,
			RunID:     entry.LogRun.RunID,
			Timestamp: entry.Timestamp,
			Level:     entry.Level,
			Source:    entry.Source,
			Message:   entry.Message,
			Metadata:  entry.Metadata,
			CreatedAt: entry.CreatedAt,
		})
	}
	return items, total, nil
}

func (s *LogService) ListRuns(projectID, userID string, req ListLogRunsRequest) ([]LogRunItem, int64, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		return nil, 0, err
	}

	runs, total, err := s.logRepo.ListRuns(repository.LogRunFilter{
		ProjectID: projectID,
		RunID:     req.RunID,
		Source:    req.Source,
		From:      req.From,
		To:        req.To,
		Page:      req.Page,
		PageSize:  req.PageSize,
	})
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	items := make([]LogRunItem, 0, len(runs))
	for _, run := range runs {
		items = append(items, toLogRunItem(run))
	}
	return items, total, nil
}

func (s *LogService) GetRun(projectID, runID, userID string) (*LogRunItem, error) {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return nil, errs.ErrLogRunNotFound
		}
		return nil, err
	}

	run, err := s.logRepo.FindRunByProjectAndRunID(projectID, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrLogRunNotFound
		}
		return nil, errs.ErrInternal
	}

	item := toLogRunItem(*run)
	return &item, nil
}

func toLogRunItem(run model.LogRun) LogRunItem {
	return LogRunItem{
		ProjectID:        run.ProjectID,
		RunID:            run.RunID,
		Source:           run.Source,
		Description:      run.Description,
		EntryCount:       run.EntryCount,
		FirstEntryAt:     run.FirstEntryAt,
		LastEntryAt:      run.LastEntryAt,
		UploaderAPIKeyID: run.UploaderAPIKeyID,
		CreatedAt:        run.CreatedAt,
		UpdatedAt:        run.UpdatedAt,
	}
}

func (s *LogService) DeleteRun(projectID, runID, userID string) error {
	if err := s.ensureProjectAccess(projectID, userID); err != nil {
		if errors.Is(err, errs.ErrProjectNotFound) {
			return errs.ErrLogRunNotFound
		}
		return err
	}

	if err := s.logRepo.DeleteRun(projectID, runID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrLogRunNotFound
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
