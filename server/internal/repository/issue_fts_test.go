package repository

// INT-209：issues_fts 派生索引的测试——命中与 Go 参考实现逐条等价、分支
// 形状（≥3 rune 走 MATCH / 1–2 rune 退 LIKE）、docid 映射一致性、旧形态
// 收敛、迁移回填幂等、增量同步、级联清理与大小写折叠边界。

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// setupIssueFTSTestDB 建内存库（外键开启）并迁移业务表，不执行
// EnsureIssueFTSSchema——回填/触发器时序由各测试自行控制。
func setupIssueFTSTestDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
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
	return db, projectID
}

// seedFTSIssue 按 (id 后缀, 序号) 建内部来源 issue，全部无 GitHub meta，
// reference 恒为 INT-<序号>，Go 参考实现因此无需感知 meta。
func seedFTSIssue(t *testing.T, db *gorm.DB, projectID, suffix string, seq int, title, body, author string) model.Issue {
	t.Helper()
	now := time.Now().UTC()
	issue := model.Issue{
		ID:             "fts-" + suffix,
		ProjectID:      projectID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: seq,
		State:          model.IssueStateOpen,
		Title:          title,
		Body:           body,
		AuthorLogin:    author,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatalf("seed issue %s: %v", issue.ID, err)
	}
	return issue
}

// issueSearchReference 是 q 搜索的 Go 参考实现（只用于测试，不进生产代码）：
// 对 title/body/author_login/reference 四路做
// strings.Contains(strings.ToLower(...))，与旧四路 LIKE 的子串语义对应。
func issueSearchReference(issue model.Issue, query string) bool {
	lq := strings.ToLower(query)
	ref := "INT-" + strconv.Itoa(issue.SequenceNumber)
	return strings.Contains(strings.ToLower(issue.Title), lq) ||
		strings.Contains(strings.ToLower(issue.Body), lq) ||
		strings.Contains(strings.ToLower(issue.AuthorLogin), lq) ||
		strings.Contains(strings.ToLower(ref), lq)
}

// TestIssueFTS_SearchEquivalence 固定查询集上的命中集合必须与 Go 参考实现
// 逐条一致。查询集覆盖混合大小写英文、中文、带空格短语、引号、%/_、数字
// 与作者名；两个分支（≥3 rune MATCH / 1–2 rune LIKE）都被踩到。非 ASCII
// 大小写折叠差异不进本表，单独见 TestIssueFTS_CaseFoldingBoundary。
func TestIssueFTS_SearchEquivalence(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	repo := NewIssueRepository(db)

	fixtures := []model.Issue{
		seedFTSIssue(t, db, projectID, "eq01", 1, "Webhook retry fails", "推送三次都返回 404，需要人工介入。", "shipbobo"),
		seedFTSIssue(t, db, projectID, "eq02", 2, "导入超时", "列表页在数据库连接池打满后请求排队超时。", "alice"),
		seedFTSIssue(t, db, projectID, "eq03", 3, "release checklist 更新", `config 里写 say "hi" 会被转义吗`, "agent-bot"),
		seedFTSIssue(t, db, projectID, "eq04", 4, "MixedCase Body", "Only body has WEBHOOK and 100%_done tokens", "mirror-sync"),
		seedFTSIssue(t, db, projectID, "eq05", 5, "纯数字标题 404", "", "alice"),
		seedFTSIssue(t, db, projectID, "eq06", 6, "无关行", "absurd filler words，这里没有查询词。", "bob"),
	}
	// body 列可空：把 eq05 的正文置 NULL，验证 COALESCE 索引路径与 LIKE 对
	// NULL 不匹配的旧语义等价（标题仍可命中）。
	if err := db.Model(&model.Issue{}).Where("id = ?", "fts-eq05").Update("body", gorm.Expr("NULL")).Error; err != nil {
		t.Fatalf("null body: %v", err)
	}

	queries := []string{
		"webhook",           // ≥3 rune 小写英文（行1 标题、行4 正文大写折叠命中）
		"WebHook",           // ≥3 rune 混合大小写
		"超时",                // 2 rune 中文 → LIKE 分支
		"数据库连接",             // ≥3 rune 中文
		"release checklist", // ≥3 rune 带空格短语
		`say "hi"`,          // ≥3 rune 含引号（ftsMatchLiteral 编码路径）
		"100%_done",         // ≥3 rune 含 % 与 _
		"404",               // ≥3 rune 纯数字（叠加序号精确分支）
		"42",                // 2 rune 纯数字（LIKE + 序号分支，零命中）
		"ab",                // 2 rune（LIKE 分支，正文 absurd 子串命中）
		"alice",             // ≥3 rune 作者名（FTS 模式下的 author_login LIKE 路）
		"shipbobo",          // ≥3 rune 作者名
		"INT-1",             // ≥3 rune reference 前缀
		"无匹配词",              // ≥3 rune 零命中
	}

	for _, q := range queries {
		rows, err := repo.ListFiltered(IssueListQuery{ProjectID: projectID, Query: q}, "updated_desc", 0, 100)
		if err != nil {
			t.Fatalf("list %q: %v", q, err)
		}
		actual := make([]string, 0, len(rows))
		for _, row := range rows {
			actual = append(actual, row.ID)
		}
		sort.Strings(actual)

		expected := make([]string, 0)
		for _, f := range fixtures {
			if issueSearchReference(f, q) {
				expected = append(expected, f.ID)
			}
		}
		sort.Strings(expected)

		if strings.Join(actual, ",") != strings.Join(expected, ",") {
			t.Fatalf("query %q: hits diverge\n actual:   %v\n expected: %v", q, actual, expected)
		}
	}
}

