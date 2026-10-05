package service

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
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

// ListLogEntriesRequest/ListLogRunsRequest 是 handler 解析 query 后的内部过滤条件，
// 不是 API 载荷类型，保留手写。
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

	source := api.Deref(req.Source)
	description := api.Deref(req.Description)
	if !logRunIDRegex.MatchString(req.RunId) || !logRunIDRegex.MatchString(req.ChunkId) {
		return nil, errs.ErrInvalidParams
	}
	if len(req.Entries) == 0 || len(req.Entries) > maxLogEntriesPerUpload {
		return nil, errs.ErrInvalidParams
	}
	if len(source) > maxLogSourceBytes {
		return nil, errs.ErrInvalidParams
	}
	if len(description) > maxLogDescriptionBytes {
		return nil, errs.ErrInvalidParams
	}

	entries := make([]model.LogEntry, 0, len(req.Entries))
	now := time.Now()
	for _, item := range req.Entries {
		if !model.IsValidLogLevel(string(item.Level)) {
			return nil, errs.ErrInvalidParams
		}
		if item.Message == "" || len(item.Message) > maxLogMessageBytes {
			return nil, errs.ErrInvalidParams
		}
		if len(api.Deref(item.Source)) > maxLogSourceBytes {
			return nil, errs.ErrInvalidParams
		}
		// 生成类型以 string 承载 timestamp，此处按旧绑定语义解析 RFC3339
		ts, perr := time.Parse(time.RFC3339Nano, item.Timestamp)
		if perr != nil || ts.IsZero() {
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
			Timestamp: ts,
			Level:     string(item.Level),
			Source:    api.Deref(item.Source),
			Message:   item.Message,
			Metadata:  metadata,
			CreatedAt: now,
		})
	}

	txResult, err := s.logRepo.UploadRunTx(projectID, req.RunId, req.ChunkId, source, description, uploaderAPIKeyID, entries)
	if err != nil {
		if errors.Is(err, repository.ErrLogRunEntryLimitExceeded) {
			return nil, errs.ErrLogRunEntryLimitExceeded
		}
		return nil, errs.ErrInternal
	}

	run := txResult.Run
	return &UploadLogsResult{
		RunId:         run.RunID,
		Source:        run.Source,
		Description:   run.Description,
		EntryCount:    run.EntryCount,
		FirstEntryAt:  api.JSONTimePtr(run.FirstEntryAt),
		LastEntryAt:   api.JSONTimePtr(run.LastEntryAt),
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
			Id:        entry.ID,
			RunId:     entry.LogRun.RunID,
			Timestamp: api.JSONTime(entry.Timestamp),
			Level:     model.LogLevel(entry.Level),
			Source:    entry.Source,
			Message:   entry.Message,
			Metadata:  api.NonEmpty(entry.Metadata),
			CreatedAt: api.JSONTime(entry.CreatedAt),
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
		ProjectId:        run.ProjectID,
		RunId:            run.RunID,
		Source:           run.Source,
		Description:      run.Description,
		EntryCount:       run.EntryCount,
		FirstEntryAt:     api.JSONTimePtr(run.FirstEntryAt),
		LastEntryAt:      api.JSONTimePtr(run.LastEntryAt),
		UploaderApiKeyId: run.UploaderAPIKeyID,
		CreatedAt:        api.JSONTime(run.CreatedAt),
		UpdatedAt:        api.JSONTime(run.UpdatedAt),
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
