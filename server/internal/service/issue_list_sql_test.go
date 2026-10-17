package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// INT-106：列表过滤、排序、分页下推 SQL 后的行为一致性矩阵。
// 全部期望值硬编码（不用旧内存实现当 oracle）；时间与排序决胜键刻意
// 构造并列，覆盖 SQL ORDER BY 与旧 sortIssues 的等价性。

type listFixtureGHMeta struct {
	number        int
	labels        string
	assignees     string
	milestone     string
	commentsCount int
}

type listFixtureIssue struct {
	seq      int
	source   model.IssueSource
	state    model.IssueState
	title    string
	body     string
	bodyHTML string
	author   string
	updated  time.Time
	created  time.Time
	gh       *listFixtureGHMeta         // nil = 不建 GitHub meta 行
	workflow *model.IssueWorkflowStatus // nil = 不建 internal meta 行（unset 的一种）
	imLabels string
}

func listFixtureStatus(s model.IssueWorkflowStatus) *model.IssueWorkflowStatus { return &s }

func listFixtureTime(hour int) time.Time {
	return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(hour) * time.Hour)
}

func seedListFixtureIssues(t *testing.T, ts *testServices, projectID string, seeds []listFixtureIssue) {
	t.Helper()
	for _, seed := range seeds {
		issue := &model.Issue{
			ID:             uuid.NewString(),
			ProjectID:      projectID,
			Source:         seed.source,
			SequenceNumber: seed.seq,
			State:          seed.state,
			Title:          seed.title,
			Body:           seed.body,
			BodyHTML:       seed.bodyHTML,
			AuthorLogin:    seed.author,
			CreatedAt:      seed.created,
			UpdatedAt:      seed.updated,
		}
		if err := ts.db.Create(issue).Error; err != nil {
			t.Fatalf("seed issue %d: %v", seed.seq, err)
		}
		if seed.gh != nil {
			meta := &model.IssueGitHubMeta{
				IssueID:       issue.ID,
				ProjectID:     projectID,
				GitHubIssueID: int64(100000 + seed.seq),
				GitHubNodeID:  "I_kw_fixture",
				Number:        seed.gh.number,
				LabelsJSON:    seed.gh.labels,
				AssigneesJSON: seed.gh.assignees,
				MilestoneJSON: seed.gh.milestone,
				CommentsCount: seed.gh.commentsCount,
				SyncedAt:      seed.updated,
			}
			if err := ts.db.Create(meta).Error; err != nil {
				t.Fatalf("seed github meta %d: %v", seed.seq, err)
			}
		}
		if seed.workflow != nil {
			meta := &model.IssueInternalMeta{
				IssueID:         issue.ID,
				WorkflowStatus:  *seed.workflow,
				LabelsJSON:      seed.imLabels,
				UpdatedByUserID: "fixture",
				CreatedAt:       seed.created,
				UpdatedAt:       seed.updated,
			}
			if err := ts.db.Create(meta).Error; err != nil {
				t.Fatalf("seed internal meta %d: %v", seed.seq, err)
			}
		}
	}
}

