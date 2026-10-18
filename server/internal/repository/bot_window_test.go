package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-115 F5：失败标记的条件写。「失败不能覆盖成功」由 UpsertFailure 的
// ON CONFLICT … WHERE context_window = 0 在数据库层原子保证，直接调用
//（绕过服务的任何预检查）也不得覆盖有效成功行。

func setupBotWindowTestDB(t *testing.T) (*gorm.DB, *BotWindowRepository) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.BotModelWindow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_model_windows_addr_model
		ON bot_model_windows(base_url, model)`).Error; err != nil {
		t.Fatalf("unique index: %v", err)
	}
	return db, NewBotWindowRepository(db)
}

func TestBotWindowUpsertFailureNeverOverwritesSuccess(t *testing.T) {
	_, repo := setupBotWindowTestDB(t)
	base := "https://api.example.com/v1"

	// 成功行先落库。
	if err := repo.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m1",
		ContextWindow: 1048576, MaxOutputTokens: 262144,
		Source: "detected", Detail: "GET /models",
		FetchedAt: time.Now().UTC().Add(-time.Hour), UpdatedAt: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("seed success: %v", err)
	}

	// 直接对同一 (addr, model) 调 UpsertFailure：成功行必须原样保持。
	if err := repo.UpsertFailure(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m1",
		ContextWindow: 0, MaxOutputTokens: 0,
		Source: "detect_failed", Detail: "boom",
		FetchedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("upsert failure over success: %v", err)
	}
	row, err := repo.FindByAddrModel(base, "m1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 ||
		row.Source != "detected" || row.Detail != "GET /models" {
		t.Fatalf("成功行不得被失败覆盖: %+v", row)
	}

	// 失败行被新失败更新（值可相同，形如重试时间刷新）。
	firstAt := time.Now().UTC().Add(-5 * time.Minute)
	if err := repo.UpsertFailure(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m2",
		ContextWindow: 0, Source: "detect_failed", Detail: "first",
		FetchedAt: firstAt, UpdatedAt: firstAt,
	}); err != nil {
		t.Fatalf("seed failure marker: %v", err)
	}
	if err := repo.UpsertFailure(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m2",
		ContextWindow: 0, Source: "detect_failed", Detail: "second",
		FetchedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("refresh failure marker: %v", err)
	}
	row2, err := repo.FindByAddrModel(base, "m2")
	if err != nil {
		t.Fatalf("find m2: %v", err)
	}
	if row2.ContextWindow != 0 || row2.Source != "detect_failed" || row2.Detail != "second" {
		t.Fatalf("失败行应被新失败更新: %+v", row2)
	}

	// 无行时插入。
	if err := repo.UpsertFailure(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m3",
		ContextWindow: 0, Source: "detect_failed", Detail: "fresh",
		FetchedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert failure marker: %v", err)
	}
	row3, err := repo.FindByAddrModel(base, "m3")
	if err != nil || row3.ContextWindow != 0 || row3.Detail != "fresh" {
		t.Fatalf("失败标记应插入: %+v err=%v", row3, err)
	}
}

// 第五轮复审（INT-115）：成功行的失败退避。MarkProbeFailed 命中有效成功行时
// 只记失败时间（窗口/输出/fetched_at 原样）；失败行与无行不命中，由
// UpsertFailure 插入或刷新（那里也记失败时间）；成功写清除退避。
func TestBotWindowMarkProbeFailed(t *testing.T) {
	_, repo := setupBotWindowTestDB(t)
	base := "https://api.example.com/v1"
	successAt := time.Now().UTC().Add(-2 * time.Hour)

	// 成功行：命中且只改 failed_at/updated_at。
	if err := repo.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m1",
		ContextWindow: 1048576, MaxOutputTokens: 262144,
		Source: "detected", Detail: "GET /models",
		FetchedAt: successAt, UpdatedAt: successAt,
	}); err != nil {
		t.Fatalf("seed success: %v", err)
	}
	at := time.Now().UTC()
	hit, err := repo.MarkProbeFailed(base, "m1", at)
	if err != nil || !hit {
		t.Fatalf("成功行应命中: hit=%v err=%v", hit, err)
	}
	row, err := repo.FindByAddrModel(base, "m1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if row.FailedAt == nil || row.FailedAt.Sub(at).Abs() > time.Second {
		t.Fatalf("失败时间应记录: %+v", row.FailedAt)
	}
	// 时间比较放宽到秒级：sqlite 存储往返不保证纳秒精度。
	if row.ContextWindow != 1048576 || row.MaxOutputTokens != 262144 ||
		row.Source != "detected" || row.Detail != "GET /models" ||
		row.FetchedAt.Sub(successAt).Abs() > time.Second {
		t.Fatalf("MarkProbeFailed 只能改失败时间，不得动成功数据: %+v", row)
	}

	// 成功写清除退避：赋值列含 failed_at，nil 写成 NULL。
	if err := repo.Upsert(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m1",
		ContextWindow: 2097152, Source: "detected", Detail: "GET /models",
		FetchedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("success upsert: %v", err)
	}
	row, err = repo.FindByAddrModel(base, "m1")
	if err != nil {
		t.Fatalf("find after upsert: %v", err)
	}
	if row.FailedAt != nil || row.ContextWindow != 2097152 {
		t.Fatalf("成功写入应清除 failed_at: %+v", row)
	}

	// 失败行与无行不命中（它们的失败时间由 UpsertFailure 记）。
	failAt := time.Now().UTC()
	if err := repo.UpsertFailure(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m2",
		ContextWindow: 0, Source: "detect_failed", Detail: "boom",
		FetchedAt: failAt, FailedAt: &failAt, UpdatedAt: failAt,
	}); err != nil {
		t.Fatalf("seed failure: %v", err)
	}
	if hit, err := repo.MarkProbeFailed(base, "m2", time.Now().UTC()); err != nil || hit {
		t.Fatalf("失败行不应命中 MarkProbeFailed: hit=%v err=%v", hit, err)
	}
	if hit, err := repo.MarkProbeFailed(base, "m3", time.Now().UTC()); err != nil || hit {
		t.Fatalf("无行不应命中 MarkProbeFailed: hit=%v err=%v", hit, err)
	}

	// UpsertFailure 刷新失败行时同时刷新失败时间。
	refreshAt := time.Now().UTC()
	if err := repo.UpsertFailure(&model.BotModelWindow{
		ID: uuid.NewString(), BaseURL: base, Model: "m2",
		ContextWindow: 0, Source: "detect_failed", Detail: "again",
		FetchedAt: refreshAt, FailedAt: &refreshAt, UpdatedAt: refreshAt,
	}); err != nil {
		t.Fatalf("refresh failure: %v", err)
	}
	row2, err := repo.FindByAddrModel(base, "m2")
	if err != nil {
		t.Fatalf("find m2: %v", err)
	}
	if row2.Detail != "again" || row2.FailedAt == nil || row2.FailedAt.Sub(refreshAt).Abs() > time.Second {
		t.Fatalf("失败行刷新应同时更新 failed_at: %+v", row2)
	}
}

// 第六轮复审（INT-115）：大小写折叠查找。LOWER(model) 命中变体；失败标记与
// 大小写变体成功行共存时成功行排前——两种落行顺序都必须成立（折叠查询多行
// 命中的返回顺序若不确定，失败行会随机遮蔽成功行）。
func TestBotWindowFindByAddrModelFold(t *testing.T) {
	base := "https://api.example.com/v1"
	seed := func(repo *BotWindowRepository, name string, window int64) {
		t.Helper()
		now := time.Now().UTC()
		var err error
		if window == 0 {
			err = repo.UpsertFailure(&model.BotModelWindow{
				ID: uuid.NewString(), BaseURL: base, Model: name,
				ContextWindow: 0, Source: "detect_failed", Detail: "boom",
				FetchedAt: now, FailedAt: &now, UpdatedAt: now,
			})
		} else {
			err = repo.Upsert(&model.BotModelWindow{
				ID: uuid.NewString(), BaseURL: base, Model: name,
				ContextWindow: window, MaxOutputTokens: 16384,
				Source: "detected", Detail: "GET /models",
				FetchedAt: now, UpdatedAt: now,
			})
		}
		if err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	type foldSeed struct {
		modelName string
		window    int64
	}
	for _, tc := range []struct {
		label string
		seeds []foldSeed // 落行顺序
	}{
		{"成功行先落", []foldSeed{{"M1", 65536}, {"m1", 0}}},
		{"失败行先落", []foldSeed{{"m1", 0}, {"M1", 65536}}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			_, repo := setupBotWindowTestDB(t)
			for _, s := range tc.seeds {
				seed(repo, s.modelName, s.window)
			}

			// 折叠查优先返回成功行，不受落行顺序影响。
			row, err := repo.FindByAddrModelFold(base, "m1")
			if err != nil || row.Model != "M1" || row.ContextWindow != 65536 {
				t.Fatalf("失败标记不得遮蔽变体成功行: %+v err=%v", row, err)
			}
			// 精确查找不受影响（对照：命中 m1 失败标记本身）。
			exact, err := repo.FindByAddrModel(base, "m1")
			if err != nil || exact.ContextWindow != 0 {
				t.Fatalf("精确查找应命中失败行: %+v err=%v", exact, err)
			}
		})
	}

	// 仅失败变体：折叠查仍返回失败行（服务层据此维持失败语义）。
	_, repo := setupBotWindowTestDB(t)
	seed(repo, "m2", 0)
	frow, err := repo.FindByAddrModelFold(base, "M2")
	if err != nil || frow.ContextWindow != 0 {
		t.Fatalf("仅失败变体时应返回失败行: %+v err=%v", frow, err)
	}
	// 无行返回 ErrRecordNotFound，调用方按「缓存缺失」处理。
	if _, err := repo.FindByAddrModelFold(base, "m3"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("无行应返回 ErrRecordNotFound: %v", err)
	}
}
