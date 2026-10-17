package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// INT-106：列表查询下推 SQL 后的形状断言——分页带 LIMIT/OFFSET，
// issues 投影与 GitHub meta 预载都不含 body/body_html/raw_json。

// sqlRecorder 捕获 gorm 实际执行的每条 SQL，供断言投影列与分页子句。
type sqlRecorder struct {
	queries []string
}

func (r *sqlRecorder) LogMode(logger.LogLevel) logger.Interface      { return r }
func (r *sqlRecorder) Info(context.Context, string, ...interface{})  {}
func (r *sqlRecorder) Warn(context.Context, string, ...interface{})  {}
func (r *sqlRecorder) Error(context.Context, string, ...interface{}) {}
func (r *sqlRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.queries = append(r.queries, sql)
}

func (r *sqlRecorder) reset() {
	r.queries = nil
}

func (r *sqlRecorder) matching(substr string) []string {
	var found []string
	for _, sql := range r.queries {
		if strings.Contains(sql, substr) {
			found = append(found, sql)
		}
	}
	return found
}

func setupIssueListSQLTestDB(t *testing.T) (*IssueRepository, *sqlRecorder, string) {
	t.Helper()

	recorder := &sqlRecorder{}
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: recorder})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueGitHubMeta{}, &model.IssueInternalMeta{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u-" + userID, Email: userID + "@x", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	projectID := uuid.NewString()
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p-" + projectID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	seedIssue := func(seq int, withMeta bool) {
		issue := &model.Issue{
			ID:             uuid.NewString(),
			ProjectID:      projectID,
			Source:         model.IssueSourceGitHub,
			SequenceNumber: seq,
			State:          model.IssueStateOpen,
			Title:          "issue title",
			Body:           "SECRET BODY",
			BodyHTML:       "<p>SECRET BODY</p>",
			AuthorLogin:    "alice",
			CreatedAt:      now.Add(time.Duration(seq) * time.Minute),
			UpdatedAt:      now.Add(time.Duration(seq) * time.Minute),
		}
		if err := db.Create(issue).Error; err != nil {
			t.Fatalf("seed issue: %v", err)
		}
		if withMeta {
			meta := &model.IssueGitHubMeta{
				IssueID:       issue.ID,
				ProjectID:     projectID,
				GitHubIssueID: int64(1000 + seq),
				Number:        seq,
				LabelsJSON:    `["bug"]`,
				RawJSON:       `{"SECRET":"RAW"}`,
				SyncedAt:      now,
			}
			if err := db.Create(meta).Error; err != nil {
				t.Fatalf("seed meta: %v", err)
			}
		}
	}
	seedIssue(1, true)
	seedIssue(2, true)
	seedIssue(3, false)

	recorder.reset()
	return NewIssueRepository(db), recorder, projectID
}

func TestIssueRepositoryListFiltered_SQLShape(t *testing.T) {
	repo, recorder, projectID := setupIssueListSQLTestDB(t)

	rows, err := repo.ListFiltered(IssueListQuery{ProjectID: projectID}, "updated_desc", 1, 10)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(rows) == 0 || len(rows) > 3 {
		t.Fatalf("unexpected rows: %d", len(rows))
	}

	mains := recorder.matching("FROM `issues`")
	if len(mains) != 1 {
		t.Fatalf("expected exactly one issues query, got %d: %v", len(mains), recorder.queries)
	}
	main := mains[0]
	if !strings.Contains(main, "LIMIT") || !strings.Contains(main, "OFFSET") {
		t.Fatalf("paged query must carry LIMIT/OFFSET, got %s", main)
	}
	if !strings.Contains(main, "issues.title") {
		t.Fatalf("expected explicit issue projection, got %s", main)
	}
	for _, banned := range []string{"body", "body_html", "raw_json"} {
		if strings.Contains(main, banned) {
			t.Fatalf("issues projection must not read %s, got %s", banned, main)
		}
	}

	preloads := recorder.matching("FROM `issue_github_meta`")
	if len(preloads) != 1 {
		t.Fatalf("expected exactly one github meta preload, got %d: %v", len(preloads), recorder.queries)
	}
	preload := preloads[0]
	if !strings.Contains(preload, "issue_id") {
		t.Fatalf("preload must select issue_id for association backfill, got %s", preload)
	}
	if strings.Contains(preload, "raw_json") {
		t.Fatalf("preload must not read raw_json, got %s", preload)
	}

	recorder.reset()
	total, err := repo.CountFiltered(IssueListQuery{ProjectID: projectID})
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected total 3, got %d", total)
	}
	for _, sql := range recorder.queries {
		if strings.Contains(sql, "raw_json") {
			t.Fatalf("count query must not read raw_json, got %s", sql)
		}
	}
}

