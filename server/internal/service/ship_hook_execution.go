package service

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PendingIssueHook 是 ship check 响应中待执行钩子的展示项，字段全部非 omitempty。
type PendingIssueHook struct {
	IssueID         string `json:"issue_id"`
	Reference       string `json:"reference"`
	Title           string `json:"title"`
	Comment         bool   `json:"comment"`
	Close           bool   `json:"close"`
	WorkflowEnabled bool   `json:"workflow_enabled"`
	WorkflowStatus  string `json:"workflow_status"`
}

func (s *ShipService) ListPendingIssueHooksForCheck(projectID string) ([]PendingIssueHook, error) {
	hooks, err := s.shipHookRepo.ListPendingByProjectID(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if len(hooks) == 0 {
		return []PendingIssueHook{}, nil
	}

	issueIDs := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		issueIDs = append(issueIDs, hook.IssueID)
	}
	issuesByID, err := s.issueRepo.ListByIDs(issueIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	result := make([]PendingIssueHook, 0, len(hooks))
	for _, hook := range hooks {
		issue, ok := issuesByID[hook.IssueID]
		if !ok {
			continue
		}
		result = append(result, PendingIssueHook{
			IssueID:         hook.IssueID,
			Reference:       buildIssueReference(issue),
			Title:           issue.Title,
			Comment:         hook.CommentEnabled,
			Close:           hook.CloseEnabled,
			WorkflowEnabled: hook.WorkflowEnabled,
			WorkflowStatus:  string(hook.WorkflowStatus),
		})
	}
	return result, nil
}

// ExecutePendingShipHooks claims pending (or abandoned running) hooks, then
// completes them with a compare-and-set update. A crash leaves a running row
// that can be reclaimed later; a rescheduled/deleted row cannot be overwritten
// by the old worker.
func (s *ShipService) ExecutePendingShipHooks(projectID, userID string, version *model.Version) (*ShipResult, error) {
	result := &ShipResult{}
	result.HookStatus = "completed"
	if version == nil {
		return result, nil
	}

	claimedAt := time.Now().UTC()
	consumed, err := s.shipHookRepo.ClaimPendingByProjectID(
		projectID,
		version.ID,
		version.VersionNumber,
		version.GithubReleaseURL,
		claimedAt,
		claimedAt.Add(-5*time.Minute),
	)
	if err != nil {
		s.logger.Error("consume pending ship hooks failed",
			zap.String("project_id", projectID),
			zap.String("version_id", version.ID),
			zap.Error(err),
		)
		result.HookStatus = "incomplete"
		result.HookError = err.Error()
		return result, err
	}

	for i := range consumed {
		hook := &consumed[i]
		if hook.FiredVersionID != "" && hook.FiredVersionID != version.ID {
			result.RecoveredVersionIDs = appendUniqueString(result.RecoveredVersionIDs, hook.FiredVersionID)
		}
		if hook.FiredVersionID == version.ID {
			result.HookTotal++
		}
		resetShipHookAttemptResults(hook)
		executionVersion := versionForShipHook(hook, version)
		s.executeConsumedShipHook(hook, userID, executionVersion)
		hook.RetryPending = shipHookHasFailure(hook)
		if err := s.shipHookRepo.CompleteExecution(hook, time.Now().UTC()); err != nil {
			s.logger.Error("persist ship hook execution result failed",
				zap.String("issue_id", hook.IssueID),
				zap.Error(err),
			)
			// A failed compare-and-set is important information to the caller:
			// the worker did not durably finish this hook. Do not report a
			// successful ship with a silently lost execution.
			result.HookStatus = "incomplete"
			result.HookError = err.Error()
			return result, err
		}
		if shipHookHasFailure(hook) {
			if hook.FiredVersionID != version.ID {
				continue
			}
			result.HookFailed++
			result.HookStatus = "failed"
			continue
		}
		if hook.FiredVersionID != version.ID {
			continue
		}
	}
	return result, nil
}

func resetShipHookAttemptResults(hook *model.IssueShipHook) {
	hook.CommentOK, hook.CommentError, hook.CommentRenderedBody = nil, "", ""
	hook.CommentSkipped = false
	hook.CloseOK, hook.CloseError = nil, ""
	hook.CloseSkipped = false
	hook.WorkflowOK, hook.WorkflowError = nil, ""
	hook.WorkflowSkipped = false
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// versionForShipHook freezes the release context at the first claim. A stale
// worker recovered during a later release must never render the later version
// into the earlier appointment.
func versionForShipHook(hook *model.IssueShipHook, fallback *model.Version) *model.Version {
	if hook.FiredVersionID == "" && hook.FiredVersionNumber == "" {
		return fallback
	}
	version := *fallback
	version.ID = hook.FiredVersionID
	version.VersionNumber = hook.FiredVersionNumber
	version.GithubReleaseURL = hook.FiredReleaseURL
	if hook.FiredAt != nil {
		firedAt := *hook.FiredAt
		version.ShippedAt = &firedAt
	}
	return &version
}

func (s *ShipService) executeConsumedShipHook(hook *model.IssueShipHook, userID string, version *model.Version) {
	issue, err := s.issueRepo.FindByID(hook.IssueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			markShipHookActionsSkipped(hook)
			return
		}
		markShipHookActionsFailed(hook, err.Error())
		return
	}

	if hook.CommentEnabled {
		if err := s.renewShipHookLease(hook); err != nil {
			markShipHookActionsFailed(hook, err.Error())
			return
		}
		s.executeShipHookComment(hook, userID, version)
	}
	if hook.WorkflowEnabled {
		if err := s.renewShipHookLease(hook); err != nil {
			markShipHookActionsFailed(hook, err.Error())
			return
		}
		s.executeShipHookWorkflow(hook, userID)
	}
	if hook.CloseEnabled {
		if err := s.renewShipHookLease(hook); err != nil {
			markShipHookActionsFailed(hook, err.Error())
			return
		}
		s.executeShipHookClose(hook, userID, issue)
	}
}

func (s *ShipService) renewShipHookLease(hook *model.IssueShipHook) error {
	if err := s.shipHookRepo.RenewLease(hook, time.Now().UTC()); err != nil {
		if err == repository.ErrStaleIssueShipHook {
			return err
		}
		return err
	}
	return nil
}

func (s *ShipService) executeShipHookComment(hook *model.IssueShipHook, userID string, version *model.Version) {
	rendered := renderShipHookCommentBody(hook.CommentBody, version.VersionNumber, version.GithubReleaseURL)
	hook.CommentRenderedBody = rendered
	_, err := s.hookActions.CreateInternalCommentIdempotent(
		hook.IssueID,
		userID,
		CreateInternalIssueCommentRequest{Body: rendered},
		"ship-hook",
		"ship-hook-comment:"+hook.ExecutionToken,
	)
	if err != nil {
		ok := false
		hook.CommentOK = &ok
		hook.CommentError = err.Error()
		return
	}
	ok := true
	hook.CommentOK = &ok
}

func (s *ShipService) executeShipHookWorkflow(hook *model.IssueShipHook, userID string) {
	current, err := s.hookActions.InternalMetaWorkflowStatus(hook.IssueID)
	if err != nil {
		ok := false
		hook.WorkflowOK = &ok
		hook.WorkflowError = err.Error()
		return
	}
	if current == hook.WorkflowStatus {
		ok := true
		hook.WorkflowOK = &ok
		hook.WorkflowSkipped = true
		return
	}

	if _, err := s.hookActions.UpdateInternalMeta(hook.IssueID, userID, hook.WorkflowStatus, "ship-hook"); err != nil {
		ok := false
		hook.WorkflowOK = &ok
		hook.WorkflowError = err.Error()
		return
	}
	ok := true
	hook.WorkflowOK = &ok
}

func (s *ShipService) executeShipHookClose(hook *model.IssueShipHook, userID string, issue *model.Issue) {
	if issue.State == model.IssueStateClosed {
		ok := true
		hook.CloseOK = &ok
		hook.CloseSkipped = true
		return
	}

	closed := model.IssueStateClosed
	reason := "completed"
	_, err := s.hookActions.UpdateInternalIssue(hook.IssueID, userID, UpdateInternalIssueRequest{
		State:       &closed,
		StateReason: &reason,
	})
	if err != nil {
		ok := false
		hook.CloseOK = &ok
		hook.CloseError = err.Error()
		return
	}
	ok := true
	hook.CloseOK = &ok
}

func renderShipHookCommentBody(template, versionNumber, releaseURL string) string {
	rendered := strings.ReplaceAll(template, "{release_url}", releaseURL)
	return strings.ReplaceAll(rendered, "{version}", versionNumber)
}

func markShipHookActionsSkipped(hook *model.IssueShipHook) {
	ok := true
	if hook.CommentEnabled {
		hook.CommentOK = &ok
		hook.CommentSkipped = true
	}
	if hook.CloseEnabled {
		hook.CloseOK = &ok
		hook.CloseSkipped = true
	}
	if hook.WorkflowEnabled {
		hook.WorkflowOK = &ok
		hook.WorkflowSkipped = true
	}
}

func markShipHookActionsFailed(hook *model.IssueShipHook, message string) {
	ok := false
	if hook.CommentEnabled {
		hook.CommentOK = &ok
		hook.CommentError = message
	}
	if hook.CloseEnabled {
		hook.CloseOK = &ok
		hook.CloseError = message
	}
	if hook.WorkflowEnabled {
		hook.WorkflowOK = &ok
		hook.WorkflowError = message
	}
}

func shipHookHasFailure(hook *model.IssueShipHook) bool {
	if hook.CommentEnabled && hook.CommentOK != nil && !*hook.CommentOK {
		return true
	}
	if hook.CloseEnabled && hook.CloseOK != nil && !*hook.CloseOK {
		return true
	}
	if hook.WorkflowEnabled && hook.WorkflowOK != nil && !*hook.WorkflowOK {
		return true
	}
	return false
}
