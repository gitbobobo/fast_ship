package service

import (
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

func batchMetaStatus(status model.IssueWorkflowStatus) *model.IssueWorkflowStatus {
	return &status
}

func TestIssueServiceBatchUpdateInternalMeta_AppliesEachItemIndependently(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-batch-meta")
	project := createTestProject(t, svc.db, user.ID)

	createInternal := func(projectID, userID, title string) *IssueResponse {
		t.Helper()
		resp, err := svc.issueService.CreateInternalIssue(projectID, userID, CreateInternalIssueRequest{Title: title})
		if err != nil {
			t.Fatalf("create internal issue %q: %v", title, err)
		}
		return resp
	}

	issueA := createInternal(project.ID, user.ID, "A")
	issueB := createInternal(project.ID, user.ID, "B")
	issueC := createInternal(project.ID, user.ID, "C")
	if _, err := svc.issueService.UpdateInternalMeta(issueC.Id, user.ID, model.IssueWorkflowStatusInProgress, "test"); err != nil {
		t.Fatalf("seed issueC meta: %v", err)
	}

	other := createTestUser(t, svc.db, "user-batch-meta-other")
	otherProject := createTestProject(t, svc.db, other.ID)
	foreignIssue := createInternal(otherProject.ID, other.ID, "foreign")

	missingID := uuid.NewString()
	invalidStatus := model.IssueWorkflowStatus("bogus")

	resp, err := svc.issueService.BatchUpdateInternalMeta(user.ID, []BatchUpdateInternalMetaItem{
		{IssueId: issueA.Id, WorkflowStatus: batchMetaStatus(model.IssueWorkflowStatusDone)},
		{IssueId: issueB.Id, WorkflowStatus: batchMetaStatus(model.IssueWorkflowStatusInProgress)},
		{IssueId: issueC.Id, WorkflowStatus: batchMetaStatus("")},
		{IssueId: missingID, WorkflowStatus: batchMetaStatus(model.IssueWorkflowStatusDone)},
		{IssueId: foreignIssue.Id, WorkflowStatus: batchMetaStatus(model.IssueWorkflowStatusDone)},
		{IssueId: issueA.Id, WorkflowStatus: &invalidStatus},
		{IssueId: issueA.Id},
	}, "test")
	if err != nil {
		t.Fatalf("batch update internal meta: %v", err)
	}

	if resp.Total != 7 || resp.Succeeded != 3 || resp.Failed != 4 {
		t.Fatalf("unexpected batch result: %+v", resp)
	}
	if resp.ElapsedMs < 0 {
		t.Fatalf("expected non-negative elapsed_ms, got %d", resp.ElapsedMs)
	}

	wantFailures := []struct {
		id  string
		msg string
	}{
		{missingID, errs.ErrIssueNotFound.Message},
		{foreignIssue.Id, errs.ErrProjectNotFound.Message},
		{issueA.Id, errs.ErrInvalidParams.Message},
		{issueA.Id, errs.ErrInvalidParams.Message},
	}
	if len(resp.Failures) != len(wantFailures) {
		t.Fatalf("expected %d failure entries, got %+v", len(wantFailures), resp.Failures)
	}
	for i, want := range wantFailures {
		got := resp.Failures[i]
		if got.Id != want.id || got.Error != want.msg {
			t.Fatalf("failure[%d]: expected id=%q error=%q, got %+v", i, want.id, want.msg, got)
		}
	}

	var metaA model.IssueInternalMeta
	if err := svc.db.Where("issue_id = ?", issueA.Id).First(&metaA).Error; err != nil {
		t.Fatalf("load issueA meta: %v", err)
	}
	if metaA.WorkflowStatus != model.IssueWorkflowStatusDone || metaA.CompletedAt == nil {
		t.Fatalf("unexpected issueA meta: %+v", metaA)
	}

	var metaB model.IssueInternalMeta
	if err := svc.db.Where("issue_id = ?", issueB.Id).First(&metaB).Error; err != nil {
		t.Fatalf("load issueB meta: %v", err)
	}
	if metaB.WorkflowStatus != model.IssueWorkflowStatusInProgress || metaB.StartedAt == nil {
		t.Fatalf("unexpected issueB meta: %+v", metaB)
	}

	var metaC model.IssueInternalMeta
	if err := svc.db.Where("issue_id = ?", issueC.Id).First(&metaC).Error; err != nil {
		t.Fatalf("load issueC meta: %v", err)
	}
	if metaC.WorkflowStatus != "" {
		t.Fatalf("expected issueC workflow status reset, got %q", metaC.WorkflowStatus)
	}

	var foreignMetaCount int64
	if err := svc.db.Model(&model.IssueInternalMeta{}).Where("issue_id = ?", foreignIssue.Id).Count(&foreignMetaCount).Error; err != nil {
		t.Fatalf("count foreign meta: %v", err)
	}
	if foreignMetaCount != 0 {
		t.Fatalf("expected no meta written for foreign issue")
	}
}

func TestIssueServiceBatchUpdateInternalMeta_RejectsEmptyItems(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-batch-meta-empty")

	for _, items := range [][]BatchUpdateInternalMetaItem{nil, {}} {
		if _, err := svc.issueService.BatchUpdateInternalMeta("user-batch-meta-empty", items, "test"); err != errs.ErrInvalidParams {
			t.Fatalf("expected ErrInvalidParams for empty items, got %v", err)
		}
	}
}

func TestIssueServiceBatchUpdateInternalMeta_RejectsOverLimit(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-batch-meta-limit")

	items := make([]BatchUpdateInternalMetaItem, 0, batchCloseDoneMaxIssues+1)
	for i := 0; i <= batchCloseDoneMaxIssues; i++ {
		items = append(items, BatchUpdateInternalMetaItem{
			IssueId:        uuid.NewString(),
			WorkflowStatus: batchMetaStatus(model.IssueWorkflowStatusDone),
		})
	}
	if _, err := svc.issueService.BatchUpdateInternalMeta("user-batch-meta-limit", items, "test"); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for oversized items, got %v", err)
	}
}
