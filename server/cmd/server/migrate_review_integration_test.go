package main

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// INT-167 迁移测试：旧结构库 → 迁移 → 新主键含 integration_id、存量行回填
// github-default、幂等二次执行无副作用；加列表（资产/运行/设置行）回填正确；
// 设置行唯一索引换键后同仓两集成可各自 enabled。

// seedLegacyReviewSchema 按改动前的结构建 6 张审查表（AUTOINCREMENT 无关，
// 列清单取自改动前模型的 AutoMigrate 产物），并模拟存量索引。
func seedLegacyReviewSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	ddls := []string{
		// 主键要扩 integration_id 的三张键表。
		"CREATE TABLE review_pull_requests (`project_id` text,`repo_full_name` text,`pr_number` integer," +
			"`user_id` text NOT NULL,`installation_id` integer,`round_number` integer NOT NULL DEFAULT 0," +
			"`head_sha` text,`code_comment_id` integer,`visual_comment_id` integer,`code_status` text," +
			"`visual_status` text,`visual_screens_fingerprint` text,`sync_pending` numeric NOT NULL DEFAULT false," +
			"`sync_epoch` integer NOT NULL DEFAULT 0,`sync_error` text,`sync_failed_epoch` integer NOT NULL DEFAULT 0," +
			"`updated_at` datetime NOT NULL,PRIMARY KEY (`project_id`,`repo_full_name`,`pr_number`))",
		"CREATE INDEX idx_review_pull_requests_user_id ON review_pull_requests(user_id)",
		"CREATE TABLE review_debounces (`project_id` text,`repo_full_name` text,`pr_number` integer," +
			"`head_sha` text NOT NULL,`action` text NOT NULL,`installation_id` integer,`pr_title` text," +
			"`pr_url` text,`base_ref` text,`head_ref` text,`fire_at` datetime NOT NULL," +
			"`updated_at` datetime NOT NULL,PRIMARY KEY (`project_id`,`repo_full_name`,`pr_number`))",
		"CREATE INDEX idx_review_debounces_fire_at ON review_debounces(fire_at)",
		"CREATE TABLE review_seen_shas (`project_id` text,`repo_full_name` text,`pr_number` integer," +
			"`head_sha` text,`pr_updated_at` datetime,`accepted_at` datetime NOT NULL," +
			"PRIMARY KEY (`project_id`,`repo_full_name`,`pr_number`,`head_sha`))",
		// 只加列的三张表。
		"CREATE TABLE review_assets (`id` text,`repo_full_name` text NOT NULL,`pr_number` integer NOT NULL," +
			"`head_sha` text NOT NULL,`position` integer NOT NULL,`label` text,`source_url` text NOT NULL," +
			"`storage_path` text NOT NULL,`mime_type` text,`size_bytes` integer,`error` text," +
			"`created_at` datetime NOT NULL,PRIMARY KEY (`id`))",
		"CREATE UNIQUE INDEX idx_review_assets_source ON review_assets(repo_full_name, pr_number, head_sha, source_url)",
		"CREATE TABLE review_runs (`id` text,`user_id` text NOT NULL,`project_id` text NOT NULL," +
			"`kind` text NOT NULL,`repo_full_name` text NOT NULL,`pr_number` integer NOT NULL," +
			"`head_sha` text NOT NULL,`round_number` integer NOT NULL,`installation_id` integer," +
			"`trigger_action` text,`pr_title` text,`pr_url` text,`base_ref` text,`head_ref` text," +
			"`environment_id` text NOT NULL,`t3_project_id` text,`instance_id` text NOT NULL," +
			"`model` text NOT NULL,`model_options_json` text,`thread_id` text NOT NULL," +
			"`launch_command_id` text NOT NULL,`initial_message_id` text NOT NULL," +
			"`status` text NOT NULL DEFAULT \"queued\",`dispatch_started_at` datetime,`dispatched_at` datetime," +
			"`started_at` datetime,`finished_at` datetime,`deadline_at` datetime NOT NULL," +
			"`prep_retry_count` integer NOT NULL DEFAULT 0,`error_code` text,`error_message` text," +
			"`summary` text,`result_json` text,`review_id` integer,`posted_at` datetime,`worktree_path` text," +
			"`workspace_root` text,`worktree_state` text NOT NULL DEFAULT \"none\",`worktree_error` text," +
			"`stop_attempted_at` datetime,`archived_at` datetime,`created_at` datetime NOT NULL," +
			"`updated_at` datetime NOT NULL,PRIMARY KEY (`id`))",
		"CREATE TABLE project_review_settings (`project_id` text,`user_id` text NOT NULL," +
			"`repo_full_name` text,`enabled` numeric NOT NULL DEFAULT false,`trigger_opened` numeric NOT NULL," +
			"`trigger_synchronize` numeric NOT NULL,`trigger_ready_for_review` numeric NOT NULL DEFAULT false," +
			"`trigger_reopened` numeric NOT NULL DEFAULT false,`code_combinations_json` text," +
			"`visual_combinations_json` text,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL," +
			"PRIMARY KEY (`project_id`))",
		"CREATE UNIQUE INDEX idx_project_review_settings_repo ON project_review_settings(repo_full_name) WHERE enabled = 1",
	}
	for _, ddl := range ddls {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("seed legacy schema: %v\n%s", err, ddl)
		}
	}
}