// buildListFixtureSeeds 生成覆盖全部筛选维度与排序并列的 25 条 issue。
// 时间单位是「偏移小时」，updated/created 按 seq 交错，制造多种决胜场景：
// seq7/8 同 updated、seq13/14 同 created+updated、seq15/16 同 created+updated。
func buildListFixtureSeeds() []listFixtureIssue {
	seeds := []listFixtureIssue{
		{seq: 1, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "Fix login crash", body: "steps to reproduce the login bug", author: "alice",
			updated: listFixtureTime(1), created: listFixtureTime(10),
			gh: &listFixtureGHMeta{number: 101, labels: `["bug","P1"]`, assignees: `[{"login":"alice"}]`, milestone: `{"title":"Sprint 1"}`, commentsCount: 3}},
		{seq: 2, source: model.IssueSourceGitHub, state: model.IssueStateClosed,
			title: "Update docs", body: "rewrite readme section", author: "bob",
			updated: listFixtureTime(2), created: listFixtureTime(9),
			gh: &listFixtureGHMeta{number: 102, labels: `["docs"]`, assignees: `[]`, milestone: `{"title":"Sprint 2"}`}},
		{seq: 3, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "Add export button", body: "exports to csv with % percent chars", author: "carol",
			updated: listFixtureTime(3), created: listFixtureTime(8),
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo), imLabels: `["backend"]`},
		{seq: 4, source: model.IssueSourceInternal, state: model.IssueStateClosed,
			title: "Polish dashboard", body: "spacing and shadows", author: "dave",
			updated: listFixtureTime(4), created: listFixtureTime(7),
			workflow: listFixtureStatus(model.IssueWorkflowStatusInProgress), imLabels: `[]`},
		{seq: 5, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "Race in worker", body: "data race on shutdown", author: "erin",
			updated: listFixtureTime(5), created: listFixtureTime(6),
			gh: &listFixtureGHMeta{number: 103, labels: `["bug"]`, assignees: `[{"login":"Frank"}]`, milestone: `{"title":"sprint 1"}`, commentsCount: 1}},
		{seq: 6, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "Spelling of percent marks", body: "literal percent and _ underscore", author: "alice",
			updated: listFixtureTime(6), created: listFixtureTime(5),
			workflow: listFixtureStatus(""), imLabels: `["docs","P1"]`},
		{seq: 7, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "Tie sort GH", author: "gina",
			updated: listFixtureTime(7), created: listFixtureTime(4),
			gh: &listFixtureGHMeta{number: 104, labels: `[]`, assignees: `[]`, commentsCount: 1}},
		{seq: 8, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "Tie sort IM", author: "hank",
			updated: listFixtureTime(7), created: listFixtureTime(4),
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
		{seq: 9, source: model.IssueSourceGitHub, state: model.IssueStateClosed,
			title: "Sprint two item", author: "ivy",
			updated: listFixtureTime(8), created: listFixtureTime(3),
			gh: &listFixtureGHMeta{number: 105, labels: `["P1"]`, assignees: `[{"login":"jack"}]`, milestone: `{"title":"Sprint 2"}`}},
		{seq: 10, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "Comma quoted labels", author: "kate",
			updated: listFixtureTime(9), created: listFixtureTime(2),
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo), imLabels: `["a,b","say \"hi\""]`},
		{seq: 11, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "Search target", body: "needle in the haystack", author: "lem",
			updated: listFixtureTime(10), created: listFixtureTime(1),
			gh: &listFixtureGHMeta{number: 106, labels: `[]`, assignees: `[]`, commentsCount: 5}},
		{seq: 12, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "Comments tie", author: "mallory",
			updated: listFixtureTime(11), created: listFixtureTime(1),
			gh:       &listFixtureGHMeta{number: 107, labels: `[]`, assignees: `[]`, commentsCount: 5},
			workflow: listFixtureStatus(model.IssueWorkflowStatusInProgress)},
		{seq: 13, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "Created tie A", author: "nina",
			updated: listFixtureTime(12), created: listFixtureTime(13),
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo)},
		{seq: 14, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "Created tie B", author: "olga",
			updated: listFixtureTime(12), created: listFixtureTime(13),
			gh: &listFixtureGHMeta{number: 108, labels: `[]`, assignees: `[]`, commentsCount: 2}},
		{seq: 15, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "Updated tie A", author: "pete",
			updated: listFixtureTime(13), created: listFixtureTime(12),
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
		{seq: 16, source: model.IssueSourceGitHub, state: model.IssueStateClosed,
			title: "Updated tie B", author: "quinn",
			updated: listFixtureTime(13), created: listFixtureTime(12),
			gh: &listFixtureGHMeta{number: 109, labels: `[]`, assignees: `[]`, commentsCount: 2}},
		{seq: 17, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "number collision", author: "rita",
			updated: listFixtureTime(16), created: listFixtureTime(16),
			gh: &listFixtureGHMeta{number: 200, labels: `[]`, assignees: `[]`}},
		// 无 internal meta 行的 unset 形态；纯数字 q 命中 sequence_number。
		{seq: 18, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "seq only match", author: "stan",
			updated: listFixtureTime(14), created: listFixtureTime(14)},
		{seq: 19, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "label wildcard chars", author: "uma",
			updated: listFixtureTime(15), created: listFixtureTime(15),
			gh: &listFixtureGHMeta{number: 201, labels: `["has%percent","under_score"]`, assignees: `[]`}},
	}
	for i := 20; i <= 25; i++ {
		seeds = append(seeds, listFixtureIssue{
			seq: i, source: model.IssueSourceGitHub, state: model.IssueStateClosed,
			title: fmt.Sprintf("Filler %d", i), author: "fred",
			updated: listFixtureTime(i), created: listFixtureTime(i),
			gh: &listFixtureGHMeta{number: 200 + i, labels: `[]`, assignees: `[]`},
		})
	}
	return seeds
}

