package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"
)

// failingDeleteStorage 按黑名单让 Delete 失败，模拟磁盘层面的删除故障
// （文件被占用、权限不足），其余操作透传给底层存储。
type failingDeleteStorage struct {
	storage.Storage
	failPaths map[string]bool
	deleted   []string
}

func (s *failingDeleteStorage) Delete(path string) error {
	if s.failPaths[path] {
		return errors.New("simulated delete failure")
	}
	if err := s.Storage.Delete(path); err != nil {
		return err
	}
	s.deleted = append(s.deleted, path)
	return nil
}

// setupLegacyScreenshotDB 建一个真正的「存量多版本库」：screenshot_screens
// 没有文件列（file_path/mime_type/file_size/uploaded_at），带 group_name 等
// 旧列与指向 projects 的外键；screenshot_versions、带 version_id 外键的
// screenshot_annotations 均按旧模型建表。后续走 runBootMigrations 完整启动
// 入口升级，不走「先建新模型再补旧列」的绕行路径。
func setupLegacyScreenshotDB(t *testing.T) (*gorm.DB, storage.Storage, string) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}); err != nil {
		t.Fatalf("migrate user/project: %v", err)
	}

	// 旧表 DDL 按老版本 GORM 的实际产物写：单行、反引号标识符。glebarez 驱动的
	// DDL 重解析把 tab 当分隔符，多行缩进的建表语句会让它解析错位（真实存量库
	// 不会出现这种形态）。
	for _, ddl := range []string{
		// 旧 screenshot_screens（INT-98 及之前）：无文件信息列
		"CREATE TABLE `screenshot_screens` (`id` text PRIMARY KEY,`project_id` text NOT NULL,`screen_key` text NOT NULL,`title` text,`group_name` text,`version_count` integer NOT NULL DEFAULT 0,`last_uploaded_at` datetime NOT NULL,`created_at` datetime NOT NULL,CONSTRAINT `fk_screenshot_screens_project` FOREIGN KEY (`project_id`) REFERENCES `projects`(`id`) ON DELETE CASCADE)",
		"CREATE TABLE `screenshot_versions` (`id` text PRIMARY KEY,`screen_id` text NOT NULL,`file_name` text NOT NULL,`file_path` text NOT NULL,`mime_type` text NOT NULL,`file_size` integer NOT NULL,`note` text,`uploaded_by` text,`uploaded_at` datetime NOT NULL,`width` integer NOT NULL DEFAULT 0,`height` integer NOT NULL DEFAULT 0,CONSTRAINT `fk_screenshot_versions_screen` FOREIGN KEY (`screen_id`) REFERENCES `screenshot_screens`(`id`) ON DELETE CASCADE)",
		// 旧 screenshot_annotations：version_id 非空并带指向 versions 的级联外键
		"CREATE TABLE `screenshot_annotations` (`id` text PRIMARY KEY,`version_id` text NOT NULL,`screen_id` text NOT NULL,`project_id` text NOT NULL,`issue_id` text,`x` real NOT NULL,`y` real NOT NULL,`width` real NOT NULL,`height` real NOT NULL,`body` text,`status` text NOT NULL DEFAULT 'open',`outdated` numeric NOT NULL DEFAULT 0,`created_by` text,`created_at` datetime NOT NULL,`updated_at` datetime NOT NULL,`resolved_at` datetime,CONSTRAINT `fk_screenshot_annotations_version` FOREIGN KEY (`version_id`) REFERENCES `screenshot_versions`(`id`) ON DELETE CASCADE,CONSTRAINT `fk_screenshot_annotations_screen` FOREIGN KEY (`screen_id`) REFERENCES `screenshot_screens`(`id`) ON DELETE CASCADE,CONSTRAINT `fk_screenshot_annotations_project` FOREIGN KEY (`project_id`) REFERENCES `projects`(`id`) ON DELETE CASCADE)",
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("create legacy table: %v", err)
		}
	}

	dir := t.TempDir()
	return db, storage.NewLocalStorage(dir), dir
}

type legacyVersionRow struct {
	ID         string
	ScreenID   string
	FilePath   string
	MimeType   string
	FileSize   int64
	UploadedBy string
	UploadedAt time.Time
	Width      int
	Height     int
}

