package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
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
	if len(first.Dependencies) != 1 || first.Dependencies[0].IssueId != dep.ID || first.Dependencies[0].Title != dep.Title {
		t.Fatalf("unexpected dependencies: %+v", first.Dependencies)
	}
	if first.Issue.Reference != "GH-42" || first.Dependencies[0].Reference != fmt.Sprintf("INT-%d", dep.SequenceNumber) {
		t.Fatalf("unexpected references: issue=%q dep=%q", first.Issue.Reference, first.Dependencies[0].Reference)
	}

	second, err := ts.recService.Upsert(issue.ID, ownerID, "another-key", UpsertIssueRecommendationRequest{
		Reason:   "改优先级",
		Priority: api.Ptr(model.IssueRecommendationPriorityHigh),
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
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok", Priority: api.Ptr(model.IssueRecommendationPriority("urgent"))}); err != errs.ErrInvalidParams {
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
	if len(resp.Dependencies) != 1 || resp.Dependencies[0].IssueId != siblingDep.ID {
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
	seed(lowIssue.ID, UpsertIssueRecommendationRequest{Reason: "low", Priority: api.Ptr(model.IssueRecommendationPriorityLow)})
	seed(midIssue.ID, UpsertIssueRecommendationRequest{Reason: "mid", Priority: api.Ptr(model.IssueRecommendationPriorityMedium)})
	seed(issue.ID, UpsertIssueRecommendationRequest{Reason: "high", Priority: api.Ptr(model.IssueRecommendationPriorityHigh)})

	all, err := ts.recService.List(ownerID, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(all.Items))
	}
	if all.Items[0].Issue.Id != issue.ID || all.Items[1].Issue.Id != midIssue.ID || all.Items[2].Issue.Id != lowIssue.ID {
		t.Fatalf("unexpected ordering: %+v", all.Items)
	}
	if all.Items[1].Issue.ProjectName != otherProject.Name {
		t.Fatalf("expected project name %q, got %q", otherProject.Name, all.Items[1].Issue.ProjectName)
	}

	scoped, err := ts.recService.List(ownerID, otherProject.ID)
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(scoped.Items) != 1 || scoped.Items[0].Issue.Id != midIssue.ID {
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
	if err := ts.recService.Delete(issue.ID, otherID, true); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner delete, got %v", err)
	}

	if err := ts.recService.Delete(issue.ID, ownerID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var depCount int64
	if err := ts.db.Model(&model.RecommendationDependency{}).Where("issue_id = ?", issue.ID).Count(&depCount).Error; err != nil {
		t.Fatalf("count deps: %v", err)
	}
	if depCount != 0 {
		t.Fatalf("expected dependency rows removed, got %d", depCount)
	}
	if err := ts.recService.Delete(issue.ID, ownerID, true); err != errs.ErrRecommendationNotFound {
		t.Fatalf("expected ErrRecommendationNotFound for repeated delete, got %v", err)
	}
}

func TestIssueRecommendation_Defer(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	dep := createRecTestIssue(t, ts, issue.ProjectID)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{
		Reason:       "  先做这个  ",
		Dependencies: []string{dep.ID},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 不带 note 延后：冻结整条推荐，reason/priority/依赖原样保留
	resp, err := ts.recService.Defer(issue.ID, ownerID, "  ")
	if err != nil {
		t.Fatalf("defer: %v", err)
	}
	if resp.Status != api.Deferred || resp.DeferredAt == nil || resp.DeferNote != nil {
		t.Fatalf("expected deferred status with nil note, got %+v", resp)
	}
	if resp.Reason != "先做这个" || len(resp.Dependencies) != 1 || resp.Dependencies[0].IssueId != dep.ID {
		t.Fatalf("expected frozen recommendation data, got %+v", resp)
	}

	// 重复延后：覆盖 note 并刷新 deferred_at（幂等 upsert）
	firstDeferredAt := *resp.DeferredAt
	time.Sleep(1100 * time.Millisecond) // SQLite timestamp 精度到秒，隔开确保 deferred_at 可观测地刷新
	resp, err = ts.recService.Defer(issue.ID, ownerID, "等等再看")
	if err != nil {
		t.Fatalf("re-defer: %v", err)
	}
	if resp.DeferNote == nil || *resp.DeferNote != "等等再看" {
		t.Fatalf("expected note overwritten, got %+v", resp.DeferNote)
	}
	if *resp.DeferredAt <= firstDeferredAt {
		t.Fatalf("expected deferred_at refreshed, got %s vs %s", *resp.DeferredAt, firstDeferredAt)
	}

	// 库里行与响应一致
	stored, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get rec: %v", err)
	}
	if stored.DeferredAt == nil || stored.DeferNote != "等等再看" || stored.Reason != "先做这个" {
		t.Fatalf("unexpected stored rec: %+v", stored)
	}
}

func TestIssueRecommendation_DeferValidation(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)

	// 推荐不存在 → 40411
	if _, err := ts.recService.Defer(issue.ID, ownerID, ""); err != errs.ErrRecommendationNotFound {
		t.Fatalf("expected ErrRecommendationNotFound, got %v", err)
	}

	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// note 超 500 rune → 40001
	if _, err := ts.recService.Defer(issue.ID, ownerID, strings.Repeat("长", 501)); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for too-long note, got %v", err)
	}

	// 非项目属主 → 40401
	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)
	if _, err := ts.recService.Defer(issue.ID, otherID, ""); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner defer, got %v", err)
	}
}

