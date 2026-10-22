package main

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 存量库升级：AutoMigrate 给已有项目行留下的 NULL 必须回填为空串，且重复执行
// 不改变已配置的值。
func TestBackfillProjectCodeRepo(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	user := &model.User{ID: "u1", Username: "u", Email: "u@example.com"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// 模拟存量行：code 两列为 NULL。
	if err := db.Exec(`INSERT INTO projects (id, user_id, name, description, github_owner, github_repo,
		public_github_token_id, code_github_token_id, created_at, updated_at)
		VALUES ('p1', 'u1', 'old', '', 'octo', 'repo', '', '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("seed legacy project: %v", err)
	}
	var nulls int64
	if err := db.Raw(`SELECT COUNT(*) FROM projects WHERE code_github_owner IS NULL OR code_github_repo IS NULL`).
		Scan(&nulls).Error; err != nil || nulls != 1 {
		t.Fatalf("前置条件失败：期望 1 行 NULL, got %d (err=%v)", nulls, err)
	}

	if err := backfillProjectCodeRepo(db, zap.NewNop()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var project model.Project
	if err := db.Where("id = ?", "p1").First(&project).Error; err != nil {
		t.Fatalf("load project: %v", err)
	}
	if project.CodeGithubOwner != "" || project.CodeGithubRepo != "" {
		t.Fatalf("存量行应回填空串, got %q/%q", project.CodeGithubOwner, project.CodeGithubRepo)
	}
	if project.CodeRepoFullName() != "octo/repo" {
		t.Fatalf("回填后 CodeRepo 应回落到公开仓库, got %q", project.CodeRepoFullName())
	}

	// 已配置的值不被覆盖。
	if err := db.Exec(`UPDATE projects SET code_github_owner = 'code', code_github_repo = 'repo' WHERE id = 'p1'`).Error; err != nil {
		t.Fatalf("set code repo: %v", err)
	}
	if err := backfillProjectCodeRepo(db, zap.NewNop()); err != nil {
		t.Fatalf("backfill again: %v", err)
	}
	if err := db.Where("id = ?", "p1").First(&project).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if project.CodeGithubOwner != "code" || project.CodeGithubRepo != "repo" {
		t.Fatalf("重复执行不应覆盖已配置值, got %q/%q", project.CodeGithubOwner, project.CodeGithubRepo)
	}
}
