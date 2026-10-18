package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 存量库升级路径（INT-118）：按旧 automations/runs/github_pr_debounces 形状建表
// 并写入数据，走 runBootMigrations 完整启动迁移，断言触发器行存在、run.trigger_id
// 已回填、旧列消失、新索引就位。
//
// 旧表用影子模型 AutoMigrate 建（而非手写 DDL）：GORM 对已存在表的差异判定
// 极其敏感，影子模型生成的 DDL 与当年版本逐字节一致，才能代表真实的存量库。

// legacyAutomation INT-118 之前的 automations 模型投影（含 8 个触发器列）。
type legacyAutomation struct {
	ID        string    `gorm:"type:text;primaryKey" json:"id"`
	UserID    string    `gorm:"type:text;not null" json:"user_id"`
	Name      string    `gorm:"type:text;not null" json:"name"`
	Enabled   bool      `gorm:"not null;default:false" json:"enabled"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	EnvironmentID string `gorm:"type:text;not null" json:"environment_id"`
	T3ProjectID   string `gorm:"column:t3_project_id;type:text;not null" json:"t3_project_id"`

	TriggerType    string     `gorm:"column:trigger_type;type:text;not null" json:"trigger_type"`
	CronExpression string     `gorm:"column:cron_expression;type:text" json:"cron_expression"`
	Timezone       string     `gorm:"type:text" json:"timezone"`
	NextRunAt      *time.Time `gorm:"index" json:"next_run_at"`

	IssueProjectID  *string `gorm:"column:issue_project_id;type:text" json:"issue_project_id,omitempty"`
	EventFilterJSON string  `gorm:"column:event_filter_json;type:text" json:"event_filter_json,omitempty"`

	PromptTemplate      string `gorm:"column:prompt_template;type:text;not null" json:"prompt_template"`
	ModelSelectionJSON  string `gorm:"column:model_selection_json;type:text" json:"model_selection_json,omitempty"`
	ModelSelectionsJSON string `gorm:"column:model_selections_json;type:text" json:"model_selections_json,omitempty"`
	DebounceSeconds     int    `gorm:"not null;default:0" json:"debounce_seconds"`
	RuntimeMode         string `gorm:"type:text;not null;default:full-access" json:"runtime_mode"`
	WorkspaceJSON       string `gorm:"column:workspace_json;type:text" json:"workspace_json,omitempty"`
	TimeoutSeconds      int    `gorm:"not null;default:3600" json:"timeout_seconds"`
	PostAction          string `gorm:"column:post_action;type:text;not null;default:none" json:"post_action"`
	PostOnTimeout       bool   `gorm:"column:post_on_timeout;not null;default:false" json:"post_on_timeout"`

	// 复刻基线的 IssueProject 关联：AutoMigrate 据此建出
	// fk_automations_issue_project（CASCADE 指向 projects）。没有它，测试库建
	// 不出真实存量库的形状，也就复现不了「DROP COLUMN 撞外键失败」的缺陷。
	IssueProject *model.Project `gorm:"foreignKey:IssueProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (legacyAutomation) TableName() string { return "automations" }

// legacyAutomationRun INT-118 之前的 automation_runs（无 trigger_id 列）。
type legacyAutomationRun struct {
	ID                 string    `gorm:"type:text;primaryKey" json:"id"`
	UserID             string    `gorm:"type:text;not null;index" json:"user_id"`
	AutomationID       string    `gorm:"column:automation_id;type:text;not null" json:"automation_id"`
	TriggerType        string    `gorm:"type:text;not null" json:"trigger_type"`
	TriggerKey         string    `gorm:"column:trigger_key;type:text;not null" json:"trigger_key"`
	TriggerContextJSON string    `gorm:"column:trigger_context_json;type:text" json:"trigger_context_json"`
	TriggeredAt        time.Time `gorm:"not null" json:"triggered_at"`
	ConfigSnapshotJSON string    `gorm:"column:config_snapshot_json;type:text;not null" json:"config_snapshot_json"`
	EnvironmentID      string    `gorm:"type:text;not null" json:"environment_id"`
	T3ProjectID        string    `gorm:"column:t3_project_id;type:text;not null" json:"t3_project_id"`
	ThreadID           string    `gorm:"column:thread_id;type:text;not null" json:"thread_id"`
	LaunchCommandID    string    `gorm:"column:launch_command_id;type:text;not null" json:"launch_command_id"`
	InitialMessageID   string    `gorm:"column:initial_message_id;type:text;not null" json:"initial_message_id"`
	Status             string    `gorm:"type:text;not null;default:''" json:"status"`
	DeadlineAt         time.Time `gorm:"not null" json:"deadline_at"`
	PostState          string    `gorm:"column:post_state;type:text;not null;default:none" json:"post_state"`
	PostAttempt        int       `gorm:"column:post_attempt;not null;default:0" json:"post_attempt"`
}

func (legacyAutomationRun) TableName() string { return "automation_runs" }

// legacyAutomationDebounce 旧主键 (automation_id, repo, pr) 的防抖账本。
type legacyAutomationDebounce struct {
	AutomationID   string    `gorm:"column:automation_id;type:text;primaryKey" json:"automation_id"`
	RepoFullName   string    `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber       int       `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	UserID         string    `gorm:"column:user_id;type:text;not null" json:"user_id"`
	ProjectID      string    `gorm:"column:project_id;type:text;not null" json:"project_id"`
	InstallationID int64     `gorm:"column:installation_id;not null" json:"installation_id"`
	Action         string    `gorm:"column:action;type:text;not null" json:"action"`
	HeadSHA        string    `gorm:"column:head_sha;type:text;not null" json:"head_sha"`
	FireAt         time.Time `gorm:"column:fire_at;not null;index" json:"fire_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (legacyAutomationDebounce) TableName() string { return "github_pr_debounces" }

// createLegacyAutomationSchema 建旧形状的表 + 旧启动索引。User/T3Environment/
// Project 用真模型建（automations 的外键目标，AutoMigrate 重建 automations 加
// 外键时要求引用行真实存在——与存量库一致）。
func createLegacyAutomationSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&model.User{}, &model.T3Environment{}, &model.Project{},
		&legacyAutomation{}, &legacyAutomationRun{}, &legacyAutomationDebounce{}); err != nil {
		t.Fatalf("migrate legacy shapes: %v", err)
	}
	// 旧 createBootIndexes 登记的两个触发器复合索引（引用即将删除的列）。
	if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_automations_enabled_trigger_next ON automations(enabled, trigger_type, next_run_at)").Error; err != nil {
		t.Fatalf("create legacy boot index 1: %v", err)
	}
	if err := db.Exec("CREATE INDEX IF NOT EXISTS idx_automations_user_trigger_project ON automations(user_id, trigger_type, issue_project_id)").Error; err != nil {
		t.Fatalf("create legacy boot index 2: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_automation_trigger ON automation_runs(automation_id, trigger_key)").Error; err != nil {
		t.Fatalf("create legacy run key index: %v", err)
	}
}

// seedLegacyUserAndEnv 落真实的外键目标行并返回。
func seedLegacyUserAndEnv(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&model.User{
		ID: "user-1", Username: "legacy-user", Email: "legacy@t", PasswordHash: "x",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.T3Environment{
		ID: "env-1", UserID: "user-1", Name: "legacy-env",
		Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed env: %v", err)
	}
}

func openAutomationMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func TestMigrateAutomationTriggers_BackfillsLegacyRows(t *testing.T) {
	db := openAutomationMigrationDB(t)
	createLegacyAutomationSchema(t, db)
	seedLegacyUserAndEnv(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	stale := now.Add(-time.Hour)
	if err := db.Exec(`
		INSERT INTO automations (id, user_id, name, enabled, created_at, updated_at,
			environment_id, t3_project_id, trigger_type, cron_expression, timezone, next_run_at,
			issue_project_id, event_filter_json, prompt_template, model_selections_json,
			debounce_seconds, runtime_mode, timeout_seconds, post_action, post_on_timeout)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "aut-1", "user-1", "夜间回报", true, now, now,
		"env-1", "t3-proj", "cron", "*/10 * * * *", "Asia/Shanghai", stale,
		nil, nil, "处理 {{issue_title}}", nil,
		0, "full-access", 3600, "none", false).Error; err != nil {
		t.Fatalf("insert legacy automation: %v", err)
	}
	for _, key := range []string{"cron-1", "event-e1"} {
		if err := db.Exec(`
			INSERT INTO automation_runs (id, user_id, automation_id, trigger_type, trigger_key,
				triggered_at, config_snapshot_json, environment_id, t3_project_id, thread_id,
				launch_command_id, initial_message_id, status, deadline_at, post_state, post_attempt)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "run-"+key, "user-1", "aut-1", "cron", key,
			now, "{}", "env-1", "t3-proj", "th-"+key,
			"cmd-"+key, "msg-"+key, "", now.Add(time.Hour), "none", 0).Error; err != nil {
			t.Fatalf("insert legacy run: %v", err)
		}
	}
	if err := db.Exec(`
		INSERT INTO github_pr_debounces (automation_id, repo_full_name, pr_number, user_id,
			project_id, installation_id, action, head_sha, fire_at, updated_at)
		VALUES ('aut-1', 'octocat/hw', 1, 'user-1', 'proj-1', 7, 'opened', 'sha-1', ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert legacy debounce: %v", err)
	}

	// 完整启动迁移（dry-run=false）。旧库形状 + 新模型：AutoMigrate 建新表/新列，
	// 迁移函数回填数据，createBootIndexes 建新索引。
	store := storage.NewLocalStorage(t.TempDir())
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("runBootMigrations: %v", err)
	}

	// 触发器行存在且复制了旧列值。
	var trigger model.AutomationTrigger
	if err := db.Where("automation_id = ?", "aut-1").First(&trigger).Error; err != nil {
		t.Fatalf("migrated trigger missing: %v", err)
	}
	if trigger.TriggerType != model.AutomationTriggerCron {
		t.Fatalf("trigger type = %s, want cron", trigger.TriggerType)
	}
	if trigger.CronExpression != "*/10 * * * *" || trigger.Timezone != "Asia/Shanghai" {
		t.Fatalf("cron fields not copied: %+v", trigger)
	}
	if trigger.NextRunAt == nil || !trigger.NextRunAt.Equal(stale) {
		t.Fatalf("next_run_at = %v, want %v", trigger.NextRunAt, stale)
	}
	if trigger.Position != 0 {
		t.Fatalf("position = %d, want 0", trigger.Position)
	}

	// 运行行 trigger_id 已回填。
	var runs []model.AutomationRun
	if err := db.Where("automation_id = ?", "aut-1").Find(&runs).Error; err != nil {
		t.Fatalf("load runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	for _, run := range runs {
		if run.TriggerID != trigger.ID {
			t.Fatalf("run %s trigger_id = %q, want %q", run.ID, run.TriggerID, trigger.ID)
		}
	}

	// 旧列已删除。
	for _, column := range legacyAutomationTriggerColumns {
		has, err := hasSQLiteColumn(db, "automations", column)
		if err != nil {
			t.Fatalf("check column %s: %v", column, err)
		}
		if has {
			t.Fatalf("legacy column %s still present after migration", column)
		}
	}

	// 引用旧列的索引已删除、新索引存在。
	assertIndexAbsent(t, db, "idx_automations_enabled_trigger_next")
	assertIndexAbsent(t, db, "idx_automations_user_trigger_project")
	assertIndexAbsent(t, db, "idx_automations_next_run_at")
	assertIndexAbsent(t, db, "idx_automation_runs_automation_trigger")
	assertIndexPresent(t, db, "idx_automation_triggers_cron_next")
	assertIndexPresent(t, db, "idx_automation_triggers_event")
	assertIndexPresent(t, db, "idx_automation_triggers_automation")
	assertIndexPresent(t, db, "idx_automation_runs_trigger_key")

	// 防抖账本按新主键重建（旧行丢弃、trigger_id 列存在且参与主键；INT-119
	// 之后新表还带 app_user_id 第四列主键，全新库由 AutoMigrate 直接建成）。
	hasTriggerID, err := hasSQLiteColumn(db, "github_pr_debounces", "trigger_id")
	if err != nil {
		t.Fatalf("check debounce trigger_id: %v", err)
	}
	if !hasTriggerID {
		t.Fatal("github_pr_debounces must be rebuilt with trigger_id column")
	}
	debouncePK := sqlitePrimaryKeyColumns(t, db, "github_pr_debounces")
	if len(debouncePK) != 4 || debouncePK[0] != "trigger_id" || debouncePK[3] != "app_user_id" {
		t.Fatalf("debounce PK = %v, want trigger_id first and app_user_id last", debouncePK)
	}
	var debounceCount int64
	if err := db.Model(&model.GitHubPRDebounce{}).Count(&debounceCount).Error; err != nil {
		t.Fatalf("count debounces: %v", err)
	}
	if debounceCount != 0 {
		t.Fatalf("debounce rows = %d, want 0 (瞬态账本升级时丢弃)", debounceCount)
	}

	// 新形状可正常写入：迁移后再建一个触发器。
	follow := &model.AutomationTrigger{
		ID: uuid.NewString(), AutomationID: "aut-1",
		TriggerType: model.AutomationTriggerIssueCreated,
		CreatedAt:   now, UpdatedAt: now,
	}
	if err := db.Create(follow).Error; err != nil {
		t.Fatalf("create trigger after migration: %v", err)
	}
}

// 关键回归（INT-118 双模型审查复现）：存量库 automations 带
// fk_automations_issue_project（CASCADE→projects）。逐列 DROP COLUMN 会在
// issue_project_id 上失败（表定义含悬空外键），列与外键永久残留；此后删除被
// 旧 automation 引用的 project 会级联删掉整条自动化。迁移必须整表重建：
// 旧列清空、automations 不再引用 projects、删项目只影响明确以它为范围的触发器。
func TestMigrateAutomationTriggers_RebuildRemovesProjectForeignKey(t *testing.T) {
	db := openAutomationMigrationDB(t)
	createLegacyAutomationSchema(t, db)
	seedLegacyUserAndEnv(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	stale := now.Add(-time.Hour)
	if err := db.Create(&model.Project{
		ID: "proj-1", UserID: "user-1", Name: "旧项目", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// 两条存量自动化：aut-proj 的项目范围指向 proj-1（被删对象），
	// aut-free 无项目范围（与 proj-1 无关，作为整链路存活的对照）。
	if err := db.Exec(`
		INSERT INTO automations (id, user_id, name, enabled, created_at, updated_at,
			environment_id, t3_project_id, trigger_type, issue_project_id,
			prompt_template, debounce_seconds, runtime_mode, timeout_seconds,
			post_action, post_on_timeout)
		VALUES ('aut-proj', 'user-1', '项目内事件', true, ?, ?, 'env-1', 't3-proj',
			'issue_created', 'proj-1', '处理 {{issue_title}}', 0, 'full-access', 3600,
			'none', false)
	`, now, now).Error; err != nil {
		t.Fatalf("insert scoped automation: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO automations (id, user_id, name, enabled, created_at, updated_at,
			environment_id, t3_project_id, trigger_type, cron_expression, timezone,
			next_run_at, issue_project_id, prompt_template, debounce_seconds,
			runtime_mode, timeout_seconds, post_action, post_on_timeout)
		VALUES ('aut-free', 'user-1', '夜间回报', true, ?, ?, 'env-1', 't3-proj',
			'cron', '*/10 * * * *', 'Asia/Shanghai', ?, NULL, '处理 {{issue_title}}',
			0, 'full-access', 3600, 'none', false)
	`, now, now, stale).Error; err != nil {
		t.Fatalf("insert unscoped automation: %v", err)
	}
	for _, r := range []struct{ id, autID, key string }{
		{"run-proj-1", "aut-proj", "event-p1"},
		{"run-free-1", "aut-free", "cron-1"},
		{"run-free-2", "aut-free", "event-f1"},
	} {
		if err := db.Exec(`
			INSERT INTO automation_runs (id, user_id, automation_id, trigger_type, trigger_key,
				triggered_at, config_snapshot_json, environment_id, t3_project_id, thread_id,
				launch_command_id, initial_message_id, status, deadline_at, post_state, post_attempt)
			VALUES (?, 'user-1', ?, 'issue_created', ?, ?, '{}', 'env-1', 't3-proj', ?,
				'cmd', 'msg', '', ?, 'none', 0)
		`, r.id, r.autID, r.key, now, "th-"+r.id, now.Add(time.Hour)).Error; err != nil {
			t.Fatalf("insert legacy run %s: %v", r.id, err)
		}
	}

	store := storage.NewLocalStorage(t.TempDir())
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("runBootMigrations: %v", err)
	}

	// 8 个旧列全部消失。
	for _, column := range legacyAutomationTriggerColumns {
		has, err := hasSQLiteColumn(db, "automations", column)
		if err != nil {
			t.Fatalf("check column %s: %v", column, err)
		}
		if has {
			t.Fatalf("legacy column %s still present after rebuild", column)
		}
	}

	// automations 的外键只剩 users / t3_environments，不再引用 projects。
	fks, err := sqliteForeignKeys(db, "automations")
	if err != nil {
		t.Fatalf("foreign_key_list(automations): %v", err)
	}
	if len(fks) != 2 {
		t.Fatalf("automations FK count = %d, want 2 (users, t3_environments)", len(fks))
	}
	for _, fk := range fks {
		if fk.Table == "projects" {
			t.Fatalf("automations still references projects via %s", fk.From)
		}
	}

	// 数据完整：自动化、触发器、运行都在，run.trigger_id 已回填。
	var triggers []model.AutomationTrigger
	if err := db.Order("automation_id ASC, position ASC").Find(&triggers).Error; err != nil {
		t.Fatalf("load triggers: %v", err)
	}
	if len(triggers) != 2 {
		t.Fatalf("triggers = %d, want 2", len(triggers))
	}
	triggersByAutomation := map[string]model.AutomationTrigger{}
	for _, tr := range triggers {
		triggersByAutomation[tr.AutomationID] = tr
	}
	cronTrigger, ok := triggersByAutomation["aut-free"]
	if !ok || cronTrigger.TriggerType != model.AutomationTriggerCron ||
		cronTrigger.CronExpression != "*/10 * * * *" || cronTrigger.Timezone != "Asia/Shanghai" {
		t.Fatalf("unscoped trigger not migrated: %+v", cronTrigger)
	}
	scopedTrigger, ok := triggersByAutomation["aut-proj"]
	if !ok || scopedTrigger.IssueProjectID == nil || *scopedTrigger.IssueProjectID != "proj-1" {
		t.Fatalf("scoped trigger not migrated with project scope: %+v", scopedTrigger)
	}
	var runs []model.AutomationRun
	if err := db.Order("id ASC").Find(&runs).Error; err != nil {
		t.Fatalf("load runs: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
	for _, run := range runs {
		want := triggersByAutomation[run.AutomationID].ID
		if run.TriggerID != want {
			t.Fatalf("run %s trigger_id = %q, want %q", run.ID, run.TriggerID, want)
		}
	}

	// 关键回归：删除被旧 automation 引用的项目。automations 已无 projects 外键，
	// 自动化与运行历史必须存活；范围指向该项目的触发器按新语义连带删除，
	// 无关自动化（aut-free）的触发器不受影响。
	if err := db.Exec(`DELETE FROM projects WHERE id = ?`, "proj-1").Error; err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for _, id := range []string{"aut-proj", "aut-free"} {
		var n int64
		if err := db.Model(&model.Automation{}).Where("id = ?", id).Count(&n).Error; err != nil {
			t.Fatalf("count automation %s: %v", id, err)
		}
		if n != 1 {
			t.Fatalf("automation %s deleted by project delete (cascade regression)", id)
		}
	}
	var scopedLeft, freeLeft int64
	if err := db.Model(&model.AutomationTrigger{}).Where("automation_id = ?", "aut-proj").Count(&scopedLeft).Error; err != nil {
		t.Fatalf("count scoped triggers: %v", err)
	}
	if err := db.Model(&model.AutomationTrigger{}).Where("automation_id = ?", "aut-free").Count(&freeLeft).Error; err != nil {
		t.Fatalf("count unscoped triggers: %v", err)
	}
	if scopedLeft != 0 {
		t.Fatalf("scoped triggers = %d, want 0（删项目按新语义带走项目范围触发器）", scopedLeft)
	}
	if freeLeft != 1 {
		t.Fatalf("unscoped triggers = %d, want 1", freeLeft)
	}
	var runLeft int64
	if err := db.Model(&model.AutomationRun{}).Where("automation_id = ?", "aut-proj").Count(&runLeft).Error; err != nil {
		t.Fatalf("count runs after project delete: %v", err)
	}
	if runLeft != 1 {
		t.Fatalf("scoped runs after project delete = %d, want 1（弱引用历史保留）", runLeft)
	}

	// 全库无悬空外键引用。
	var violations []struct {
		Table string `gorm:"column:table"`
	}
	if err := db.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("foreign_key_check reported %d violations", len(violations))
	}
}

// 迁移幂等：跑两遍，第二遍不再产生触发器行、不报错。
func TestMigrateAutomationTriggers_Idempotent(t *testing.T) {
	db := openAutomationMigrationDB(t)
	createLegacyAutomationSchema(t, db)
	seedLegacyUserAndEnv(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec(`
		INSERT INTO automations (id, user_id, name, enabled, created_at, updated_at,
			environment_id, t3_project_id, trigger_type, prompt_template,
			debounce_seconds, runtime_mode, timeout_seconds, post_action, post_on_timeout)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "aut-1", "user-1", "夜间回报", true, now, now,
		"env-1", "t3-proj", "issue_created", "处理 {{issue_title}}",
		0, "full-access", 3600, "none", false).Error; err != nil {
		t.Fatalf("insert legacy automation: %v", err)
	}

	store := storage.NewLocalStorage(t.TempDir())
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("first runBootMigrations: %v", err)
	}
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("second runBootMigrations: %v", err)
	}
	var triggerCount int64
	if err := db.Model(&model.AutomationTrigger{}).Count(&triggerCount).Error; err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if triggerCount != 1 {
		t.Fatalf("triggers after double migration = %d, want 1", triggerCount)
	}
}

// 部分迁移重跑：DROP trigger_type 失败（代码只 Warn）时幂等闸门重开，重跑
// 不得重复回填触发器，运行行的 trigger_id 也不得换新。
func TestMigrateAutomationTriggers_PartialDropRerunDoesNotDuplicate(t *testing.T) {
	db := openAutomationMigrationDB(t)
	createLegacyAutomationSchema(t, db)
	seedLegacyUserAndEnv(t, db)

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec(`
		INSERT INTO automations (id, user_id, name, enabled, created_at, updated_at,
			environment_id, t3_project_id, trigger_type, prompt_template,
			debounce_seconds, runtime_mode, timeout_seconds, post_action, post_on_timeout)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "aut-1", "user-1", "夜间回报", true, now, now,
		"env-1", "t3-proj", "issue_created", "处理 {{issue_title}}",
		0, "full-access", 3600, "none", false).Error; err != nil {
		t.Fatalf("insert legacy automation: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO automation_runs (id, user_id, automation_id, trigger_type, trigger_key,
			triggered_at, config_snapshot_json, environment_id, t3_project_id, thread_id,
			launch_command_id, initial_message_id, status, deadline_at, post_state, post_attempt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "run-1", "user-1", "aut-1", "issue_created", "event-e1",
		now, "{}", "env-1", "t3-proj", "th-1",
		"cmd-1", "msg-1", "", now.Add(time.Hour), "none", 0).Error; err != nil {
		t.Fatalf("insert legacy run: %v", err)
	}

	store := storage.NewLocalStorage(t.TempDir())
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("first runBootMigrations: %v", err)
	}
	var trigger model.AutomationTrigger
	if err := db.Where("automation_id = ?", "aut-1").First(&trigger).Error; err != nil {
		t.Fatalf("migrated trigger missing: %v", err)
	}

	// 模拟上次 DROP trigger_type 失败：旧列残留，闸门重开。
	if err := db.Exec("ALTER TABLE automations ADD COLUMN trigger_type text").Error; err != nil {
		t.Fatalf("re-add legacy column: %v", err)
	}
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("second runBootMigrations: %v", err)
	}

	// 触发器行数不变（复用已有行，不重复回填）。
	var triggerCount int64
	if err := db.Model(&model.AutomationTrigger{}).Count(&triggerCount).Error; err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if triggerCount != 1 {
		t.Fatalf("triggers after partial-drop rerun = %d, want 1", triggerCount)
	}
	// 运行行 trigger_id 不换新（沿用已存在的最早触发器）。
	var run model.AutomationRun
	if err := db.Where("id = ?", "run-1").First(&run).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.TriggerID != trigger.ID {
		t.Fatalf("run trigger_id = %q, want unchanged %q", run.TriggerID, trigger.ID)
	}
	// 重跑把残留列再次清掉，闸门重新关闭。
	hasLegacy, err := hasSQLiteColumn(db, "automations", "trigger_type")
	if err != nil {
		t.Fatalf("check legacy column: %v", err)
	}
	if hasLegacy {
		t.Fatal("rerun must drop the lingering legacy column")
	}
}

// 全新库：迁移为幂等空操作（无 automations 旧列、防抖表已带 trigger_id）。
func TestMigrateAutomationTriggers_FreshDBIsNoop(t *testing.T) {
	db := openAutomationMigrationDB(t)
	store := storage.NewLocalStorage(t.TempDir())
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("runBootMigrations: %v", err)
	}
	hasLegacy, err := hasSQLiteColumn(db, "automations", "trigger_type")
	if err != nil {
		t.Fatalf("check legacy column: %v", err)
	}
	if hasLegacy {
		t.Fatal("fresh db must not gain legacy trigger columns")
	}
	hasTriggerID, err := hasSQLiteColumn(db, "github_pr_debounces", "trigger_id")
	if err != nil {
		t.Fatalf("check debounce column: %v", err)
	}
	if !hasTriggerID {
		t.Fatal("fresh db debounce table must have trigger_id")
	}
}

// sqlitePrimaryKeyColumns 读表的 PK 列（按 PK 序）。
func sqlitePrimaryKeyColumns(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	var rows []struct {
		Name string `gorm:"column:name"`
		PK   int    `gorm:"column:pk"`
	}
	if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%s)", table)).Scan(&rows).Error; err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	var out []string
	for _, row := range rows {
		if row.PK > 0 {
			out = append(out, row.Name)
		}
	}
	return out
}

func assertIndexPresent(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n).Error; err != nil {
		t.Fatalf("check index %s: %v", name, err)
	}
	if n != 1 {
		t.Fatalf("index %s missing after migration", name)
	}
}

func assertIndexAbsent(t *testing.T, db *gorm.DB, name string) {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name).Scan(&n).Error; err != nil {
		t.Fatalf("check index %s: %v", name, err)
	}
	if n != 0 {
		t.Fatalf("index %s should have been dropped", name)
	}
}
