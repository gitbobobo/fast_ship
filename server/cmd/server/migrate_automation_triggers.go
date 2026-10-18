package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 自动化触发器拆表迁移（INT-118）：automations 上的 8 个触发器列迁到新表
// automation_triggers（每条自动化一行，position=0），automation_runs.trigger_id
// 回填为该触发器，github_pr_debounces 主键改为 (trigger_id, repo, pr)。
//
// 入口拆成两段、各在 runBootMigrations 的固定位置执行：
//   - dropLegacyPRDebounces：主 AutoMigrate 之前。SQLite 不允许 ADD COLUMN 挂
//     PRIMARY KEY，AutoMigrate 对已存在的旧防抖表只会追加列、换不了主键——先按
//     「缺 trigger_id 列」判定丢弃旧表，AutoMigrate 随后按新主键重建。防抖行是
//     秒级瞬态账本，升级时丢弃可接受。
//   - migrateAutomationTriggers：主 AutoMigrate 之后、createBootIndexes 之前。
//     此时 automation_triggers 表与 automation_runs.trigger_id 列已就绪，只搬数据。
//
// 幂等：全新库两个函数都是空操作；存量库以「automations 是否残留任一旧触发器
// 列」为闸门，迁移完成后旧列清空，下次启动不再进入。旧列清不掉唯一的正确方式
// 是整表重建（逐列 DROP COLUMN 撞上被外键引用的 issue_project_id 会失败，只 Warn
// 会把残留列连同级联删除风险永久留下——重建在事务里，失败即启动失败）。
// 重跑时回填按「已有触发器行即跳过」保持幂等，不产生重复触发器。

// legacyAutomationTriggerColumns 8 个待删除的旧触发器列。
var legacyAutomationTriggerColumns = []string{
	"trigger_type",
	"cron_expression",
	"timezone",
	"next_run_at",
	"issue_project_id",
	"event_filter_json",
	"model_selections_json",
	"debounce_seconds",
}

