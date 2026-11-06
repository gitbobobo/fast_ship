package repository

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// INT-212：ListFilteredWithTotal 的等价与边界测试——窗口合并形态的 total 与
// 本页必须与「CountFiltered + ListFiltered」逐条一致，投影、Preload、
// EXPLAIN 计划与分页契约（超末页空切片、total 不变）不回归。

// setupIssueListWithTotalTestDB 建启用 FTS 的内存库并灌入带区分度的夹具：
// 正文里埋 webhook/ab 命中词、部分挂 GitHub meta（带 number）、中英文混排。
func setupIssueListWithTotalTestDB(t *testing.T) (*IssueRepository, string) {
	t.Helper()
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure issue fts: %v", err)
	}
	now := time.Now().UTC()
	for i := 1; i <= 40; i++ {
		issue := model.Issue{
			ID:             fmt.Sprintf("total-%04d", i),
			ProjectID:      projectID,
			SequenceNumber: i,
			State:          model.IssueStateOpen,
			Title:          fmt.Sprintf("webhook issue #%d", i),
			Body:           "修复 webhook 重试后 release checklist 未更新，ab 测试超时。",
			AuthorLogin:    "alice",
			CreatedAt:      now,
			UpdatedAt:      now.Add(time.Duration(i) * time.Minute),
		}
		if i%3 == 0 {
			issue.Source = model.IssueSourceGitHub
			issue.AuthorLogin = "bob"
		} else {
			issue.Source = model.IssueSourceInternal
		}
		if err := db.Create(&issue).Error; err != nil {
			t.Fatalf("seed issue: %v", err)
		}
		if i%3 == 0 {
			meta := model.IssueGitHubMeta{
				IssueID:       issue.ID,
				ProjectID:     projectID,
				GitHubIssueID: int64(9000 + i),
				Number:        i,
				SyncedAt:      now,
			}
			if err := db.Create(&meta).Error; err != nil {
				t.Fatalf("seed meta: %v", err)
			}
		}
	}
	return &IssueRepository{db: db}, projectID
}

// TestIssueListWithTotal_Equivalence 覆盖 ≥3 rune MATCH、1–2 rune LIKE、纯数字、
// 作者名、零命中与无 q 六类查询：total 与本页 id 序列必须与两次查询完全一致，
// 投影不含正文。
func TestIssueListWithTotal_Equivalence(t *testing.T) {
	repo, projectID := setupIssueListWithTotalTestDB(t)

	for _, query := range []string{"webhook", "重试", "ab", "3", "alice", "zzznomatch", ""} {
		lq := IssueListQuery{ProjectID: projectID, Query: query}
		wantTotal, err := repo.CountFiltered(lq)
		if err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		wantPage, err := repo.ListFiltered(lq, "updated_desc", 0, 10)
		if err != nil {
			t.Fatalf("list %q: %v", query, err)
		}
		gotPage, gotTotal, err := repo.ListFilteredWithTotal(lq, "updated_desc", 0, 10)
		if err != nil {
			t.Fatalf("with total %q: %v", query, err)
		}
		if gotTotal != wantTotal {
			t.Errorf("q=%q total 不一致：want %d got %d", query, wantTotal, gotTotal)
		}
		if len(gotPage) != len(wantPage) {
			t.Fatalf("q=%q 页长不一致：want %d got %d", query, len(wantPage), len(gotPage))
		}
		for i := range wantPage {
			if gotPage[i].ID != wantPage[i].ID {
				t.Errorf("q=%q page[%d] 不一致：want %s got %s", query, i, wantPage[i].ID, gotPage[i].ID)
				break
			}
			if gotPage[i].Body != "" || gotPage[i].BodyHTML != "" {
				t.Errorf("q=%q page[%d] 携带正文（%q）", query, i, gotPage[i].Body)
			}
		}
	}
}

// TestIssueListWithTotal_PageBeyondEnd 翻页超过末页时返回空切片且 total 不变：
// 窗口行集为空拿不到聚合值，offset>0 必须回退 COUNT 补上 total。
func TestIssueListWithTotal_PageBeyondEnd(t *testing.T) {
	repo, projectID := setupIssueListWithTotalTestDB(t)
	lq := IssueListQuery{ProjectID: projectID, Query: "webhook"}

	page, total, err := repo.ListFilteredWithTotal(lq, "updated_desc", 10000, 10)
	if err != nil {
		t.Fatalf("with total: %v", err)
	}
	if len(page) != 0 {
		t.Fatalf("超末页应返回空切片，got %d 行", len(page))
	}
	wantTotal, err := repo.CountFiltered(lq)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != wantTotal || total == 0 {
		t.Fatalf("超末页 total 不变：want %d got %d", wantTotal, total)
	}
}