// setupIssueFTSRecordedDB 建绑定 sqlRecorder 的 FTS 测试库（issue_list_test.go
// 的 sqlRecorder 捕获实际执行的 SQL），已执行 EnsureIssueFTSSchema。
func setupIssueFTSRecordedDB(t *testing.T) (*gorm.DB, *sqlRecorder, string) {
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
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
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
	return db, recorder, projectID
}

// TestIssueFTS_QueryBranchShape 断言分支形状：1–2 rune 整组四路 LIKE 且不含
// issues_fts；≥3 rune 标题/正文走 MATCH、作者与 reference 保持 LIKE；纯数字
// 查询两分支各自叠加序号精确匹配。
func TestIssueFTS_QueryBranchShape(t *testing.T) {
	db, recorder, projectID := setupIssueFTSRecordedDB(t)

	type expect struct {
		contains []string
		absent   []string
	}
	cases := []struct {
		query string
		want  expect
	}{
		{
			query: "ab",
			want: expect{
				contains: []string{"lower(issues.title) LIKE", "lower(issues.body) LIKE"},
				absent:   []string{"issues_fts"},
			},
		},
		{
			query: "超时",
			want: expect{
				contains: []string{"lower(issues.title) LIKE"},
				absent:   []string{"issues_fts"},
			},
		},
		{
			query: "webhook",
			want: expect{
				contains: []string{"issues_fts MATCH", "lower(issues.author_login) LIKE"},
				absent:   []string{"lower(issues.title) LIKE", "lower(issues.body) LIKE"},
			},
		},
		{
			query: "42",
			want: expect{
				// recorder 捕获的 SQL 已内联变量值，数字分支按内联形式断言。
				contains: []string{"lower(issues.title) LIKE", "issues.sequence_number = 42", "gh.number = 42"},
				absent:   []string{"issues_fts"},
			},
		},
		{
			query: "404",
			want: expect{
				contains: []string{"issues_fts MATCH", "issues.sequence_number = 404", "gh.number = 404"},
				absent:   []string{"lower(issues.title) LIKE", "lower(issues.body) LIKE"},
			},
		},
	}

	for _, tc := range cases {
		recorder.reset()
		if _, err := NewIssueRepository(db).CountFiltered(IssueListQuery{ProjectID: projectID, Query: tc.query}); err != nil {
			t.Fatalf("count %q: %v", tc.query, err)
		}
		mains := recorder.matching("FROM `issues`")
		if len(mains) != 1 {
			t.Fatalf("query %q: expected one issues query, got %d", tc.query, len(mains))
		}
		sql := mains[0]
		for _, want := range tc.want.contains {
			if !strings.Contains(sql, want) {
				t.Fatalf("query %q: SQL missing %q:\n%s", tc.query, want, sql)
			}
		}
		for _, absent := range tc.want.absent {
			if strings.Contains(sql, absent) {
				t.Fatalf("query %q: SQL must not contain %q:\n%s", tc.query, absent, sql)
			}
		}
	}
}