func expectListSeqs(t *testing.T, items []IssueResponse, total int64, wantTotal int64, wantSeqs []int) {
	t.Helper()
	if total != wantTotal {
		t.Fatalf("expected total %d, got %d", wantTotal, total)
	}
	if len(items) != len(wantSeqs) {
		t.Fatalf("expected %d items, got %d", len(wantSeqs), len(items))
	}
	for i, want := range wantSeqs {
		if items[i].SequenceNumber != want {
			t.Fatalf("position %d: expected seq %d, got seq %d (%q)", i, want, items[i].SequenceNumber, items[i].Title)
		}
	}
}

func TestIssueServiceList_SQLPushdownConsistencyMatrix(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	seedListFixtureIssues(t, svc, project.ID, buildListFixtureSeeds())

	all := []int{25, 24, 23, 22, 21, 20, 17, 19, 18, 16, 15, 14, 13, 12, 11, 10, 9, 7, 8, 6, 5, 4, 3, 2, 1}

	cases := []struct {
		name      string
		filters   IssueListFilters
		page      int
		pageSize  int
		wantTotal int64
		wantSeqs  []int
	}{
		{"default sort is updated_desc", IssueListFilters{}, 1, 100, 25, all},
		{"unknown sort falls back to updated_desc", IssueListFilters{Sort: "bogus"}, 1, 100, 25, all},
		{"updated_asc", IssueListFilters{Sort: "updated_asc"}, 1, 100, 25,
			[]int{1, 2, 3, 4, 5, 6, 8, 7, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 17, 20, 21, 22, 23, 24, 25}},
		{"created_desc", IssueListFilters{Sort: "created_desc"}, 1, 100, 25,
			[]int{25, 24, 23, 22, 21, 20, 17, 19, 18, 14, 13, 16, 15, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 11}},
		{"created_asc", IssueListFilters{Sort: "created_asc"}, 1, 100, 25,
			[]int{11, 12, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 15, 16, 13, 14, 18, 19, 17, 20, 21, 22, 23, 24, 25}},
		{"comments_desc", IssueListFilters{Sort: "comments_desc"}, 1, 100, 25,
			[]int{12, 11, 1, 16, 14, 7, 5, 25, 24, 23, 22, 21, 20, 17, 19, 18, 15, 13, 10, 9, 8, 6, 4, 3, 2}},
		{"comments_asc", IssueListFilters{Sort: "comments_asc"}, 1, 100, 25,
			[]int{2, 3, 4, 6, 8, 9, 10, 13, 15, 18, 19, 17, 20, 21, 22, 23, 24, 25, 5, 7, 14, 16, 1, 11, 12}},
		{"state closed", IssueListFilters{State: "closed"}, 1, 100, 10,
			[]int{25, 24, 23, 22, 21, 20, 16, 9, 4, 2}},
		{"state open", IssueListFilters{State: "open"}, 1, 100, 15,
			[]int{17, 19, 18, 15, 14, 13, 12, 11, 10, 7, 8, 6, 5, 3, 1}},
		{"source github", IssueListFilters{Source: "github"}, 1, 100, 17,
			[]int{25, 24, 23, 22, 21, 20, 17, 19, 16, 14, 12, 11, 9, 7, 5, 2, 1}},
		{"source internal", IssueListFilters{Source: "internal"}, 1, 100, 8,
			[]int{18, 15, 13, 10, 8, 6, 4, 3}},
		{"workflow todo", IssueListFilters{Workflow: "todo"}, 1, 100, 3, []int{13, 10, 3}},
		{"workflow in_progress", IssueListFilters{Workflow: "in_progress"}, 1, 100, 2, []int{12, 4}},
		{"workflow done", IssueListFilters{Workflow: "done"}, 1, 100, 2, []int{15, 8}},
		// unset = 无 internal meta 行或 workflow_status 为空：全部无 meta 行的
		// GitHub issue + seq18（无行）+ seq6（空串状态）都在其中。
		{"workflow unset covers missing row and empty status", IssueListFilters{Workflow: "unset"}, 1, 100, 18,
			[]int{25, 24, 23, 22, 21, 20, 17, 19, 18, 16, 14, 11, 9, 7, 6, 5, 2, 1}},
		{"label github exact", IssueListFilters{Label: "bug"}, 1, 100, 2, []int{5, 1}},
		{"label case insensitive", IssueListFilters{Label: "BUG"}, 1, 100, 2, []int{5, 1}},
		{"label merges github and internal", IssueListFilters{Label: "p1"}, 1, 100, 3, []int{9, 6, 1}},
		{"label internal only", IssueListFilters{Label: "backend"}, 1, 100, 1, []int{3}},
		{"label with comma", IssueListFilters{Label: "a,b"}, 1, 100, 1, []int{10}},
		{"label with literal percent", IssueListFilters{Label: "has%percent"}, 1, 100, 1, []int{19}},
		{"label with literal underscore", IssueListFilters{Label: "under_score"}, 1, 100, 1, []int{19}},
		{"label with quotes", IssueListFilters{Label: `say "hi"`}, 1, 100, 1, []int{10}},
		{"label missing", IssueListFilters{Label: "missing-label"}, 1, 100, 0, []int{}},
		{"assignee exact", IssueListFilters{Assignee: "alice"}, 1, 100, 1, []int{1}},
		{"assignee case insensitive", IssueListFilters{Assignee: "frank"}, 1, 100, 1, []int{5}},
		{"assignee upper case filter", IssueListFilters{Assignee: "FRANK"}, 1, 100, 1, []int{5}},
		{"assignee other", IssueListFilters{Assignee: "jack"}, 1, 100, 1, []int{9}},
		{"assignee empty list does not match", IssueListFilters{Assignee: "bob"}, 1, 100, 0, []int{}},
		{"milestone case insensitive", IssueListFilters{Milestone: "Sprint 1"}, 1, 100, 2, []int{5, 1}},
		{"milestone other", IssueListFilters{Milestone: "sprint 2"}, 1, 100, 2, []int{9, 2}},
		{"milestone missing", IssueListFilters{Milestone: "missing"}, 1, 100, 0, []int{}},
		{"q title substring", IssueListFilters{Query: "login"}, 1, 100, 1, []int{1}},
		{"q body substring", IssueListFilters{Query: "needle"}, 1, 100, 1, []int{11}},
		{"q body case insensitive", IssueListFilters{Query: "CSV"}, 1, 100, 1, []int{3}},
		{"q author substring", IssueListFilters{Query: "carol"}, 1, 100, 1, []int{3}},
		{"q internal reference", IssueListFilters{Query: "INT-10"}, 1, 100, 1, []int{10}},
		{"q github reference", IssueListFilters{Query: "GH-101"}, 1, 100, 1, []int{1}},
		{"q github reference lowercase", IssueListFilters{Query: "gh-101"}, 1, 100, 1, []int{1}},
		{"q pure number hits github number", IssueListFilters{Query: "200"}, 1, 100, 1, []int{17}},
		{"q pure number hits sequence", IssueListFilters{Query: "18"}, 1, 100, 1, []int{18}},
		{"q literal percent", IssueListFilters{Query: "% percent"}, 1, 100, 1, []int{3}},
		{"q literal underscore", IssueListFilters{Query: "_ underscore"}, 1, 100, 1, []int{6}},
		{"q author prefix", IssueListFilters{Query: "Fred"}, 1, 100, 6, []int{25, 24, 23, 22, 21, 20}},
		{"q title substring across sources", IssueListFilters{Query: "Tie"}, 1, 100, 7, []int{16, 15, 14, 13, 12, 7, 8}},
		{"q no match", IssueListFilters{Query: "zzznotfound"}, 1, 100, 0, []int{}},
		{"combined state+workflow+source+label", IssueListFilters{State: "open", Workflow: "todo", Source: "internal", Label: "backend"}, 1, 100, 1, []int{3}},
		{"combined closed+github+label", IssueListFilters{State: "closed", Source: "github", Label: "docs"}, 1, 100, 1, []int{2}},
		{"combined unset+github", IssueListFilters{Workflow: "unset", Source: "github"}, 1, 100, 16,
			[]int{25, 24, 23, 22, 21, 20, 17, 19, 16, 14, 11, 9, 7, 5, 2, 1}},
		{"page two", IssueListFilters{}, 2, 20, 25, []int{5, 4, 3, 2, 1}},
		{"page beyond end keeps total", IssueListFilters{}, 3, 20, 25, []int{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, total, err := svc.issueService.List(project.ID, user.ID, tc.filters, tc.page, tc.pageSize)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			expectListSeqs(t, items, total, tc.wantTotal, tc.wantSeqs)
		})
	}
}

