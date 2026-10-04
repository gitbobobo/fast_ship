package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
)

var recTestSeq = 100

func setupRecommendationIssue(t *testing.T) (*testServices, *model.Issue, string) {
	t.Helper()
	ts := setupTestServices(t)
	ownerID := uuid.NewString()
	createTestUser(t, ts.db, ownerID)
	project := createTestProject(t, ts.db, ownerID)
	issue := createTestIssue(t, ts.db, project.ID)
	return ts, issue, ownerID
}

// createRecTestIssue 只建 issues 行（无 github meta），供依赖/列表场景使用。
func createRecTestIssue(t *testing.T, ts *testServices, projectID string) *model.Issue {
	t.Helper()
	recTestSeq++
	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.New().String(),
		ProjectID:      projectID,
		Source:         model.IssueSourceGitHub,
		SequenceNumber: recTestSeq,
		State:          model.IssueStateOpen,
		Title:          "dep issue",
		AuthorLogin:    "alice",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
	}
	if err := ts.db.Create(issue).Error; err != nil {
		t.Fatalf("create dep issue: %v", err)
	}
	return issue
}

func createInternalIssue(t *testing.T, ts *testServices, projectID string, state model.IssueState) *model.Issue {
	t.Helper()
	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.New().String(),
		ProjectID:      projectID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: 99,
		State:          state,
		Title:          "internal issue",
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
	}
	if err := ts.db.Create(issue).Error; err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	return issue
}

func TestIssueRecommendation_Upsert(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	dep := createRecTestIssue(t, ts, issue.ProjectID)

	first, err := ts.recService.Upsert(issue.ID, ownerID, "bot-key", UpsertIssueRecommendationRequest{
		Reason:       "  值得先做  ",
		Dependencies: []string{dep.ID},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if first.Reason != "值得先做" || first.Priority != model.IssueRecommendationPriorityMedium || first.CreatedBy != "bot-key" {
		t.Fatalf("unexpected recommendation: %+v", first)
	}
	if len(first.Dependencies) != 1 || first.Dependencies[0].IssueID != dep.ID || first.Dependencies[0].Title != dep.Title {
		t.Fatalf("unexpected dependencies: %+v", first.Dependencies)
	}
	if first.Issue.Reference != "GH-42" || first.Dependencies[0].Reference != fmt.Sprintf("INT-%d", dep.SequenceNumber) {
		t.Fatalf("unexpected references: issue=%q dep=%q", first.Issue.Reference, first.Dependencies[0].Reference)
	}

	second, err := ts.recService.Upsert(issue.ID, ownerID, "another-key", UpsertIssueRecommendationRequest{
		Reason:   "改优先级",
		Priority: model.IssueRecommendationPriorityHigh,
	})
	if err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if second.Priority != model.IssueRecommendationPriorityHigh || second.CreatedBy != "another-key" || len(second.Dependencies) != 0 {
		t.Fatalf("unexpected overwritten recommendation: %+v", second)
	}
	if second.CreatedAt != first.CreatedAt {
		t.Fatalf("expected created_at preserved, got %s vs %s", second.CreatedAt, first.CreatedAt)
	}

	var depCount int64
	if err := ts.db.Model(&model.RecommendationDependency{}).Where("issue_id = ?", issue.ID).Count(&depCount).Error; err != nil {
		t.Fatalf("count deps: %v", err)
	}
	if depCount != 0 {
		t.Fatalf("expected dependencies replaced, got %d rows", depCount)
	}
}

func TestIssueRecommendation_UpsertValidation(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)

	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "  "}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty reason, got %v", err)
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: strings.Repeat("长", 501)}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for too-long reason, got %v", err)
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok", Priority: "urgent"}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for invalid priority, got %v", err)
	}
	deps := make([]string, 21)
	for i := range deps {
		deps[i] = uuid.NewString()
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok", Dependencies: deps}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for too many deps, got %v", err)
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok", Dependencies: []string{issue.ID}}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for self dependency, got %v", err)
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok", Dependencies: []string{uuid.NewString()}}); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound for missing dep, got %v", err)
	}
	if _, err := ts.recService.Upsert(uuid.NewString(), ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound for missing issue, got %v", err)
	}
}

