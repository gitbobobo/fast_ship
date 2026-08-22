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