func TestIssueServiceCountIssues_MatchesListTotals(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	seedListFixtureIssues(t, svc, project.ID, buildListFixtureSeeds())

	cases := []struct {
		name    string
		filters IssueListFilters
		want    int64
	}{
		{"all", IssueListFilters{}, 25},
		{"open", IssueListFilters{State: "open"}, 15},
		{"closed github", IssueListFilters{State: "closed", Source: "github"}, 9},
		{"todo", IssueListFilters{Workflow: "todo"}, 3},
		{"unset", IssueListFilters{Workflow: "unset"}, 18},
		{"label", IssueListFilters{Label: "bug"}, 2},
		{"q", IssueListFilters{Query: "Tie"}, 7},
		{"no match", IssueListFilters{Query: "zzznotfound"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := svc.issueService.CountIssues(project.ID, user.ID, tc.filters)
			if err != nil {
				t.Fatalf("count issues: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected count %d, got %d", tc.want, got)
			}
		})
	}
}

func TestIssueServiceList_OmitsBodyButKeepsDetail(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{{
		seq: 1, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
		title: "Body carrier", body: "SECRET-BODY", bodyHTML: "<p>SECRET-BODY</p>", author: "alice",
		updated: listFixtureTime(1), created: listFixtureTime(2),
		gh: &listFixtureGHMeta{number: 101, labels: `["bug"]`, assignees: `[{"login":"alice"}]`, milestone: `{"title":"S1"}`, commentsCount: 2},
	}})

	items, total, err := svc.issueService.List(project.ID, user.ID, IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one item, got total=%d len=%d", total, len(items))
	}
	if items[0].Body != "" || items[0].BodyHtml != "" {
		t.Fatalf("list response must not carry body, got %q / %q", items[0].Body, items[0].BodyHtml)
	}
	if items[0].Github == nil {
		t.Fatalf("github meta must still be preloaded, got nil")
	}
	if items[0].Github.Number != 101 || items[0].Github.CommentsCount != 2 || len(items[0].Github.Labels) != 1 {
		t.Fatalf("github meta fields must survive the projection, got %+v", items[0].Github)
	}
	if items[0].Reference != "GH-101" {
		t.Fatalf("expected GH-101 reference, got %q", items[0].Reference)
	}
	if items[0].PullRequestSummary == nil {
		t.Fatalf("pull request summary must still be attached")
	}

	detail, err := svc.issueService.Get(items[0].Id, user.ID)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.Body != "SECRET-BODY" || detail.BodyHtml != "<p>SECRET-BODY</p>" {
		t.Fatalf("detail must keep full body, got %q / %q", detail.Body, detail.BodyHtml)
	}
}