func insertLegacyFixtures(t *testing.T, db *gorm.DB, store storage.Storage) (screenID, keepVersionID string, removedPaths []string) {
	t.Helper()
	now := time.Now().UTC()
	user := &model.User{ID: "user-1", Username: "u", Email: "u@example.com", PasswordHash: "h", CreatedAt: now, UpdatedAt: now}
	project := &model.Project{ID: "proj-1", UserID: user.ID, Name: "p", Description: "", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	screenID = "screen-1"
	if err := db.Exec(`
		INSERT INTO screenshot_screens (id, project_id, screen_key, title, group_name, version_count, last_uploaded_at, created_at)
		VALUES (?, ?, 'home', '旧标题', ' Desktop ', 2, ?, ?)
	`, screenID, project.ID, now.Add(-time.Minute), now.Add(-time.Hour)).Error; err != nil {
		t.Fatalf("insert legacy screen: %v", err)
	}

	versions := []legacyVersionRow{
		{ID: "v-old", ScreenID: screenID, FilePath: "proj-1/screenshots/v-old.png", MimeType: "image/png", FileSize: 10, UploadedBy: "旧用户", UploadedAt: now.Add(-2 * time.Hour), Width: 30, Height: 20},
		{ID: "v-new", ScreenID: screenID, FilePath: "proj-1/screenshots/v-new.png", MimeType: "image/png", FileSize: 20, UploadedBy: "新用户", UploadedAt: now.Add(-time.Minute), Width: 100, Height: 50},
	}
	for _, v := range versions {
		if err := db.Exec(`
			INSERT INTO screenshot_versions (id, screen_id, file_name, file_path, mime_type, file_size, note, uploaded_by, uploaded_at, width, height)
			VALUES (?, ?, 'shot.png', ?, ?, ?, '', ?, ?, ?, ?)
		`, v.ID, v.ScreenID, v.FilePath, v.MimeType, v.FileSize, v.UploadedBy, v.UploadedAt, v.Width, v.Height).Error; err != nil {
			t.Fatalf("insert legacy version: %v", err)
		}
		if err := store.Save(v.FilePath, bytes.NewReader([]byte("png:"+v.FilePath))); err != nil {
			t.Fatalf("store legacy file: %v", err)
		}
	}

	// 标注：一条挂在旧版本（迁移后应过期），一条挂在最新版本（保持不过期）
	annotations := []struct {
		id        string
		versionID string
		body      string
	}{
		{id: "ann-old", versionID: "v-old", body: "旧版标注"},
		{id: "ann-new", versionID: "v-new", body: "新版标注"},
	}
	for _, a := range annotations {
		if err := db.Exec(`
			INSERT INTO screenshot_annotations (id, screen_id, project_id, version_id, x, y, width, height, body, status, outdated, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0.1, 0.1, 0.2, 0.2, ?, 'open', 0, 'u', ?, ?)
		`, a.id, screenID, project.ID, a.versionID, a.body, now, now).Error; err != nil {
			t.Fatalf("insert legacy annotation: %v", err)
		}
	}

	return screenID, "v-new", []string{"proj-1/screenshots/v-old.png"}
}

// B1 回归：从旧表结构（无文件列、有 screenshot_versions 行与真实外键）出发，
// 走完整启动入口（外键修复、可空列准备、AutoMigrate、折叠迁移、索引登记）
// 必须成功升级，而不是在 AutoMigrate 给有数据行的旧表加 NOT NULL 列时 fatal。
func TestRunBootMigrations_FoldsLegacyMultiVersionDB(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	screenID, keepVersionID, removedPaths := insertLegacyFixtures(t, db, store)

	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("boot migrations: %v", err)
	}

	// 折叠：最新版本的文件信息并入 screen，group_name 规范化迁入 platform
	var screen model.ScreenshotScreen
	if err := db.Where("id = ?", screenID).First(&screen).Error; err != nil {
		t.Fatalf("find screen: %v", err)
	}
	if screen.FilePath != "proj-1/screenshots/"+keepVersionID+".png" {
		t.Fatalf("expected latest version file folded, got %q", screen.FilePath)
	}
	if screen.MimeType != "image/png" || screen.FileSize != 20 {
		t.Fatalf("unexpected folded file info: %+v", screen)
	}
	if screen.Width != 100 || screen.Height != 50 {
		t.Fatalf("expected folded dims 100x50, got %dx%d", screen.Width, screen.Height)
	}
	if screen.Platform == nil || *screen.Platform != "desktop" {
		t.Fatalf("expected platform from group_name, got %+v", screen.Platform)
	}
	if screen.UploadedBy != "新用户" {
		t.Fatalf("expected folded uploaded_by, got %q", screen.UploadedBy)
	}

	// 挂在旧版本上的标注过期，最新版本上的保持不变
	var outdated []model.ScreenshotAnnotation
	if err := db.Where("outdated = ?", true).Find(&outdated).Error; err != nil {
		t.Fatalf("find outdated: %v", err)
	}
	if len(outdated) != 1 || outdated[0].ID != "ann-old" {
		t.Fatalf("expected only ann-old outdated, got %+v", outdated)
	}
	var fresh model.ScreenshotAnnotation
	if err := db.Where("id = ?", "ann-new").First(&fresh).Error; err != nil {
		t.Fatalf("find ann-new: %v", err)
	}
	if fresh.Outdated {
		t.Fatalf("expected ann-new not outdated")
	}

	// versions 表、version_id 列与旧 screen 列清理干净
	if exists, err := sqliteTableExists(db, "screenshot_versions"); err != nil || exists {
		t.Fatalf("expected screenshot_versions dropped, exists=%v err=%v", exists, err)
	}
	for _, column := range []string{"version_id", "version_count", "group_name", "last_uploaded_at"} {
		table := "screenshot_annotations"
		if column != "version_id" {
			table = "screenshot_screens"
		}
		has, err := hasSQLiteColumn(db, table, column)
		if err != nil || has {
			t.Fatalf("expected legacy column %s on %s dropped, has=%v err=%v", column, table, has, err)
		}
	}

	// 变体唯一索引已建立在迁移完成的数据上
	var indexName string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_screenshot_screens_variant'`).Scan(&indexName).Error; err != nil || indexName == "" {
		t.Fatalf("expected variant index created, got %q err=%v", indexName, err)
	}

	// 被折叠的旧版本文件删除，最新版本文件保留；待删队列清空
	for _, path := range removedPaths {
		if exists, _ := store.Exists(path); exists {
			t.Fatalf("expected obsolete file %q removed", path)
		}
	}
	if exists, _ := store.Exists(screen.FilePath); !exists {
		t.Fatalf("expected kept file %q to survive", screen.FilePath)
	}
	var pending int64
	if err := db.Model(&model.ScreenshotPendingDelete{}).Count(&pending).Error; err != nil || pending != 0 {
		t.Fatalf("expected pending-delete queue drained, got %d err=%v", pending, err)
	}
}

