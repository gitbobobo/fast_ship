package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// fkExpectation 描述子表上应有的一条外键。on_delete 一律要求 CASCADE。
type fkExpectation struct {
	column   string
	refTable string
	refCol   string
}

// legacyFKTable 描述一张存量库可能需要重建的子表：表名、重建用模型、期望外键集。
type legacyFKTable struct {
	table    string
	model    interface{}
	expected []fkExpectation
}

// 双侧声明时 GORM 取父侧 constraint：前四张表父侧曾漏写 OnDelete:CASCADE，
// 在所有存量库上落成 NO ACTION；后三张表此前根本没有外键。
var legacyFKTables = []legacyFKTable{
	{table: "issue_github_meta", model: &model.IssueGitHubMeta{}, expected: []fkExpectation{{"issue_id", "issues", "id"}}},
	{table: "issue_comments", model: &model.IssueComment{}, expected: []fkExpectation{{"issue_id", "issues", "id"}}},
	{table: "issue_timeline_events", model: &model.IssueTimelineEvent{}, expected: []fkExpectation{{"issue_id", "issues", "id"}}},
	{table: "artifacts", model: &model.Artifact{}, expected: []fkExpectation{{"version_id", "versions", "id"}}},
	{table: "issue_read_states", model: &model.IssueReadState{}, expected: []fkExpectation{{"issue_id", "issues", "id"}, {"user_id", "users", "id"}}},
	{table: "issue_sync_states", model: &model.IssueSyncState{}, expected: []fkExpectation{{"project_id", "projects", "id"}}},
	{table: "github_repo_labels", model: &model.GitHubRepoLabel{}, expected: []fkExpectation{{"project_id", "projects", "id"}}},
}

type sqliteFKInfo struct {
	From     string `gorm:"column:from"`
	Table    string `gorm:"column:table"`
	To       string `gorm:"column:to"`
	OnDelete string `gorm:"column:on_delete"`
}

// repairLegacyForeignKeys 把存量库上与模型不符的子表外键修正为 ON DELETE CASCADE。
// 必须在 AutoMigrate 之前执行：AutoMigrate 会为缺失外键隐式整表重建，
// 悬空行尚未清理时会直接失败；先修完，AutoMigrate 才会把各表视为已合规而跳过。
func repairLegacyForeignKeys(db *gorm.DB, logger *zap.Logger, backupPath string) error {
	var pending []legacyFKTable
	for _, spec := range legacyFKTables {
		conforms, err := foreignKeysConform(db, spec)
		if err != nil {
			return fmt.Errorf("检查表 %s 外键失败: %w", spec.table, err)
		}
		if !conforms {
			pending = append(pending, spec)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	names := make([]string, 0, len(pending))
	for _, spec := range pending {
		names = append(names, spec.table)
	}
	logger.Warn("存量库外键与级联约定不符，开始重建子表", zap.Strings("tables", names))

	if backupPath != "" {
		if err := db.Exec("VACUUM INTO ?", backupPath).Error; err != nil {
			return fmt.Errorf("重建前备份数据库失败: %w", err)
		}
		logger.Info("重建前已备份数据库", zap.String("path", backupPath))
	}

	// 重建对象都是叶子子表、没有其他表反向引用它们，整个修复放进单个事务，
	// 并保持外键检查开启：拷贝阶段的 INSERT 顺带再校验一遍数据。
	return db.Transaction(func(tx *gorm.DB) error {
		for _, spec := range pending {
			if err := deleteDanglingForeignKeyRows(tx, spec, logger); err != nil {
				return err
			}
			if err := rebuildTableForForeignKeys(tx, spec, logger); err != nil {
				return err
			}
		}
		return nil
	})
}

// foreignKeysConform 报告表当前外键是否与期望完全一致（集合相等且全部 CASCADE）。
// 表不存在视为合规：交给 AutoMigrate 按新模型直接创建。
func foreignKeysConform(db *gorm.DB, spec legacyFKTable) (bool, error) {
	exists, err := sqliteTableExists(db, spec.table)
	if err != nil || !exists {
		return true, err
	}

	fks, err := sqliteForeignKeys(db, spec.table)
	if err != nil {
		return false, err
	}
	if len(fks) != len(spec.expected) {
		return false, nil
	}
	for _, want := range spec.expected {
		matched := false
		for _, fk := range fks {
			if fk.From == want.column && fk.Table == want.refTable && fk.To == want.refCol &&
				strings.EqualFold(fk.OnDelete, "CASCADE") {
				matched = true
				break
			}
		}
		if !matched {
			return false, nil
		}
	}
	return true, nil
}

func sqliteForeignKeys(db *gorm.DB, table string) ([]sqliteFKInfo, error) {
	var fks []sqliteFKInfo
	err := db.Raw(fmt.Sprintf("PRAGMA foreign_key_list(%q)", table)).Scan(&fks).Error
	return fks, err
}

// deleteDanglingForeignKeyRows 删除指向不存在父行的子行；不先清掉，新表的
// 外键会让拷贝或后续启动失败。删除数量写日志，不留数据副本。
func deleteDanglingForeignKeyRows(tx *gorm.DB, spec legacyFKTable, logger *zap.Logger) error {
	conds := make([]string, 0, len(spec.expected))
	for _, e := range spec.expected {
		conds = append(conds, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM %q WHERE %q.%q = %q.%q)",
			e.refTable, e.refTable, e.refCol, spec.table, e.column))
	}
	res := tx.Exec(fmt.Sprintf("DELETE FROM %q WHERE %s", spec.table, strings.Join(conds, " OR ")))
	if res.Error != nil {
		return fmt.Errorf("清理表 %s 悬空行失败: %w", spec.table, res.Error)
	}
	if res.RowsAffected > 0 {
		logger.Warn("已清理指向不存在父行的悬空行",
			zap.String("table", spec.table), zap.Int64("deleted", res.RowsAffected))
	}
	return nil
}