func TestIssueServiceList_EmptyProject(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	items, total, err := svc.issueService.List(project.ID, user.ID, IssueListFilters{Query: "anything"}, 2, 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected empty result, got total=%d len=%d", total, len(items))
	}
}

// TestIssueServiceList_JSONColumnBadDataAndLegacyShapes 覆盖 JSON 列的坏数据容错与
// 标签/指派人旧对象形态兼容：坏 JSON 的行不得让查询失败、也不得参与匹配；对象数组
// 形态的标签（8b274f3 改存储格式前的存量数据）与混排数组的每个元素都要能按名称/
// 登录名匹配。期望值全部硬编码。注意 Label/Assignee/Milestone 为空串即「不过滤」
// （新旧一致的契约），空串筛选值不会到达 SQL，无 name 对象元素取到 NULL、与任何
// 非空筛选值都不相等，等价地由各「不匹配」case 覆盖。
func TestIssueServiceList_JSONColumnBadDataAndLegacyShapes(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "legacy object label", author: "alice",
			updated: listFixtureTime(1), created: listFixtureTime(1),
			gh: &listFixtureGHMeta{number: 101, labels: `[{"name":"Legacy","color":"ffffff"}]`, assignees: `[]`}},
		{seq: 2, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "legacy im object label", author: "bob",
			updated: listFixtureTime(2), created: listFixtureTime(2),
			gh:       &listFixtureGHMeta{number: 102, labels: `[]`, assignees: `[]`},
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo), imLabels: `[{"name":"LegacyIM"}]`},
		{seq: 3, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "mixed label array", author: "carol",
			updated: listFixtureTime(3), created: listFixtureTime(3),
			gh: &listFixtureGHMeta{number: 103, labels: `["plain",{"name":"Obj"}]`, assignees: `[]`}},
		{seq: 4, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "nameless object label", author: "dave",
			updated: listFixtureTime(4), created: listFixtureTime(4),
			gh: &listFixtureGHMeta{number: 104, labels: `[{"color":"fff"}]`, assignees: `[]`}},
		{seq: 5, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "bad gh labels json", author: "erin",
			updated: listFixtureTime(5), created: listFixtureTime(5),
			gh: &listFixtureGHMeta{number: 105, labels: `{oops`, assignees: `[]`}},
		{seq: 6, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "bad assignees json", author: "frank",
			updated: listFixtureTime(6), created: listFixtureTime(6),
			gh: &listFixtureGHMeta{number: 106, labels: `[]`, assignees: `not-json`}},
		{seq: 7, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "bad milestone json", author: "gina",
			updated: listFixtureTime(7), created: listFixtureTime(7),
			gh: &listFixtureGHMeta{number: 107, labels: `[]`, assignees: `[]`, milestone: `nope`}},
		{seq: 8, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "mixed assignee array", author: "hank",
			updated: listFixtureTime(8), created: listFixtureTime(8),
			gh: &listFixtureGHMeta{number: 108, labels: `[]`, assignees: `["alice",{"login":"Bob"}]`}},
		{seq: 9, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "bad im labels json", author: "ivy",
			updated: listFixtureTime(9), created: listFixtureTime(9),
			gh:       &listFixtureGHMeta{number: 109, labels: `[]`, assignees: `[]`},
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo), imLabels: `{oops`},
		{seq: 10, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "empty string json columns", author: "jack",
			updated: listFixtureTime(10), created: listFixtureTime(10),
			gh: &listFixtureGHMeta{number: 110, labels: ``, assignees: ``, milestone: ``}},
	})

	cases := []struct {
		name     string
		filters  IssueListFilters
		wantSeqs []int
	}{
		// 坏数据行只影响对应筛选，不影响无筛选查询。
		{"no filter keeps bad-data rows", IssueListFilters{}, []int{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}},
		// 标签对象形态（历史数据兼容）与混排数组。
		{"legacy object-form gh label matches case-insensitively", IssueListFilters{Label: "legacy"}, []int{1}},
		{"legacy object-form im label matches", IssueListFilters{Label: "legacyim"}, []int{2}},
		{"mixed label array string element", IssueListFilters{Label: "plain"}, []int{3}},
		{"mixed label array object element", IssueListFilters{Label: "obj"}, []int{3}},
		// 无 name 的对象元素与坏 JSON 行都不匹配任何筛选值（含各自的坏文本本身，
		// 筛选是全等匹配、不做子串）。
		{"nameless object element does not match its color", IssueListFilters{Label: "fff"}, []int{}},
		{"bad gh labels json does not match", IssueListFilters{Label: "oops"}, []int{}},
		// assignees：对象元素匹配、字符串元素不匹配、坏 JSON 不参与。
		{"mixed assignee array object element matches", IssueListFilters{Assignee: "bob"}, []int{8}},
		{"mixed assignee array string element does not match", IssueListFilters{Assignee: "alice"}, []int{}},
		{"bad assignees json does not match", IssueListFilters{Assignee: "not-json"}, []int{}},
		// milestone：坏 JSON 不匹配且不报错。
		{"bad milestone json does not match", IssueListFilters{Milestone: "nope"}, []int{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, total, err := svc.issueService.List(project.ID, user.ID, tc.filters, 1, 100)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			expectListSeqs(t, items, total, int64(len(tc.wantSeqs)), tc.wantSeqs)

			count, err := svc.issueService.CountIssues(project.ID, user.ID, tc.filters)
			if err != nil {
				t.Fatalf("count issues: %v", err)
			}
			if count != int64(len(tc.wantSeqs)) {
				t.Fatalf("expected count %d, got %d", len(tc.wantSeqs), count)
			}
		})
	}
}

