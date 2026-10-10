package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 新建库的所有外键都必须是 ON DELETE CASCADE，防止以后再只写一侧约束
// （双侧声明时 GORM 取父侧，belongs-to 单侧写了也没用）。
func TestAutoMigrate_AllForeignKeysCascade(t *testing.T) {
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	var tables []string
	if err := db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	`).Scan(&tables).Error; err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables created")
	}

	// 有意不设例外的表不加入白名单；新增例外须注明理由。
	allowedNonCascade := map[string]map[string]bool{
		// 截图标注关联 Issue 是可选弱引用：删 Issue 只解除关联（SET NULL），标注本体保留。
		"screenshot_annotations": {"issue_id": true},
	}

	for _, table := range tables {
		fks, err := sqliteForeignKeys(db, table)
		if err != nil {
			t.Fatalf("foreign_key_list(%s): %v", table, err)
		}
		for _, fk := range fks {
			if strings.EqualFold(fk.OnDelete, "CASCADE") {
				continue
			}
			if allowedNonCascade[table][fk.From] {
				continue
			}
			t.Fatalf("table %s column %s -> %s.%s is %q, want CASCADE",
				table, fk.From, fk.Table, fk.To, fk.OnDelete)
		}
	}
}

// 存量库迁移：旧库上四张子表是 NO ACTION、三张表没有外键，且存在悬空行。
// 修复后：外键全部 CASCADE、悬空行清掉、正常行保留、二次运行不再触发重建。
func TestRepairLegacyForeignKeys_RebuildsAndCleans(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")
	db, err := gorm.Open(sqlite.Open("file:"+dbPath+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	// 父表按当前模型建（它们的 CASCADE 本来就对）。
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Version{}, &model.Issue{}); err != nil {
		t.Fatalf("migrate parents: %v", err)
	}

	// 旧库形态一：FK 存在但 NO ACTION。用当前模型建表后去掉 CASCADE 关键字还原。
	for table, m := range map[string]interface{}{
		"issue_github_meta":     &model.IssueGitHubMeta{},
		"issue_comments":        &model.IssueComment{},
		"issue_timeline_events": &model.IssueTimelineEvent{},
		"artifacts":             &model.Artifact{},
	} {
		recreateTableWithoutCascade(t, db, table, m)
	}

	// 旧库形态二：完全没有外键。
	for _, ddl := range []string{
		`CREATE TABLE issue_read_states (
			user_id TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			last_read_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (user_id, issue_id)
		)`,
		`CREATE TABLE issue_sync_states (
			project_id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'idle',
			last_issue_updated_at DATETIME,
			last_synced_at DATETIME,
			last_successful_sync_at DATETIME,
			last_error TEXT
		)`,
		`CREATE TABLE github_repo_labels (
			project_id TEXT NOT NULL,
			name TEXT NOT NULL,
			color TEXT,
			description TEXT,
			synced_at DATETIME NOT NULL,
			PRIMARY KEY (project_id, name)
		)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("create legacy table: %v\n%s", err, ddl)
		}
	}
	// 验证「旧表索引占用名字时重建不冲突」的路径。
	if err := db.Exec("CREATE INDEX idx_issue_comments_issue_id ON issue_comments(issue_id)").Error; err != nil {
		t.Fatalf("create probe index: %v", err)
	}

	now := "2026-10-09 08:00:00"
	if err := db.Create(&model.User{
		ID: "u-1", Username: "u", Email: "u@example.com", PasswordHash: "x",
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.User{
		ID: "u-2", Username: "u2", Email: "u2@example.com", PasswordHash: "x",
	}).Error; err != nil {
		t.Fatalf("seed user2: %v", err)
	}
	if err := db.Create(&model.Project{
		ID: "p-1", UserID: "u-1", Name: "demo",
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Create(&model.Version{
		ID: "v-1", ProjectID: "p-1", VersionNumber: "0.1.0", Status: model.VersionStatusPending,
	}).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if err := db.Create(&model.Issue{
		ID: "i-1", ProjectID: "p-1", Source: model.IssueSourceInternal,
		SequenceNumber: 1, State: model.IssueStateOpen, Title: "t",
	}).Error; err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if err := db.Create(&model.Issue{
		ID: "i-2", ProjectID: "p-1", Source: model.IssueSourceInternal,
		SequenceNumber: 2, State: model.IssueStateOpen, Title: "t2",
	}).Error; err != nil {
		t.Fatalf("seed issue2: %v", err)
	}

	// 正常子行。u-2 的水位挂在 i-2 上：i-1 被级联删除后它仍存活，
	// 留给后面的 user 级联断言用。
	validInserts := []string{
		fmt.Sprintf(`INSERT INTO issue_github_meta (issue_id, project_id, github_issue_id, number, synced_at)
			VALUES ('i-1', 'p-1', 100, 1, '%s')`, now),
		fmt.Sprintf(`INSERT INTO issue_comments (id, issue_id, github_comment_id, created_at, updated_at)
			VALUES ('c-1', 'i-1', 200, '%s', '%s')`, now, now),
		fmt.Sprintf(`INSERT INTO issue_timeline_events (id, issue_id, event_key, event_type, created_at)
			VALUES ('e-1', 'i-1', 'k1', 'labeled', '%s')`, now),
		fmt.Sprintf(`INSERT INTO artifacts (id, version_id, file_name, file_size, file_path, uploaded_at)
			VALUES ('a-1', 'v-1', 'app.zip', 1, 'p-1/v-1/app.zip', '%s')`, now),
		fmt.Sprintf(`INSERT INTO issue_read_states (user_id, issue_id, last_read_at, updated_at)
			VALUES ('u-1', 'i-1', '%s', '%s')`, now, now),
		fmt.Sprintf(`INSERT INTO issue_read_states (user_id, issue_id, last_read_at, updated_at)
			VALUES ('u-2', 'i-2', '%s', '%s')`, now, now),
		`INSERT INTO issue_sync_states (project_id, status) VALUES ('p-1', 'idle')`,
		fmt.Sprintf(`INSERT INTO github_repo_labels (project_id, name, synced_at)
			VALUES ('p-1', 'bug', '%s')`, now),
	}
	for _, sql := range validInserts {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("insert valid row: %v\n%s", err, sql)
		}
	}

	// 悬空行（NO ACTION 外键同样拦插入，先关外键检查写进去）。
	if err := db.Exec("PRAGMA foreign_keys = OFF").Error; err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	danglingInserts := []string{
		`INSERT INTO issue_github_meta (issue_id, project_id, github_issue_id, number, synced_at)
			VALUES ('i-ghost', 'p-1', 101, 2, '2026-10-09 08:00:00')`,
		`INSERT INTO issue_comments (id, issue_id, github_comment_id, created_at, updated_at)
			VALUES ('c-ghost', 'i-ghost', 201, '2026-10-09 08:00:00', '2026-10-09 08:00:00')`,
		`INSERT INTO issue_timeline_events (id, issue_id, event_key, event_type, created_at)
			VALUES ('e-ghost', 'i-ghost', 'k2', 'closed', '2026-10-09 08:00:00')`,
		`INSERT INTO artifacts (id, version_id, file_name, file_size, file_path, uploaded_at)
			VALUES ('a-ghost', 'v-ghost', 'x.zip', 1, 'p/x.zip', '2026-10-09 08:00:00')`,
		`INSERT INTO issue_read_states (user_id, issue_id, last_read_at, updated_at)
			VALUES ('u-ghost', 'i-1', '2026-10-09 08:00:00', '2026-10-09 08:00:00')`,
		`INSERT INTO issue_read_states (user_id, issue_id, last_read_at, updated_at)
			VALUES ('u-1', 'i-ghost', '2026-10-09 08:00:00', '2026-10-09 08:00:00')`,
		`INSERT INTO issue_sync_states (project_id, status) VALUES ('p-ghost', 'idle')`,
		`INSERT INTO github_repo_labels (project_id, name, synced_at)
			VALUES ('p-ghost', 'wontfix', '2026-10-09 08:00:00')`,
	}
	for _, sql := range danglingInserts {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("insert dangling row: %v\n%s", err, sql)
		}
	}
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		t.Fatalf("enable fk: %v", err)
	}

	backupPath := dbPath + ".bak-test"
	if err := repairLegacyForeignKeys(db, zap.NewNop(), backupPath); err != nil {
		t.Fatalf("repair: %v", err)
	}

	info, err := os.Stat(backupPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty backup file at %s: %v", backupPath, err)
	}

	// 外键全部与期望一致且 CASCADE。
	for _, spec := range legacyFKTables {
		conforms, err := foreignKeysConform(db, spec)
		if err != nil {
			t.Fatalf("recheck %s: %v", spec.table, err)
		}
		if !conforms {
			t.Fatalf("table %s still not conforming after repair", spec.table)
		}
	}

	// 悬空行被清、正常行保留。
	expectedCounts := map[string]int64{
		"issue_github_meta":     1,
		"issue_comments":        1,
		"issue_timeline_events": 1,
		"artifacts":             1,
		"issue_read_states":     2,
		"issue_sync_states":     1,
		"github_repo_labels":    1,
	}
	for table, want := range expectedCounts {
		var got int64
		if err := db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %q", table)).Scan(&got).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Fatalf("table %s: got %d rows, want %d", table, got, want)
		}
	}
	if tableExists(t, db, "issue_comments__pre_fk_repair") {
		t.Fatal("legacy table left behind")
	}
	if !indexExists(t, db, "issue_comments", "idx_issue_comments_issue_id") {
		t.Fatal("model index not recreated after rebuild")
	}

	// 级联真的生效：删父行带走子行。
	if err := db.Exec("DELETE FROM issues WHERE id = 'i-1'").Error; err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	for table, col := range map[string]string{
		"issue_github_meta":     "issue_id",
		"issue_comments":        "issue_id",
		"issue_timeline_events": "issue_id",
		"issue_read_states":     "issue_id",
	} {
		var n int64
		db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %q WHERE %q = 'i-1'", table, col)).Scan(&n)
		if n != 0 {
			t.Fatalf("table %s: %d rows survived issue cascade", table, n)
		}
	}
	var survivingReadStates int64
	db.Raw("SELECT COUNT(*) FROM issue_read_states").Scan(&survivingReadStates)
	if survivingReadStates != 1 {
		t.Fatalf("expected u-2 read state to survive i-1 cascade, got %d", survivingReadStates)
	}

	if err := db.Exec("DELETE FROM versions WHERE id = 'v-1'").Error; err != nil {
		t.Fatalf("delete version: %v", err)
	}
	var artifactRows int64
	db.Raw("SELECT COUNT(*) FROM artifacts WHERE version_id = 'v-1'").Scan(&artifactRows)
	if artifactRows != 0 {
		t.Fatal("artifacts survived version cascade")
	}

	// user 级联：u-2 的水位行随用户删除。
	if err := db.Exec("DELETE FROM users WHERE id = 'u-2'").Error; err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var readStateRows int64
	db.Raw("SELECT COUNT(*) FROM issue_read_states").Scan(&readStateRows)
	if readStateRows != 0 {
		t.Fatalf("issue_read_states survived user cascade: %d", readStateRows)
	}

	if err := db.Exec("DELETE FROM projects WHERE id = 'p-1'").Error; err != nil {
		t.Fatalf("delete project: %v", err)
	}
	for table, col := range map[string]string{
		"issue_sync_states":  "project_id",
		"github_repo_labels": "project_id",
	} {
		var n int64
		db.Raw(fmt.Sprintf("SELECT COUNT(*) FROM %q WHERE %q = 'p-1'", table, col)).Scan(&n)
		if n != 0 {
			t.Fatalf("table %s: %d rows survived project cascade", table, n)
		}
	}

	// 二次运行（模拟重启）：外键已合规，不再备份不再重建。
	secondBackup := dbPath + ".bak-second"
	if err := repairLegacyForeignKeys(db, zap.NewNop(), secondBackup); err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if _, err := os.Stat(secondBackup); !os.IsNotExist(err) {
		t.Fatal("second run unexpectedly created a backup")
	}

	// 修复后的库过 AutoMigrate 不再触发隐式重建。
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("automigrate after repair: %v", err)
	}
}