// B1：--dry-run 必须在旧日志表删除、外键修复、AutoMigrate 之前进入只读分支，
// 打印统计后退出且库与文件原样保留。
func TestRunBootMigrations_DryRunTouchesNothing(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	insertLegacyFixtures(t, db, store)

	if err := runBootMigrations(db, store, zap.NewNop(), true, "", nil); err != nil {
		t.Fatalf("dry-run boot: %v", err)
	}

	// 旧结构原样：versions 表还在、文件列没加、数据没动
	if exists, err := sqliteTableExists(db, "screenshot_versions"); err != nil || !exists {
		t.Fatalf("expected versions table kept after dry-run, exists=%v err=%v", exists, err)
	}
	for _, column := range []string{"file_path", "mime_type", "file_size", "uploaded_at"} {
		has, err := hasSQLiteColumn(db, "screenshot_screens", column)
		if err != nil || has {
			t.Fatalf("expected dry-run to skip adding %s, has=%v err=%v", column, has, err)
		}
	}
	var screens int64
	if err := db.Raw(`SELECT COUNT(*) FROM screenshot_screens`).Scan(&screens).Error; err != nil {
		t.Fatalf("count screens: %v", err)
	}
	if screens != 1 {
		t.Fatalf("expected dry-run to leave data untouched, got %d screens", screens)
	}
	var versions int64
	if err := db.Raw(`SELECT COUNT(*) FROM screenshot_versions`).Scan(&versions).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 2 {
		t.Fatalf("expected dry-run to leave versions untouched, got %d", versions)
	}

	// AutoMigrate 与折叠都没跑：新表未创建
	for _, table := range []string{"screenshot_pending_deletes", "log_runs"} {
		if exists, _ := sqliteTableExists(db, table); exists {
			t.Fatalf("expected dry-run to skip creating %s", table)
		}
	}
	for _, path := range []string{"proj-1/screenshots/v-old.png", "proj-1/screenshots/v-new.png"} {
		if exists, _ := store.Exists(path); !exists {
			t.Fatalf("expected dry-run to keep file %q", path)
		}
	}
}