func TestIssueServiceGetFilterOptions_MergesGitHubAndInternalSources(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "gh source", author: "alice",
			updated: listFixtureTime(1), created: listFixtureTime(1),
			gh: &listFixtureGHMeta{number: 101, labels: `["bug","P1"]`, assignees: `[{"login":"alice"}]`, milestone: `{"title":"Sprint 1"}`}},
		{seq: 2, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "internal source", author: "bob",
			updated: listFixtureTime(2), created: listFixtureTime(2),
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo), imLabels: `["backend","P1"]`},
		{seq: 3, source: model.IssueSourceGitHub, state: model.IssueStateOpen,
			title: "bare meta", author: "carol",
			updated: listFixtureTime(3), created: listFixtureTime(3),
			gh: &listFixtureGHMeta{number: 103, labels: `[]`, assignees: `[]`}},
	})

	opts, err := svc.issueService.GetFilterOptions(project.ID, user.ID)
	if err != nil {
		t.Fatalf("get filter options: %v", err)
	}
	wantLabels := []string{"P1", "backend", "bug"}
	if len(opts.Labels) != len(wantLabels) {
		t.Fatalf("expected labels %v, got %v", wantLabels, opts.Labels)
	}
	for i, want := range wantLabels {
		if opts.Labels[i] != want {
			t.Fatalf("expected labels %v, got %v", wantLabels, opts.Labels)
		}
	}
	if len(opts.Assignees) != 1 || opts.Assignees[0] != "alice" {
		t.Fatalf("expected assignees [alice], got %v", opts.Assignees)
	}
	if len(opts.Milestones) != 1 || opts.Milestones[0] != "Sprint 1" {
		t.Fatalf("expected milestones [Sprint 1], got %v", opts.Milestones)
	}
}

