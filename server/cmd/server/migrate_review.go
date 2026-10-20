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

	// 本 PR 新增的七张审查表在开发期为「账本按项目分区」演进过一次结构。
	// 只能在**检测到旧结构**时重建一次：这段迁移每次启动都会跑，无条件 DROP
	// 会把用户已有配置、运行与记录在每次重启时清空。
	// 旧结构判据 = review_pull_requests 的主键里没有 project_id。
	needsRebuild, err := reviewTablesNeedRebuild(db)
	if err != nil {
		return err
	}
	if needsRebuild {
		newReviewTables := []string{
			"review_runs",
			"review_run_tokens",
			"review_pull_requests",
			"review_assets",
			"review_debounces",
			"review_seen_shas",
			"project_review_settings",
		}
		for _, table := range newReviewTables {
			if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %q", table)).Error; err != nil {
				return fmt.Errorf("重建审查表 %s 失败: %w", table, err)
			}
		}
		logger.Info("检测到旧版审查表结构，已整表重建", zap.Strings("tables", newReviewTables))
	}

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
	return nil
}

// reviewTablesNeedRebuild 判断审查表是否仍是「账本未按项目分区」的旧结构。
// 表不存在（首次建库）或已是新结构时返回 false——迁移每次启动都跑，不能
// 误删用户数据。
func reviewTablesNeedRebuild(db *gorm.DB) (bool, error) {
	exists, err := sqliteTableExists(db, "review_pull_requests")
	if err != nil || !exists {
		return false, err
	}
	pk, err := sqlitePrimaryKeyColumns(db, "review_pull_requests")
	if err != nil {
		return false, err
	}
	return !pk["project_id"], nil
}

// sqlitePrimaryKeyColumns 读 PRAGMA table_info 的主键列集合。
func sqlitePrimaryKeyColumns(db *gorm.DB, table string) (map[string]bool, error) {
	type columnInfo struct {
		Name string `gorm:"column:name"`
		PK   int    `gorm:"column:pk"`
	}
	var rows []columnInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%q)", table)).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("读取表 %s 结构失败: %w", table, err)
	}
	out := map[string]bool{}
	for _, row := range rows {
		if row.PK > 0 {
			out[row.Name] = true
		}
	}
	return out, nil
}