// rebuildTableForForeignKeys 单表重建：改名旧表、删掉占名字的旧索引、
// 按模型建出新表（含正确外键与模型索引）、拷贝共有列、删旧表。
func rebuildTableForForeignKeys(tx *gorm.DB, spec legacyFKTable, logger *zap.Logger) error {
	legacy := spec.table + "__pre_fk_repair"

	oldCols, err := sqliteTableColumnNames(tx, spec.table)
	if err != nil {
		return fmt.Errorf("读取表 %s 结构失败: %w", spec.table, err)
	}
	if err := tx.Exec(fmt.Sprintf("ALTER TABLE %q RENAME TO %q", spec.table, legacy)).Error; err != nil {
		return fmt.Errorf("重命名表 %s 失败: %w", spec.table, err)
	}
	if err := dropSQLiteTableIndexes(tx, legacy); err != nil {
		return fmt.Errorf("删除表 %s 旧索引失败: %w", spec.table, err)
	}
	if err := tx.Migrator().CreateTable(spec.model); err != nil {
		return fmt.Errorf("重建表 %s 失败: %w", spec.table, err)
	}

	newCols, err := sqliteTableColumnNames(tx, spec.table)
	if err != nil {
		return fmt.Errorf("读取重建表 %s 结构失败: %w", spec.table, err)
	}
	common := make([]string, 0, len(newCols))
	oldSet := make(map[string]bool, len(oldCols))
	for _, c := range oldCols {
		oldSet[c] = true
	}
	for _, c := range newCols {
		if oldSet[c] {
			common = append(common, c)
		}
	}
	if len(common) == 0 {
		return fmt.Errorf("表 %s 重建后没有可拷贝的共有列", spec.table)
	}
	quoted := make([]string, 0, len(common))
	for _, c := range common {
		quoted = append(quoted, fmt.Sprintf("%q", c))
	}
	colList := strings.Join(quoted, ", ")
	copySQL := fmt.Sprintf("INSERT INTO %q (%s) SELECT %s FROM %q", spec.table, colList, colList, legacy)
	res := tx.Exec(copySQL)
	if res.Error != nil {
		return fmt.Errorf("拷贝表 %s 数据失败: %w", spec.table, res.Error)
	}
	if err := tx.Exec(fmt.Sprintf("DROP TABLE %q", legacy)).Error; err != nil {
		return fmt.Errorf("删除旧表 %s 失败: %w", spec.table, err)
	}

	logger.Info("子表已按级联约定重建",
		zap.String("table", spec.table), zap.Int64("rows", res.RowsAffected))
	return nil
}

func sqliteTableColumnNames(db *gorm.DB, table string) ([]string, error) {
	var cols []sqliteColumnInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%q)", table)).Scan(&cols).Error; err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.Name)
	}
	return names, nil
}

// dropSQLiteTableIndexes 删掉表的全部手写索引；索引名是库内全局的，
// 不删会与新表要建的同名索引冲突。sqlite_autoindex 随表走，不用也不能删。
func dropSQLiteTableIndexes(db *gorm.DB, table string) error {
	var indexes []sqliteIndexInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA index_list(%q)", table)).Scan(&indexes).Error; err != nil {
		return err
	}
	for _, idx := range indexes {
		if idx.Name == "" || strings.HasPrefix(idx.Name, "sqlite_autoindex") {
			continue
		}
		if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %q", idx.Name)).Error; err != nil {
			return err
		}
	}
	return nil
}

// foreignKeyBackupPath 生成重建前备份文件名：data/fast_ship.db.bak-<UTC时间>。
// 重名（同秒重启重试、手工备份撞名）就追加序号；VACUUM INTO 对已存在文件报错，
// 不避让会让进程进入致命的启动循环。备份不自动删除。
func foreignKeyBackupPath(dbPath string) string {
	ts := time.Now().UTC().Format("20060102-150405")
	path := fmt.Sprintf("%s.bak-%s", dbPath, ts)
	for i := 1; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = fmt.Sprintf("%s.bak-%s-%d", dbPath, ts, i)
	}
}
