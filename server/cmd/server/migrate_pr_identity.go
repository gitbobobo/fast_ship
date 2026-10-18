package main

import (
	"fmt"
	"strings"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// migratePRReviewIdentity（INT-119 升级）：INT-93 发布的 github_pr_reviews /
// github_pr_seen_shas、INT-118 发布的 github_pr_debounces 主键都不含
// app_user_id，SQLite 改不了主键，必须备份式重建。必须先于 AutoMigrate：它
// 只会补列不会改主键，补完列的旧表上同 PR 的第二身份插入仍会撞旧主键，且旧行
// app_user_id 为 NULL 时 `app_user_id = ''` 查不到旧账。旧行身份统一回填 ''
// （= 部署 config 身份，与 INT-93 的全局身份语义一致）。与其它迁移一致：任何
// 一步失败返回错误 → 启动失败，宁可起不来也不带错跑。整段只用传入句柄顺序
// 执行（单连接池，不在嵌套事务里借池根）。

// sqlitePKColumn PRAGMA table_info 的列名与主键序号（pk > 0 即主键列）。
type sqlitePKColumn struct {
	Name string `gorm:"column:name"`
	PK   int    `gorm:"column:pk"`
}

// migratePRReviewIdentity 按 INT-119 身份主键要求重建三张存量账表；全新库
// （表不存在）与已升级库（app_user_id 已在主键里）都是 no-op，重复启动幂等。
// github_pr_debounces 依赖本文件之前已执行的 dropLegacyPRDebounces：它先把
// automation_id 旧主键的表整表丢弃，这里负责把 INT-118 的 trigger_id 主键、
// 无身份列的形态重建成含 app_user_id 的四列主键。
func migratePRReviewIdentity(db *gorm.DB, logger *zap.Logger) error {
	specs := []struct {
		table string
		mdl   interface{}
	}{
		{table: "github_pr_reviews", mdl: &model.GitHubPRReview{}},
		{table: "github_pr_seen_shas", mdl: &model.GitHubPRSeenSHA{}},
		{table: "github_pr_debounces", mdl: &model.GitHubPRDebounce{}},
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, spec := range specs {
			needs, err := prIdentityTableNeedsRebuild(tx, spec.table)
			if err != nil {
				return fmt.Errorf("检查表 %s 身份主键失败: %w", spec.table, err)
			}
			if !needs {
				continue
			}
			if err := rebuildPRIdentityTable(tx, spec.table, spec.mdl, logger); err != nil {
				return fmt.Errorf("重建表 %s 失败: %w", spec.table, err)
			}
		}
		return nil
	})
}

// prIdentityTableNeedsRebuild 表不存在（全新库交给 AutoMigrate 直接建）或
// app_user_id 已是主键一列（新建/升级过）→ 不需要重建；列存在但不在主键里
// （曾补列失败留下的中间形态）同样要重建。
func prIdentityTableNeedsRebuild(db *gorm.DB, table string) (bool, error) {
	exists, err := sqliteTableExists(db, table)
	if err != nil || !exists {
		return false, err
	}
	var cols []sqlitePKColumn
	if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%q)", table)).Scan(&cols).Error; err != nil {
		return false, err
	}
	for _, c := range cols {
		if c.Name == "app_user_id" {
			return c.PK == 0, nil
		}
	}
	return true, nil
}

// rebuildPRIdentityTable 备份式重建：改名旧表、清掉占全局名字的旧索引、按模型
// 建出新表（复合主键含 app_user_id）、共有列拷贝并把身份列回填为空串、删旧表。
// 单事务内执行（调用方提供 tx），失败整体回滚、数据不变。
func rebuildPRIdentityTable(tx *gorm.DB, table string, mdl interface{}, logger *zap.Logger) error {
	const identityCol = "app_user_id"
	legacy := table + "_legacy"

	oldCols, err := sqliteTableColumnNames(tx, table)
	if err != nil {
		return fmt.Errorf("读取表 %s 结构失败: %w", table, err)
	}
	if err := tx.Exec(fmt.Sprintf("ALTER TABLE %q RENAME TO %q", table, legacy)).Error; err != nil {
		return fmt.Errorf("重命名表 %s 失败: %w", table, err)
	}
	if err := dropSQLiteTableIndexes(tx, legacy); err != nil {
		return fmt.Errorf("删除表 %s 旧索引失败: %w", legacy, err)
	}
	if err := tx.Migrator().CreateTable(mdl); err != nil {
		return fmt.Errorf("按模型重建表 %s 失败: %w", table, err)
	}

	newCols, err := sqliteTableColumnNames(tx, table)
	if err != nil {
		return fmt.Errorf("读取重建表 %s 结构失败: %w", table, err)
	}
	oldSet := make(map[string]bool, len(oldCols))
	for _, c := range oldCols {
		oldSet[c] = true
	}
	// 身份列不进共有列：旧表要么没有它、要么主键不含它（值一律回填 ''），
	// 单独以字面量补进 INSERT，避免把 NULL 拷进 NOT NULL 新列。
	common := make([]string, 0, len(newCols))
	for _, c := range newCols {
		if c != identityCol && oldSet[c] {
			common = append(common, c)
		}
	}
	if len(common) == 0 {
		return fmt.Errorf("表 %s 重建后没有可拷贝的共有列", table)
	}
	quoted := make([]string, 0, len(common)+1)
	selects := make([]string, 0, len(common)+1)
	for _, c := range common {
		quoted = append(quoted, fmt.Sprintf("%q", c))
		selects = append(selects, fmt.Sprintf("%q", c))
	}
	quoted = append(quoted, fmt.Sprintf("%q", identityCol))
	if oldSet[identityCol] {
		// 中间形态（列在但主键不含）：保留已有值，NULL 回填 ''。
		selects = append(selects, fmt.Sprintf("COALESCE(%q, '')", identityCol))
	} else {
		selects = append(selects, "''")
	}
	copySQL := fmt.Sprintf("INSERT INTO %q (%s) SELECT %s FROM %q",
		table, strings.Join(quoted, ", "), strings.Join(selects, ", "), legacy)
	res := tx.Exec(copySQL)
	if res.Error != nil {
		return fmt.Errorf("拷贝表 %s 数据失败: %w", table, res.Error)
	}
	if err := tx.Exec(fmt.Sprintf("DROP TABLE %q", legacy)).Error; err != nil {
		return fmt.Errorf("删除旧表 %s 失败: %w", legacy, err)
	}

	logger.Info("PR 评审账已按验签身份重建（旧行回填 config 身份）",
		zap.String("table", table), zap.Int64("rows", res.RowsAffected))
	return nil
}
