package main

import (
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