// TestIssueListWithTotal_PreloadsGitHubMeta 合并路径的本页 GitHub meta 预载
// 与 ListFiltered 同形状：带 meta 的行装上 number，不带的为空。
func TestIssueListWithTotal_PreloadsGitHubMeta(t *testing.T) {
	repo, projectID := setupIssueListWithTotalTestDB(t)
	lq := IssueListQuery{ProjectID: projectID, Query: "webhook"}

	page, _, err := repo.ListFilteredWithTotal(lq, "updated_desc", 0, 40)
	if err != nil {
		t.Fatalf("with total: %v", err)
	}
	loaded, empty := 0, 0
	for _, issue := range page {
		if issue.Source == model.IssueSourceGitHub {
			if issue.GitHubMeta == nil || issue.GitHubMeta.Number == 0 {
				t.Fatalf("issue %s 来源为 github 但 meta 未预载（%+v）", issue.ID, issue.GitHubMeta)
			}
			loaded++
			continue
		}
		if issue.GitHubMeta != nil {
			t.Fatalf("issue %s 无 meta 却装上 %+v", issue.ID, issue.GitHubMeta)
		}
		empty++
	}
	if loaded == 0 || empty == 0 {
		t.Fatalf("夹具应同时含两类行，loaded=%d empty=%d", loaded, empty)
	}
}

// explainIssueSearchWithTotal 用 DryRun 会话构建与 ListFilteredWithTotal 窗口
// 路径完全相同的 SQL（不执行），再对它跑 EXPLAIN QUERY PLAN，返回逐行 detail。
func explainIssueSearchWithTotal(t *testing.T, repo *IssueRepository, projectID, query string) []string {
	t.Helper()
	dryRepo := &IssueRepository{db: repo.db.Session(&gorm.Session{DryRun: true})}
	q := dryRepo.buildFilteredQuery(IssueListQuery{ProjectID: projectID, Query: query}).
		Select(issueListColumns + ", COUNT(*) OVER() AS match_total").
		Order(issueListOrderClause("updated_desc")).
		Offset(0).Limit(20)
	var rows []model.Issue
	q.Find(&rows) // DryRun：只构建 SQL，不执行
	if q.Statement.SQL.String() == "" {
		t.Fatalf("dry run did not build SQL")
	}

	type eqpRow struct {
		ID     int
		Parent int
		Detail string
	}
	var plan []eqpRow
	if err := repo.db.Raw("EXPLAIN QUERY PLAN "+q.Statement.SQL.String(), q.Statement.Vars...).Scan(&plan).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}
	details := make([]string, 0, len(plan))
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	return details
}

// TestIssueListWithTotal_ExplainPlan 锁执行计划：≥3 rune 查询的合并 SQL 仍走
// issues_fts MATCH + issue_fts_docs 映射，1–2 rune 不得触碰 FTS。
func TestIssueListWithTotal_ExplainPlan(t *testing.T) {
	repo, projectID := setupIssueListWithTotalTestDB(t)

	details := explainIssueSearchWithTotal(t, repo, projectID, "webhook")
	ftsHit, docsSeen := false, false
	for _, d := range details {
		t.Logf("plan[webhook]: %s", d)
		if strings.Contains(d, "issues_fts") && strings.Contains(d, "VIRTUAL TABLE INDEX") {
			ftsHit = true
		}
		if strings.Contains(d, "issue_fts_docs") {
			docsSeen = true
		}
	}
	if !ftsHit || !docsSeen {
		t.Fatalf("合并 SQL 必须仍走 issues_fts MATCH + issue_fts_docs 映射，实际计划：\n%s", strings.Join(details, "\n"))
	}

	details = explainIssueSearchWithTotal(t, repo, projectID, "ab")
	for _, d := range details {
		if strings.Contains(d, "issues_fts") {
			t.Fatalf("1–2 rune 查询不得触碰 issues_fts，实际计划：\n%s", strings.Join(details, "\n"))
		}
	}
}