func TestIssueServiceGetFilterOptions_EmptyProject(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	opts, err := svc.issueService.GetFilterOptions(project.ID, user.ID)
	if err != nil {
		t.Fatalf("get filter options: %v", err)
	}
	if len(opts.Labels) != 0 || len(opts.Assignees) != 0 || len(opts.Milestones) != 0 {
		t.Fatalf("expected empty options, got %+v", opts)
	}
}

func TestIssueServiceBatchCloseDoneIssues_ClosesInternalDoneIssues(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)
	seedListFixtureIssues(t, svc, project.ID, []listFixtureIssue{
		{seq: 1, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "done one", author: "alice",
			updated: listFixtureTime(1), created: listFixtureTime(1),
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
		{seq: 2, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "done two", author: "alice",
			updated: listFixtureTime(2), created: listFixtureTime(2),
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
		{seq: 3, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: "still todo", author: "alice",
			updated: listFixtureTime(3), created: listFixtureTime(3),
			workflow: listFixtureStatus(model.IssueWorkflowStatusTodo)},
		{seq: 4, source: model.IssueSourceInternal, state: model.IssueStateClosed,
			title: "already closed", author: "alice",
			updated: listFixtureTime(4), created: listFixtureTime(4),
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone)},
	})

	resp, err := svc.issueService.BatchCloseDoneIssues(project.ID, user.ID, "")
	if err != nil {
		t.Fatalf("batch close: %v", err)
	}
	if resp.Total != 2 || resp.Succeeded != 2 || resp.Failed != 0 || len(resp.Failures) != 0 {
		t.Fatalf("unexpected batch close result: %+v", resp)
	}

	var doneOpen []model.Issue
	if err := svc.db.Where("project_id = ? AND state = ?", project.ID, model.IssueStateOpen).Find(&doneOpen).Error; err != nil {
		t.Fatalf("load issues: %v", err)
	}
	if len(doneOpen) != 1 || doneOpen[0].SequenceNumber != 3 {
		t.Fatalf("only the todo issue should stay open, got %+v", doneOpen)
	}

	var closed [2]model.Issue
	if err := svc.db.Where("project_id = ? AND sequence_number IN ?", project.ID, []int{1, 2}).Order("sequence_number").Find(&closed).Error; err != nil {
		t.Fatalf("load closed issues: %v", err)
	}
	for _, issue := range closed {
		if issue.State != model.IssueStateClosed || issue.StateReason != "completed" {
			t.Fatalf("expected closed/completed for seq %d, got %+v", issue.SequenceNumber, issue)
		}
	}
}

