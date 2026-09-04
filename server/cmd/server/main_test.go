package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestBackfillIssueSourceModel_RemovesLegacyIssueColumns(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}, &model.Project{}); err != nil {
		t.Fatalf("migrate user/project tables: %v", err)
	}

	if err := db.Exec(`
		CREATE TABLE issues (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			source TEXT,
			sequence_number INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL,
			state_reason TEXT,
			title TEXT NOT NULL,
			body TEXT,
			body_html TEXT,
			author_user_id TEXT,
			author_login TEXT,
			author_avatar_url TEXT,
			closed_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			github_issue_id INTEGER NOT NULL,
			github_node_id TEXT,
			number INTEGER,
			html_url TEXT,
			author_association TEXT,
			assignees_json TEXT,
			labels_json TEXT,
			milestone_json TEXT,
			reactions_json TEXT,
			comments_count INTEGER,
			locked NUMERIC,
			active_lock_reason TEXT,
			synced_at DATETIME,
			raw_json TEXT
		)
	`).Error; err != nil {
		t.Fatalf("create legacy issues table: %v", err)
	}

	if err := db.AutoMigrate(&model.Issue{}, &model.IssueGitHubMeta{}); err != nil {
		t.Fatalf("migrate issue tables: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)").Error; err != nil {
		t.Fatalf("create github meta unique index: %v", err)
	}

	now := time.Now().UTC()
	user := &model.User{
		ID:           "user-1",
		Username:     "shipbobo",
		Email:        "shipbobo@example.com",
		PasswordHash: "hashed",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	project := &model.Project{
		ID:                   "project-1",
		UserID:               user.ID,
		Name:                 "demo",
		Description:          "demo",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("token"),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	if err := db.Exec(`
		INSERT INTO issues (
			id, project_id, source, sequence_number, state, state_reason, title, body, body_html,
			author_user_id, author_login, author_avatar_url, closed_at, created_at, updated_at,
			github_issue_id, github_node_id, number, html_url, author_association, assignees_json,
			labels_json, milestone_json, reactions_json, comments_count, locked, active_lock_reason,
			synced_at, raw_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"issue-github-1",
		project.ID,
		"internal",
		42,
		"open",
		"",
		"legacy github issue",
		"body",
		"<p>body</p>",
		user.ID,
		user.Username,
		"",
		nil,
		now,
		now,
		1001,
		"I_kw_test",
		42,
		"https://github.com/owner/repo/issues/42",
		"OWNER",
		`[{"login":"alice"}]`,
		`[{"name":"bug"}]`,
		`{"number":1,"title":"v1"}`,
		`{"total_count":1}`,
		3,
		false,
		"",
		now,
		`{"id":1001}`,
	).Error; err != nil {
		t.Fatalf("insert legacy issue: %v", err)
	}

	if err := backfillIssueSourceModel(db); err != nil {
		t.Fatalf("backfill issue source model: %v", err)
	}

	hasLegacyColumn, err := hasSQLiteColumn(db, "issues", "github_issue_id")
	if err != nil {
		t.Fatalf("check legacy column: %v", err)
	}
	if hasLegacyColumn {
		t.Fatalf("expected github_issue_id column to be removed from issues")
	}

	var meta model.IssueGitHubMeta
	if err := db.Where("issue_id = ?", "issue-github-1").First(&meta).Error; err != nil {
		t.Fatalf("load migrated github meta: %v", err)
	}
	if meta.ProjectID != project.ID || meta.GitHubIssueID != 1001 || meta.Number != 42 {
		t.Fatalf("unexpected migrated github meta: %+v", meta)
	}

	var migratedIssue model.Issue
	if err := db.Where("id = ?", "issue-github-1").First(&migratedIssue).Error; err != nil {
		t.Fatalf("load migrated issue: %v", err)
	}
	if migratedIssue.Source != model.IssueSourceGitHub {
		t.Fatalf("expected migrated issue source to be github, got %q", migratedIssue.Source)
	}

	internalIssue := &model.Issue{
		ID:              "issue-internal-1",
		ProjectID:       project.ID,
		Source:          model.IssueSourceInternal,
		SequenceNumber:  43,
		State:           model.IssueStateOpen,
		Title:           "internal issue",
		Body:            "body",
		AuthorUserID:    user.ID,
		AuthorLogin:     user.Username,
		AuthorAvatarURL: "",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(internalIssue).Error; err != nil {
		t.Fatalf("create internal issue after migration: %v", err)
	}
}

func TestBackfillIssueSourceModel_RepairsMigratedIssueSourceFromGitHubMeta(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueGitHubMeta{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)").Error; err != nil {
		t.Fatalf("create github meta unique index: %v", err)
	}

	now := time.Now().UTC()
	user := &model.User{
		ID:           "user-1",
		Username:     "shipbobo",
		Email:        "shipbobo@example.com",
		PasswordHash: "hashed",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	project := &model.Project{
		ID:                   "project-1",
		UserID:               user.ID,
		Name:                 "demo",
		Description:          "demo",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("token"),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	issue := &model.Issue{
		ID:              "issue-github-1",
		ProjectID:       project.ID,
		Source:          model.IssueSourceInternal,
		SequenceNumber:  42,
		State:           model.IssueStateOpen,
		Title:           "migrated github issue",
		Body:            "body",
		AuthorLogin:     "octocat",
		AuthorAvatarURL: "",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	meta := &model.IssueGitHubMeta{
		IssueID:       issue.ID,
		ProjectID:     project.ID,
		GitHubIssueID: 1001,
		GitHubNodeID:  "I_kw_test",
		Number:        42,
		HTMLURL:       "https://github.com/owner/repo/issues/42",
		SyncedAt:      now,
	}
	if err := db.Create(meta).Error; err != nil {
		t.Fatalf("create github meta: %v", err)
	}

	if err := backfillIssueSourceModel(db); err != nil {
		t.Fatalf("backfill issue source model: %v", err)
	}

	var repaired model.Issue
	if err := db.Where("id = ?", issue.ID).First(&repaired).Error; err != nil {
		t.Fatalf("load repaired issue: %v", err)
	}
	if repaired.Source != model.IssueSourceGitHub {
		t.Fatalf("expected repaired issue source to be github, got %q", repaired.Source)
	}
}

func openLogMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func tableExists(t *testing.T, db *gorm.DB, tableName string) bool {
	t.Helper()
	var name string
	err := db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, tableName).Scan(&name).Error
	if err != nil {
		t.Fatalf("check table %s: %v", tableName, err)
	}
	return name == tableName
}

func TestDropLegacyLogTables_RemovesOldBatchSchema(t *testing.T) {
	db := openLogMigrationTestDB(t)

	if err := db.Exec(`
		CREATE TABLE log_batches (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			run_id TEXT NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create legacy log_batches: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE log_entries (
			id TEXT PRIMARY KEY,
			batch_id TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			level TEXT NOT NULL,
			message TEXT NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create legacy log_entries: %v", err)
	}
	if err := db.Exec(`INSERT INTO log_batches (id, project_id, run_id) VALUES ('batch-1', 'project-1', 'run-1')`).Error; err != nil {
		t.Fatalf("insert legacy batch: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO log_entries (id, batch_id, timestamp, level, message)
		VALUES ('entry-1', 'batch-1', '2026-06-29T12:00:00Z', 'info', 'legacy')
	`).Error; err != nil {
		t.Fatalf("insert legacy entry: %v", err)
	}

	dropLegacyLogTables(db, zap.NewNop())

	if tableExists(t, db, "log_entries") {
		t.Fatalf("expected legacy log_entries to be dropped")
	}
	if tableExists(t, db, "log_batches") {
		t.Fatalf("expected legacy log_batches to be dropped")
	}
}

func TestDropLegacyLogTables_PreservesNewRunSchemaAcrossRestart(t *testing.T) {
	db := openLogMigrationTestDB(t)
	now := time.Now().UTC()

	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.LogRun{}, &model.LogRunChunk{}, &model.LogEntry{}); err != nil {
		t.Fatalf("migrate new log tables: %v", err)
	}
	if err := db.Create(&model.User{
		ID:           "user-1",
		Username:     "loguser",
		Email:        "log@example.com",
		PasswordHash: "x",
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{
		ID:        "project-1",
		UserID:    "user-1",
		Name:      "demo",
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	run := &model.LogRun{
		ID:        "run-internal-1",
		ProjectID: "project-1",
		RunID:     "client-run-1",
		Source:    "smux",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(run).Error; err != nil {
		t.Fatalf("create log run: %v", err)
	}
	entry := &model.LogEntry{
		ID:        "entry-1",
		LogRunID:  run.ID,
		Timestamp: now,
		Level:     "info",
		Message:   "persist me",
		CreatedAt: now,
	}
	if err := db.Create(entry).Error; err != nil {
		t.Fatalf("create log entry: %v", err)
	}

	dropLegacyLogTables(db, zap.NewNop())
	dropLegacyLogTables(db, zap.NewNop())

	if !tableExists(t, db, "log_runs") {
		t.Fatalf("expected log_runs to remain after cleanup")
	}
	if !tableExists(t, db, "log_entries") {
		t.Fatalf("expected new log_entries to remain after cleanup")
	}

	hasBatchID, err := hasSQLiteColumn(db, "log_entries", "batch_id")
	if err != nil {
		t.Fatalf("check batch_id column: %v", err)
	}
	if hasBatchID {
		t.Fatalf("expected new log_entries without batch_id column")
	}
	hasLogRunID, err := hasSQLiteColumn(db, "log_entries", "log_run_id")
	if err != nil {
		t.Fatalf("check log_run_id column: %v", err)
	}
	if !hasLogRunID {
		t.Fatalf("expected log_entries to keep log_run_id column")
	}

	var persisted model.LogEntry
	if err := db.Where("id = ?", entry.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted entry after cleanup: %v", err)
	}
	if persisted.Message != "persist me" {
		t.Fatalf("unexpected entry message: %q", persisted.Message)
	}
}

func TestDropLegacyCollabTables_MigratesAndRemovesOldSchema(t *testing.T) {
	db := openLogMigrationTestDB(t)
	now := time.Now().UTC()

	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueCollabDocument{}); err != nil {
		t.Fatalf("migrate documents table: %v", err)
	}
	if err := db.Create(&model.User{
		ID:           "user-1",
		Username:     "collab",
		Email:        "collab@example.com",
		PasswordHash: "x",
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{
		ID:        "project-1",
		UserID:    "user-1",
		Name:      "demo",
		CreatedAt: now,
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := db.Create(&model.Issue{
		ID:             "issue-1",
		ProjectID:      "project-1",
		Source:         model.IssueSourceInternal,
		SequenceNumber: 1,
		State:          model.IssueStateOpen,
		Title:          "test",
		Body:           "body",
		AuthorUserID:   "user-1",
		AuthorLogin:    "collab",
		CreatedAt:      now,
		UpdatedAt:      now,
	}).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	for _, table := range []string{
		"issue_collab_notes",
		"issue_collab_questions",
		"issue_collab_suggestions",
		"issue_collab_plans",
		"issue_collab_reviews",
	} {
		if err := db.Exec(fmt.Sprintf("CREATE TABLE %s (id TEXT PRIMARY KEY)", table)).Error; err != nil {
			t.Fatalf("create legacy table %s: %v", table, err)
		}
	}
	if err := db.Exec(`
		CREATE TABLE issue_collab_summaries (
			issue_id TEXT PRIMARY KEY,
			body TEXT NOT NULL,
			commit_ids_json TEXT,
			author_user_id TEXT NOT NULL,
			author_kind TEXT NOT NULL DEFAULT 'agent',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create legacy summaries: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE issue_collab_consensus (
			issue_id TEXT PRIMARY KEY,
			body TEXT NOT NULL,
			author_user_id TEXT NOT NULL,
			author_kind TEXT NOT NULL DEFAULT 'agent',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create legacy consensus: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO issue_collab_summaries (issue_id, body, commit_ids_json, author_user_id, author_kind, created_at, updated_at)
		VALUES ('issue-1', '总结正文', '["abc"]', 'user-1', 'agent', ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert legacy summary: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO issue_collab_consensus (issue_id, body, author_user_id, author_kind, created_at, updated_at)
		VALUES ('issue-1', '共识正文', 'user-1', 'agent', ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert legacy consensus: %v", err)
	}

	if err := dropLegacyCollabTables(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate collab tables: %v", err)
	}
	if err := dropLegacyCollabTables(db, zap.NewNop()); err != nil {
		t.Fatalf("idempotent migrate collab tables: %v", err)
	}

	for _, table := range []string{
		"issue_collab_notes",
		"issue_collab_questions",
		"issue_collab_suggestions",
		"issue_collab_plans",
		"issue_collab_reviews",
		"issue_collab_consensus",
		"issue_collab_summaries",
	} {
		if tableExists(t, db, table) {
			t.Fatalf("expected legacy table %s to be dropped", table)
		}
	}
	if !tableExists(t, db, "issue_collab_documents") {
		t.Fatalf("expected issue_collab_documents to exist after migration")
	}

	var summaryDoc model.IssueCollabDocument
	if err := db.Where("issue_id = ? AND kind = ?", "issue-1", model.CollabDocumentKindSummary).First(&summaryDoc).Error; err != nil {
		t.Fatalf("load migrated summary: %v", err)
	}
	if summaryDoc.Body != "总结正文" {
		t.Fatalf("unexpected migrated summary body: %q", summaryDoc.Body)
	}

	var consensusDoc model.IssueCollabDocument
	if err := db.Where("issue_id = ? AND kind = ?", "issue-1", model.CollabDocumentKindConsensus).First(&consensusDoc).Error; err != nil {
		t.Fatalf("load migrated consensus: %v", err)
	}
	if consensusDoc.Body != "共识正文" {
		t.Fatalf("unexpected migrated consensus body: %q", consensusDoc.Body)
	}

	var docCount int64
	if err := db.Model(&model.IssueCollabDocument{}).Count(&docCount).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docCount != 2 {
		t.Fatalf("expected 2 migrated documents after idempotent second run, got %d", docCount)
	}
}

func TestDropLegacyCollabTables_CopyFailurePreservesLegacyTables(t *testing.T) {
	db := openLogMigrationTestDB(t)
	now := time.Now().UTC()

	if err := db.AutoMigrate(&model.IssueCollabDocument{}); err != nil {
		t.Fatalf("migrate documents table: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE issue_collab_summaries (
			issue_id TEXT PRIMARY KEY,
			body TEXT NOT NULL,
			author_user_id TEXT NOT NULL,
			author_kind TEXT NOT NULL DEFAULT 'agent',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create legacy summaries: %v", err)
	}
	// 孤儿 issue_id，触发 documents 表外键约束。
	if err := db.Exec(`
		INSERT INTO issue_collab_summaries (issue_id, body, author_user_id, author_kind, created_at, updated_at)
		VALUES ('orphan-issue', '应保留', 'user-1', 'agent', ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert orphan legacy summary: %v", err)
	}

	if err := dropLegacyCollabTables(db, zap.NewNop()); err == nil {
		t.Fatalf("expected migration error for orphan issue_id")
	}
	if !tableExists(t, db, "issue_collab_summaries") {
		t.Fatalf("expected issue_collab_summaries to remain after copy failure")
	}

	var count int64
	if err := db.Model(&model.IssueCollabDocument{}).Count(&count).Error; err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no migrated documents after copy failure, got %d", count)
	}
}

func TestDropLegacyCollabTables_CopyFailureWhenDocumentsTableMissing(t *testing.T) {
	db := openLogMigrationTestDB(t)
	now := time.Now().UTC()

	if err := db.Exec(`
		CREATE TABLE issue_collab_summaries (
			issue_id TEXT PRIMARY KEY,
			body TEXT NOT NULL,
			author_user_id TEXT NOT NULL,
			author_kind TEXT NOT NULL DEFAULT 'agent',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create legacy summaries: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO issue_collab_summaries (issue_id, body, author_user_id, author_kind, created_at, updated_at)
		VALUES ('issue-1', '应保留', 'user-1', 'agent', ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert legacy summary: %v", err)
	}

	if err := dropLegacyCollabTables(db, zap.NewNop()); err == nil {
		t.Fatalf("expected migration error when issue_collab_documents table is missing")
	}
	if !tableExists(t, db, "issue_collab_summaries") {
		t.Fatalf("expected issue_collab_summaries to remain when documents table is missing")
	}
}

func TestBackfillIssueReadStates_ScansSQLiteTextTimestamps(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.Project{},
		&model.Issue{},
		&model.IssueComment{},
		&model.IssueReadState{},
		&model.IssueReadCatchup{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := db.Create(&model.User{
		ID: "user-1", Username: "u", Email: "u@example.com", PasswordHash: "x",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.Project{
		ID: "proj-1", UserID: "user-1", Name: "demo", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// 存量库常见两种 TEXT 时间：空格分隔 + T 分隔。Raw Scan 进 time.Time 会炸。
	if err := db.Exec(`
		INSERT INTO issues (id, project_id, source, sequence_number, state, title, body, created_at, updated_at)
		VALUES
		  ('issue-space', 'proj-1', 'github', 1, 'open', 'a', '', '2026-09-03 08:00:00+00:00', '2026-09-03 08:00:00+00:00'),
		  ('issue-t', 'proj-1', 'github', 2, 'open', 'b', '', '2026-09-03T09:00:00+00:00', '2026-09-03T09:00:00+00:00'),
		  ('issue-internal', 'proj-1', 'internal', 3, 'open', 'c', '', '2026-09-03 08:00:00+00:00', '2026-09-03 08:00:00+00:00')
	`).Error; err != nil {
		t.Fatalf("seed issues: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO issue_comments (id, issue_id, source, github_comment_id, body, created_at, updated_at)
		VALUES ('c1', 'issue-t', 'github', 1, '评论', '2026-09-03T11:00:00+00:00', '2026-09-03T11:00:00+00:00')
	`).Error; err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	if err := backfillIssueReadStates(db, zap.NewNop()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if err := backfillIssueReadStates(db, zap.NewNop()); err != nil {
		t.Fatalf("idempotent backfill: %v", err)
	}

	assertReadAt := func(issueID, wantRFC3339 string) {
		t.Helper()
		var state model.IssueReadState
		if err := db.Where("user_id = ? AND issue_id = ?", "user-1", issueID).First(&state).Error; err != nil {
			t.Fatalf("load read state %s: %v", issueID, err)
		}
		got := state.LastReadAt.UTC().Format(time.RFC3339)
		if got != wantRFC3339 {
			t.Fatalf("issue %s last_read_at = %s, want %s", issueID, got, wantRFC3339)
		}
	}
	assertReadAt("issue-space", "2026-09-03T08:00:00Z")
	assertReadAt("issue-t", "2026-09-03T11:00:00Z")

	var internalCount int64
	if err := db.Model(&model.IssueReadState{}).Where("issue_id = ?", "issue-internal").Count(&internalCount).Error; err != nil {
		t.Fatalf("count internal: %v", err)
	}
	if internalCount != 0 {
		t.Fatalf("internal issue should not get a read watermark, got %d", internalCount)
	}
}

func TestParseSQLiteTimestamp(t *testing.T) {
	cases := []string{
		"2026-09-03T10:00:00Z",
		"2026-09-03T10:00:00+00:00",
		"2026-09-03 10:00:00+00:00",
		"2026-09-03 10:00:00",
	}
	want := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	for _, raw := range cases {
		got, err := parseSQLiteTimestamp(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if !got.Equal(want) {
			t.Fatalf("parse %q = %s, want %s", raw, got, want)
		}
	}
}