// legacyAutomationRow 是 automations 表旧触发器列的读取投影；模型已不含这些列，
// 迁移经 Raw SQL 访问。
type legacyAutomationRow struct {
	ID              string     `gorm:"column:id"`
	TriggerType     string     `gorm:"column:trigger_type"`
	CronExpression  *string    `gorm:"column:cron_expression"`
	Timezone        *string    `gorm:"column:timezone"`
	NextRunAt       *time.Time `gorm:"column:next_run_at"`
	IssueProjectID  *string    `gorm:"column:issue_project_id"`
	EventFilter     *string    `gorm:"column:event_filter_json"`
	ModelSelections *string    `gorm:"column:model_selections_json"`
	DebounceSeconds *int       `gorm:"column:debounce_seconds"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func migrateAutomationTriggers(db *gorm.DB, logger *zap.Logger) error {
	hasLegacy, err := automationsHasLegacyTriggerColumn(db)
	if err != nil {
		return err
	}
	if !hasLegacy {
		return nil
	}

	// 按实际残留的旧列拼 SELECT：上次 DROP 中途失败的库只剩部分旧列，引用已删
	// 列的查询会直接 no such column。缺列字段扫成零值即可——已回填的自动化在
	// 下面按「已有触发器行」跳过，不会用残缺值再建触发器。
	remaining := make([]string, 0, len(legacyAutomationTriggerColumns))
	for _, column := range legacyAutomationTriggerColumns {
		has, err := hasSQLiteColumn(db, "automations", column)
		if err != nil {
			return err
		}
		if has {
			remaining = append(remaining, column)
		}
	}
	quoted := make([]string, 0, len(remaining)+3)
	for _, column := range append([]string{"id", "created_at", "updated_at"}, remaining...) {
		quoted = append(quoted, fmt.Sprintf("%q", column))
	}
	var rows []legacyAutomationRow
	if err := db.Raw(fmt.Sprintf(
		"SELECT %s FROM automations ORDER BY created_at ASC",
		strings.Join(quoted, ", "),
	)).Scan(&rows).Error; err != nil {
		return fmt.Errorf("读取存量自动化失败: %w", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			row := &rows[i]
			// 幂等回填：闸门重开的重跑会再次走到这里，已有触发器行即复用
			// （position 最小/创建最早的那条），不再插入重复行。
			var existing model.AutomationTrigger
			err := tx.Where("automation_id = ?", row.ID).
				Order("position ASC, created_at ASC, id ASC").
				First(&existing).Error
			trigger := &existing
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 无触发器行且旧列为空 = 迁移后才创建的新形状自动化（合法地
				// 没有触发器），没有可回填的旧数据，跳过。
				if row.TriggerType == "" {
					continue
				}
				trigger = &model.AutomationTrigger{
					ID:                  uuid.NewString(),
					AutomationID:        row.ID,
					TriggerType:         model.AutomationTriggerType(row.TriggerType),
					Position:            0,
					CronExpression:      derefString(row.CronExpression),
					Timezone:            derefString(row.Timezone),
					NextRunAt:           row.NextRunAt,
					IssueProjectID:      row.IssueProjectID,
					EventFilterJSON:     derefString(row.EventFilter),
					ModelSelectionsJSON: derefString(row.ModelSelections),
					DebounceSeconds:     derefInt(row.DebounceSeconds),
					CreatedAt:           row.CreatedAt,
					UpdatedAt:           row.UpdatedAt,
				}
				if err := tx.Create(trigger).Error; err != nil {
					return fmt.Errorf("自动化 %s 回填触发器失败: %w", row.ID, err)
				}
			} else if err != nil {
				return fmt.Errorf("自动化 %s 查询已回填触发器失败: %w", row.ID, err)
			}
			// 存量运行行全部归到该自动化现有的最早触发器（只填空值，幂等；
			// 重跑时沿用已有触发器，trigger_id 不换新）。
			if err := tx.Exec(`
				UPDATE automation_runs SET trigger_id = ?
				WHERE automation_id = ? AND (trigger_id IS NULL OR trigger_id = '')
			`, trigger.ID, row.ID).Error; err != nil {
				return fmt.Errorf("自动化 %s 回填运行 trigger_id 失败: %w", row.ID, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// 整表重建去掉全部旧触发器列。逐列 DROP COLUMN 在旧列被外键引用时失败
	// （issue_project_id 携带 fk_automations_issue_project），残留列上的级联
	// 外键会让删项目误删自动化，所以必须在启动时一次清干净。
	if err := rebuildAutomationsWithoutLegacyTriggers(db); err != nil {
		return fmt.Errorf("automations 旧触发器列重建失败: %w", err)
	}
	logger.Info("自动化触发器拆表迁移完成", zap.Int("automations", len(rows)))
	return nil
}

// automationsHasLegacyTriggerColumn 报告 automations 是否残留任一旧触发器列。
// 闸门从「trigger_type 是否存在」放宽为「任一旧列存在」：已被逐列 DROP 清掉
// trigger_type、却把 issue_project_id 卡在外键上的库也要重进迁移，由整表重建收尾。
func automationsHasLegacyTriggerColumn(db *gorm.DB) (bool, error) {
	for _, column := range legacyAutomationTriggerColumns {
		has, err := hasSQLiteColumn(db, "automations", column)
		if err != nil {
			return false, err
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

// automationsCreateTableDDL 让 GORM 在一次性内存库里生成 automations 的建表
// DDL（外键、默认值、模型索引与 AutoMigrate 产物逐字一致），返回建表与建索引
// 的 SQL 文本。SQLite 不校验外键目标表存在性，无需先建 users/t3_environments。
func automationsCreateTableDDL() (string, []string, error) {
	tmp, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory"), &gorm.Config{})
	if err != nil {
		return "", nil, fmt.Errorf("打开临时库生成建表 DDL 失败: %w", err)
	}
	sqlDB, err := tmp.DB()
	if err != nil {
		return "", nil, err
	}
	defer sqlDB.Close()
	if err := tmp.Migrator().CreateTable(&model.Automation{}); err != nil {
		return "", nil, fmt.Errorf("生成 automations 建表 DDL 失败: %w", err)
	}
	var tableSQL string
	if err := tmp.Raw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'automations'`).
		Scan(&tableSQL).Error; err != nil {
		return "", nil, fmt.Errorf("读取 automations 建表 DDL 失败: %w", err)
	}
	if tableSQL == "" {
		return "", nil, fmt.Errorf("临时库里没有 automations 建表 DDL")
	}
	var indexSQLs []string
	if err := tmp.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'automations' AND sql IS NOT NULL`).
		Scan(&indexSQLs).Error; err != nil {
		return "", nil, fmt.Errorf("读取 automations 索引 DDL 失败: %w", err)
	}
	return tableSQL, indexSQLs, nil
}

// rebuildAutomationsWithoutLegacyTriggers 单表重建 automations：改名旧表、删掉
// 占名字的旧索引、按模型 DDL 建出新表（只含 users/t3_environments 外键与模型
// 索引）、拷贝共有列、删旧表（沿用 foreign_keys.go 的 rebuildTableForForeignKeys
// 套路，处理了反向引用）。
//
// 整个重建必须在专用连接上手工事务完成，不能用 db.Transaction：
//   - foreign_keys=ON 时 SQLite 会无视 legacy_alter_table——RENAME 把
//     automation_triggers / automation_runs 指向 automations 的外键改写成旧表名，
//     随后 DROP 旧表的隐式删除又沿该外键级联清空触发器与运行行（两者都实测过）；
//   - 而 PRAGMA foreign_keys 在事务内是显式 no-op，只能在事务外切换。
//
// 因此顺序是：连接级 PRAGMA（foreign_keys OFF + legacy_alter_table ON，均读回
// 确认生效）→ BEGIN → 全部 DDL → COMMIT → 恢复 PRAGMA。任何一步失败回滚并
// 让启动失败，不 Warn 继续。
func rebuildAutomationsWithoutLegacyTriggers(db *gorm.DB) error {
	const legacy = "automations__pre_int118"
	createTableSQL, indexSQLs, err := automationsCreateTableDDL()
	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("取底层连接池失败: %w", err)
	}
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("取迁移专用连接失败: %w", err)
	}
	defer conn.Close()
	exec := func(query string) error {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return err
		}
		return nil
	}

	if err := exec("PRAGMA foreign_keys = OFF"); err != nil {
		return fmt.Errorf("关闭外键约束失败: %w", err)
	}
	if err := exec("PRAGMA legacy_alter_table = ON"); err != nil {
		return fmt.Errorf("设置 legacy_alter_table 失败: %w", err)
	}
	// 两个 PRAGMA 都读回确认：任一被驱动吞掉，RENAME/DROP 就会按默认行为
	// 改写或级联子表，宁可失败重试也不能带病继续。
	var fkOn, legacyOn int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fkOn); err != nil {
		return fmt.Errorf("读取 foreign_keys 失败: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA legacy_alter_table").Scan(&legacyOn); err != nil {
		return fmt.Errorf("读取 legacy_alter_table 失败: %w", err)
	}
	if fkOn != 0 || legacyOn != 1 {
		return fmt.Errorf("重建 PRAGMA 设置未生效（foreign_keys=%d, legacy_alter_table=%d）", fkOn, legacyOn)
	}
	defer func() {
		_ = exec("PRAGMA legacy_alter_table = OFF")
		_ = exec("PRAGMA foreign_keys = ON")
	}()

	if err := exec("BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("开启重建事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = exec("ROLLBACK")
		}
	}()

	if err := exec(fmt.Sprintf("ALTER TABLE %q RENAME TO %q", "automations", legacy)); err != nil {
		return fmt.Errorf("重命名表 automations 失败: %w", err)
	}
	// 旧索引名是库内全局的，不删会与建表/建索引 DDL 撞名。
	if err := dropRawTableIndexes(ctx, conn, legacy); err != nil {
		return fmt.Errorf("删除表 automations 旧索引失败: %w", err)
	}
	if err := exec(createTableSQL); err != nil {
		return fmt.Errorf("重建表 automations 失败: %w", err)
	}
	for _, indexSQL := range indexSQLs {
		if err := exec(indexSQL); err != nil {
			return fmt.Errorf("重建 automations 索引失败: %w", err)
		}
	}

	// 拷贝共有列：新模型不含 8 个旧触发器列，自然丢弃。
	oldCols, err := rawTableColumnNames(ctx, conn, legacy)
	if err != nil {
		return fmt.Errorf("读取旧表 automations 结构失败: %w", err)
	}
	newCols, err := rawTableColumnNames(ctx, conn, "automations")
	if err != nil {
		return fmt.Errorf("读取重建表 automations 结构失败: %w", err)
	}
	oldSet := make(map[string]bool, len(oldCols))
	for _, c := range oldCols {
		oldSet[c] = true
	}
	quoted := make([]string, 0, len(newCols))
	for _, c := range newCols {
		if oldSet[c] {
			quoted = append(quoted, fmt.Sprintf("%q", c))
		}
	}
	if len(quoted) == 0 {
		return fmt.Errorf("表 automations 重建后没有可拷贝的共有列")
	}
	colList := strings.Join(quoted, ", ")
	if err := exec(fmt.Sprintf("INSERT INTO %q (%s) SELECT %s FROM %q", "automations", colList, colList, legacy)); err != nil {
		return fmt.Errorf("拷贝表 automations 数据失败: %w", err)
	}
	if err := exec(fmt.Sprintf("DROP TABLE %q", legacy)); err != nil {
		return fmt.Errorf("删除旧表 automations 失败: %w", err)
	}

	if err := exec("COMMIT"); err != nil {
		return fmt.Errorf("提交重建事务失败: %w", err)
	}
	committed = true

	// 完整性断言：重建后全库不允许有悬空外键引用（foreign_key_check 不受
	// foreign_keys 开关影响，对拷贝阶段未校验的行做最终核对）。
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("外键完整性检查失败: %w", err)
	}
	defer rows.Close()
	violations := 0
	firstTable := ""
	for rows.Next() {
		violations++
		if firstTable == "" {
			var table string
			var rest any
			if scanErr := rows.Scan(&table, &rest, &rest, &rest); scanErr == nil {
				firstTable = table
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("读取外键完整性检查结果失败: %w", err)
	}
	if violations > 0 {
		return fmt.Errorf("automations 重建后存在悬空外键引用（%d 行，先见于 %s）", violations, firstTable)
	}
	return nil
}

// dropRawTableIndexes 删掉表的全部手写索引（专用连接版本，见
// foreign_keys.go 的 dropSQLiteTableIndexes）。
func dropRawTableIndexes(ctx context.Context, conn *sql.Conn, table string) error {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA index_list(%q)", table))
	if err != nil {
		return err
	}
	names := make([]string, 0, 8)
	for rows.Next() {
		var name string
		var seq, unique int
		var origin, partial sql.NullString
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return err
		}
		if name == "" || strings.HasPrefix(name, "sqlite_autoindex") {
			continue
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range names {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf("DROP INDEX IF EXISTS %q", name)); err != nil {
			return err
		}
	}
	return nil
}

// rawTableColumnNames 读表的列名（专用连接版本，见 sqliteTableColumnNames）。
func rawTableColumnNames(ctx context.Context, conn *sql.Conn, table string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		var cid int
		var colType sql.NullString
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// dropLegacyPRDebounces 丢弃旧主键的防抖账本（缺 trigger_id 列即视为旧表）。
// 新表已带 trigger_id（全新库或已重建）时不动。
func dropLegacyPRDebounces(db *gorm.DB) error {
	hasTable, err := sqliteTableExists(db, "github_pr_debounces")
	if err != nil {
		return err
	}
	if !hasTable {
		return nil
	}
	hasTriggerID, err := hasSQLiteColumn(db, "github_pr_debounces", "trigger_id")
	if err != nil {
		return err
	}
	if hasTriggerID {
		return nil
	}
	if err := db.Exec("DROP TABLE IF EXISTS github_pr_debounces").Error; err != nil {
		return fmt.Errorf("丢弃旧防抖账本失败: %w", err)
	}
	return nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