func TestIssueRecommendation_DeferredBlocksUpsert(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := ts.recService.Defer(issue.ID, ownerID, ""); err != nil {
		t.Fatalf("defer: %v", err)
	}
	// 延后项再 PUT → 40911，且整行未被覆盖
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k2", UpsertIssueRecommendationRequest{Reason: "重新推荐"}); err != errs.ErrRecommendationDeferred {
		t.Fatalf("expected ErrRecommendationDeferred, got %v", err)
	}
	stored, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get rec: %v", err)
	}
	if stored.Reason != "ok" || stored.CreatedBy != "k" || stored.DeferredAt == nil {
		t.Fatalf("expected deferred row untouched, got %+v", stored)
	}
}

// Delete 的读+延后检查+删在同一事务：defer 先提交、delete 后执行时，事务内必读到延后态。
func TestIssueRecommendation_DeleteDeferred(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// API Key 删 active 项照旧放行
	if err := ts.recService.Delete(issue.ID, ownerID, false); err != nil {
		t.Fatalf("api-key delete active: %v", err)
	}
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if _, err := ts.recService.Defer(issue.ID, ownerID, ""); err != nil {
		t.Fatalf("defer: %v", err)
	}

	// defer 已提交后再删：API Key 40911 且行仍在（事务内读到 committed 延后态）
	if err := ts.recService.Delete(issue.ID, ownerID, false); err != errs.ErrRecommendationDeferred {
		t.Fatalf("expected ErrRecommendationDeferred for api-key delete, got %v", err)
	}
	stored, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("expected deferred row preserved after rejected delete: %v", err)
	}
	if stored.DeferredAt == nil {
		t.Fatalf("expected row still deferred, got %+v", stored)
	}

	// JWT 删延后项 → 彻底移除
	if err := ts.recService.Delete(issue.ID, ownerID, true); err != nil {
		t.Fatalf("jwt delete deferred: %v", err)
	}
	if _, err := ts.recRepo.Get(issue.ID); err == nil {
		t.Fatalf("expected recommendation hard-deleted")
	}
	// 删除=遗忘：可再被推荐
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "再来"}); err != nil {
		t.Fatalf("re-upsert after delete: %v", err)
	}
}

func TestIssueRecommendation_Restore(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 未延后时调用：幂等成功返回当前项
	resp, err := ts.recService.Restore(issue.ID, ownerID)
	if err != nil {
		t.Fatalf("restore active rec: %v", err)
	}
	if resp.Status != api.Active || resp.DeferredAt != nil {
		t.Fatalf("expected active item, got %+v", resp)
	}

	if _, err := ts.recService.Defer(issue.ID, ownerID, "稍后"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	deferred, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get deferred rec: %v", err)
	}
	beforeUpdatedAt := deferred.UpdatedAt
	time.Sleep(1100 * time.Millisecond)

	resp, err = ts.recService.Restore(issue.ID, ownerID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if resp.Status != api.Active || resp.DeferredAt != nil || resp.DeferNote != nil {
		t.Fatalf("expected restored active item, got %+v", resp)
	}
	stored, err := ts.recRepo.Get(issue.ID)
	if err != nil {
		t.Fatalf("get rec: %v", err)
	}
	if stored.DeferredAt != nil || stored.DeferNote != "" {
		t.Fatalf("expected defer fields cleared, got %+v", stored)
	}
	if !stored.UpdatedAt.After(beforeUpdatedAt) {
		t.Fatalf("expected updated_at refreshed, got %v vs %v", stored.UpdatedAt, beforeUpdatedAt)
	}
	// 恢复后 Agent 可再推荐
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "再推荐"}); err != nil {
		t.Fatalf("upsert after restore: %v", err)
	}
}