// TestIssueFTS_MappingConsistency docid 映射一致性：随机 churn（建/改/删交
// 错）后 issue_fts_docs 行数 == issues_fts 行数 == issues 行数，doc ↔ FTS
// rowid 与 issue_id ↔ issues.id 双向一一对应，FTS 行内容与 issues 行
// COALESCE 后逐行一致；命中随 churn 正确变化。
func TestIssueFTS_MappingConsistency(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	repo := NewIssueRepository(db)

	// 建 6 条、改前 3 条、删后 2 条、再补建 2 条——三支触发器都被踩到。
	ids := make([]string, 0, 6)
	for i := 1; i <= 6; i++ {
		issue := seedFTSIssue(t, db, projectID, fmt.Sprintf("mc%02d", i), i,
			"churn marker "+strconv.Itoa(i), "正文 "+strconv.Itoa(i), "alice")
		ids = append(ids, issue.ID)
	}
	for _, id := range ids[:3] {
		if err := db.Model(&model.Issue{}).Where("id = ?", id).Updates(map[string]any{
			"title": "改后 churn 词 gamma",
			"body":  "gamma body",
		}).Error; err != nil {
			t.Fatalf("update %s: %v", id, err)
		}
	}
	for _, id := range ids[4:] {
		if err := db.Delete(&model.Issue{}, "id = ?", id).Error; err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}
	seedFTSIssue(t, db, projectID, "mc07", 7, "补建 marker delta", "delta 正文", "bob")
	seedFTSIssue(t, db, projectID, "mc08", 8, "再补 marker epsilon", "epsilon 正文", "bob")

	assertIssueFTSMappingSynced(t, db)

	// 命中随 churn 正确：保留行可搜、改后新词命中、删掉行不再命中。
	assertIssueFTSHits(t, repo, projectID, "churn marker", []string{"fts-mc04"})
	assertIssueFTSHits(t, repo, projectID, "补建 marker", []string{"fts-mc07"})
	assertIssueFTSHits(t, repo, projectID, "再补 marker", []string{"fts-mc08"})
	assertIssueFTSHits(t, repo, projectID, "正文 5", nil)
}

// assertIssueFTSMappingSynced 锁映射不变量：行数三方一致，doc ↔ FTS rowid
// 与 issue_id ↔ issues.id 双向一一对应，FTS 行内容与 issues 行 COALESCE 后
// 逐行一致。
func assertIssueFTSMappingSynced(t *testing.T, db *gorm.DB) {
	t.Helper()
	countOf := func(sql string) int64 {
		t.Helper()
		var n int64
		if err := db.Raw(sql).Scan(&n).Error; err != nil {
			t.Fatalf("count %q: %v", sql, err)
		}
		return n
	}
	issueN := countOf(`SELECT COUNT(*) FROM issues`)
	docN := countOf(`SELECT COUNT(*) FROM issue_fts_docs`)
	ftsN := countOf(`SELECT COUNT(*) FROM issues_fts`)
	if docN != issueN || ftsN != issueN {
		t.Fatalf("行数不一致：issues=%d issue_fts_docs=%d issues_fts=%d", issueN, docN, ftsN)
	}
	if orphan := countOf(`SELECT COUNT(*) FROM issue_fts_docs d LEFT JOIN issues_fts f ON f.rowid = d.doc WHERE f.rowid IS NULL`); orphan != 0 {
		t.Fatalf("映射行缺 FTS 行：%d", orphan)
	}
	if orphan := countOf(`SELECT COUNT(*) FROM issues_fts f LEFT JOIN issue_fts_docs d ON d.doc = f.rowid WHERE d.doc IS NULL`); orphan != 0 {
		t.Fatalf("FTS 行缺映射行：%d", orphan)
	}
	if orphan := countOf(`SELECT COUNT(*) FROM issue_fts_docs d LEFT JOIN issues i ON i.id = d.issue_id WHERE i.id IS NULL`); orphan != 0 {
		t.Fatalf("映射行缺 issues 行：%d", orphan)
	}
	if orphan := countOf(`SELECT COUNT(*) FROM issues i LEFT JOIN issue_fts_docs d ON d.issue_id = i.id WHERE d.doc IS NULL`); orphan != 0 {
		t.Fatalf("issues 行缺映射行：%d", orphan)
	}
	mismatch := countOf(`SELECT COUNT(*) FROM issues_fts f
JOIN issue_fts_docs d ON d.doc = f.rowid
JOIN issues i ON i.id = d.issue_id
WHERE f.title <> COALESCE(i.title, '') OR f.body <> COALESCE(i.body, '')`)
	if mismatch != 0 {
		t.Fatalf("FTS 行内容与 issues 行不一致：%d", mismatch)
	}
}