// 幂等：折叠完成后的第二次启动（versions 表已消失）是空操作；全新库直接空操作。
func TestRunBootMigrations_IdempotentAndSkipsFreshDB(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	insertLegacyFixtures(t, db, store)

	key := []byte("12345678901234567890123456789012")
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", key); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// 第二次执行：表已删，空操作不报错
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", key); err != nil {
		t.Fatalf("second migrate should be a no-op, got %v", err)
	}

	var screens int64
	if err := db.Model(&model.ScreenshotScreen{}).Count(&screens).Error; err != nil {
		t.Fatalf("count screens: %v", err)
	}
	if screens != 1 {
		t.Fatalf("expected 1 screen to survive, got %d", screens)
	}

	// 全新库（无 versions 表）直接空操作
	freshDB, freshStore, _ := setupLegacyScreenshotDB(t)
	if err := runBootMigrations(freshDB, freshStore, zap.NewNop(), false, "", key); err != nil {
		t.Fatalf("fresh db migrate: %v", err)
	}
}

// m6 回归：折叠产生的待删文件先落队列再删；删除失败时队列行保留、
// 下次启动继续删，files_removed 按成功数计。
func TestRunBootMigrations_PendingDeleteRetriesAcrossBoots(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	_, _, removedPaths := insertLegacyFixtures(t, db, store)
	if len(removedPaths) != 1 {
		t.Fatalf("setup: expected one obsolete path, got %v", removedPaths)
	}

	failing := &failingDeleteStorage{Storage: store, failPaths: map[string]bool{removedPaths[0]: true}}
	if err := runBootMigrations(db, failing, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("boot with failing delete: %v", err)
	}

	// 删除失败：文件还在，队列行保留
	if exists, _ := store.Exists(removedPaths[0]); !exists {
		t.Fatalf("expected failed file %q to remain", removedPaths[0])
	}
	var pending int64
	if err := db.Model(&model.ScreenshotPendingDelete{}).Count(&pending).Error; err != nil || pending != 1 {
		t.Fatalf("expected pending row kept, got %d err=%v", pending, err)
	}

	// 下次启动（versions 表已消失）继续出队删除
	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("retry boot: %v", err)
	}
	if exists, _ := store.Exists(removedPaths[0]); exists {
		t.Fatalf("expected file %q removed on retry", removedPaths[0])
	}
	if err := db.Model(&model.ScreenshotPendingDelete{}).Count(&pending).Error; err != nil || pending != 0 {
		t.Fatalf("expected queue drained on retry, got %d err=%v", pending, err)
	}
}

// 进程在 Delete 成功后、出队提交前退出时，下次启动对已缺失文件得到 ENOENT；
// 应视同清理成功出队，不再当可重试失败永久报错。
func TestDrainScreenshotPendingDeletes_MissingFileCountsAsSuccess(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	if err := db.AutoMigrate(&model.ScreenshotPendingDelete{}); err != nil {
		t.Fatalf("migrate pending deletes: %v", err)
	}
	missing := "screenshots/already-gone.png"
	if err := db.Create(&model.ScreenshotPendingDelete{
		Path: missing, CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed pending row: %v", err)
	}

	core, logs := observer.New(zapcore.ErrorLevel)
	removed, err := drainScreenshotPendingDeletes(db, store, zap.New(core))
	if err != nil {
		t.Fatalf("drain missing file: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected missing file to count as 1 drained, got %d", removed)
	}
	var pending int64
	if err := db.Model(&model.ScreenshotPendingDelete{}).Count(&pending).Error; err != nil || pending != 0 {
		t.Fatalf("expected queue empty, got %d err=%v", pending, err)
	}
	if logs.Len() != 0 {
		t.Fatalf("expected no error logs for missing file, got %+v", logs.All())
	}
}

// 折叠后没有版本行的空壳截图（新模型下无内容可展示）随之删除。
func TestRunBootMigrations_DropsShellScreensWithoutVersions(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	insertLegacyFixtures(t, db, store)
	now := time.Now().UTC()
	if err := db.Exec(`
		INSERT INTO screenshot_screens (id, project_id, screen_key, title, group_name, version_count, last_uploaded_at, created_at)
		VALUES ('screen-empty', 'proj-1', 'empty', '空壳', NULL, 0, ?, ?)
	`, now, now).Error; err != nil {
		t.Fatalf("insert shell screen: %v", err)
	}

	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("boot migrations: %v", err)
	}

	var count int64
	if err := db.Model(&model.ScreenshotScreen{}).Where("id = ?", "screen-empty").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected shell screen dropped, got %d err=%v", count, err)
	}
}

