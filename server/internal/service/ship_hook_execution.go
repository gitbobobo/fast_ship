package service

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
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

// ExecutePendingShipHooks 消费项目下全部 pending 钩子并逐个执行动作；钩子失败不影响发货结果，只计入 HookFailed。
func (s *ShipService) ExecutePendingShipHooks(projectID, userID string, version *model.Version) (*ShipResult, error) {
	result := &ShipResult{}
	if version == nil {
		return result, nil
	}

	firedAt := time.Now().UTC()
	if version.ShippedAt != nil {
		firedAt = version.ShippedAt.UTC()
	}

	consumed, err := s.shipHookRepo.ConsumePendingByProjectID(
		projectID,
		version.ID,
		version.VersionNumber,
		version.GithubReleaseURL,
		firedAt,
	)
	if err != nil {
		s.logger.Error("consume pending ship hooks failed",
			zap.String("project_id", projectID),
			zap.String("version_id", version.ID),
			zap.Error(err),
		)
		return result, err
	}

	result.HookTotal = len(consumed)
	for i := range consumed {
		hook := &consumed[i]
		s.executeConsumedShipHook(hook, userID, version)
		if err := s.shipHookRepo.Upsert(hook); err != nil {
			s.logger.Error("persist ship hook execution result failed",
				zap.String("issue_id", hook.IssueID),
				zap.Error(err),
			)
		}
		if shipHookHasFailure(hook) {
			result.HookFailed++
		}
	}
	return result, nil
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
		s.executeShipHookComment(hook, userID, version)
	}
	if hook.WorkflowEnabled {
		s.executeShipHookWorkflow(hook, userID)
	}
	if hook.CloseEnabled {
		s.executeShipHookClose(hook, userID, issue)
	}
}

func (s *ShipService) executeShipHookComment(hook *model.IssueShipHook, userID string, version *model.Version) {
	rendered := renderShipHookCommentBody(hook.CommentBody, version.VersionNumber, version.GithubReleaseURL)
	hook.CommentRenderedBody = rendered
	_, err := s.issueService.CreateInternalComment(hook.IssueID, userID, CreateInternalIssueCommentRequest{Body: rendered}, "ship-hook")
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
	current, err := s.issueService.InternalMetaWorkflowStatus(hook.IssueID)
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

	if _, err := s.issueService.UpdateInternalMeta(hook.IssueID, userID, hook.WorkflowStatus, "ship-hook"); err != nil {
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
	_, err := s.issueService.UpdateInternalIssue(hook.IssueID, userID, UpdateInternalIssueRequest{
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