func TestIssueServiceBatchCloseDoneIssues_TooManyIssues(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, user.ID)

	// 201 条最小行的 done+open issue，命中 batchCloseDoneMaxIssues 上限分支。
	base := listFixtureTime(1)
	seeds := make([]listFixtureIssue, 0, 201)
	for i := 1; i <= 201; i++ {
		seeds = append(seeds, listFixtureIssue{
			seq: i, source: model.IssueSourceInternal, state: model.IssueStateOpen,
			title: fmt.Sprintf("done %d", i), author: "bulk",
			updated: base.Add(time.Duration(i) * time.Minute), created: base,
			workflow: listFixtureStatus(model.IssueWorkflowStatusDone),
		})
	}
	seedListFixtureIssues(t, svc, project.ID, seeds)

	_, err := svc.issueService.BatchCloseDoneIssues(project.ID, user.ID, "")
	if err == nil {
		t.Fatalf("expected ErrBatchCloseTooMany, got nil")
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrBatchCloseTooMany.Code {
		t.Fatalf("expected ErrBatchCloseTooMany, got %v", err)
	}

	var openCount int64
	if err := svc.db.Model(&model.Issue{}).Where("project_id = ? AND state = ?", project.ID, model.IssueStateOpen).Count(&openCount).Error; err != nil {
		t.Fatalf("count open issues: %v", err)
	}
	if openCount != 201 {
		t.Fatalf("nothing should be closed when over the cap, got %d open", openCount)
	}
}
