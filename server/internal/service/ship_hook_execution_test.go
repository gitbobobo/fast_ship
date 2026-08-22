package service

import (
	"errors"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

type fakeShipHookActions struct {
	failIssueID   string
	expireIssueID string
	onComment     func(issueID string)
}

func (f *fakeShipHookActions) CreateInternalCommentIdempotent(issueID, userID string, req CreateInternalIssueCommentRequest, source, idempotencyKey string) (*IssueCommentResponse, error) {
	if issueID == f.failIssueID {
		return nil, errors.New("comment provider unavailable")
	}
	if issueID == f.expireIssueID && f.onComment != nil {
		f.onComment(issueID)
	}
	return &IssueCommentResponse{}, nil
}

func (f *fakeShipHookActions) InternalMetaWorkflowStatus(string) (model.IssueWorkflowStatus, error) {
	return model.IssueWorkflowStatusTodo, nil
}

func (f *fakeShipHookActions) UpdateInternalMeta(string, string, model.IssueWorkflowStatus, string) (*IssueInternalMetaResponse, error) {
	return &IssueInternalMetaResponse{}, nil
}

func (f *fakeShipHookActions) UpdateInternalIssue(string, string, UpdateInternalIssueRequest) (*IssueResponse, error) {
	return &IssueResponse{}, nil
}

func TestVersionForShipHookRecoveryKeepsOriginalReleaseContext(t *testing.T) {
	recoveryVersion := &model.Version{
		ID: "version-b", VersionNumber: "2.0.0", GithubReleaseURL: "url-b",
	}
	firedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	hook := &model.IssueShipHook{
		FiredVersionID: "version-a", FiredVersionNumber: "1.0.0", FiredReleaseURL: "url-a", FiredAt: &firedAt,
	}
	got := versionForShipHook(hook, recoveryVersion)
	if got.ID != "version-a" || got.VersionNumber != "1.0.0" || got.GithubReleaseURL != "url-a" {
		t.Fatalf("recovery used later release context: %+v", got)
	}
}

func TestExecutePendingShipHooksAggregatesRecoveredVersionResults(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "hook-recovery-user")
	project := createTestProject(t, svc.db, user.ID)
	original := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.Status = model.VersionStatusShipped
		v.ShipHooksStatus = "failed"
	})
	retryVersion := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.VersionNumber = "v2.0.0"
		v.ShipHooksStatus = "pending"
	})
	successIssue := createTestIssue(t, svc.db, project.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 1
	})
	if err := svc.db.Model(&model.IssueGitHubMeta{}).Where("issue_id = ?", successIssue.ID).Update("github_issue_id", 1002).Error; err != nil {
		t.Fatalf("prepare second issue marker: %v", err)
	}
	failureIssue := createTestIssue(t, svc.db, project.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
	})
	now := time.Now().UTC()
	for _, issue := range []*model.Issue{successIssue, failureIssue} {
		hook := &model.IssueShipHook{
			IssueID: issue.ID, ProjectID: project.ID,
			Status: model.IssueShipHookStatusFired, CommentEnabled: true,
			CommentBody: "released {version}", FiredVersionID: original.ID,
			FiredVersionNumber: original.VersionNumber, FiredReleaseURL: original.GithubReleaseURL,
			FiredAt: &now, RetryPending: true, ExecutionToken: uuid.NewString(),
			CreatedByUserID: user.ID, UpdatedByUserID: user.ID, CreatedAt: now, UpdatedAt: now,
		}
		if err := svc.shipHookRepo.Upsert(hook); err != nil {
			t.Fatalf("seed retry hook: %v", err)
		}
	}

	actions := &fakeShipHookActions{failIssueID: failureIssue.ID}
	svc.shipService.hookActions = actions
	first, err := svc.shipService.ExecutePendingShipHooks(project.ID, user.ID, retryVersion)
	if err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	if first.HookTotal != 0 || first.HookFailed != 0 {
		t.Fatalf("recovery hooks polluted new version counts: %+v", first)
	}
	svc.shipService.markRecoveredHookVersions(first.RecoveredVersionIDs)
	gotOriginal, err := svc.versionRepo.FindByID(original.ID)
	if err != nil {
		t.Fatalf("reload original after partial recovery: %v", err)
	}
	if gotOriginal.ShipHooksStatus != "failed" {
		t.Fatalf("partial recovery incorrectly completed original: %s", gotOriginal.ShipHooksStatus)
	}

	actions.failIssueID = ""
	second, err := svc.shipService.ExecutePendingShipHooks(project.ID, user.ID, retryVersion)
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if second.HookTotal != 0 || second.HookFailed != 0 {
		t.Fatalf("second recovery hooks polluted new version counts: %+v", second)
	}
	svc.shipService.markRecoveredHookVersions(second.RecoveredVersionIDs)
	gotOriginal, err = svc.versionRepo.FindByID(original.ID)
	if err != nil {
		t.Fatalf("reload original after complete recovery: %v", err)
	}
	if gotOriginal.ShipHooksStatus != "completed" {
		t.Fatalf("complete recovery did not complete original: %s", gotOriginal.ShipHooksStatus)
	}
}