// TestIssueFTS_LegacyShapeConvergence 旧形态收敛：手工搭第 1 轮形态（带
// UNINDEXED issue_id 列的 issues_fts + 旧触发器）并让旧触发器同步存量行，
// 跑 ensure 后旧虚表被 DROP 重建、旧触发器被摘除、存量行回填可命中、增量
// 同步恢复、重跑幂等。
func TestIssueFTS_LegacyShapeConvergence(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)

	// 第 1 轮形态的虚表与三支旧触发器（列名与定义照抄 issue_fts.go 旧版）。
	legacyDDL := []string{
		`CREATE VIRTUAL TABLE issues_fts USING fts5(
    issue_id UNINDEXED,
    title,
    body,
    tokenize='trigram'
)`,
		`CREATE TRIGGER trg_issues_fts_insert AFTER INSERT ON issues
BEGIN
    INSERT INTO issues_fts(issue_id, title, body)
    VALUES (NEW.id, COALESCE(NEW.title, ''), COALESCE(NEW.body, ''));
END`,
		`CREATE TRIGGER trg_issues_fts_update AFTER UPDATE ON issues
BEGIN
    DELETE FROM issues_fts WHERE issue_id = OLD.id;
    INSERT INTO issues_fts(issue_id, title, body)
    VALUES (NEW.id, COALESCE(NEW.title, ''), COALESCE(NEW.body, ''));
END`,
		`CREATE TRIGGER trg_issues_fts_delete AFTER DELETE ON issues
BEGIN
    DELETE FROM issues_fts WHERE issue_id = OLD.id;
END`,
	}
	for _, ddl := range legacyDDL {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}
	// 经旧触发器进旧 FTS 的存量行，模拟已跑过第 1 轮形态的库。
	seedFTSIssue(t, db, projectID, "lg01", 1, "legacy needle alpha", "旧形态存量正文", "alice")
	seedFTSIssue(t, db, projectID, "lg02", 2, "无关行", "普通正文", "bob")

	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts (legacy): %v", err)
	}

	// 虚表已是新形态：DDL 原文不再含 issue_id 列。
	var ftsDDL string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'issues_fts'`).Scan(&ftsDDL).Error; err != nil {
		t.Fatalf("read fts ddl: %v", err)
	}
	if ftsDDL == "" || strings.Contains(ftsDDL, "issue_id") {
		t.Fatalf("issues_fts 未收敛到新形态：%s", ftsDDL)
	}
	// 旧触发器全部摘除，新触发器三支在位。
	countTriggers := func(where string) int64 {
		t.Helper()
		var n int64
		if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND ` + where).Scan(&n).Error; err != nil {
			t.Fatalf("count triggers: %v", err)
		}
		return n
	}
	if n := countTriggers("name IN ('trg_issues_fts_insert','trg_issues_fts_update','trg_issues_fts_delete')"); n != 0 {
		t.Fatalf("旧触发器残留 %d 支", n)
	}
	if n := countTriggers("name LIKE 'trg_issues_fts_doc_%'"); n != 3 {
		t.Fatalf("新触发器应有 3 支，实际 %d", n)
	}
	// 数据完整：映射一致 + 存量行可命中。
	assertIssueFTSMappingSynced(t, db)
	repo := NewIssueRepository(db)
	assertIssueFTSHits(t, repo, projectID, "legacy needle", []string{"fts-lg01"})
	assertIssueFTSHits(t, repo, projectID, "旧形态存量正文", []string{"fts-lg01"})

	// 收敛后增量同步恢复：改/删走新触发器。
	if err := db.Model(&model.Issue{}).Where("id = ?", "fts-lg01").Update("title", "收敛后改词 beta").Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "legacy needle", nil)
	assertIssueFTSHits(t, repo, projectID, "收敛后改词", []string{"fts-lg01"})
	if err := db.Delete(&model.Issue{}, "id = ?", "fts-lg02").Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertIssueFTSMappingSynced(t, db)

	// 重跑 ensure 幂等：结构与行数不变。
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts twice: %v", err)
	}
	assertIssueFTSMappingSynced(t, db)
	assertIssueFTSHits(t, repo, projectID, "收敛后改词", []string{"fts-lg01"})
}