func TestIssueRecommendation_RestoreClosedIssue(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	internal := createInternalIssue(t, ts, issue.ProjectID, model.IssueStateOpen)
	if _, err := ts.recService.Upsert(internal.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := ts.recService.Defer(internal.ID, ownerID, ""); err != nil {
		t.Fatalf("defer: %v", err)
	}
	// 直接把 issues 表 state 改成 closed（绕过自动移除挂钩），restore 防御校验应拒绝
	if err := ts.db.Model(&model.Issue{}).Where("id = ?", internal.ID).Update("state", model.IssueStateClosed).Error; err != nil {
		t.Fatalf("close issue: %v", err)
	}
	if _, err := ts.recService.Restore(internal.ID, ownerID); err != errs.ErrIssueNotRecommendable {
		t.Fatalf("expected ErrIssueNotRecommendable, got %v", err)
	}
	// 推荐行仍保持延后态
	stored, err := ts.recRepo.Get(internal.ID)
	if err != nil {
		t.Fatalf("get rec: %v", err)
	}
	if stored.DeferredAt == nil {
		t.Fatalf("expected rec still deferred after failed restore")
	}
}

func TestIssueRecommendation_ListIncludesDeferred(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	lowIssue := createRecTestIssue(t, ts, issue.ProjectID)
	deferredIssue := createRecTestIssue(t, ts, issue.ProjectID)

	seed := func(issueID string, req UpsertIssueRecommendationRequest) {
		t.Helper()
		if _, err := ts.recService.Upsert(issueID, ownerID, "k", req); err != nil {
			t.Fatalf("seed upsert: %v", err)
		}
	}
	seed(deferredIssue.ID, UpsertIssueRecommendationRequest{Reason: "deferred-high", Priority: api.Ptr(model.IssueRecommendationPriorityHigh)})
	seed(lowIssue.ID, UpsertIssueRecommendationRequest{Reason: "low", Priority: api.Ptr(model.IssueRecommendationPriorityLow)})
	seed(issue.ID, UpsertIssueRecommendationRequest{Reason: "mid", Priority: api.Ptr(model.IssueRecommendationPriorityMedium)})
	if _, err := ts.recService.Defer(deferredIssue.ID, ownerID, "下周再看"); err != nil {
		t.Fatalf("defer: %v", err)
	}

	all, err := ts.recService.List(ownerID, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(all.Items))
	}
	// active 在前：mid > low；延后项排最后（尽管 priority=high）
	if all.Items[0].Issue.Id != issue.ID || all.Items[1].Issue.Id != lowIssue.ID || all.Items[2].Issue.Id != deferredIssue.ID {
		t.Fatalf("unexpected ordering: %+v", all.Items)
	}
	deferredItem := all.Items[2]
	if deferredItem.Status != api.Deferred || deferredItem.DeferredAt == nil || deferredItem.DeferNote == nil || *deferredItem.DeferNote != "下周再看" {
		t.Fatalf("unexpected deferred item: %+v", deferredItem)
	}
	if all.Items[0].Status != api.Active || all.Items[0].DeferredAt != nil || all.Items[0].DeferNote != nil {
		t.Fatalf("unexpected active item fields: %+v", all.Items[0])
	}
}