func TestIssueRecommendation_DependencyOwnership(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)

	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)
	foreignProject := createTestProject(t, ts.db, otherID)
	foreignDep := createRecTestIssue(t, ts, foreignProject.ID)
	ownDep := createRecTestIssue(t, ts, issue.ProjectID)

	// 依赖他人项目的 issue 与依赖不存在同等处理：40405
	for _, deps := range [][]string{{foreignDep.ID}, {ownDep.ID, foreignDep.ID}} {
		if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{
			Reason:       "ok",
			Dependencies: deps,
		}); err != errs.ErrIssueNotFound {
			t.Fatalf("expected ErrIssueNotFound for foreign dep %v, got %v", deps, err)
		}
	}
	if _, err := ts.recRepo.Get(issue.ID); err == nil {
		t.Fatalf("expected no recommendation written after rejected upsert")
	}

	// 同一 owner 下跨项目依赖允许
	siblingProject := createTestProject(t, ts.db, ownerID)
	siblingDep := createRecTestIssue(t, ts, siblingProject.ID)
	resp, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{
		Reason:       "ok",
		Dependencies: []string{siblingDep.ID},
	})
	if err != nil {
		t.Fatalf("upsert with same-owner cross-project dep: %v", err)
	}
	if len(resp.Dependencies) != 1 || resp.Dependencies[0].IssueID != siblingDep.ID {
		t.Fatalf("unexpected dependencies: %+v", resp.Dependencies)
	}
}

func TestIssueRecommendation_FailedUpsertKeepsPriorState(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	dep := createRecTestIssue(t, ts, issue.ProjectID)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{
		Reason:       "原始理由",
		Dependencies: []string{dep.ID},
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// 失败的覆盖不能改动已存推荐与依赖
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k2", UpsertIssueRecommendationRequest{
		Reason:       "覆盖理由",
		Dependencies: []string{uuid.NewString()},
	}); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound, got %v", err)
	}
	rec, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get rec: %v", err)
	}
	if rec.Reason != "原始理由" || rec.CreatedBy != "k" {
		t.Fatalf("expected prior recommendation untouched, got %+v", rec)
	}
	var depCount int64
	if err := ts.db.Model(&model.RecommendationDependency{}).Where("issue_id = ?", issue.ID).Count(&depCount).Error; err != nil {
		t.Fatalf("count deps: %v", err)
	}
	if depCount != 1 {
		t.Fatalf("expected prior dependencies untouched, got %d rows", depCount)
	}
}

func TestIssueRecommendation_NotRecommendable(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)

	closedIssue := createInternalIssue(t, ts, issue.ProjectID, model.IssueStateClosed)
	if _, err := ts.recService.Upsert(closedIssue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != errs.ErrIssueNotRecommendable {
		t.Fatalf("expected ErrIssueNotRecommendable for closed issue, got %v", err)
	}

	if _, err := ts.issueService.UpdateInternalMeta(issue.ID, ownerID, model.IssueWorkflowStatusInProgress, "tester"); err != nil {
		t.Fatalf("update meta: %v", err)
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != errs.ErrIssueNotRecommendable {
		t.Fatalf("expected ErrIssueNotRecommendable for in_progress issue, got %v", err)
	}
}

func TestIssueRecommendation_AccessControl(t *testing.T) {
	ts, issue, _ := setupRecommendationIssue(t)
	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)

	if _, err := ts.recService.Upsert(issue.ID, otherID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner upsert, got %v", err)
	}
	if _, err := ts.recService.List(otherID, issue.ProjectID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner list filter, got %v", err)
	}
}

func TestIssueRecommendation_List(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	otherProject := createTestProject(t, ts.db, ownerID)
	lowIssue := createRecTestIssue(t, ts, issue.ProjectID)
	midIssue := createRecTestIssue(t, ts, otherProject.ID)

	seed := func(issueID string, req UpsertIssueRecommendationRequest) {
		t.Helper()
		if _, err := ts.recService.Upsert(issueID, ownerID, "k", req); err != nil {
			t.Fatalf("seed upsert: %v", err)
		}
	}
	seed(lowIssue.ID, UpsertIssueRecommendationRequest{Reason: "low", Priority: model.IssueRecommendationPriorityLow})
	seed(midIssue.ID, UpsertIssueRecommendationRequest{Reason: "mid", Priority: model.IssueRecommendationPriorityMedium})
	seed(issue.ID, UpsertIssueRecommendationRequest{Reason: "high", Priority: model.IssueRecommendationPriorityHigh})

	all, err := ts.recService.List(ownerID, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(all.Items))
	}
	if all.Items[0].Issue.ID != issue.ID || all.Items[1].Issue.ID != midIssue.ID || all.Items[2].Issue.ID != lowIssue.ID {
		t.Fatalf("unexpected ordering: %+v", all.Items)
	}
	if all.Items[1].Issue.ProjectName != otherProject.Name {
		t.Fatalf("expected project name %q, got %q", otherProject.Name, all.Items[1].Issue.ProjectName)
	}

	scoped, err := ts.recService.List(ownerID, otherProject.ID)
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(scoped.Items) != 1 || scoped.Items[0].Issue.ID != midIssue.ID {
		t.Fatalf("unexpected scoped items: %+v", scoped.Items)
	}
}

