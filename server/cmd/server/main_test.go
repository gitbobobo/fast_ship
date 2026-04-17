package main

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
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
		"",
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
