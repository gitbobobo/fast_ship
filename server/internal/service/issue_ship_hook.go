package service

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

const maxShipHookCommentBodyLen = 4000

// IssueShipHookService owns hook appointments, execution-state persistence
// primitives, and the API response projection. ShipService owns the external
// action runner and depends on its narrow action interface.
type IssueShipHookService struct {
	issueRepo    *repository.IssueRepository
	projectRepo  *repository.ProjectRepository
	shipHookRepo *repository.IssueShipHookRepository
}

func NewIssueShipHookService(issueRepo *repository.IssueRepository, projectRepo *repository.ProjectRepository, shipHookRepo *repository.IssueShipHookRepository) *IssueShipHookService {
	return &IssueShipHookService{issueRepo: issueRepo, projectRepo: projectRepo, shipHookRepo: shipHookRepo}
}

func (s *IssueShipHookService) UpsertShipHook(issueID, userID string, req UpsertShipHookRequest) (*IssueShipHookResponse, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}

	commentEnabled := false
	commentBody := ""
	if req.CommentBody != nil {
		trimmed := strings.TrimSpace(*req.CommentBody)
		if trimmed == "" {
			return nil, errs.ErrInvalidParams
		}
		if utf8.RuneCountInString(trimmed) > maxShipHookCommentBodyLen {
			return nil, errs.ErrInvalidParams
		}
		commentEnabled = true
		commentBody = trimmed
	}

	closeEnabled := api.Deref(req.Close)

	workflowEnabled := false
	var workflowStatus model.IssueWorkflowStatus
	if req.WorkflowStatus != nil {
		if !model.IsValidIssueWorkflowStatus(*req.WorkflowStatus) {
			return nil, errs.ErrInvalidParams
		}
		workflowEnabled = true
		workflowStatus = *req.WorkflowStatus
	}

	if !commentEnabled && !closeEnabled && !workflowEnabled {
		return nil, errs.ErrInvalidParams
	}

	// 冲突时 Upsert 的 DoUpdates 白名单不含 created_by_user_id/created_at，旧值由 DB 自动保留。
	now := time.Now().UTC()
	hook := &model.IssueShipHook{
		IssueID:         issueID,
		ProjectID:       issue.ProjectID,
		Status:          model.IssueShipHookStatusPending,
		CommentEnabled:  commentEnabled,
		CommentBody:     commentBody,
		CloseEnabled:    closeEnabled,
		WorkflowEnabled: workflowEnabled,
		WorkflowStatus:  workflowStatus,
		CreatedByUserID: userID,
		UpdatedByUserID: userID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if err := s.shipHookRepo.Upsert(hook); err != nil {
		return nil, errs.ErrInternal
	}

	resp := s.toIssueShipHookResponse(hook)
	return resp, nil
}

func (s *IssueShipHookService) DeleteShipHook(issueID, userID string) error {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrIssueNotFound
		}
		return errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrProjectNotFound
		}
		return errs.ErrNotOwner
	}

	if err := s.shipHookRepo.DeleteByIssueID(issueID); err != nil {
		return errs.ErrInternal
	}
	return nil
}

func (s *IssueShipHookService) loadShipHook(issueID string) (*model.IssueShipHook, error) {
	hook, err := s.shipHookRepo.GetByIssueID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errs.ErrInternal
	}
	return hook, nil
}

func (s *IssueShipHookService) shipHooksByIssueIDs(issues []model.Issue) (map[string]*model.IssueShipHook, error) {
	issueIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		issueIDs = append(issueIDs, issue.ID)
	}

	raw, err := s.shipHookRepo.ListByIssueIDs(issueIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	result := make(map[string]*model.IssueShipHook, len(raw))
	for issueID, hook := range raw {
		result[issueID] = &hook
	}
	return result, nil
}

func (s *IssueShipHookService) toIssueShipHookResponse(hook *model.IssueShipHook) *IssueShipHookResponse {
	if hook == nil {
		return nil
	}

	resp := &IssueShipHookResponse{
		Status:          hook.Status,
		CommentEnabled:  hook.CommentEnabled,
		CommentBody:     api.NonEmpty(hook.CommentBody),
		CloseEnabled:    hook.CloseEnabled,
		WorkflowEnabled: hook.WorkflowEnabled,
		WorkflowStatus:  string(hook.WorkflowStatus),
	}

	if hook.Status == model.IssueShipHookStatusFired {
		resp.VersionId = api.NonEmpty(hook.FiredVersionID)
		resp.VersionNumber = api.NonEmpty(hook.FiredVersionNumber)
		resp.ReleaseUrl = api.NonEmpty(hook.FiredReleaseURL)
		if hook.FiredAt != nil {
			resp.FiredAt = api.Ptr(formatTime(hook.FiredAt.UTC()))
		}
		if hook.CommentEnabled || hook.CloseEnabled || hook.WorkflowEnabled {
			results := &IssueShipHookResultsResponse{}
			if hook.CommentEnabled {
				results.Comment = shipHookActionResult(hook.CommentOK, hook.CommentSkipped, hook.CommentError)
			}
			if hook.CloseEnabled {
				results.Close = shipHookActionResult(hook.CloseOK, hook.CloseSkipped, hook.CloseError)
			}
			if hook.WorkflowEnabled {
				results.WorkflowStatus = shipHookActionResult(hook.WorkflowOK, hook.WorkflowSkipped, hook.WorkflowError)
			}
			resp.Results = results
		}
		if hook.CommentRenderedBody != "" {
			resp.CommentBody = api.Ptr(hook.CommentRenderedBody)
		}
	}

	return resp
}

func shipHookActionResult(ok *bool, skipped bool, errMsg string) *IssueShipHookActionResult {
	if ok == nil {
		return &IssueShipHookActionResult{Ok: false, Error: api.NonEmpty(errMsg)}
	}
	result := &IssueShipHookActionResult{
		Ok:      *ok,
		Skipped: api.True(skipped),
	}
	if errMsg != "" {
		result.Error = &errMsg
	}
	return result
}