// 第三个写入口：group_name → platform 必须走与服务端写入相同的规范化
// （保留哨兵/逗号拒绝、长度上限），不能绕成 SQL lower(trim)。不合规旧分组名
// platform 保持未标注，原名不静默丢弃——追加进 title（title 为空则直接作为
// title）；合规值照常迁入。
func TestRunBootMigrations_GroupNameFollowsPlatformRules(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	insertLegacyFixtures(t, db, store)

	now := time.Now().UTC()
	longName := strings.Repeat("g", 120)
	cases := []struct{ id, title, groupName string }{
		{"screen-comma", "带逗号", "ios,tablet"},
		{"screen-none", "", "__none__"},
		{"screen-none-upper", "已标注", "__NONE__"},
		{"screen-long", "", longName},
		{"screen-ok", "", " Harmony "},
	}
	for _, c := range cases {
		if err := db.Exec(`
			INSERT INTO screenshot_screens (id, project_id, screen_key, title, group_name, version_count, last_uploaded_at, created_at)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?)
		`, c.id, "proj-1", c.id, c.title, c.groupName, now, now).Error; err != nil {
			t.Fatalf("insert screen %s: %v", c.id, err)
		}
		path := "proj-1/screenshots/" + c.id + ".png"
		if err := db.Exec(`
			INSERT INTO screenshot_versions (id, screen_id, file_name, file_path, mime_type, file_size, note, uploaded_by, uploaded_at, width, height)
			VALUES (?, ?, 'shot.png', ?, 'image/png', 1, '', 'u', ?, 10, 10)
		`, c.id+"-v", c.id, path, now).Error; err != nil {
			t.Fatalf("insert version %s: %v", c.id, err)
		}
		if err := store.Save(path, bytes.NewReader([]byte("png:"+c.id))); err != nil {
			t.Fatalf("store file %s: %v", path, err)
		}
	}

	if err := runBootMigrations(db, store, zap.NewNop(), false, "", []byte("12345678901234567890123456789012")); err != nil {
		t.Fatalf("boot migrations: %v", err)
	}

	// 不合规：platform 未标注（NULL），原分组名进 title 兜底
	for _, c := range []struct{ id, wantTitle string }{
		{"screen-comma", "带逗号 ios,tablet"},
		{"screen-none", "__none__"},
		{"screen-none-upper", "已标注 __NONE__"},
		{"screen-long", longName},
	} {
		var screen model.ScreenshotScreen
		if err := db.Where("id = ?", c.id).First(&screen).Error; err != nil {
			t.Fatalf("find %s: %v", c.id, err)
		}
		if screen.Platform != nil {
			t.Fatalf("%s: expected platform NULL, got %q", c.id, *screen.Platform)
		}
		if screen.Title != c.wantTitle {
			t.Fatalf("%s: expected title %q, got %q", c.id, c.wantTitle, screen.Title)
		}
	}

	// 合规：照常 trim+lowercase 迁入
	var ok model.ScreenshotScreen
	if err := db.Where("id = ?", "screen-ok").First(&ok).Error; err != nil {
		t.Fatalf("find screen-ok: %v", err)
	}
	if ok.Platform == nil || *ok.Platform != "harmony" {
		t.Fatalf("expected platform harmony, got %+v", ok.Platform)
	}
}

func TestCollectScreenshotMigrationStats_ReadsLegacyDB(t *testing.T) {
	db, store, _ := setupLegacyScreenshotDB(t)
	insertLegacyFixtures(t, db, store)

	stats, err := collectScreenshotMigrationStats(db)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Screens != 1 || stats.VersionTotal != 2 || stats.ObsoleteVersions != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// 全新库（表不存在）按 0 处理，不报错
	freshDB, _, _ := setupLegacyScreenshotDB(t)
	if err := freshDB.Exec("DROP TABLE screenshot_versions").Error; err != nil {
		t.Fatalf("drop versions: %v", err)
	}
	if err := freshDB.Exec("DROP TABLE screenshot_annotations").Error; err != nil {
		t.Fatalf("drop annotations: %v", err)
	}
	if err := freshDB.Exec("DROP TABLE screenshot_screens").Error; err != nil {
		t.Fatalf("drop screens: %v", err)
	}
	empty, err := collectScreenshotMigrationStats(freshDB)
	if err != nil || empty.Screens != 0 || empty.VersionTotal != 0 {
		t.Fatalf("expected zero stats for missing tables, got %+v err=%v", empty, err)
	}

	// prepareScreenshotFoldMigration：给旧表补可空文件列，幂等
	if err := prepareScreenshotFoldMigration(db); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := prepareScreenshotFoldMigration(db); err != nil {
		t.Fatalf("prepare idempotent: %v", err)
	}
	for _, column := range []string{"file_path", "mime_type", "file_size", "uploaded_at"} {
		has, err := hasSQLiteColumn(db, "screenshot_screens", column)
		if err != nil || !has {
			t.Fatalf("expected column %s added, has=%v err=%v", column, has, err)
		}
	}
}