func TestIssueRecommendation_Delete(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	dep := createRecTestIssue(t, ts, issue.ProjectID)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{
		Reason:       "ok",
		Dependencies: []string{dep.ID},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)
	if err := ts.recService.Delete(issue.ID, otherID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner delete, got %v", err)
	}

	if err := ts.recService.Delete(issue.ID, ownerID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var depCount int64
	if err := ts.db.Model(&model.RecommendationDependency{}).Where("issue_id = ?", issue.ID).Count(&depCount).Error; err != nil {
		t.Fatalf("count deps: %v", err)
	}
	if depCount != 0 {
		t.Fatalf("expected dependency rows removed, got %d", depCount)
	}
	if err := ts.recService.Delete(issue.ID, ownerID); err != errs.ErrRecommendationNotFound {
		t.Fatalf("expected ErrRecommendationNotFound for repeated delete, got %v", err)
	}
}

func TestIssueRecommendation_RemovedOnWorkflowAdvance(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := ts.issueService.UpdateInternalMeta(issue.ID, ownerID, model.IssueWorkflowStatusInProgress, "tester"); err != nil {
		t.Fatalf("update meta: %v", err)
	}
	if _, err := ts.recRepo.Get(issue.ID); err == nil {
		t.Fatalf("expected recommendation removed after in_progress")
	}
}

func TestIssueRecommendation_RemovedOnInternalClose(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	internal := createInternalIssue(t, ts, issue.ProjectID, model.IssueStateOpen)
	if _, err := ts.recService.Upsert(internal.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	closed := model.IssueStateClosed
	if _, err := ts.issueService.UpdateInternalIssue(internal.ID, ownerID, UpdateInternalIssueRequest{State: &closed}); err != nil {
		t.Fatalf("update internal issue: %v", err)
	}
	if _, err := ts.recRepo.Get(internal.ID); err == nil {
		t.Fatalf("expected recommendation removed after close")
	}
}

func TestIssueRecommendation_UpsertResponseCreatedAt(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// 把库里的 created_at 改成一个固定旧时间，覆盖写后响应必须返回该值而非本次写入时间
	fixed := time.Date(2020, 5, 4, 3, 2, 1, 0, time.UTC)
	if err := ts.db.Model(&model.IssueRecommendation{}).Where("issue_id = ?", issue.ID).Update("created_at", fixed).Error; err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}

	resp, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "覆盖"})
	if err != nil {
		t.Fatalf("overwrite upsert: %v", err)
	}
	stored, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get rec: %v", err)
	}
	if stored.CreatedAt.Year() != 2020 {
		t.Fatalf("expected db created_at preserved, got %v", stored.CreatedAt)
	}
	if resp.CreatedAt != formatTime(stored.CreatedAt) {
		t.Fatalf("expected response created_at %s, got %s", formatTime(stored.CreatedAt), resp.CreatedAt)
	}
}

func TestIssueRecommendation_RemovedOnGitHubSyncClose(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	item := &ghclient.Issue{
		Issue: gh.Issue{
			ID:        gh.Int64(1001),
			Number:    gh.Int(42),
			State:     gh.String("closed"),
			Title:     gh.String(issue.Title),
			CreatedAt: &gh.Timestamp{Time: time.Now().UTC().Add(-time.Hour)},
			UpdatedAt: &gh.Timestamp{Time: time.Now().UTC()},
			ClosedAt:  &gh.Timestamp{Time: time.Now().UTC()},
			User:      &gh.User{Login: gh.String("alice")},
		},
	}
	if _, err := ts.issueService.upsertGitHubIssue(issue.ProjectID, item); err != nil {
		t.Fatalf("upsert github issue: %v", err)
	}
	if _, err := ts.recRepo.Get(issue.ID); err == nil {
		t.Fatalf("expected recommendation removed after github sync close")
	}
}

// 已关闭的新 issue 走 create 分支，推荐删除在同一事务内（此时为 no-op）且不报错。
func TestIssueRecommendation_GitHubSyncClosedCreate(t *testing.T) {
	ts, issue, _ := setupRecommendationIssue(t)

	item := &ghclient.Issue{
		Issue: gh.Issue{
			ID:        gh.Int64(2002),
			Number:    gh.Int(43),
			State:     gh.String("closed"),
			Title:     gh.String("已关闭的新 issue"),
			CreatedAt: &gh.Timestamp{Time: time.Now().UTC().Add(-time.Hour)},
			UpdatedAt: &gh.Timestamp{Time: time.Now().UTC()},
			ClosedAt:  &gh.Timestamp{Time: time.Now().UTC()},
			User:      &gh.User{Login: gh.String("bob")},
		},
	}
	stored, err := ts.issueService.upsertGitHubIssue(issue.ProjectID, item)
	if err != nil {
		t.Fatalf("upsert github issue: %v", err)
	}
	if stored.State != model.IssueStateClosed {
		t.Fatalf("expected closed issue persisted, got %s", stored.State)
	}
	if _, err := ts.recRepo.Get(stored.ID); err == nil {
		t.Fatalf("expected no recommendation for newly closed issue")
	}
}