// pkColumns 读某表的主键列（按 PRAGMA 的 pk 序）。
func pkColumns(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	type columnInfo struct {
		Name string `gorm:"column:name"`
		PK   int    `gorm:"column:pk"`
	}
	var rows []columnInfo
	if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		t.Fatalf("read pk of %s: %v", table, err)
	}
	var out []string
	for _, r := range rows {
		if r.PK > 0 {
			out = append(out, r.Name)
		}
	}
	return out
}

// TestMigrateReviewIntegrationRebuildsLegacyPKs 旧主键缺 integration_id 的三张
// 键表整表重建：数据保留、回填 github-default、AutoMigrate 之后结构即终态、
// 重复迁移零副作用。
func TestMigrateReviewIntegrationRebuildsLegacyPKs(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	seedLegacyReviewSchema(t, db)

	now := time.Now().UTC()
	seeds := []string{
		// 账本：一行在途同步的存量行。
		"INSERT INTO review_pull_requests (project_id, repo_full_name, pr_number, user_id, installation_id," +
			" round_number, head_sha, sync_pending, sync_epoch, updated_at)" +
			" VALUES ('p1','o/r',7,'u1',42,3,'sha-a',1,5,'" + now.Format(time.RFC3339Nano) + "')",
		// 防抖与已见 SHA。
		"INSERT INTO review_debounces (project_id, repo_full_name, pr_number, head_sha, action, fire_at, updated_at)" +
			" VALUES ('p1','o/r',7,'sha-b','synchronize','" + now.Format(time.RFC3339Nano) + "','" + now.Format(time.RFC3339Nano) + "')",
		"INSERT INTO review_seen_shas (project_id, repo_full_name, pr_number, head_sha, accepted_at)" +
			" VALUES ('p1','o/r',7,'sha-a','" + now.Format(time.RFC3339Nano) + "')",
	}
	for _, q := range seeds {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("seed row: %v\n%s", err, q)
		}
	}

	// 迁移 + AutoMigrate + 手工索引，与 runBootMigrations 的顺序一致。
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.AutoMigrate(&model.ReviewPullRequest{}, &model.ReviewDebounce{}, &model.ReviewSeenSHA{}, &model.ForgeIntegration{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("boot indexes: %v", err)
	}

	wantPKs := map[string][]string{
		"review_pull_requests": {"project_id", "repo_full_name", "pr_number", "integration_id"},
		"review_debounces":     {"project_id", "repo_full_name", "pr_number", "integration_id"},
		"review_seen_shas":     {"project_id", "repo_full_name", "pr_number", "head_sha", "integration_id"},
	}
	for table, want := range wantPKs {
		got := pkColumns(t, db, table)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s pk = %v, want %v", table, got, want)
		}
	}

	// 存量行回填 github-default，其余列原样保留。
	var ledgers []model.ReviewPullRequest
	if err := db.Find(&ledgers).Error; err != nil {
		t.Fatalf("load ledgers: %v", err)
	}
	if len(ledgers) != 1 || ledgers[0].IntegrationID != model.ForgeIntegrationBuiltinGitHubID ||
		ledgers[0].HeadSHA != "sha-a" || ledgers[0].SyncEpoch != 5 || ledgers[0].SyncPending != true {
		t.Fatalf("ledgers = %+v, want 1 行回填 github-default 且数据保留", ledgers)
	}
	var seen model.ReviewSeenSHA
	if err := db.First(&seen).Error; err != nil {
		t.Fatalf("load seen: %v", err)
	}
	if seen.IntegrationID != model.ForgeIntegrationBuiltinGitHubID || seen.HeadSHA != "sha-a" {
		t.Fatalf("seen = %+v, want 回填 github-default", seen)
	}

	// 幂等：重复迁移与重复启动序列都不改数据、不报错。
	for i := 0; i < 2; i++ {
		if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
			t.Fatalf("migrate #%d: %v", i, err)
		}
		if err := db.AutoMigrate(&model.ReviewPullRequest{}, &model.ReviewDebounce{}, &model.ReviewSeenSHA{}, &model.ForgeIntegration{}); err != nil {
			t.Fatalf("automigrate #%d: %v", i, err)
		}
	}
	var n int64
	if err := db.Model(&model.ReviewPullRequest{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("ledgers after re-migrate = %d (%v), want 1", n, err)
	}
}