// TestIssueFTS_MigrateBackfillIdempotent 迁移与回填：先插行后 ensure（存量行
// 可搜到）；ensure 跑两遍不产生重复索引行；ensure 后 AutoMigrate（gorm 对既有
// 表是空操作）触发器仍同步增量。
func TestIssueFTS_MigrateBackfillIdempotent(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	repo := NewIssueRepository(db)

	// 存量行先于索引存在：触发器还不存在，只能靠 ensure 的回填进入 FTS。
	seedFTSIssue(t, db, projectID, "bf01", 1, "backfill needle alpha", "存量正文 beta", "alice")
	seedFTSIssue(t, db, projectID, "bf02", 2, "无关行", "普通正文", "bob")
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "needle", []string{"fts-bf01"})
	assertIssueFTSHits(t, repo, projectID, "存量正文", []string{"fts-bf01"})

	// 幂等：ensure 跑两遍后索引行数与 issues 行数一致，无重复。
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts twice: %v", err)
	}
	var ftsCount, issueCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM issues_fts`).Scan(&ftsCount).Error; err != nil {
		t.Fatalf("count fts: %v", err)
	}
	if err := db.Model(&model.Issue{}).Count(&issueCount).Error; err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if ftsCount != issueCount {
		t.Fatalf("backfill must be idempotent: fts rows=%d issues=%d", ftsCount, issueCount)
	}

	// AutoMigrate 重跑对既有表是空操作，但触发器必须仍然在位：增量仍同步。
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueGitHubMeta{}, &model.IssueInternalMeta{}); err != nil {
		t.Fatalf("remigrate: %v", err)
	}
	created := seedFTSIssue(t, db, projectID, "bf03", 3, "增量词 gamma", "gamma body", "alice")
	assertIssueFTSHits(t, repo, projectID, "增量词", []string{"fts-bf03"})

	if err := db.Model(&model.Issue{}).Where("id = ?", created.ID).Updates(map[string]any{
		"title": "改后词 delta",
		"body":  "delta body",
	}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "增量词", nil)
	assertIssueFTSHits(t, repo, projectID, "改后词", []string{"fts-bf03"})

	if err := db.Delete(&model.Issue{}, "id = ?", created.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "改后词", nil)
}

// TestIssueFTS_IncrementalSync 增量同步：insert/update/delete 后 FTS 命中随
// 之变化。覆盖 gorm 两种写形态：Save 全量更新与 Model().Update 单列更新。
func TestIssueFTS_IncrementalSync(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	repo := NewIssueRepository(db)

	issue := seedFTSIssue(t, db, projectID, "inc01", 1, "first title marker", "second body marker", "alice")
	assertIssueFTSHits(t, repo, projectID, "first", []string{"fts-inc01"})
	assertIssueFTSHits(t, repo, projectID, "second", []string{"fts-inc01"})

	// Save 全量更新：标题换词，正文不动。
	issue.Title = "third title marker"
	if err := repo.Save(&issue); err != nil {
		t.Fatalf("save: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "first", nil)
	assertIssueFTSHits(t, repo, projectID, "third", []string{"fts-inc01"})
	assertIssueFTSHits(t, repo, projectID, "second", []string{"fts-inc01"})

	// Model().Update 单列更新：正文换词。
	if err := db.Model(&model.Issue{}).Where("id = ?", issue.ID).Update("body", "fourth body marker").Error; err != nil {
		t.Fatalf("update body: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "second", nil)
	assertIssueFTSHits(t, repo, projectID, "fourth", []string{"fts-inc01"})

	if err := db.Delete(&model.Issue{}, "id = ?", issue.ID).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "third", nil)
	assertIssueFTSHits(t, repo, projectID, "fourth", nil)
}

// TestIssueFTS_ProjectCascadeRemovesFTSRows 级联行为实测：删 project（外键
// CASCADE）逐行删 issues，AFTER DELETE 触发器同步清 FTS——实测无孤儿行残留。
// 理论上只有「DROP/重建 issues 表」的迁移会留下孤儿行（触发器随表消失），
// 孤儿行按 issues.id 回连主表不影响查询正确性，边界见 EnsureIssueFTSSchema。
func TestIssueFTS_ProjectCascadeRemovesFTSRows(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	// 外键必须开启，否则 CASCADE 不生效、测试失去意义。
	var fk int
	if err := db.Raw(`PRAGMA foreign_keys`).Scan(&fk).Error; err != nil || fk != 1 {
		t.Fatalf("foreign_keys pragma must be on, got %d (err=%v)", fk, err)
	}

	for i, suffix := range []string{"ca01", "ca02", "ca03"} {
		seedFTSIssue(t, db, projectID, suffix, i+1, "cascade marker "+suffix, "正文 "+suffix, "alice")
	}
	repo := NewIssueRepository(db)
	assertIssueFTSHits(t, repo, projectID, "cascade", []string{"fts-ca01", "fts-ca02", "fts-ca03"})

	if err := db.Delete(&model.Project{}, "id = ?", projectID).Error; err != nil {
		t.Fatalf("delete project: %v", err)
	}
	var ftsCount, issueCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM issues_fts`).Scan(&ftsCount).Error; err != nil {
		t.Fatalf("count fts: %v", err)
	}
	if err := db.Model(&model.Issue{}).Count(&issueCount).Error; err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issueCount != 0 || ftsCount != 0 {
		t.Fatalf("cascade must leave no rows: issues=%d fts=%d（fts 孤儿行残留）", issueCount, ftsCount)
	}
}

