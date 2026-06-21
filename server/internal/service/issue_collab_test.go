package service

import (
	"testing"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

func setupCollabIssue(t *testing.T) (*testServices, *model.Issue, string) {
	t.Helper()
	ts := setupTestServices(t)
	ownerID := uuid.NewString()
	createTestUser(t, ts.db, ownerID)
	project := createTestProject(t, ts.db, ownerID)
	issue := createTestIssue(t, ts.db, project.ID)
	return ts, issue, ownerID
}

func TestIssueCollab_GetAreaEmpty(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	area, err := ts.collabService.GetArea(issue.ID, ownerID)
	if err != nil {
		t.Fatalf("get area: %v", err)
	}
	if len(area.Suggestions) != 0 || area.Plan != nil || area.Review != nil || area.Summary != nil {
		t.Fatalf("expected empty area, got %+v", area)
	}
}

func TestIssueCollab_ReplaceSuggestions(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	first, err := ts.collabService.ReplaceSuggestions(issue.ID, ownerID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{
		Items: []IssueCollabSuggestionInput{
			{Body: "  建议一  "},
			{Body: "建议二"},
		},
	})
	if err != nil {
		t.Fatalf("replace suggestions: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("expected 2 suggestions, got %d", len(first))
	}
	if first[0].Body != "建议一" || first[0].SortOrder != 0 || first[1].SortOrder != 1 {
		t.Fatalf("unexpected suggestions: %+v", first)
	}
	if first[0].Author.Kind != string(model.CollabAuthorAgent) || first[0].Author.Login != collabAgentLogin {
		t.Fatalf("expected agent actor, got %+v", first[0].Author)
	}

	// 全量替换：旧两条被清掉，换成三条
	second, err := ts.collabService.ReplaceSuggestions(issue.ID, ownerID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{
		Items: []IssueCollabSuggestionInput{{Body: "新建议一"}, {Body: "新建议二"}, {Body: "新建议三"}},
	})
	if err != nil {
		t.Fatalf("replace again: %v", err)
	}
	if len(second) != 3 || second[0].Body != "新建议一" {
		t.Fatalf("unexpected replaced suggestions: %+v", second)
	}

	// 清空：空数组允许（返回 []）
	emptied, err := ts.collabService.ReplaceSuggestions(issue.ID, ownerID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{
		Items: []IssueCollabSuggestionInput{},
	})
	if err != nil {
		t.Fatalf("clear suggestions: %v", err)
	}
	if len(emptied) != 0 {
		t.Fatalf("expected empty slice after clear, got %d", len(emptied))
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if len(area.Suggestions) != 0 {
		t.Fatalf("expected 0 suggestions in area, got %d", len(area.Suggestions))
	}
}

func TestIssueCollab_UpsertPlanAndReview(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	plan1, err := ts.collabService.UpsertPlan(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabPlanRequest{Body: "  初版计划  "})
	if err != nil {
		t.Fatalf("upsert plan: %v", err)
	}
	if plan1.Body != "初版计划" || plan1.Author.Kind != string(model.CollabAuthorAgent) {
		t.Fatalf("unexpected plan: %+v", plan1)
	}

	// 覆盖更新：CreatedAt 保持不变
	plan2, err := ts.collabService.UpsertPlan(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabPlanRequest{Body: "更新后的计划"})
	if err != nil {
		t.Fatalf("upsert plan again: %v", err)
	}
	if plan2.Body != "更新后的计划" {
		t.Fatalf("expected updated body, got %q", plan2.Body)
	}
	if plan2.CreatedAt != plan1.CreatedAt {
		t.Fatalf("expected created_at preserved, got %s vs %s", plan2.CreatedAt, plan1.CreatedAt)
	}

	// Review 同构
	review1, err := ts.collabService.UpsertReview(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabReviewRequest{Body: "审查通过"})
	if err != nil {
		t.Fatalf("upsert review: %v", err)
	}
	review2, err := ts.collabService.UpsertReview(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabReviewRequest{Body: "审查更新"})
	if err != nil {
		t.Fatalf("upsert review again: %v", err)
	}
	if review2.Body != "审查更新" || review2.CreatedAt != review1.CreatedAt {
		t.Fatalf("unexpected review upsert: %+v", review2)
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if area.Plan == nil || area.Plan.Body != "更新后的计划" {
		t.Fatalf("unexpected area plan: %+v", area.Plan)
	}
	if area.Review == nil || area.Review.Body != "审查更新" {
		t.Fatalf("unexpected area review: %+v", area.Review)
	}
}

func TestIssueCollab_SummaryUpsert(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	s1, err := ts.collabService.UpsertSummary(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabSummaryRequest{
		Body:      "已完成",
		CommitIDs: []string{"abc1234", "0123456789abcdef0123456789abcdef01234567"},
	})
	if err != nil {
		t.Fatalf("upsert summary: %v", err)
	}
	if len(s1.CommitIDs) != 2 {
		t.Fatalf("expected 2 commit ids, got %d", len(s1.CommitIDs))
	}

	s2, err := ts.collabService.UpsertSummary(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabSummaryRequest{
		Body:      "已更新",
		CommitIDs: []string{"abcdef1"},
	})
	if err != nil {
		t.Fatalf("upsert summary again: %v", err)
	}
	if s2.Body != "已更新" || len(s2.CommitIDs) != 1 || s2.CreatedAt != s1.CreatedAt {
		t.Fatalf("unexpected upsert result: %+v", s2)
	}

	area, _ := ts.collabService.GetArea(issue.ID, ownerID)
	if area.Summary == nil || area.Summary.Body != "已更新" {
		t.Fatalf("unexpected area summary: %+v", area.Summary)
	}
}

func TestIssueCollab_Validation(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)

	// items == nil（如 "items":null）拒绝，防误清空
	if _, err := ts.collabService.ReplaceSuggestions(issue.ID, ownerID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{Items: nil}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for nil items, got %v", err)
	}

	// 空 body 拒绝
	if _, err := ts.collabService.ReplaceSuggestions(issue.ID, ownerID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{
		Items: []IssueCollabSuggestionInput{{Body: "   "}},
	}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty body, got %v", err)
	}

	// 超限条数
	tooMany := make([]IssueCollabSuggestionInput, collabMaxSuggestions+1)
	for i := range tooMany {
		tooMany[i] = IssueCollabSuggestionInput{Body: "x"}
	}
	if _, err := ts.collabService.ReplaceSuggestions(issue.ID, ownerID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{Items: tooMany}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for too many suggestions, got %v", err)
	}

	// plan 空 body
	if _, err := ts.collabService.UpsertPlan(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabPlanRequest{Body: "  "}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for empty plan body, got %v", err)
	}

	// summary bad commit id
	if _, err := ts.collabService.UpsertSummary(issue.ID, ownerID, model.CollabAuthorAgent, UpsertIssueCollabSummaryRequest{
		Body: "s", CommitIDs: []string{"not-a-sha"},
	}); err != errs.ErrInvalidParams {
		t.Fatalf("expected ErrInvalidParams for bad commit id, got %v", err)
	}
}

func TestIssueCollab_AccessControl(t *testing.T) {
	ts, issue, ownerID := setupCollabIssue(t)
	otherID := uuid.NewString()
	createTestUser(t, ts.db, otherID)

	// 非所有者无权访问
	if _, err := ts.collabService.GetArea(issue.ID, otherID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner, got %v", err)
	}

	// issue 不存在
	if _, err := ts.collabService.GetArea(uuid.NewString(), ownerID); err != errs.ErrIssueNotFound {
		t.Fatalf("expected ErrIssueNotFound for missing issue, got %v", err)
	}

	// 非所有者写
	if _, err := ts.collabService.ReplaceSuggestions(issue.ID, otherID, model.CollabAuthorAgent, ReplaceIssueCollabSuggestionsRequest{
		Items: []IssueCollabSuggestionInput{{Body: "x"}},
	}); err != errs.ErrProjectNotFound {
		t.Fatalf("expected ErrProjectNotFound for non-owner write, got %v", err)
	}
}