// TestMigrateReviewIntegrationRecoversOrphanNew 崩溃恢复：旧版实现在 DROP
// 旧表与 RENAME _new 之间被打断，会留下「正式表缺失 + 数据完好的 _new」——
// 再启动必须改回 _new 而不是让 AutoMigrate 建空表、把数据困在残留表里。
// 已是新结构时残留 _new 只清不占。
func TestMigrateReviewIntegrationRecoversOrphanNew(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	seedLegacyReviewSchema(t, db)
	now := time.Now().UTC()
	if err := db.Exec("INSERT INTO review_pull_requests (project_id, repo_full_name, pr_number, user_id," +
		" installation_id, round_number, head_sha, updated_at)" +
		" VALUES ('p1','o/r',7,'u1',42,0,'sha-a','" + now.Format(time.RFC3339Nano) + "')").Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 模拟「DROP 旧表后崩溃」：正式表缺失，数据完好的 _new 还在。
	if err := db.Exec("ALTER TABLE review_pull_requests RENAME TO review_pull_requests_new").Error; err != nil {
		t.Fatalf("simulate crash: %v", err)
	}
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("recover migrate: %v", err)
	}
	var ledgers []model.ReviewPullRequest
	if err := db.Find(&ledgers).Error; err != nil {
		t.Fatalf("load ledgers: %v", err)
	}
	if len(ledgers) != 1 || ledgers[0].HeadSHA != "sha-a" ||
		ledgers[0].IntegrationID != model.ForgeIntegrationBuiltinGitHubID {
		t.Fatalf("ledgers = %+v, want 残留 _new 恢复出的原行", ledgers)
	}
	var orphan int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'review_pull_requests_new'").Scan(&orphan).Error; err != nil {
		t.Fatalf("check orphan: %v", err)
	}
	if orphan != 0 {
		t.Fatal("恢复后 _new 残留表不应存在")
	}

	// 已是新结构时残留 _new（例如崩溃发生在改名后、清理前）直接清掉，
	// 正式表内容不受影响。
	if err := db.Exec("CREATE TABLE review_pull_requests_new (x text)").Error; err != nil {
		t.Fatalf("seed stray _new: %v", err)
	}
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("cleanup migrate: %v", err)
	}
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE name = 'review_pull_requests_new'").Scan(&orphan).Error; err != nil {
		t.Fatalf("check stray: %v", err)
	}
	if orphan != 0 {
		t.Fatal("已迁移状态下残留 _new 应被清理")
	}
	var n int64
	if err := db.Model(&model.ReviewPullRequest{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("ledgers = %d (%v), want 1", n, err)
	}
}

