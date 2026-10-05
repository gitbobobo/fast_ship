package service

import (
	"errors"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"time"
)

func (s *IssueService) ReplaceChecklist(issueID, userID string, req ReplaceIssueChecklistRequest, actor string) (*IssueInternalMetaResponse, error) {
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

	items, snapshot, err := buildChecklistSnapshot(issueID, userID, req.Items)
	if err != nil {
		return nil, errs.ErrInvalidParams
	}

	now := time.Now().UTC()
	meta, err := s.internalMetaRepo.Get(issue.ID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
		meta = &model.IssueInternalMeta{
			IssueID:   issue.ID,
			CreatedAt: now,
		}
	}

	meta.ProgressPercent = snapshot.ProgressPercent
	meta.ChecklistTotal = snapshot.Total
	meta.ChecklistDone = snapshot.Done
	meta.UpdatedByUserID = userID
	meta.UpdatedAt = now
	if len(items) == 0 {
		meta.ChecklistUpdatedAt = nil
	} else {
		value := now
		meta.ChecklistUpdatedAt = &value
	}

	if err := s.checklistRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.checklistRepo.ReplaceForIssueTx(tx, issue.ID, items); err != nil {
			return err
		}
		if err := s.internalMetaRepo.UpsertTx(tx, meta); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue checklist replaced",
		zap.String("action", "replace_checklist"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
	)
	return s.toIssueInternalMetaResponse(issue.ProjectID, meta, items, nil), nil
}

func (s *IssueService) UpdateInternalMeta(issueID, userID string, workflowStatus model.IssueWorkflowStatus, actor string) (*IssueInternalMetaResponse, error) {
	if !model.IsValidIssueWorkflowStatus(workflowStatus) {
		return nil, errs.ErrInvalidParams
	}

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

	now := time.Now().UTC()
	meta, err := s.internalMetaRepo.Get(issue.ID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
		meta = &model.IssueInternalMeta{
			IssueID:   issue.ID,
			CreatedAt: now,
		}
	}

	meta.WorkflowStatus = workflowStatus
	meta.UpdatedByUserID = userID
	meta.UpdatedAt = now
	applyExplicitWorkflowStatus(meta, workflowStatus, now)

	// 同步刷新 issues 主表的 updated_at（仅此一列），使看板按 updated_at DESC 排序时能反映状态变更。
	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.internalMetaRepo.UpsertTx(tx, meta); err != nil {
			return err
		}
		if workflowStatus == model.IssueWorkflowStatusInProgress || workflowStatus == model.IssueWorkflowStatusDone {
			if err := s.recRepo.DeleteTx(tx, issue.ID); err != nil {
				return err
			}
		}
		if err := s.issueRepo.TouchUpdatedAt(tx, issue.ID, now); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue internal meta updated",
		zap.String("action", "update_internal_meta"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
		zap.String("workflow_status", string(workflowStatus)),
	)
	return s.toIssueInternalMetaResponse(issue.ProjectID, meta, nil, nil), nil
}

// BatchUpdateInternalMeta 按 issue_id 批量更新 workflow_status，供跨项目持有 UUID 的代理一次提交多条。
// 每条独立成败，部分失败计入 failures 不中断整批；items 为空或超过上限时整体报错。
func (s *IssueService) BatchUpdateInternalMeta(userID string, items []BatchUpdateInternalMetaItem, actor string) (*BatchCloseDoneIssuesResponse, error) {
	start := time.Now()
	if len(items) == 0 || len(items) > batchCloseDoneMaxIssues {
		return nil, errs.ErrInvalidParams
	}

	resp := &BatchCloseDoneIssuesResponse{
		Total:    int64(len(items)),
		Failures: make([]BatchCloseDoneIssueFailure, 0),
	}

	for _, item := range items {
		var updateErr error
		if item.WorkflowStatus == nil {
			updateErr = errs.ErrInvalidParams
		} else {
			_, updateErr = s.UpdateInternalMeta(item.IssueId, userID, *item.WorkflowStatus, actor)
		}
		if updateErr != nil {
			resp.Failed++
			if len(resp.Failures) < batchCloseDoneMaxFailures {
				msg := updateErr.Error()
				var appErr *errs.AppError
				if errors.As(updateErr, &appErr) {
					msg = appErr.Message
				}
				resp.Failures = append(resp.Failures, BatchCloseDoneIssueFailure{
					Id:    item.IssueId,
					Error: msg,
				})
			}
			continue
		}
		resp.Succeeded++
	}

	resp.ElapsedMs = time.Since(start).Milliseconds()
	s.logger.Info("batch update internal meta",
		zap.String("action", "batch_update_internal_meta"),
		zap.String("user_id", userID),
		zap.String("actor", actor),
		zap.Int64("total", resp.Total),
		zap.Int("succeeded", resp.Succeeded),
		zap.Int("failed", resp.Failed),
		zap.Int64("elapsed_ms", resp.ElapsedMs),
	)
	return resp, nil
}
