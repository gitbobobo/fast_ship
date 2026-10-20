package selflog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// testPipeline 建一条以临时独立日志库为存储的管道；silent=true 时 stdout
// 输出静默。用完调用方自行 Close。
func testPipeline(t *testing.T, opts func(*Options)) (*Pipeline, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "self_logs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	o := Options{
		Sink:          store,
		Stdout:        slog.NewTextHandler(io.Discard, nil),
		FlushInterval: 5 * time.Millisecond,
		MinLevel:      slog.LevelDebug,
	}
	if opts != nil {
		opts(&o)
	}
	p, err := Start(o)
	if err != nil {
		t.Fatalf("start pipeline: %v", err)
	}
	t.Cleanup(p.Close)
	return p, store
}

func storeCount(t *testing.T, s *Store) int64 {
	t.Helper()
	n, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func TestPipelineWritesStructuredFields(t *testing.T) {
	p, store := testPipeline(t, nil)
	logger := slog.New(p.Handler())

	logger.Info("同步完成",
		slog.String("module", "github"),
		slog.String("run_id", "run-1"),
		slog.String("user_id", "u-1"),
		slog.Int("count", 3),
	)
	p.Close()

	if storeCount(t, store) != 1 {
		t.Fatalf("entry count = %d, want 1", storeCount(t, store))
	}
	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	e := page.Items[0]
	if e.Level != LevelInfo || e.Module != "github" || e.RunID != "run-1" || e.UserID != "u-1" {
		t.Fatalf("entry = %+v", e)
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(e.Extra), &extra); err != nil {
		t.Fatalf("extra not json: %v (%s)", err, e.Extra)
	}
	if extra["count"] != float64(3) {
		t.Fatalf("extra.count = %v, want 3", extra["count"])
	}
}

func TestPipelineSanitizesSecrets(t *testing.T) {
	p, store := testPipeline(t, nil)
	logger := slog.New(p.Handler())

	logger.Warn("配置了 token="+fakeSecret("fsk_", "AbCdEfGh12345678")+" 和 https://u:"+fakeSecret("fsr_", "SecretValue123456")+"@example.com",
		slog.String("module", "api_key"),
		slog.String("authorization", "Bearer real-secret-value"),
		slog.String("github_token", fakeSecret("ghp_", "AbCdEfGhIjKlMnOpQrStUvWx")),
		slog.String("password", "plain-password"),
		slog.String("note", "refresh token "+fakeSecret("fsr_", "SecretValue123456")+" leaked in note"),
		slog.String("jwt", fakeSecret("eyJ", "hbGciOiJIUzI1NiJ9."+fakeSecret("eyJ", "eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c")+"")),
	)
	p.Close()

	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	dump := page.Items[0].Message + page.Items[0].Extra
	for _, secret := range []string{
		fakeSecret("fsk_", "AbCdEfGh12345678"), fakeSecret("fsr_", "SecretValue123456"), fakeSecret("ghp_", "AbCdEfGhIjKlMnOpQrStUvWx"),
		"plain-password", "real-secret-value", fakeSecret("eyJ", "hbGciOiJIUzI1NiJ9"),
	} {
		if strings.Contains(dump, secret) {
			t.Errorf("日志泄漏明文 %q:\n%s", secret, dump)
		}
	}
	if !strings.Contains(page.Items[0].Extra, "[REDACTED]") {
		t.Fatalf("脱敏占位缺失: %s", page.Items[0].Extra)
	}
}

// failingSink 恒定写失败，验证写库失败不反压业务调用方。
type failingSink struct {
	mu      sync.Mutex
	calls   int
	batches [][]Entry
}

func (f *failingSink) Add(_ context.Context, entries []Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.batches) < 10 {
		f.batches = append(f.batches, entries)
	}
	return fmt.Errorf("disk on fire")
}

func (f *failingSink) stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, len(f.batches)
}