// 新库（从零 AutoMigrate）直接出新结构：主键天然含 integration_id，迁移是
// 空操作。
func TestMigrateReviewIntegrationFreshDBIsNoop(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate on empty db: %v", err)
	}
	if err := db.AutoMigrate(&model.ReviewPullRequest{}, &model.ReviewDebounce{},
		&model.ReviewSeenSHA{}, &model.ReviewAsset{}, &model.ReviewRun{}, &model.ProjectReviewSettings{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate after automigrate: %v", err)
	}
	for table, want := range map[string]string{
		"review_pull_requests": "project_id,repo_full_name,pr_number,integration_id",
		"review_debounces":     "project_id,repo_full_name,pr_number,integration_id",
		"review_seen_shas":     "project_id,repo_full_name,pr_number,head_sha,integration_id",
	} {
		if got := strings.Join(pkColumns(t, db, table), ","); got != want {
			t.Fatalf("%s pk = %s, want %s", table, got, want)
		}
	}
}

// TestMigrateReviewIntegrationAddsColumns 加列表（资产/运行/设置行）：列补齐、
// 回填 github-default；截图资产唯一索引换键后同仓两集成各留一行、同集成去重。
func TestMigrateReviewIntegrationAddsColumns(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	seedLegacyReviewSchema(t, db)

	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	for _, q := range []string{
		// 资产一行、运行两行、设置行两行（其中一行 enabled）。
		"INSERT INTO review_assets (id, repo_full_name, pr_number, head_sha, position, source_url, storage_path, created_at)" +
			" VALUES ('a1','o/r',7,'sha-a',0,'https://x/1.png','/c/1','" + stamp + "')",
		"INSERT INTO review_runs (id, user_id, project_id, kind, repo_full_name, pr_number, head_sha, round_number," +
			" environment_id, instance_id, model, thread_id, launch_command_id, initial_message_id, status, deadline_at, created_at, updated_at)" +
			" VALUES ('r1','u1','p1','code','o/r',7,'sha-a',1,'e1','i1','m1','t1','c1','m1','completed','" + stamp + "','" + stamp + "','" + stamp + "')",
		"INSERT INTO review_runs (id, user_id, project_id, kind, repo_full_name, pr_number, head_sha, round_number," +
			" environment_id, instance_id, model, thread_id, launch_command_id, initial_message_id, status, deadline_at, created_at, updated_at)" +
			" VALUES ('r2','u1','p1','visual','o/r',7,'sha-a',1,'e1','i1','m1','t2','c2','m2','completed','" + stamp + "','" + stamp + "','" + stamp + "')",
		"INSERT INTO project_review_settings (project_id, user_id, repo_full_name, enabled, trigger_opened, trigger_synchronize, created_at, updated_at)" +
			" VALUES ('p1','u1','o/r',1,1,1,'" + stamp + "','" + stamp + "')",
		"INSERT INTO project_review_settings (project_id, user_id, repo_full_name, enabled, trigger_opened, trigger_synchronize, created_at, updated_at)" +
			" VALUES ('p2','u2','o/r',0,1,1,'" + stamp + "','" + stamp + "')",
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatalf("seed row: %v\n%s", err, q)
		}
	}

	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.AutoMigrate(&model.ReviewAsset{}, &model.ReviewRun{}, &model.ProjectReviewSettings{}, &model.ForgeIntegration{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("boot indexes: %v", err)
	}

	for _, q := range []string{
		"SELECT integration_id FROM review_assets WHERE id = 'a1'",
		"SELECT integration_id FROM review_runs WHERE id = 'r1'",
		"SELECT integration_id FROM review_runs WHERE id = 'r2'",
		"SELECT integration_id FROM project_review_settings WHERE project_id = 'p1'",
		"SELECT integration_id FROM project_review_settings WHERE project_id = 'p2'",
	} {
		var got string
		if err := db.Raw(q).Scan(&got).Error; err != nil {
			t.Fatalf("load: %v\n%s", err, q)
		}
		if got != model.ForgeIntegrationBuiltinGitHubID {
			t.Fatalf("%s = %q, want %s", q, got, model.ForgeIntegrationBuiltinGitHubID)
		}
	}

	// 资产唯一索引已换键（integration_id 键首）：AutoMigrate 按模型标签重建后，
	// 同 (repo,pr,head,source) 在第二个集成下可落行，同集成重复被吃掉。
	conflict := clause.OnConflict{
		Columns: []clause.Column{
			{Name: "integration_id"}, {Name: "repo_full_name"}, {Name: "pr_number"},
			{Name: "head_sha"}, {Name: "source_url"},
		},
		DoNothing: true,
	}
	forge := &model.ReviewAsset{ID: "a2", IntegrationID: "forge-x", RepoFullName: "o/r",
		PRNumber: 7, HeadSHA: "sha-a", SourceURL: "https://x/1.png", CreatedAt: now}
	if err := db.Clauses(conflict).Create(forge).Error; err != nil {
		t.Fatalf("second integration row must land: %v", err)
	}
	dup := &model.ReviewAsset{ID: "a3", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		RepoFullName: "o/r", PRNumber: 7, HeadSHA: "sha-a", SourceURL: "https://x/1.png", CreatedAt: now}
	if err := db.Clauses(conflict).Create(dup).Error; err != nil {
		t.Fatalf("dup insert: %v", err)
	}
	var n int64
	if err := db.Model(&model.ReviewAsset{}).Where("repo_full_name = 'o/r'").Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("assets = %d (%v), want 2（同源两集成各一行，同集成去重）", n, err)
	}

	// 幂等：重复迁移不报错、行数不变。
	if err := migrateReviewIntegrationIDs(db, zap.NewNop()); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if err := db.Model(&model.ReviewRun{}).Count(&n).Error; err != nil || n != 2 {
		t.Fatalf("runs after re-migrate = %d (%v), want 2", n, err)
	}
}