// TestIssueFTS_CaseFoldingBoundary 大小写折叠口径实测（modernc 驱动，2026-01
// 实测结论，不为此做任何处理，只锁行为）：
//   - ≥3 rune MATCH 分支：trigram 折叠非 ASCII 大小写（É→é、П→п），查询
//     「ÉCOLE」能命中正文「école」，方向与 Go ToLower 一致、比 LIKE 更宽；
//     不去音调（"ecole" 不命中 "école"）。
//   - 1–2 rune LIKE 分支：沿用 lower()/LIKE 的 ASCII-only 折叠，非 ASCII 大写
//     不折叠（旧边界原样保留）——同一对大小写两个分支命中方向不同。
func TestIssueFTS_CaseFoldingBoundary(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	repo := NewIssueRepository(db)

	seedFTSIssue(t, db, projectID, "cf01", 1, "École française", "正文无查询词", "alice")
	seedFTSIssue(t, db, projectID, "cf02", 2, "无关行", "привет мир", "bob")

	// ≥3 rune MATCH 分支：非 ASCII 大小写折叠命中。
	assertIssueFTSHits(t, repo, projectID, "école française", []string{"fts-cf01"})
	assertIssueFTSHits(t, repo, projectID, "ÉCOLE FRANÇAISE", []string{"fts-cf01"})
	assertIssueFTSHits(t, repo, projectID, "ПРИВЕТ", []string{"fts-cf02"})
	// 不去音调：纯 ASCII 拼写不命中带音符原文。
	assertIssueFTSHits(t, repo, projectID, "ecole", nil)

	// 1–2 rune LIKE 分支：ASCII-only 折叠——「ÉC」不命中「École」（旧边界），
	// 而 3 rune 的「éco」经 FTS 折叠命中，同一对大小写分支间方向相反。
	assertIssueFTSHits(t, repo, projectID, "ÉC", nil)
	assertIssueFTSHits(t, repo, projectID, "éco", []string{"fts-cf01"})
}

// TestIssueFTS_ExplainQueryPlanUsesFTS 用 EXPLAIN QUERY PLAN 锁执行计划：
// ≥3 rune 查询的 title/body 匹配必须落到 issues_fts 的虚表 MATCH 扫描，
// 1–2 rune 不得触碰 issues_fts。
func TestIssueFTS_ExplainQueryPlanUsesFTS(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}

	details := explainIssueSearch(t, db, projectID, "webhook")
	ftsHit := false
	docsSeen := false
	for _, d := range details {
		t.Logf("plan[webhook]: %s", d)
		if strings.Contains(d, "issues_fts") && strings.Contains(d, "VIRTUAL TABLE INDEX") {
			ftsHit = true
		}
		if strings.Contains(d, "issue_fts_docs") {
			docsSeen = true
		}
	}
	if !ftsHit {
		t.Fatalf("≥3 rune 查询必须走 issues_fts MATCH 扫描，实际计划：\n%s", strings.Join(details, "\n"))
	}
	if !docsSeen {
		t.Fatalf("计划必须经 issue_fts_docs 映射换回 issue_id（免 content 回读），实际计划：\n%s", strings.Join(details, "\n"))
	}

	details = explainIssueSearch(t, db, projectID, "ab")
	for _, d := range details {
		t.Logf("plan[ab]: %s", d)
		if strings.Contains(d, "issues_fts") {
			t.Fatalf("1–2 rune 查询不得触碰 issues_fts，实际计划：\n%s", strings.Join(details, "\n"))
		}
	}
}

// explainIssueSearch 用 DryRun 会话构建与 ListFiltered 完全相同的 SQL（不执行），
// 再对它跑 EXPLAIN QUERY PLAN，返回逐行 detail。
func explainIssueSearch(t *testing.T, db *gorm.DB, projectID, query string) []string {
	t.Helper()
	repo := &IssueRepository{db: db.Session(&gorm.Session{DryRun: true})}
	q := repo.buildFilteredQuery(IssueListQuery{ProjectID: projectID, Query: query}).
		Select(issueListColumns).
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
	if err := db.Raw("EXPLAIN QUERY PLAN "+q.Statement.SQL.String(), q.Statement.Vars...).Scan(&plan).Error; err != nil {
		t.Fatalf("explain: %v", err)
	}
	details := make([]string, 0, len(plan))
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	return details
}

// assertIssueFTSHits 跑一次带 q 的列表查询，断言命中 id 集合与期望一致
// （与顺序无关）。
func assertIssueFTSHits(t *testing.T, repo *IssueRepository, projectID, query string, wantIDs []string) {
	t.Helper()
	rows, err := repo.ListFiltered(IssueListQuery{ProjectID: projectID, Query: query}, "updated_desc", 0, 100)
	if err != nil {
		t.Fatalf("list %q: %v", query, err)
	}
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		got = append(got, row.ID)
	}
	sort.Strings(got)
	want := append([]string(nil), wantIDs...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("query %q: hits = %v, want %v", query, got, want)
	}
}

