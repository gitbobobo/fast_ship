package main

import (
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migrateReviewTables 在 AutoMigrate 之前整表删除通用自动化与旧 PR 评审账
// （INT-135）。拍板共识：直接删表、不迁移旧数据；幂等，重复执行无副作用。
//
// 必须先于 AutoMigrate：旧表上的外键指向已删模型，留着会让 AutoMigrate 的
// 隐式重建与索引清理互相干扰。
func migrateReviewTables(db *gorm.DB, logger *zap.Logger) error {
	legacyTables := []string{
		"automations",
		"automation_triggers",
		"automation_runs",
		"automation_run_tokens",
		"github_pr_reviews",
		"github_pr_debounces",
		"github_pr_seen_shas",
	}
	for _, table := range legacyTables {
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %q", table)).Error; err != nil {
			return fmt.Errorf("删除遗留表 %s 失败: %w", table, err)
		}
	}
	logger.Info("已删除通用自动化与旧 PR 评审遗留表", zap.Strings("tables", legacyTables))

	// 站内通知表保留，但旧自动化的通知 kind 已不再合法（只留三类审查故障），
	// 升级后这些行的文案与目标（automation_runs 等）都已失效：直接清掉。
	// 首次建库时 notifications 还没被 AutoMigrate 建出来，跳过。
	if exists, err := sqliteTableExists(db, "notifications"); err != nil {
		return err
	} else if exists {
		if err := db.Exec(`DELETE FROM notifications WHERE kind IN (
			'automation_dispatch_failed','automation_run_timeout','automation_post_failed','automation_busy','agent')`).Error; err != nil {
			return fmt.Errorf("清理旧自动化通知失败: %w", err)
		}
	}

	// 仓库键归一小写（INT-135 修复轮）：旧实现可能带着 GitHub 规范大小写
	// 写库，与 rerun 侧的用户输入大小写形成两套账本。幂等 UPDATE；表不
	// 存在（首次建库，AutoMigrate 尚未跑）时跳过。
	repoTables := []string{
		"review_seen_shas", "review_debounces", "review_runs",
		"review_pull_requests", "review_assets", "project_review_settings",
	}
	for _, table := range repoTables {
		exists, err := sqliteTableExists(db, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := db.Exec(fmt.Sprintf(
			"UPDATE %q SET repo_full_name = lower(repo_full_name) WHERE repo_full_name <> lower(repo_full_name)",
			table)).Error; err != nil {
			return fmt.Errorf("归一 %s.repo_full_name 失败: %w", table, err)
		}
	}
	return nil
}