// TestProjectReviewSettingsRepoIndexPerIntegration 设置行唯一索引换键：
// (integration_id, repo_full_name) WHERE enabled——同名仓库在两个集成下可各自
// 被一个项目启用，同集成重复仍被拦；旧定义索引由 sqlite_master 比对重建。
func TestProjectReviewSettingsRepoIndexPerIntegration(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.ProjectReviewSettings{}, &model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 模拟升级前的旧定义索引（repo_full_name 单列）。
	if err := db.Exec("CREATE UNIQUE INDEX idx_project_review_settings_repo ON project_review_settings(repo_full_name) WHERE enabled = 1").Error; err != nil {
		t.Fatalf("create stale index: %v", err)
	}
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("rebuild index: %v", err)
	}
	var def string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_project_review_settings_repo'").Scan(&def).Error; err != nil {
		t.Fatalf("read index def: %v", err)
	}
	if !strings.Contains(def, "integration_id") {
		t.Fatalf("index not rebuilt, def = %q", def)
	}

	now := time.Now().UTC()
	row := func(projectID, integration string) *model.ProjectReviewSettings {
		return &model.ProjectReviewSettings{
			ProjectID: projectID, UserID: "u-" + projectID,
			RepoFullName: "octo/app", IntegrationID: integration,
			Enabled: true, CreatedAt: now, UpdatedAt: now,
		}
	}
	// 同仓库两个集成各自 enabled 共存。
	if err := db.Create(row("p-gh", model.ForgeIntegrationBuiltinGitHubID)).Error; err != nil {
		t.Fatalf("create github row: %v", err)
	}
	if err := db.Create(row("p-fj", "forge-x")).Error; err != nil {
		t.Fatalf("create forgejo row: %v", err)
	}
	// 同集成同仓库的第二个 enabled 行被唯一索引拦截。
	dup := row("p-gh2", model.ForgeIntegrationBuiltinGitHubID)
	if err := db.Create(dup).Error; err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("same-integration dup err = %v, want UNIQUE constraint failed", err)
	}
	// 重复启动：当期定义不再重建，幂等。
	if err := createBootIndexes(db); err != nil {
		t.Fatalf("second boot: %v", err)
	}
}
