package service

import (
	"errors"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
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

	s.logger.With(zap.String("user_id", userID)).Info("issue checklist replaced",
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
	prevWorkflowStatus := meta.WorkflowStatus

	meta.WorkflowStatus = workflowStatus
	meta.UpdatedByUserID = userID
	meta.UpdatedAt = now
	applyExplicitWorkflowStatus(meta, workflowStatus, now)

	// 事件归属用户在事务外解析：单连接池下闭包内再借连接会永久自锁（INT-97）。
	ownerID := issue.AuthorUserID
	if workflowStatus != prevWorkflowStatus && ownerID == "" {
		project, err := s.projectRepo.FindByIDGlobal(issue.ProjectID)
		if err != nil {
			return nil, errs.ErrInternal
		}
		ownerID = project.UserID
	}

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
		// 自动化事件（INT-90）：workflow_status 变化与 state 变化是两类事件，
		// 无变化（重复设置同一状态）不触发。
		if workflowStatus != prevWorkflowStatus {
			if err := s.emitIssueEventTx(tx, IssueEvent{
				Type:                model.AutomationTriggerIssueStatusChanged,
				UserID:              ownerID,
				ProjectID:           issue.ProjectID,
				IssueID:             issue.ID,
				TriggerKey:          "event:" + uuid.NewString(),
				StatusField:         "workflow_status",
				From:                string(prevWorkflowStatus),
				To:                  string(workflowStatus),
				IssueSequenceNumber: issue.SequenceNumber,
				IssueTitle:          issue.Title,
				OccurredAt:          now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.With(zap.String("user_id", userID)).Info("issue internal meta updated",
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
	s.logger.With(zap.String("user_id", userID)).Info("batch update internal meta",
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

// emitInternalUpdateEventsTx 内部 issue 更新（UpdateInternalIssue）的自动化事件：
// state 变化发状态事件，标签只对新增集合发一次事件。无变化不触发。
// internalMeta 为 nil 表示本次未改标签。ownerID 由调用方在事务外解析传入——
// 闭包内调任何走池根的方法都会在单连接池下自锁（INT-97）。
func (s *IssueService) emitInternalUpdateEventsTx(
	tx *gorm.DB,
	ownerID string,
	issue *model.Issue,
	req UpdateInternalIssueRequest,
	prevState model.IssueState,
	prevLabels []string,
	internalMeta *model.IssueInternalMeta,
) error {
	now := time.Now().UTC()
	if req.State != nil && issue.State != prevState {
		if err := s.emitIssueEventTx(tx, IssueEvent{
			Type:                model.AutomationTriggerIssueStatusChanged,
			UserID:              ownerID,
			ProjectID:           issue.ProjectID,
			IssueID:             issue.ID,
			TriggerKey:          "event:" + uuid.NewString(),
			StatusField:         "state",
			From:                string(prevState),
			To:                  string(issue.State),
			IssueSequenceNumber: issue.SequenceNumber,
			IssueTitle:          issue.Title,
			OccurredAt:          now,
		}); err != nil {
			return err
		}
	}
	if internalMeta != nil {
		added := missingLabels(parseLabelsJSON(internalMeta.LabelsJSON), prevLabels)
		if len(added) > 0 {
			if err := s.emitIssueEventTx(tx, IssueEvent{
				Type:                model.AutomationTriggerIssueLabelAdded,
				UserID:              ownerID,
				ProjectID:           issue.ProjectID,
				IssueID:             issue.ID,
				TriggerKey:          "event:" + uuid.NewString(),
				LabelsAdded:         added,
				IssueSequenceNumber: issue.SequenceNumber,
				IssueTitle:          issue.Title,
				OccurredAt:          now,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