func TestRecoveredVersionStaysCompletedWhenLaterHookPersistenceFails(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "hook-recovery-infra-user")
	project := createTestProject(t, svc.db, user.ID)
	original := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.Status = model.VersionStatusShipped
		v.ShipHooksStatus = "failed"
	})
	current := createTestVersion(t, svc.db, project.ID, func(v *model.Version) {
		v.VersionNumber = "v2.0.0"
		v.ShipHooksStatus = "pending"
	})
	originalIssue := createTestIssue(t, svc.db, project.ID)
	if err := svc.db.Model(&model.IssueGitHubMeta{}).Where("issue_id = ?", originalIssue.ID).Update("github_issue_id", 1002).Error; err != nil {
		t.Fatalf("prepare current issue marker: %v", err)
	}
	currentIssue := createTestIssue(t, svc.db, project.ID, func(issue *model.Issue) {
		issue.SequenceNumber = 2
	})
	now := time.Now().UTC()
	if err := svc.shipHookRepo.Upsert(&model.IssueShipHook{
		IssueID: originalIssue.ID, ProjectID: project.ID, Status: model.IssueShipHookStatusFired,
		CommentEnabled: true, CommentBody: "released {version}", FiredVersionID: original.ID,
		FiredVersionNumber: original.VersionNumber, RetryPending: true, ExecutionToken: uuid.NewString(),
		FiredAt: &now, CreatedByUserID: user.ID, UpdatedByUserID: user.ID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed recovered hook: %v", err)
	}
	if err := svc.shipHookRepo.Upsert(&model.IssueShipHook{
		IssueID: currentIssue.ID, ProjectID: project.ID, Status: model.IssueShipHookStatusPending,
		CommentEnabled: true, CommentBody: "released {version}", CreatedByUserID: user.ID,
		UpdatedByUserID: user.ID, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("seed current hook: %v", err)
	}

	actions := &fakeShipHookActions{expireIssueID: currentIssue.ID}
	actions.onComment = func(issueID string) {
		if err := svc.db.Model(&model.IssueShipHook{}).Where("issue_id = ?", issueID).
			Update("lease_expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
			t.Fatalf("expire current hook lease: %v", err)
		}
	}
	svc.shipService.hookActions = actions
	result, err := svc.shipService.ExecutePendingShipHooks(project.ID, user.ID, current)
	if err == nil {
		t.Fatal("expected current hook persistence error")
	}
	if len(result.RecoveredVersionIDs) != 1 || result.RecoveredVersionIDs[0] != original.ID {
		t.Fatalf("unexpected recovered versions: %+v", result.RecoveredVersionIDs)
	}
	svc.shipService.markRecoveredHookVersions(result.RecoveredVersionIDs)
	gotOriginal, err := svc.versionRepo.FindByID(original.ID)
	if err != nil {
		t.Fatalf("reload original: %v", err)
	}
	if gotOriginal.ShipHooksStatus != "completed" {
		t.Fatalf("current hook error polluted completed original: %s", gotOriginal.ShipHooksStatus)
	}
}
