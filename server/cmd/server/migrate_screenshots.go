package main

import (
	"fmt"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/service"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// screenshotMigrationStats 是折叠迁移的统计口径：dry-run 打印与执行完成日志
// 共用同一组只读 SQL，保证「将做什么」与「做了什么」数字一致。
type screenshotMigrationStats struct {
	Screens          int64
	VersionTotal     int64
	ObsoleteVersions int64
}

// collectScreenshotMigrationStats 只读统计存量多版本库：截图行数、旧版本总行数、
// 将被折叠删除的旧版本行数。表缺失（全新库）按 0 处理。
func collectScreenshotMigrationStats(db *gorm.DB) (screenshotMigrationStats, error) {
	var stats screenshotMigrationStats
	hasScreens, err := sqliteTableExists(db, "screenshot_screens")
	if err != nil {
		return stats, fmt.Errorf("检查 screenshot_screens 表: %w", err)
	}
	hasVersions, err := sqliteTableExists(db, "screenshot_versions")
	if err != nil {
		return stats, fmt.Errorf("检查 screenshot_versions 表: %w", err)
	}
	if hasVersions {
		if err := db.Raw(`SELECT COUNT(*) FROM screenshot_versions`).Scan(&stats.VersionTotal).Error; err != nil {
			return stats, fmt.Errorf("统计旧版本行失败: %w", err)
		}
		if err := db.Raw(`
			SELECT COUNT(*) FROM screenshot_versions v
			WHERE v.rowid < (SELECT MAX(v2.rowid) FROM screenshot_versions v2 WHERE v2.screen_id = v.screen_id)
		`).Scan(&stats.ObsoleteVersions).Error; err != nil {
			return stats, fmt.Errorf("统计将被删除的旧版本失败: %w", err)
		}
	}
	if hasScreens {
		if err := db.Raw(`SELECT COUNT(*) FROM screenshot_screens`).Scan(&stats.Screens).Error; err != nil {
			return stats, fmt.Errorf("统计截图行失败: %w", err)
		}
	}
	return stats, nil
}

// prepareScreenshotFoldMigration 在 AutoMigrate 之前给存量多版本库补上
// screenshot_screens 的四个文件列（可空、无默认值）。有数据行的旧表上加
// NOT NULL 无默认值的列会被 SQLite 拒绝，AutoMigrate 直接失败、启动中断；
// 先补可空列让 AutoMigrate 视为已存在而跳过，折叠回填后各行的文件信息
// 都来自最新版本（无版本的空壳行在折叠事务里删除）。
func prepareScreenshotFoldMigration(db *gorm.DB) error {
	exists, err := sqliteTableExists(db, "screenshot_screens")
	if err != nil || !exists {
		return err
	}
	for _, column := range []struct{ name, ddl string }{
		{"file_path", "TEXT"},
		{"mime_type", "TEXT"},
		{"file_size", "INTEGER"},
		{"uploaded_at", "DATETIME"},
	} {
		has, err := hasSQLiteColumn(db, "screenshot_screens", column.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if err := db.Exec(fmt.Sprintf("ALTER TABLE screenshot_screens ADD COLUMN %q %s", column.name, column.ddl)).Error; err != nil {
			return fmt.Errorf("补截图文件列 %s 失败: %w", column.name, err)
		}
	}
	return nil
}

// migrateScreenshots 把多版本截图库折叠为单文件截图（INT-99）：
// 每 screen 取最新版本（rowid 口径）的文件信息并入 screenshot_screens，
// group_name 迁入 platform（lower+trim，空→NULL），旧版本行连同表一并删除，
// 挂在旧版本上的 open 标注改挂 screen 并置 outdated。
// 依赖 AutoMigrate 已建好新列（platform/locale/theme、文件字段、outdated）；
// 随服务启动自动执行，幂等可重复（versions 表消失后即空操作）。
// 待删文件在删除事务提交前落 screenshot_pending_deletes 队列，提交后
// 统一出队删除；删除失败的行留待下次启动重试。
func migrateScreenshots(db *gorm.DB, fileStorage storage.Storage, logger *zap.Logger) error {
	exists, err := sqliteTableExists(db, "screenshot_versions")
	if err != nil {
		return fmt.Errorf("检查 screenshot_versions 表: %w", err)
	}

	stats, err := collectScreenshotMigrationStats(db)
	if err != nil {
		return err
	}

	folded := false
	if exists {
		if err := foldScreenshotVersions(db, logger); err != nil {
			return err
		}
		folded = true
	}

	// 折叠与遗留清理共用一个出口：本次折叠的待删文件与上次启动删除失败的
	// 遗留队列都在这里重试
	removed, err := drainScreenshotPendingDeletes(db, fileStorage, logger)
	if err != nil {
		return err
	}
	if folded {
		logger.Info("截图库迁移完成",
			zap.Int64("screens", stats.Screens),
			zap.Int64("versions_removed", stats.VersionTotal),
			zap.Int64("versions_obsolete", stats.ObsoleteVersions),
			zap.Int("files_removed", removed))
	} else if removed > 0 {
		logger.Info("清理上次启动遗留的待删截图文件", zap.Int("files_removed", removed))
	}
	return nil
}

// foldScreenshotVersions 在单个事务里完成折叠：文件信息并入 screen 行、
// 维度迁移、过期置位、遗留列清理与 versions 表删除。旧版本文件路径先写入
// screenshot_pending_deletes 再提交——回滚时文件与队列原样保留，提交后
// 由调用方出队删除，删除失败不丢路径。
func foldScreenshotVersions(db *gorm.DB, logger *zap.Logger) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// 1. 每 screen 折叠为最新版本：文件信息并入 screen 行（相关子查询取 rowid 最大者）
		if err := tx.Exec(`
			UPDATE screenshot_screens SET
				file_path = (SELECT v.file_path FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1),
				mime_type = (SELECT v.mime_type FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1),
				file_size = (SELECT v.file_size FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1),
				width = COALESCE((SELECT v.width FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1), 0),
				height = COALESCE((SELECT v.height FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1), 0),
				uploaded_by = (SELECT v.uploaded_by FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1),
				uploaded_at = (SELECT v.uploaded_at FROM screenshot_versions v WHERE v.screen_id = screenshot_screens.id ORDER BY v.rowid DESC LIMIT 1)
			WHERE id IN (SELECT DISTINCT screen_id FROM screenshot_versions)
		`).Error; err != nil {
			return fmt.Errorf("折叠最新版本到截图行失败: %w", err)
		}

		// 2. group_name 按规范化值迁入 platform（空串与 NULL 都保持未标注）；幂等
		if err := tx.Exec(`
			UPDATE screenshot_screens SET platform = lower(trim(group_name))
			WHERE platform IS NULL AND group_name IS NOT NULL AND trim(group_name) <> ''
		`).Error; err != nil {
			return fmt.Errorf("迁移 group_name 到 platform 失败: %w", err)
		}

		// 3. 挂在非最新版本上的 open 标注置过期（它们面对的是替换后的图）
		if err := tx.Exec(`
			UPDATE screenshot_annotations SET outdated = 1
			WHERE status = 'open'
			  AND version_id IS NOT NULL
			  AND EXISTS (
				SELECT 1 FROM screenshot_versions v
				WHERE v.id = screenshot_annotations.version_id
				  AND v.rowid < (SELECT MAX(v2.rowid) FROM screenshot_versions v2 WHERE v2.screen_id = v.screen_id)
			  )
		`).Error; err != nil {
			return fmt.Errorf("标记过期标注失败: %w", err)
		}

		// 4. 收集将被删除的旧版本文件路径（最新版本文件保留并继续被 screen 引用），
		//    提交前落待删队列；同一路径去重，出队时按行删除
		var obsoleteFilePaths []string
		if err := tx.Raw(`
			SELECT DISTINCT v.file_path FROM screenshot_versions v
			WHERE v.rowid < (SELECT MAX(v2.rowid) FROM screenshot_versions v2 WHERE v2.screen_id = v.screen_id)
		`).Scan(&obsoleteFilePaths).Error; err != nil {
			return fmt.Errorf("收集旧版本文件路径失败: %w", err)
		}
		if len(obsoleteFilePaths) > 0 {
			rows := make([]model.ScreenshotPendingDelete, 0, len(obsoleteFilePaths))
			for _, path := range obsoleteFilePaths {
				rows = append(rows, model.ScreenshotPendingDelete{Path: path, CreatedAt: time.Now().UTC()})
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
				return fmt.Errorf("登记待删截图文件失败: %w", err)
			}
		}

		// 5. 折叠后仍没有文件信息的截图行（无任何版本行的空壳）在新模型下
		//    无内容可展示，直接删除；旧 schema 里标注必挂版本、版本级联，
		//    这类行上不会有标注
		if err := tx.Exec(`DELETE FROM screenshot_screens WHERE file_path IS NULL`).Error; err != nil {
			return fmt.Errorf("删除无文件的空壳截图行失败: %w", err)
		}

		// 6. 标注表先摆脱 version_id 列与指向 versions 的外键（重建子表拷贝共有列），
		//    再删 versions 表。顺序关键：只要标注表还声明着 CASCADE 外键，
		//    DROP TABLE 的隐式 DELETE 就会连带删掉挂在旧版本上的标注。
		hasVersionID, err := hasSQLiteColumn(tx, "screenshot_annotations", "version_id")
		if err != nil {
			return fmt.Errorf("检查标注表 version_id 列: %w", err)
		}
		if hasVersionID {
			spec := legacyFKTable{table: "screenshot_annotations", model: &model.ScreenshotAnnotation{}}
			if err := rebuildTableForForeignKeys(tx, spec, logger); err != nil {
				return fmt.Errorf("重建标注表移除 version_id 失败: %w", err)
			}
		}
		if err := tx.Exec(`DROP TABLE screenshot_versions`).Error; err != nil {
			return fmt.Errorf("删除 screenshot_versions 表失败: %w", err)
		}

		// 7. 清掉截图表的遗留多版本列（模型已不含，留着只会误导排查）
		for _, column := range []string{"version_count", "group_name", "last_uploaded_at"} {
			has, err := hasSQLiteColumn(tx, "screenshot_screens", column)
			if err != nil {
				return fmt.Errorf("检查截图表 %s 列: %w", column, err)
			}
			if !has {
				continue
			}
			if err := tx.Exec(fmt.Sprintf(`ALTER TABLE screenshot_screens DROP COLUMN %q`, column)).Error; err != nil {
				return fmt.Errorf("删除截图表遗留列 %s 失败: %w", column, err)
			}
		}
		return nil
	})
}

// drainScreenshotPendingDeletes 出队待删截图文件（实现在 service，覆盖上传
// 失败落队与启动清理共用同一条路径）。文件已不存在视同清理成功。
func drainScreenshotPendingDeletes(db *gorm.DB, fileStorage storage.Storage, logger *zap.Logger) (int, error) {
	return service.DrainScreenshotPendingDeletes(db, fileStorage, logger)
}