func TestAsyncWriteFailureDoesNotAffectBusiness(t *testing.T) {
	sink := &failingSink{}
	p, err := Start(Options{
		Sink:          sink,
		Stdout:        slog.NewTextHandler(io.Discard, nil),
		FlushInterval: 5 * time.Millisecond,
		BatchSize:     10,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	logger := slog.New(p.Handler())

	// 连续写入远超批量上限，任何一次都不允许阻塞或 panic。
	for i := 0; i < 200; i++ {
		logger.Info("业务日志不受写库失败影响", slog.Int("i", i))
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls, _ := sink.stats(); calls > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.Close()

	if calls, batchCount := sink.stats(); calls == 0 || batchCount == 0 {
		t.Fatalf("写库应被尝试过: calls=%d batches=%d", calls, batchCount)
	}
	if p.Failed() <= 0 {
		t.Fatalf("失败计数应大于 0, got %d", p.Failed())
	}
}

func TestRetentionCleanupByDaysAndCap(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()

	now := time.Now().UTC()
	old := []Entry{{Time: now.AddDate(0, 0, -10), Level: LevelInfo, Message: "old"}}
	fresh := []Entry{{Time: now, Level: LevelInfo, Message: "fresh"}}
	if err := store.Add(ctx, old); err != nil {
		t.Fatalf("seed old: %v", err)
	}
	if err := store.Add(ctx, fresh); err != nil {
		t.Fatalf("seed fresh: %v", err)
	}
	// 再补 8 条新日志凑上限 10。
	extra := make([]Entry, 0, 8)
	for i := 0; i < 8; i++ {
		extra = append(extra, Entry{Time: now.Add(time.Duration(i) * time.Second), Level: LevelInfo, Message: "bulk"})
	}
	if err := store.Add(ctx, extra); err != nil {
		t.Fatalf("seed bulk: %v", err)
	}

	removed, err := store.Cleanup(ctx, RetentionOptions{KeepDays: 7, MaxEntries: 5})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// 1 条过期 + 超出上限 5 条（10-1=9 条存活，删 4 条最旧）。
	if removed != 5 {
		t.Fatalf("removed = %d, want 5", removed)
	}
	if got := storeCount(t, store); got != 5 {
		t.Fatalf("count = %d, want 5", got)
	}
	page, err := store.Query(ctx, Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if page.Items[0].Message == "old" {
		t.Fatalf("过期日志未被清理")
	}
	_ = p
}

func TestCursorPaginationStableWithIdenticalTimestamps(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()

	// 30 条时间完全相同的日志：键集 (time, id) 必须不重不漏。
	same := time.Now().UTC().Truncate(time.Second)
	batch := make([]Entry, 0, 30)
	for i := 0; i < 30; i++ {
		batch = append(batch, Entry{Time: same, Level: LevelInfo, Message: fmt.Sprintf("m-%02d", i)})
	}
	if err := store.Add(ctx, batch); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	run := func(asc bool) {
		seen := map[int64]bool{}
		cursor := ""
		pages := 0
		for {
			page, err := store.Query(ctx, Filter{}, cursor, 7, asc)
			if err != nil {
				t.Fatalf("query page %d: %v", pages, err)
			}
			for _, e := range page.Items {
				if seen[e.ID] {
					t.Fatalf("asc=%v 重复 ID %d", asc, e.ID)
				}
				seen[e.ID] = true
			}
			pages++
			if !page.HasMore {
				break
			}
			cursor = page.NextCursor
			if cursor == "" {
				t.Fatalf("asc=%v has_more=true 但 next_cursor 为空", asc)
			}
			if pages > 10 {
				t.Fatalf("asc=%v 翻页不收敛", asc)
			}
		}
		if len(seen) != 30 {
			t.Fatalf("asc=%v 共取到 %d 条, want 30", asc, len(seen))
		}
	}
	run(true)
	run(false)

	// 游标与方向不匹配应报错。
	if _, _, err := DecodeCursor(EncodeCursor(same, 1, true), false); err == nil {
		t.Fatalf("方向不匹配的游标应被拒绝")
	}
	if _, _, err := DecodeCursor("not-a-cursor", true); err == nil {
		t.Fatalf("非法游标应被拒绝")
	}
}

func TestQueryFilterCombinations(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := []Entry{
		{Time: now.Add(-3 * time.Hour), Level: LevelInfo, Module: "http", Message: "请求完成", RequestID: "req-1", UserID: "u-1", Extra: `{"status":200}`},
		{Time: now.Add(-2 * time.Hour), Level: LevelWarn, Module: "http", Message: "请求慢", RequestID: "req-2", UserID: "u-1"},
		{Time: now.Add(-1 * time.Hour), Level: LevelError, Module: "automation", Message: "调度失败", RunID: "run-9", UserID: "u-2"},
		{Time: now.Add(-30 * time.Minute), Level: LevelInfo, Module: "t3", Message: "线程启动", ThreadID: "th-1"},
		{Time: now, Level: LevelError, Module: "github", Message: "同步失败 token=" + fakeSecret("ghp_", "AbCdEfGhIjKlMnOpQrSt") + "", UserID: "u-1"},
		{Time: now.Add(time.Second), Level: LevelInfo, Module: "app", Message: "服务启动", SystemScope: true},
	}
	if err := store.Add(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	count := func(f Filter) int {
		page, err := store.Query(ctx, f, "", 100, false)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		return len(page.Items)
	}

	if got := count(Filter{Level: LevelError}); got != 2 {
		t.Errorf("精确级别过滤 = %d, want 2", got)
	}
	if got := count(Filter{MinLevel: LevelWarn}); got != 3 {
		t.Errorf("不低于级别过滤 = %d, want 3", got)
	}
	if got := count(Filter{Modules: []string{"http", "t3"}}); got != 3 {
		t.Errorf("模块多值过滤 = %d, want 3", got)
	}
	start := now.Add(-90 * time.Minute)
	end := now.Add(-29 * time.Minute)
	if got := count(Filter{StartTime: &start, EndTime: &end}); got != 2 {
		t.Errorf("时间范围过滤 = %d, want 2", got)
	}
	if got := count(Filter{Keyword: "失败"}); got != 2 {
		t.Errorf("关键词包含 = %d, want 2", got)
	}
	if got := count(Filter{ExcludeKw: "失败"}); got != 4 {
		t.Errorf("关键词排除 = %d, want 4", got)
	}
	if got := count(Filter{RunID: "run-9"}); got != 1 {
		t.Errorf("run_id 过滤 = %d, want 1", got)
	}
	if got := count(Filter{ExtraEq: [][2]string{{"status", "200"}}}); got != 1 {
		t.Errorf("extra 等值过滤 = %d, want 1", got)
	}
	if got := count(Filter{VisibleUserID: "u-1"}); got != 4 {
		t.Errorf("可见性过滤 = %d, want 4 (3 条 u-1 + 1 条显式系统行)", got)
	}
	if got := count(Filter{VisibleUserID: "u-1", ThreadID: "th-1"}); got != 0 {
		t.Errorf("带 thread_id 的无主行不应再当系统行公开 = %d, want 0", got)
	}
	sys, err := store.Query(ctx, Filter{VisibleUserID: "u-1", Keyword: "服务启动"}, "", 10, false)
	if err != nil {
		t.Fatalf("query system: %v", err)
	}
	if len(sys.Items) != 1 || !sys.Items[0].SystemScope {
		t.Errorf("显式系统行应对 u-1 可见: %+v", sys.Items)
	}

	// 组合：模块 + 级别。
	if got := count(Filter{Modules: []string{"http"}, MinLevel: LevelWarn}); got != 1 {
		t.Errorf("组合过滤 = %d, want 1", got)
	}
}

func TestTraceByRequestAndRun(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := []Entry{
		{Time: now.Add(-2 * time.Second), Level: LevelInfo, Module: "http", Message: "请求开始", RequestID: "req-t"},
		{Time: now.Add(-1 * time.Second), Level: LevelError, Module: "http", Message: "panic", RequestID: "req-t", Extra: `{"stack":"..."}`},
		{Time: now, Level: LevelError, Module: "http", Message: "500", RequestID: "req-t"},
		{Time: now, Level: LevelError, Module: "automation", Message: "派发失败", RunID: "run-t"},
	}
	if err := store.Add(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	items, truncated, err := store.Trace(ctx, Filter{RequestID: "req-t"})
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if len(items) != 3 || truncated {
		t.Fatalf("trace = %d items (truncated=%v), want 3 false", len(items), truncated)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Time.Before(items[i-1].Time) {
			t.Fatalf("trace 未按时间正序")
		}
	}

	items, _, err = store.Trace(ctx, Filter{RunID: "run-t"})
	if err != nil || len(items) != 1 {
		t.Fatalf("trace by run = %d items, err=%v", len(items), err)
	}
}

func TestAggregateByModuleLevelTime(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Hour)
	seed := []Entry{
		{Time: base, Level: LevelInfo, Module: "http"},
		{Time: base.Add(time.Minute), Level: LevelInfo, Module: "http"},
		{Time: base.Add(2 * time.Minute), Level: LevelError, Module: "automation"},
		{Time: base.Add(2 * time.Hour), Level: LevelWarn, Module: "t3"},
	}
	if err := store.Add(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	rows, truncated, err := store.Aggregate(ctx, Filter{}, true, false, false, "")
	if err != nil {
		t.Fatalf("aggregate module: %v", err)
	}
	if truncated {
		t.Fatalf("small aggregate should not be truncated")
	}
	if len(rows) != 3 {
		t.Fatalf("by module = %+v", rows)
	}
	for _, row := range rows {
		if row.Module == "http" && row.Count != 2 {
			t.Fatalf("by module http count = %d, want 2 (%+v)", row.Count, rows)
		}
	}

	rows, _, err = store.Aggregate(ctx, Filter{}, false, true, false, "")
	if err != nil {
		t.Fatalf("aggregate level: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("by level = %+v", rows)
	}

	rows, _, err = store.Aggregate(ctx, Filter{}, false, false, true, "minute")
	if err != nil {
		t.Fatalf("aggregate minute: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("by minute bucket = %+v", rows)
	}

	rows, _, err = store.Aggregate(ctx, Filter{}, false, false, true, "hour")
	if err != nil {
		t.Fatalf("aggregate hour: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("by hour bucket = %+v", rows)
	}
}

func TestPipelineMinLevelSkipsIngestButMirrorsStdout(t *testing.T) {
	var buf bytes.Buffer
	p, store := testPipeline(t, func(o *Options) {
		o.MinLevel = slog.LevelWarn
		o.Stdout = slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	})
	logger := slog.New(p.Handler())
	logger.Info("info only on stdout")
	logger.Error("error ingested")
	p.Close()

	if storeCount(t, store) != 1 {
		t.Fatalf("低于 MinLevel 的条目不应入库, count=%d", storeCount(t, store))
	}
	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if page.Items[0].Message != "error ingested" {
		t.Fatalf("入库条目 = %q, want error ingested", page.Items[0].Message)
	}
	out := buf.String()
	if !strings.Contains(out, "info only on stdout") {
		t.Fatalf("stdout 应仍输出低于入库级别的日志: %s", out)
	}
	if !strings.Contains(out, "error ingested") {
		t.Fatalf("stdout 应输出 ERROR: %s", out)
	}
}

func TestZapBridgeCapturesServiceLogs(t *testing.T) {
	p, store := testPipeline(t, nil)

	stdout := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.Lock(zapcore.AddSync(io.Discard)), zapcore.DebugLevel)
	core := zapcore.NewTee(stdout, NewZapCore(p.IngestHandler(), zapcore.DebugLevel))
	zapLogger := zap.New(core, zap.AddStacktrace(zapcore.ErrorLevel)).With(zap.String("module", "automation"))

	zapLogger.Info("自动化线程已启动",
		zap.String("run_id", "run-z"),
		zap.String("user_id", "u-z"),
		zap.Duration("elapsed", 3*time.Second),
	)
	zapLogger.Error("查询到期自动化失败", zap.Error(fmt.Errorf("db locked")))
	p.Close()

	page, err := store.Query(context.Background(), Filter{Modules: []string{"automation"}}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("zap 桥接条目 = %d, want 2", len(page.Items))
	}
	started := page.Items[0]
	if started.RunID != "run-z" || started.UserID != "u-z" || started.Level != LevelInfo {
		t.Fatalf("started entry = %+v", started)
	}
	if !strings.Contains(started.Extra, "3") {
		t.Fatalf("duration 字段丢失: %s", started.Extra)
	}
	failed := page.Items[1]
	if failed.Level != LevelError || !strings.Contains(failed.Message, "查询到期自动化失败") {
		t.Fatalf("error entry = %+v", failed)
	}
	if !strings.Contains(failed.Extra, "db locked") || !strings.Contains(failed.Extra, "stack") {
		t.Fatalf("zap 错误字段/堆栈未随桥接入库: %s", failed.Extra)
	}
}

func TestPipelineSanitizesNestedAndNonStringSecrets(t *testing.T) {
	p, store := testPipeline(t, nil)
	logger := slog.New(p.Handler())

	logger.Warn("nested secrets",
		slog.String("request_id", fakeSecret("fsk_", "AbCdEfGh12345678")),
		slog.String("token_id", "tok-keep-me"),
		slog.String("api_key_id", "key-keep-me"),
		slog.Int("password", 12345),
		slog.Any("credentials", map[string]interface{}{
			"password": "hunter2-plain",
			"token":    fakeSecret("ghp_", "AbCdEfGhIjKlMnOpQrStUvWx"),
			"note":     "ok",
		}),
		slog.Any("items", []interface{}{
			map[string]interface{}{"authorization": "Bearer real-secret-value"},
			"refresh " + fakeSecret("fsr_", "SecretValue123456") + "",
		}),
	)
	p.Close()

	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	e := page.Items[0]
	if strings.Contains(e.RequestID, fakeSecret("fsk_", "AbCdEfGh12345678")) {
		t.Fatalf("关联列 request_id 未脱敏: %q", e.RequestID)
	}
	dump := e.Message + e.Extra + e.RequestID
	for _, secret := range []string{
		fakeSecret("fsk_", "AbCdEfGh12345678"), "hunter2-plain", fakeSecret("ghp_", "AbCdEfGhIjKlMnOpQrStUvWx"),
		"real-secret-value", fakeSecret("fsr_", "SecretValue123456"), "12345",
	} {
		if strings.Contains(dump, secret) {
			t.Errorf("泄漏明文 %q:\n%s", secret, dump)
		}
	}
	if !strings.Contains(e.Extra, "tok-keep-me") || !strings.Contains(e.Extra, "key-keep-me") {
		t.Fatalf("token_id/api_key_id 被误伤: %s", e.Extra)
	}
}

func TestPipelineWithGroupPreservesBoundCorrelation(t *testing.T) {
	p, store := testPipeline(t, nil)
	logger := slog.New(p.Handler()).With(
		slog.String("module", "http"),
		slog.String("request_id", "req-bound"),
	)
	logger.WithGroup("req").Info("grouped", slog.String("path", "/x"))
	p.Close()

	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	e := page.Items[0]
	if e.Module != "http" || e.RequestID != "req-bound" {
		t.Fatalf("With 后再 WithGroup 不应把关联列改写成组前缀: %+v", e)
	}
	if !strings.Contains(e.Extra, `"req.path":"/x"`) && !strings.Contains(e.Extra, `"req.path": "/x"`) {
		t.Fatalf("记录属性应带组前缀: %s", e.Extra)
	}
}

func TestPipelineWithGroupThenWithAttrs(t *testing.T) {
	p, store := testPipeline(t, nil)
	logger := slog.New(p.Handler()).WithGroup("g").With(slog.String("k", "v"))
	logger.Info("msg", slog.Int("n", 1))
	p.Close()

	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	extra := page.Items[0].Extra
	if !strings.Contains(extra, "g.k") || !strings.Contains(extra, "g.n") {
		t.Fatalf("WithGroup 后再 WithAttrs/Handle 应保留组前缀: %s", extra)
	}
}

func TestZapBridgeRedactsNestedObjects(t *testing.T) {
	p, store := testPipeline(t, nil)
	stdout := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.Lock(zapcore.AddSync(io.Discard)), zapcore.DebugLevel)
	core := zapcore.NewTee(stdout, NewZapCore(p.IngestHandler(), zapcore.DebugLevel))
	zapLogger := zap.New(core)

	zapLogger.Error("creds",
		zap.Any("credentials", map[string]string{"password": "hunter2-plain", "token": fakeSecret("fsk_", "AbCdEfGh12345678")}),
		zap.String("token_id", "tok-keep-me"),
	)
	p.Close()

	page, err := store.Query(context.Background(), Filter{}, "", 10, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	dump := page.Items[0].Extra
	if strings.Contains(dump, "hunter2-plain") || strings.Contains(dump, fakeSecret("fsk_", "AbCdEfGh12345678")) {
		t.Fatalf("zap 嵌套对象未脱敏: %s", dump)
	}
	if !strings.Contains(dump, "tok-keep-me") {
		t.Fatalf("token_id 被误伤: %s", dump)
	}
}

func TestTraceKeepsNewestEntries(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	const extra = 5
	batch := make([]Entry, 0, TraceLimit+extra)
	for i := 0; i < TraceLimit+extra; i++ {
		msg := fmt.Sprintf("m-%04d", i)
		if i == 0 {
			msg = "oldest-start"
		}
		if i == TraceLimit+extra-1 {
			msg = "newest-panic"
		}
		batch = append(batch, Entry{
			Time:      now.Add(time.Duration(i) * time.Millisecond),
			Level:     LevelInfo,
			Message:   msg,
			RequestID: "req-trunc",
		})
	}
	if err := store.Add(ctx, batch); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	items, truncated, err := store.Trace(ctx, Filter{RequestID: "req-trunc"})
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if !truncated || len(items) != TraceLimit {
		t.Fatalf("trace = %d truncated=%v, want %d true", len(items), truncated, TraceLimit)
	}
	if items[0].Message == "oldest-start" {
		t.Fatalf("应丢掉最旧条目，保留最新 %d 条", TraceLimit)
	}
	if items[len(items)-1].Message != "newest-panic" {
		t.Fatalf("链路末尾应是最新 panic: %q", items[len(items)-1].Message)
	}
	for i := 1; i < len(items); i++ {
		if items[i].Time.Before(items[i-1].Time) {
			t.Fatalf("trace 未按时间正序")
		}
	}
}

func TestAggregateKeepsNewestBuckets(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour)
	const total = maxAggregateRows + 3
	batch := make([]Entry, 0, total)
	for i := 0; i < total; i++ {
		batch = append(batch, Entry{
			Time:    base.Add(time.Duration(i) * time.Minute),
			Level:   LevelInfo,
			Module:  "http",
			Message: fmt.Sprintf("b-%d", i),
		})
	}
	if err := store.Add(ctx, batch); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	rows, truncated, err := store.Aggregate(ctx, Filter{}, false, false, true, "minute")
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if !truncated || len(rows) != maxAggregateRows {
		t.Fatalf("rows=%d truncated=%v, want %d true", len(rows), truncated, maxAggregateRows)
	}
	oldest := base.UTC().Format(time.RFC3339)
	newest := base.Add(time.Duration(total-1) * time.Minute).UTC().Truncate(time.Minute).Format(time.RFC3339)
	if rows[0].TimeBucket == oldest {
		t.Fatalf("应丢掉最旧时间桶: %+v", rows[0])
	}
	if rows[len(rows)-1].TimeBucket != newest {
		t.Fatalf("最后一桶应是最新: got %s want %s", rows[len(rows)-1].TimeBucket, newest)
	}
}

func TestCleanupConcurrentWritesDoNotDropBatches(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "cleanup-race.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	old := make([]Entry, 0, 2500)
	for i := 0; i < 2500; i++ {
		old = append(old, Entry{Time: now.AddDate(0, 0, -10), Level: LevelInfo, Message: "old"})
	}
	if err := store.Add(ctx, old); err != nil {
		t.Fatalf("seed old: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	cleanupErr := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := store.Cleanup(ctx, RetentionOptions{KeepDays: 7}); err != nil {
			cleanupErr <- err
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			batch := make([]Entry, 0, 50)
			for j := 0; j < 50; j++ {
				batch = append(batch, Entry{Time: now, Level: LevelInfo, Message: fmt.Sprintf("fresh-%d-%d", n, j)})
			}
			if err := store.Add(ctx, batch); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	close(cleanupErr)
	for err := range errCh {
		t.Fatalf("清理期间写入失败: %v", err)
	}
	if err := <-cleanupErr; err != nil {
		t.Fatalf("分批清理失败: %v", err)
	}
	page, err := store.Query(ctx, Filter{Keyword: "fresh-"}, "", 500, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 400 {
		t.Fatalf("并发写入条数 = %d, want 400（清理不应丢新批）", len(page.Items))
	}
}

func TestSensitiveKeyExactMatchLeavesIdentifiers(t *testing.T) {
	if isSensitiveKey("token_id") || isSensitiveKey("api_key_id") || isSensitiveKey("github_token_id") {
		t.Fatalf("标识字段不应整值打码")
	}
	if !isSensitiveKey("token") || !isSensitiveKey("http.authorization") || !isSensitiveKey("password") {
		t.Fatalf("确切秘密字段应整值打码")
	}
}

// TestCapacityAndWritePerformance 基本容量与写入性能验证：
// 5000 条典型日志批量入库后，单条平均体积应有界，批量写入耗时应远低于交互阈值。
func TestCapacityAndWritePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("容量基准在 -short 下跳过")
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "capacity.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.db.Exec("DELETE FROM self_logs")

	const total = 5000
	base := time.Now().UTC()
	start := time.Now()
	for i := 0; i < total; i += 200 {
		batch := make([]Entry, 0, 200)
		for j := 0; j < 200; j++ {
			n := i + j
			batch = append(batch, Entry{
				Time:      base.Add(time.Duration(n) * time.Millisecond),
				Level:     LevelInfo,
				Module:    "http",
				Message:   fmt.Sprintf("GET /api/issues/%d 200 %.2fms", n%97, float64(n%50)/10),
				RequestID: fmt.Sprintf("req-%d", n),
				UserID:    "u-perf",
				Extra:     fmt.Sprintf(`{"status":200,"duration_ms":%d}`, n%50),
			})
		}
		if err := store.Add(context.Background(), batch); err != nil {
			t.Fatalf("add batch: %v", err)
		}
	}
	elapsed := time.Since(start)

	if got := storeCount(t, store); got != total {
		t.Fatalf("count = %d, want %d", got, total)
	}
	size := store.FileSize()
	perEntry := size / total
	// 典型条目 < 300 字节；上限放宽到 2KB 防环境差异抖动。
	if perEntry > 2048 {
		t.Fatalf("平均单条 %d 字节超出预期上限 2048", perEntry)
	}
	// 5000 条批量写入应在 10s 内完成（本地 SQLite 远低于此值）。
	if elapsed > 10*time.Second {
		t.Fatalf("5000 条写入耗时 %v 超出预期", elapsed)
	}
	t.Logf("容量: %d 条 / %d 字节, 平均 %d 字节/条, 写入耗时 %v", total, size, perEntry, elapsed)
}

func TestStoreRejectsSystemScopeOnBusinessRows(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.Add(ctx, []Entry{
		{Time: now, Level: LevelInfo, Message: "误标系统", ProjectID: "proj-1", SystemScope: true},
		{Time: now.Add(time.Second), Level: LevelInfo, Message: "显式系统", SystemScope: true},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	all, err := store.Query(ctx, Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(all.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(all.Items))
	}
	var biz, sys *Entry
	for i := range all.Items {
		switch all.Items[i].Message {
		case "误标系统":
			biz = &all.Items[i]
		case "显式系统":
			sys = &all.Items[i]
		}
	}
	if biz == nil || biz.SystemScope || biz.ProjectID != "proj-1" {
		t.Fatalf("带关联列的行应强制非系统: %+v", biz)
	}
	if sys == nil || !sys.SystemScope {
		t.Fatalf("无关联列的显式系统行应保留: %+v", sys)
	}
	visible, err := store.Query(ctx, Filter{VisibleUserID: "u-any"}, "", 10, true)
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if len(visible.Items) != 1 || visible.Items[0].Message != "显式系统" {
		t.Fatalf("误标业务行不得对所有认证用户公开: %+v", visible.Items)
	}
}

func TestVisibilityPredicateSharedAcrossReads(t *testing.T) {
	p, store := testPipeline(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.Add(ctx, []Entry{
		{Time: now, Level: LevelInfo, Module: "http", Message: "owned", RequestID: "req-owned", UserID: "u-1"},
		{Time: now.Add(time.Second), Level: LevelWarn, Module: "t3", Message: "hidden", RequestID: "req-hidden", ThreadID: "th-hidden"},
		{Time: now.Add(2 * time.Second), Level: LevelInfo, Module: "app", Message: "boot", SystemScope: true},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p.Close()

	page, err := store.Query(ctx, Filter{VisibleUserID: "u-1"}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("list u-1 = %d, want 2 (owned+system)", len(page.Items))
	}
	page, err = store.Query(ctx, Filter{VisibleUserID: "u-2"}, "", 10, true)
	if err != nil {
		t.Fatalf("query u-2: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Message != "boot" {
		t.Fatalf("list u-2 应只见系统行: %+v", page.Items)
	}

	rows, _, err := store.Aggregate(ctx, Filter{VisibleUserID: "u-1"}, true, false, false, "")
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	byModule := map[string]int64{}
	for _, row := range rows {
		byModule[row.Module] = row.Count
	}
	if byModule["http"] != 1 || byModule["app"] != 1 || byModule["t3"] != 0 {
		t.Fatalf("aggregate 应排除无主关联行: %+v", rows)
	}

	items, _, err := store.Trace(ctx, Filter{VisibleUserID: "u-1", RequestID: "req-owned"})
	if err != nil || len(items) != 1 {
		t.Fatalf("trace owned = %d err=%v, want 1", len(items), err)
	}
	items, _, err = store.Trace(ctx, Filter{VisibleUserID: "u-1", RequestID: "req-hidden"})
	if err != nil || len(items) != 0 {
		t.Fatalf("trace hidden = %d err=%v, want 0", len(items), err)
	}
}

func TestPipelineIngestsSystemScopeAttr(t *testing.T) {
	p, store := testPipeline(t, nil)
	logger := slog.New(p.Handler())
	logger.Info("服务启动", slog.Bool(AttrSystemScope, true), slog.String("module", "app"))
	logger.Info("带项目却误标系统", slog.Bool(AttrSystemScope, true), slog.String("project_id", "proj-x"))
	p.Close()

	page, err := store.Query(context.Background(), Filter{}, "", 10, true)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	var boot, biz *Entry
	for i := range page.Items {
		switch page.Items[i].Message {
		case "服务启动":
			boot = &page.Items[i]
		case "带项目却误标系统":
			biz = &page.Items[i]
		}
	}
	if boot == nil || !boot.SystemScope {
		t.Fatalf("启动行应入库为系统行: %+v", boot)
	}
	if biz == nil || biz.SystemScope || biz.ProjectID != "proj-x" {
		t.Fatalf("带关联列的误标行应强制非系统: %+v", biz)
	}
	visible, err := store.Query(context.Background(), Filter{VisibleUserID: "u-1"}, "", 10, true)
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if len(visible.Items) != 1 || visible.Items[0].Message != "服务启动" {
		t.Fatalf("仅系统行应对任意认证用户可见: %+v", visible.Items)
	}
}

// fakeSecret 运行时拼接出密钥形状的假值；脱敏断言依赖形状，
// 但字面量直接写进源码会被密钥扫描工具误杀，故运行时拼接。
func fakeSecret(prefix, suffix string) string {
	return prefix + suffix
}