func TestIssueRepositoryListFiltered_ListRowsExcludeBody(t *testing.T) {
	repo, _, projectID := setupIssueListSQLTestDB(t)

	rows, err := repo.ListFiltered(IssueListQuery{ProjectID: projectID}, "updated_desc", 0, 10)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	withMeta := 0
	for _, row := range rows {
		if row.Body != "" || row.BodyHTML != "" {
			t.Fatalf("list rows must not carry body, got %q / %q", row.Body, row.BodyHTML)
		}
		if row.Title == "" || row.SequenceNumber == 0 {
			t.Fatalf("list rows must keep non-body columns, got %+v", row)
		}
		if row.GitHubMeta == nil {
			continue
		}
		withMeta++
		if row.GitHubMeta.RawJSON != "" {
			t.Fatalf("preloaded meta must not carry raw_json, got %q", row.GitHubMeta.RawJSON)
		}
		if row.GitHubMeta.Number == 0 || row.GitHubMeta.LabelsJSON != `["bug"]` {
			t.Fatalf("preloaded meta must keep list fields, got %+v", row.GitHubMeta)
		}
	}
	if withMeta != 2 {
		t.Fatalf("expected 2 preloaded metas, got %d", withMeta)
	}
}

func TestIssueRepositoryListFiltered_PageBeyondEnd(t *testing.T) {
	repo, _, projectID := setupIssueListSQLTestDB(t)

	rows, err := repo.ListFiltered(IssueListQuery{ProjectID: projectID}, "updated_desc", 100, 10)
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty page beyond end, got %d rows", len(rows))
	}

	total, err := repo.CountFiltered(IssueListQuery{ProjectID: projectID})
	if err != nil {
		t.Fatalf("count filtered: %v", err)
	}
	if total != 3 {
		t.Fatalf("total must be unaffected by paging, got %d", total)
	}
}

// TestIssueRepositoryListFiltered_JSONValidGuard：label/assignee/milestone 条件必须
// 带 json_valid 防护——坏 JSON 直接喂给 json_each/json_extract 会抛 malformed JSON
// 让整条查询失败，这里锁住防护，防止后续改动把它删掉。
func TestIssueRepositoryListFiltered_JSONValidGuard(t *testing.T) {
	repo, recorder, projectID := setupIssueListSQLTestDB(t)

	for _, q := range []IssueListQuery{
		{ProjectID: projectID, Label: "bug"},
		{ProjectID: projectID, Assignee: "alice"},
		{ProjectID: projectID, Milestone: "sprint"},
	} {
		recorder.reset()
		if _, err := repo.CountFiltered(q); err != nil {
			t.Fatalf("count filtered %+v: %v", q, err)
		}
		mains := recorder.matching("FROM `issues`")
		if len(mains) != 1 {
			t.Fatalf("expected one issues query, got %d: %v", len(mains), recorder.queries)
		}
		if !strings.Contains(mains[0], "json_valid") {
			t.Fatalf("filter SQL must keep the json_valid guard, got %s", mains[0])
		}
	}
}

func TestIssueRepositoryListFilterOptionSources_Projection(t *testing.T) {
	repo, recorder, projectID := setupIssueListSQLTestDB(t)

	rows, err := repo.ListFilterOptionSources(projectID)
	if err != nil {
		t.Fatalf("list filter option sources: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	withMeta := 0
	for _, row := range rows {
		if row.LabelsJSON == `["bug"]` {
			withMeta++
		}
		if row.InternalLabelsJSON != "" {
			t.Fatalf("unexpected internal labels %q", row.InternalLabelsJSON)
		}
	}
	if withMeta != 2 {
		t.Fatalf("expected 2 rows with github labels, got %d", withMeta)
	}
	for _, sql := range recorder.queries {
		for _, banned := range []string{"body", "raw_json"} {
			if strings.Contains(sql, banned) {
				t.Fatalf("filter options query must not read %s, got %s", banned, sql)
			}
		}
	}
}
