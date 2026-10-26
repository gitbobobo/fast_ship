package main

import (
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 审查表加 integration_id 维度（INT-167）：所有审查键从 (project_id,
// repo_full_name, pr_number[, head_sha]) 扩为末尾（或 assets 的键首）带
// integration_id，让同名仓库在 GitHub 与 Forgejo 两个集成下各成一套账。
// integration_id 一律归一化存储：存量行全部是 GitHub 时代的产物，回填内置
// 字面量 model.ForgeIntegrationBuiltinGitHubID（= "github-default"）。
//
// 幂等：整表重建只在 PRAGMA 读出主键缺 integration_id 时执行一次；加列只在
// 列缺失时执行；回填与索引重建以现值为准，重复启动无副作用。必须在
// AutoMigrate 之前挂到启动序列（migrateReviewTables 之后）：重建出的就是
// 新结构，AutoMigrate 只补普通索引、不再动这些表。

const reviewIntegrationBackfill = "'github-default'"

// reviewPKRebuilds 主键要扩 integration_id 的三张表：SQLite 不能 ALTER 主键，
// 只能整表重建。新结构 DDL 与 model.ReviewPullRequest/ReviewDebounce/
// ReviewSeenSHA 的 AutoMigrate 产物逐列一致（PK 列在 SQLite 下不标 NOT NULL），
// 重建后 AutoMigrate 对这些表是空操作；带索引的模型索引（user_id/fire_at）
// 随表重建，其余手工索引由 createBootIndexes 的 IF NOT EXISTS 补齐。
var reviewPKRebuilds = []struct {
	table     string // 旧表名（也是重建后的表名）
	createDDL string // _new 表的建表语句（含新主键）
	// columns 是 INSERT SELECT 的旧列清单（integration_id 以常量补在末尾）。
	columns string
	indexes []string // 旧表上由模型标签建出的普通索引，重建后照原定义补回
}{
	{
		table: "review_pull_requests",
		createDDL: `CREATE TABLE review_pull_requests_new (` +
			"`project_id` text,`repo_full_name` text,`pr_number` integer,`integration_id` text," +
			"`user_id` text NOT NULL,`installation_id` integer," +
			"`round_number` integer NOT NULL DEFAULT 0,`head_sha` text," +
			"`code_comment_id` integer,`visual_comment_id` integer," +
			"`code_status` text,`visual_status` text,`visual_screens_fingerprint` text," +
			"`sync_pending` numeric NOT NULL DEFAULT false,`sync_epoch` integer NOT NULL DEFAULT 0," +
			"`sync_error` text,`sync_failed_epoch` integer NOT NULL DEFAULT 0," +
			"`updated_at` datetime NOT NULL," +
			"PRIMARY KEY (`project_id`,`repo_full_name`,`pr_number`,`integration_id`))",
		columns: "project_id, repo_full_name, pr_number, user_id, installation_id, round_number, head_sha, " +
			"code_comment_id, visual_comment_id, code_status, visual_status, visual_screens_fingerprint, " +
			"sync_pending, sync_epoch, sync_error, sync_failed_epoch, updated_at",
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_review_pull_requests_user_id ON review_pull_requests(user_id)",
		},
	},
	{
		table: "review_debounces",
		createDDL: `CREATE TABLE review_debounces_new (` +
			"`project_id` text,`repo_full_name` text,`pr_number` integer,`integration_id` text," +
			"`head_sha` text NOT NULL,`action` text NOT NULL,`installation_id` integer," +
			"`pr_title` text,`pr_url` text,`base_ref` text,`head_ref` text," +
			"`fire_at` datetime NOT NULL,`updated_at` datetime NOT NULL," +
			"PRIMARY KEY (`project_id`,`repo_full_name`,`pr_number`,`integration_id`))",
		columns: "project_id, repo_full_name, pr_number, head_sha, action, installation_id, " +
			"pr_title, pr_url, base_ref, head_ref, fire_at, updated_at",
		indexes: []string{
			"CREATE INDEX IF NOT EXISTS idx_review_debounces_fire_at ON review_debounces(fire_at)",
		},
	},
	{
		table: "review_seen_shas",
		createDDL: `CREATE TABLE review_seen_shas_new (` +
			"`project_id` text,`repo_full_name` text,`pr_number` integer,`head_sha` text," +
			"`integration_id` text,`pr_updated_at` datetime,`accepted_at` datetime NOT NULL," +
			"PRIMARY KEY (`project_id`,`repo_full_name`,`pr_number`,`head_sha`,`integration_id`))",
		columns: "project_id, repo_full_name, pr_number, head_sha, pr_updated_at, accepted_at",
	},
}

// migrateReviewIntegrationIDs 审查表的 integration_id 迁移入口（INT-167）：
// 三张键表整表重建、三张非键表加列，存量行统一回填 github-default，截图资产
// 唯一索引按新键（integration_id 键首）重建。首次建库（表还不存在）时全部
// 跳过，AutoMigrate 直接按新模型建表。
func migrateReviewIntegrationIDs(db *gorm.DB, logger *zap.Logger) error {
	for _, spec := range reviewPKRebuilds {
		newName := spec.table + "_new"
		exists, err := sqliteTableExists(db, spec.table)
		if err != nil {
			return err
		}
		if !exists {
			// 正式表缺失但 _new 残留：旧版实现 DROP/RENAME 之间崩溃的产物。
			// 数据在 _new 里完好，改名恢复成正式表，不让 AutoMigrate 建空表。
			orphan, err := sqliteTableExists(db, newName)
			if err != nil {
				return err
			}
			if !orphan {
				continue
			}
			if err := db.Exec(fmt.Sprintf("ALTER TABLE %q RENAME TO %q", newName, spec.table)).Error; err != nil {
				return fmt.Errorf("恢复残留 %s 失败: %w", newName, err)
			}
			if logger != nil {
				logger.Info("发现审查表迁移残留 _new，已恢复为正式表", zap.String("table", spec.table))
			}
			continue
		}
		pk, err := sqlitePrimaryKeyColumns(db, spec.table)
		if err != nil {
			return err
		}
		if pk["integration_id"] {
			// 已是新结构：正式表在、_new 只可能是历史崩溃残留，清掉后收工。
			if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %q", newName)).Error; err != nil {
				return fmt.Errorf("清理残留 %s 失败: %w", newName, err)
			}
			continue
		}
		// 整表重建：这三张表没有外键子行（只有它们指向 projects/users，没有
		// 反向引用），DROP/RENAME 不触发级联与悬空约束。全序列包一个事务：
		// SQLite 支持事务内 DDL，崩溃只留下「旧表原样」或「新表已改名」两
		// 种原子态，不会再把数据困在 _new 里。
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %q", newName)).Error; err != nil {
				return fmt.Errorf("清理 %s 失败: %w", newName, err)
			}
			if err := tx.Exec(spec.createDDL).Error; err != nil {
				return fmt.Errorf("重建 %s 失败: %w", spec.table, err)
			}
			insert := fmt.Sprintf("INSERT INTO %q (%s, integration_id) SELECT %s, %s FROM %q",
				newName, spec.columns, spec.columns, reviewIntegrationBackfill, spec.table)
			if err := tx.Exec(insert).Error; err != nil {
				return fmt.Errorf("回填 %s 存量行失败: %w", spec.table, err)
			}
			if err := tx.Exec(fmt.Sprintf("DROP TABLE %q", spec.table)).Error; err != nil {
				return fmt.Errorf("删除旧 %s 失败: %w", spec.table, err)
			}
			if err := tx.Exec(fmt.Sprintf("ALTER TABLE %q RENAME TO %q", newName, spec.table)).Error; err != nil {
				return fmt.Errorf("改回 %s 表名失败: %w", spec.table, err)
			}
			for _, idx := range spec.indexes {
				if err := tx.Exec(idx).Error; err != nil {
					return fmt.Errorf("重建 %s 索引失败: %w", spec.table, err)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if logger != nil {
			logger.Info("审查表主键已扩 integration_id，存量行回填 github-default",
				zap.String("table", spec.table))
		}
	}

	// 非键表加列（review_assets/review_runs/project_review_settings）：加
	// NOT NULL 列必须带非空默认——SQLite 只允许这种形态的 ADD COLUMN；默认
	// 顺带完成存量行回填，随后的 UPDATE 兜住 '' 值并与重复启动保持幂等。
	addColumnTables := []string{"review_assets", "review_runs", "project_review_settings"}
	for _, table := range addColumnTables {
		exists, err := sqliteTableExists(db, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		has, err := hasSQLiteColumn(db, table, "integration_id")
		if err != nil {
			return err
		}
		if !has {
			if err := db.Exec(fmt.Sprintf("ALTER TABLE %q ADD COLUMN integration_id text NOT NULL DEFAULT 'github-default'", table)).Error; err != nil {
				return fmt.Errorf("给 %s 加 integration_id 失败: %w", table, err)
			}
		}
		res := db.Exec(fmt.Sprintf("UPDATE %q SET integration_id = %s WHERE integration_id IS NULL OR integration_id = ''",
			table, reviewIntegrationBackfill))
		if res.Error != nil {
			return fmt.Errorf("回填 %s integration_id 失败: %w", table, res.Error)
		}
		if res.RowsAffected > 0 && logger != nil {
			logger.Info("审查表 integration_id 已回填", zap.String("table", table), zap.Int64("rows", res.RowsAffected))
		}
	}

	// 截图资产唯一索引换键（integration_id 键首，原 1-4 位顺延为 2-5）：旧索引
	// 不含新列，留着会让 AutoMigrate 按名跳过重建、并发去重退化为旧口径——
	// 只在检出的定义缺 integration_id 时 DROP，新定义由 AutoMigrate 按模型
	// 标签建回（本迁移早于 AutoMigrate 跑）。
	exists, err := sqliteTableExists(db, "review_assets")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	stale, err := sqliteIndexLacksColumn(db, "idx_review_assets_source", "integration_id")
	if err != nil || !stale {
		return err
	}
	if err := db.Exec("DROP INDEX IF EXISTS idx_review_assets_source").Error; err != nil {
		return fmt.Errorf("重建 idx_review_assets_source 失败: %w", err)
	}
	return nil
}

// sqliteIndexLacksColumn 判断索引存在且不含指定列；索引不存在返回 false。
func sqliteIndexLacksColumn(db *gorm.DB, index, column string) (bool, error) {
	var idxName string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, index).
		Scan(&idxName).Error; err != nil {
		return false, err
	}
	if idxName != index {
		return false, nil
	}
	var columns []sqliteColumnInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA index_info(%q)", index)).Scan(&columns).Error; err != nil {
		return false, fmt.Errorf("读取索引 %s 结构失败: %w", index, err)
	}
	for _, c := range columns {
		if c.Name == column {
			return false, nil
		}
	}
	return true, nil
}