// 排序细则：active 组内同优先级按 updated_at 降序，deferred 组内同优先级按 deferred_at 降序，
// deferred 无论优先级多高都排在全部 active 之后。直接回写时间戳保证顺序可观测、不依赖时序。
func TestIssueRecommendation_ListDeferredOrdering(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	aOld := createRecTestIssue(t, ts, issue.ProjectID)
	aNew := createRecTestIssue(t, ts, issue.ProjectID)
	dOld := createRecTestIssue(t, ts, issue.ProjectID)
	dNew := createRecTestIssue(t, ts, issue.ProjectID)

	seed := func(issueID string, req UpsertIssueRecommendationRequest) {
		t.Helper()
		if _, err := ts.recService.Upsert(issueID, ownerID, "k", req); err != nil {
			t.Fatalf("seed upsert: %v", err)
		}
	}
	high := api.Ptr(model.IssueRecommendationPriorityHigh)
	medium := api.Ptr(model.IssueRecommendationPriorityMedium)
	seed(issue.ID, UpsertIssueRecommendationRequest{Reason: "a-high", Priority: high})
	seed(aOld.ID, UpsertIssueRecommendationRequest{Reason: "a-mid-old", Priority: medium})
	seed(aNew.ID, UpsertIssueRecommendationRequest{Reason: "a-mid-new", Priority: medium})
	seed(dOld.ID, UpsertIssueRecommendationRequest{Reason: "d-old", Priority: high})
	seed(dNew.ID, UpsertIssueRecommendationRequest{Reason: "d-new", Priority: high})
	if _, err := ts.recService.Defer(dOld.ID, ownerID, ""); err != nil {
		t.Fatalf("defer old: %v", err)
	}
	if _, err := ts.recService.Defer(dNew.ID, ownerID, ""); err != nil {
		t.Fatalf("defer new: %v", err)
	}

	now := time.Now().UTC()
	// UpdateColumn 跳过 updated_at 自动回写，保证排序时间戳确定
	setTS := func(issueID, col string, v time.Time) {
		t.Helper()
		if err := ts.db.Model(&model.IssueRecommendation{}).Where("issue_id = ?", issueID).UpdateColumn(col, v).Error; err != nil {
			t.Fatalf("set %s: %v", col, err)
		}
	}
	setTS(issue.ID, "updated_at", now.Add(-2*time.Hour))
	setTS(aOld.ID, "updated_at", now.Add(-3*time.Hour))
	setTS(aNew.ID, "updated_at", now.Add(-time.Hour))
	setTS(dOld.ID, "deferred_at", now.Add(-2*time.Hour))
	setTS(dNew.ID, "deferred_at", now.Add(-30*time.Minute))

	all, err := ts.recService.List(ownerID, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{issue.ID, aNew.ID, aOld.ID, dNew.ID, dOld.ID}
	if len(all.Items) != len(want) {
		t.Fatalf("expected %d items, got %d", len(want), len(all.Items))
	}
	for i, id := range want {
		if all.Items[i].Issue.Id != id {
			t.Fatalf("unexpected order at %d: want %s got %s (full: %+v)", i, id, all.Items[i].Issue.Id, all.Items)
		}
	}
	if all.Items[3].Status != api.Deferred || all.Items[4].Status != api.Deferred {
		t.Fatalf("expected tail items deferred")
	}
}

// 自动移除挂钩对延后行照常硬删：workflow_status 前移与 state=closed 两条路径都不区分 active/deferred。
func TestIssueRecommendation_DeferredRemovedByHooks(t *testing.T) {
	ts, issue, ownerID := setupRecommendationIssue(t)
	if _, err := ts.recService.Upsert(issue.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := ts.recService.Defer(issue.ID, ownerID, ""); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if _, err := ts.issueService.UpdateInternalMeta(issue.ID, ownerID, model.IssueWorkflowStatusInProgress, "tester"); err != nil {
		t.Fatalf("update meta: %v", err)
	}
	if _, err := ts.recRepo.Get(issue.ID); err == nil {
		t.Fatalf("expected deferred recommendation removed after in_progress")
	}

	internal := createInternalIssue(t, ts, issue.ProjectID, model.IssueStateOpen)
	if _, err := ts.recService.Upsert(internal.ID, ownerID, "k", UpsertIssueRecommendationRequest{Reason: "ok"}); err != nil {
		t.Fatalf("upsert internal: %v", err)
	}
	if _, err := ts.recService.Defer(internal.ID, ownerID, ""); err != nil {
		t.Fatalf("defer internal: %v", err)
	}
	closed := model.IssueStateClosed
	if _, err := ts.issueService.UpdateInternalIssue(internal.ID, ownerID, UpdateInternalIssueRequest{State: &closed}); err != nil {
		t.Fatalf("close internal: %v", err)
	}
	if _, err := ts.recRepo.Get(internal.ID); err == nil {
		t.Fatalf("expected deferred recommendation removed after close")
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
