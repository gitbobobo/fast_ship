package service

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
)

const maxShipHookCommentBodyLen = 4000

type UpsertShipHookRequest struct {
	CommentBody    *string
	Close          bool
	WorkflowStatus *model.IssueWorkflowStatus
}

type IssueShipHookActionResult struct {
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type IssueShipHookResultsResponse struct {
	Comment        *IssueShipHookActionResult `json:"comment,omitempty"`
	Close          *IssueShipHookActionResult `json:"close,omitempty"`
	WorkflowStatus *IssueShipHookActionResult `json:"workflow_status,omitempty"`
}

type IssueShipHookResponse struct {
	Status          string                        `json:"status"`
	CommentEnabled  bool                          `json:"comment_enabled"`
	CommentBody     string                        `json:"comment_body,omitempty"`
	CloseEnabled    bool                          `json:"close_enabled"`
	WorkflowEnabled bool                          `json:"workflow_enabled"`
	WorkflowStatus  string                        `json:"workflow_status"`
	VersionID       string                        `json:"version_id,omitempty"`
	VersionNumber   string                        `json:"version_number,omitempty"`
	ReleaseURL      string                        `json:"release_url,omitempty"`
	FiredAt         string                        `json:"fired_at,omitempty"`
	Results         *IssueShipHookResultsResponse `json:"results,omitempty"`
}

func (s *IssueService) UpsertShipHook(issueID, userID string, req UpsertShipHookRequest) (*IssueShipHookResponse, error) {
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

	closeEnabled := req.Close

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

func (s *IssueService) DeleteShipHook(issueID, userID string) error {
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

func (s *IssueService) loadShipHook(issueID string) (*model.IssueShipHook, error) {
	hook, err := s.shipHookRepo.GetByIssueID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errs.ErrInternal
	}
	return hook, nil
}

func (s *IssueService) shipHooksByIssueIDs(issues []model.Issue) (map[string]*model.IssueShipHook, error) {
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

func (s *IssueService) toIssueShipHookResponse(hook *model.IssueShipHook) *IssueShipHookResponse {
	if hook == nil {
		return nil
	}

	resp := &IssueShipHookResponse{
		Status:          string(hook.Status),
		CommentEnabled:  hook.CommentEnabled,
		CommentBody:     hook.CommentBody,
		CloseEnabled:    hook.CloseEnabled,
		WorkflowEnabled: hook.WorkflowEnabled,
		WorkflowStatus:  string(hook.WorkflowStatus),
	}

	if hook.Status == model.IssueShipHookStatusFired {
		resp.VersionID = hook.FiredVersionID
		resp.VersionNumber = hook.FiredVersionNumber
		resp.ReleaseURL = hook.FiredReleaseURL
		if hook.FiredAt != nil {
			resp.FiredAt = formatTime(hook.FiredAt.UTC())
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
			resp.CommentBody = hook.CommentRenderedBody
		}
	}

	return resp
}

func shipHookActionResult(ok *bool, skipped bool, errMsg string) *IssueShipHookActionResult {
	if ok == nil {
		return &IssueShipHookActionResult{OK: false, Error: errMsg}
	}
	result := &IssueShipHookActionResult{
		OK:      *ok,
		Skipped: skipped,
	}
	if errMsg != "" {
		result.Error = errMsg
	}
	return result
}