// 全新库上修复函数是空转：不备份、不重建。
func TestRepairLegacyForeignKeys_NoOpOnFreshDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fresh.db")
	db, err := gorm.Open(sqlite.Open("file:"+dbPath+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	backupPath := dbPath + ".bak-fresh"
	if err := repairLegacyForeignKeys(db, zap.NewNop(), backupPath); err != nil {
		t.Fatalf("repair on fresh db: %v", err)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatal("fresh db unexpectedly created a backup")
	}
}

// 用当前模型建出目标表，再按旧库形态去掉 ON DELETE CASCADE 重建。
func recreateTableWithoutCascade(t *testing.T, db *gorm.DB, table string, m interface{}) {
	t.Helper()
	if err := db.Migrator().CreateTable(m); err != nil {
		t.Fatalf("create %s via model: %v", table, err)
	}
	var ddl string
	if err := db.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", table,
	).Scan(&ddl).Error; err != nil {
		t.Fatalf("read ddl for %s: %v", table, err)
	}
	if !strings.Contains(ddl, "ON DELETE CASCADE") {
		t.Fatalf("%s ddl lacks ON DELETE CASCADE: %s", table, ddl)
	}
	legacyDDL := strings.ReplaceAll(ddl, " ON DELETE CASCADE", "")
	if err := db.Exec(fmt.Sprintf("DROP TABLE %q", table)).Error; err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
	if err := db.Exec(legacyDDL).Error; err != nil {
		t.Fatalf("recreate %s legacy: %v\n%s", table, err, legacyDDL)
	}
}

func indexExists(t *testing.T, db *gorm.DB, table, name string) bool {
	t.Helper()
	var indexes []sqliteIndexInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA index_list(%q)", table)).Scan(&indexes).Error; err != nil {
		t.Fatalf("index_list %s: %v", table, err)
	}
	for _, idx := range indexes {
		if idx.Name == name {
			return true
		}
	}
	return false
}