// TestIssueFTS_NulQueryFallsBackToLike 含 NUL 的 ≥3 rune 查询整组退回 LIKE：
// FTS5 字面量无法表达 NUL（查询解析器提前终止报 unterminated string），不能为
// 修索引引入报错回归（INT-209 审查发现）。期望命中用旧四路 LIKE 原句直接算
// （驱动会把 NUL 截断、模式退化为宽匹配——这是改造前就存在的旧行为，保持
// 原样，不在此修正），同时断言 SQL 形状确实落在 LIKE 分支。
func TestIssueFTS_NulQueryFallsBackToLike(t *testing.T) {
	db, recorder, projectID := setupIssueFTSRecordedDB(t)
	repo := NewIssueRepository(db)
	seedFTSIssue(t, db, projectID, "nul01", 1, "webhook 推送失败", "正文含查询词", "alice")

	for _, q := range []string{"abc\x00def", "\x00ab", "webhook\x00"} {
		recorder.reset()
		// 旧四路 LIKE 的期望结果（旧语义原样：lower(...) LIKE %q%）。
		pattern := "%" + escapeLikePattern(strings.ToLower(q)) + "%"
		var wantIDs []string
		if err := db.Raw(`SELECT id FROM issues WHERE project_id = ? AND (lower(title) LIKE ? ESCAPE '\'
			OR lower(body) LIKE ? ESCAPE '\' OR lower(author_login) LIKE ? ESCAPE '\'
			OR lower('INT-' || sequence_number) LIKE ? ESCAPE '\')`,
			projectID, pattern, pattern, pattern, pattern).Scan(&wantIDs).Error; err != nil {
			t.Fatalf("reference like %q: %v", q, err)
		}
		assertIssueFTSHits(t, repo, projectID, q, wantIDs)
		mains := recorder.matching("FROM `issues`")
		if len(mains) != 1 {
			t.Fatalf("query %q: expected one issues query, got %d", q, len(mains))
		}
		if !strings.Contains(mains[0], "lower(issues.body) LIKE") {
			t.Fatalf("NUL query %q must fall back to LIKE branch:\n%s", q, mains[0])
		}
		if strings.Contains(mains[0], "issues_fts") {
			t.Fatalf("NUL query %q must not touch issues_fts:\n%s", q, mains[0])
		}
	}
}

// TestIssueFTS_InterruptedMigrationSelfHeals 迁移中断残留状态的自愈：ensure
// 的第一步就是摘旧触发器（先于任何旧虚表删除），因此即使外部手工构造出
// 「旧触发器引用已删表」的死局状态，跑一次 ensure 也能收敛到新结构，且 issues
// 表恢复可 UPDATE（INT-209 审查发现的触发器残留死局回归）。
func TestIssueFTS_InterruptedMigrationSelfHeals(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	repo := NewIssueRepository(db)
	seedFTSIssue(t, db, projectID, "rc01", 1, "recovery marker", "正文无查询词", "alice")

	// 构造中断死局：旧触发器在位、issues_fts 已被删（旧顺序 DROP 虚表在
	// DROP 触发器之前中断会留下的状态）。此时任何 issues 上的 UPDATE 都会
	// 因触发器引用缺失表而报错。
	legacyDDL := []string{
		"CREATE VIRTUAL TABLE issues_fts USING fts5(issue_id UNINDEXED, title, body, tokenize='trigram')",
		`CREATE TRIGGER trg_issues_fts_update AFTER UPDATE ON issues
BEGIN
    DELETE FROM issues_fts WHERE issue_id = OLD.id;
END`,
	}
	for _, stmt := range legacyDDL {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("seed broken state: %v", err)
		}
	}
	if err := db.Exec(`DROP TABLE issues_fts`).Error; err != nil {
		t.Fatalf("simulate interrupted drop: %v", err)
	}
	if err := db.Model(&model.Issue{}).Where("id = ?", "fts-rc01").Update("title", "中断验证").Error; err == nil {
		t.Fatalf("orphan trigger must break UPDATE on issues (broken state not reproduced)")
	}

	// 一次 ensure 收敛：旧触发器被摘、新结构建齐、存量行回填、UPDATE 恢复。
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure after interruption: %v", err)
	}
	if err := db.Model(&model.Issue{}).Where("id = ?", "fts-rc01").Update("title", "recovery marker 更新").Error; err != nil {
		t.Fatalf("update after heal: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "更新", []string{"fts-rc01"})
	assertIssueFTSHits(t, repo, projectID, "中断验证", nil)

	// 再补一个「无触发器的旧虚表」残留（先摘触发器后中断会留下的无害状态）：
	// legacy 检测应重建为新结构，存量行由回填补齐。
	for _, stmt := range []string{
		`DROP TRIGGER trg_issues_fts_doc_insert`,
		`DROP TRIGGER trg_issues_fts_doc_update`,
		`DROP TRIGGER trg_issues_fts_doc_delete`,
		`DROP TABLE issues_fts`,
		`DROP TABLE issue_fts_docs`,
		"CREATE VIRTUAL TABLE issues_fts USING fts5(issue_id UNINDEXED, title, body, tokenize='trigram')",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("build stale legacy table state: %v", err)
		}
	}
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure after stale legacy table: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "更新", []string{"fts-rc01"})
	// 新触发器恢复：增量继续同步。
	if err := db.Model(&model.Issue{}).Where("id = ?", "fts-rc01").Update("title", "终态词").Error; err != nil {
		t.Fatalf("update after legacy rebuild: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "终态词", []string{"fts-rc01"})
}

// TestIssueFTS_MetadataOnlyUpdateSkipsReindex 元数据写入不走重索引（P2 修复）：
// TouchUpdatedAt / 全列 Save（title/body 值未变）后 FTS 行 rowid 集合不变——
// UPDATE OF + WHEN 双层守卫生效；正文真实变更仍正常重索引。最后断言三条触发
// 器的存量定义与期望串逐字一致（收敛逻辑按原文比对，这里锁住比对基准）。
func TestIssueFTS_MetadataOnlyUpdateSkipsReindex(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	repo := NewIssueRepository(db)
	issue := seedFTSIssue(t, db, projectID, "mo01", 1, "metadata marker", "正文 marker", "alice")
	assertIssueFTSHits(t, repo, projectID, "marker", []string{"fts-mo01"})

	ftsRowids := func() []int {
		var ids []int
		if err := db.Raw(`SELECT rowid FROM issues_fts ORDER BY rowid`).Scan(&ids).Error; err != nil {
			t.Fatalf("select fts rowids: %v", err)
		}
		return ids
	}
	before := ftsRowids()

	// 元数据写入：SET 不含 title/body，OF 守卫直接跳过。
	if err := repo.TouchUpdatedAt(db, issue.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("touch updated_at: %v", err)
	}
	if got := ftsRowids(); !reflect.DeepEqual(got, before) {
		t.Fatalf("metadata-only update must not rewrite fts rows: %v -> %v", before, got)
	}

	// 全列写入但 title/body 值未变（gorm Save）：WHEN 守卫按值跳过。
	if err := repo.Save(&issue); err != nil {
		t.Fatalf("no-op save: %v", err)
	}
	if got := ftsRowids(); !reflect.DeepEqual(got, before) {
		t.Fatalf("no-op save must not rewrite fts rows: %v -> %v", before, got)
	}

	// 正文真实变更仍重索引（守卫不吞变更）：旧正文独有短语不再命中，标题
	// 里的 marker 不受影响。
	if err := db.Model(&model.Issue{}).Where("id = ?", issue.ID).Update("body", "新的正文词").Error; err != nil {
		t.Fatalf("update body: %v", err)
	}
	assertIssueFTSHits(t, repo, projectID, "正文 marker", nil)
	assertIssueFTSHits(t, repo, projectID, "新的正文词", []string{"fts-mo01"})
	assertIssueFTSHits(t, repo, projectID, "marker", []string{"fts-mo01"})

	for _, trg := range issueFTSDocTriggers {
		var ddl string
		if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg.name).Scan(&ddl).Error; err != nil {
			t.Fatalf("read trigger %s: %v", trg.name, err)
		}
		if ddl != trg.create {
			t.Fatalf("trigger %s stored definition drifted from expected:\nstored: %s\nwant:  %s", trg.name, ddl, trg.create)
		}
	}
}

// TestIssueFTS_UnexpectedShapeRebuilds 异形虚表按列集合精确判定重建（Note 2）：
// 列集合 ≠ {title, body}（含 issue_ids_json 这类名字近似的未来形态）一律
// DROP 重建并回填，不依赖 DDL 原文子串判断。
func TestIssueFTS_UnexpectedShapeRebuilds(t *testing.T) {
	db, projectID := setupIssueFTSTestDB(t)
	seedFTSIssue(t, db, projectID, "us01", 1, "unexpected marker", "正文无查询词", "alice")
	if err := db.Exec(`CREATE VIRTUAL TABLE issues_fts USING fts5(issue_ids_json UNINDEXED, title, body, tokenize='trigram')`).Error; err != nil {
		t.Fatalf("seed unexpected shape: %v", err)
	}
	if err := db.Exec(`INSERT INTO issues_fts(issue_ids_json, title, body) VALUES ('x', 'stale words', '')`).Error; err != nil {
		t.Fatalf("seed stale fts row: %v", err)
	}
	if err := EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure fts: %v", err)
	}
	repo := NewIssueRepository(db)
	// 重建后杂行清掉、存量行回填可命中。
	assertIssueFTSHits(t, repo, projectID, "stale", nil)
	assertIssueFTSHits(t, repo, projectID, "unexpected", []string{"fts-us01"})

	var cols []struct{ Name string }
	if err := db.Raw(`PRAGMA table_info(issues_fts)`).Scan(&cols).Error; err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	if len(cols) != 2 || cols[0].Name != "title" || cols[1].Name != "body" {
		t.Fatalf("columns after rebuild = %+v, want [title body]", cols)
	}
}
