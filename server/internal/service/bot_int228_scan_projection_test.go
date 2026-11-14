package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// INT-228 测试（维护扫描投影与启动时序）：夜间扫描、预算评估与用量日汇总
// 只为元数据（id / 归属用户时区 / 名字）走投影查询，不为它们 Get 整行——
// 整行读取会把已拼好的系统提示词与 provider 密文（APIKeyEncrypted）反复
// 拷进内存；Start 不再同步等首次夜间全扫描，监听不被拉长。

// int228CaptureQueries 注册 SQL 捕获回调，记录执行过的全部查询语句；
// 返回读取已捕获语句的函数（切片可安全并发读，只在测试主 goroutine 读）。
// 投影查询走 gorm Scan（row/raw 回调组），不走 query 组，三组都要挂。
func int228CaptureQueries(t *testing.T, db *gorm.DB, after func(tx *gorm.DB)) func() []string {
	t.Helper()
	var mu sync.Mutex
	var sqls []string
	capture := func(tx *gorm.DB) {
		mu.Lock()
		sqls = append(sqls, tx.Statement.SQL.String())
		mu.Unlock()
		if after != nil {
			after(tx)
		}
	}
	for _, reg := range []struct {
		name string
		reg  func(string, func(*gorm.DB)) error
	}{
		{"int228:capture-query", db.Callback().Query().After("gorm:query").Register},
		{"int228:capture-row", db.Callback().Row().After("*").Register},
		{"int228:capture-raw", db.Callback().Raw().After("*").Register},
	} {
		if err := reg.reg(reg.name, capture); err != nil {
			t.Fatalf("register %s: %v", reg.name, err)
		}
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sqls...)
	}
}

// int228AssertNoSensitiveReads 断言捕获的 SQL 里没有任何整行 bot 读取、
// 提示词列或 provider 密钥面：投影路径绝不把 system_prompt_text、
// persona_prompt、api_key 或 ai_providers 表读进内存。
func int228AssertNoSensitiveReads(t *testing.T, sqls []string) {
	t.Helper()
	for _, sql := range sqls {
		lower := strings.ToLower(sql)
		for _, banned := range []string{"system_prompt_text", "persona_prompt", "api_key", "ai_providers"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("维护扫描不得读取敏感列/表 %q，实得 SQL: %s", banned, sql)
			}
		}
		if strings.Contains(lower, "from `bots`") && strings.Contains(lower, "select *") {
			t.Fatalf("维护扫描不得 SELECT * 整行读 bots，实得 SQL: %s", sql)
		}
	}
}

// TestInt228NightlyScanReadsProjectionOnly 夜间扫描候选：整轮扫描对 bots 只
// 有一次「id + 归属用户时区」的 LEFT JOIN 投影，不为时区 Get 整行。
func TestInt228NightlyScanReadsProjectionOnly(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int228nightly")
	setUserTimezone(t, env, userID, "Asia/Shanghai")
	createBotRowAt(t, env, userID, "http://127.0.0.1:9", "fake-model")

	// 未到当地触发钟点（UTC 18:59 = 上海次日 02:59）：扫描只读候选投影，
	// 不入队，维护 worker 也不会因触发而读库。
	env.bots.setNowFn(func() time.Time {
		return time.Date(2026, 10, 16, 18, 59, 0, 0, time.UTC)
	})
	sqls := int228CaptureQueries(t, env.db, nil)
	env.bots.scanNightlyOnce()

	int228AssertNoSensitiveReads(t, sqls())
	botQueries := 0
	joinQueries := 0
	for _, sql := range sqls() {
		lower := strings.ToLower(sql)
		if strings.Contains(lower, "from `bots`") {
			botQueries++
		}
		if strings.Contains(lower, "coalesce(users.timezone") {
			joinQueries++
		}
	}
	if botQueries != 1 {
		t.Fatalf("整轮扫描对 bots 表应只有 1 次投影查询，实得 %d: %v", botQueries, sqls())
	}
	if joinQueries != 1 {
		t.Fatalf("扫描候选应为一条 LEFT JOIN users 的时区投影，实得 %d: %v", joinQueries, sqls())
	}
	// 严格限制候选查询的投影列：只有 id 与时区，夹带任何其他列都算越界。
	int228AssertProjectionColumns(t, sqls(), "`bots`", "bots.id as bot_id,coalesce(users.timezone, '') as timezone")
}

// int228AssertProjectionColumns 断言对某表的查询只投影允许列（剥掉引号、
// 反引号、括号与 as 别名后逐列比对），并禁止 SELECT *。逗号按括号深度
// 切分，避免 COALESCE(a, b) 内的逗号被误拆。
func int228AssertProjectionColumns(t *testing.T, sqls []string, table, allowed string) {
	t.Helper()
	allowedSet := map[string]bool{}
	for _, col := range splitTopLevel(allowed) {
		col = strings.TrimSpace(col)
		allowedSet[col] = true
		// 别名形式同义：allowed 里写 "id AS bot_id" 时 "id" 也算允许。
		if asIdx := strings.LastIndex(strings.ToLower(col), " as "); asIdx >= 0 {
			allowedSet[strings.TrimSpace(col[:asIdx])] = true
		}
	}
	for _, sql := range sqls {
		lower := strings.ToLower(sql)
		fromIdx := strings.Index(lower, "from "+table)
		if fromIdx < 0 || !strings.HasPrefix(lower, "select ") {
			continue
		}
		selectList := lower[len("select "):fromIdx]
		for _, expr := range splitTopLevel(selectList) {
			expr = strings.TrimSpace(expr)
			// 函数表达式保留括号整体比对；普通列剥掉 as 别名后比对。
			if strings.HasSuffix(expr, ")") {
				if !allowedSet[expr] {
					t.Fatalf("对 %s 只允许投影 %s，投影表达式 %q 越界，实得 SQL: %s", table, allowed, expr, sql)
				}
				continue
			}
			if asIdx := strings.LastIndex(expr, " as "); asIdx >= 0 {
				expr = strings.TrimSpace(expr[:asIdx])
			}
			expr = strings.Trim(expr, "`\" '")
			if expr == "" || expr == "*" || !allowedSet[expr] {
				t.Fatalf("对 %s 只允许投影 %s，投影列 %q 越界，实得 SQL: %s", table, allowed, expr, sql)
			}
		}
	}
}

// splitTopLevel 按顶层逗号切分表达式（括号内的逗号不切）。
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// TestInt228BudgetEvalReadsProjectionOnly 预算评估取 bot 名字走 IN 投影，
// 不为名字 Get 整行。
func TestInt228BudgetEvalReadsProjectionOnly(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "int228budget")
	bot := createBotRowAt(t, env, userID, "http://127.0.0.1:9", "fake-model")
	if _, err := env.bots.UpsertBotBudget(userID, bot.ID, "daily", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{1},
	}); err != nil {
		t.Fatalf("upsert budget: %v", err)
	}

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	env.bots.setNowFn(func() time.Time { return now })
	sqls := int228CaptureQueries(t, env.db, nil)
	env.bots.evalBudgetsOnce(true)

	int228AssertNoSensitiveReads(t, sqls())
	int228AssertProjectionColumns(t, sqls(), "`bots`", "id,user_id,name")
}

// TestInt228RollupScanReadsProjectionOnly 用量日汇总扫描取 id/user_id/name
// 走批量投影，删除前补齐同样走投影，均不 Get 整行。
func TestInt228RollupScanReadsProjectionOnly(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int228rollup")
	setUserTimezone(t, env, userID, "UTC")
	created := createBotRowAt(t, env, userID, "http://127.0.0.1:9", "fake-model")

	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.setNowFn(func() time.Time { return fixed })
	run := seedUsageRun(t, env, created.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
		r.CostEstimate = ptrCost(0.5)
	})
	seedUsageRequest(t, env, run.ID, ptrCost(0.5), "int228-1")

	sqls := int228CaptureQueries(t, env.db, nil)
	env.bots.scanUsageRollupOnce()
	// 删除前补齐路径也走投影。
	if err := env.bots.rollupBotUsageRange(created.ID); err != nil {
		t.Fatalf("rollup range: %v", err)
	}

	int228AssertNoSensitiveReads(t, sqls())
	// 汇总路径对 bots 只允许 id/user_id/name 投影与 count(*) 存在复检，
	// 不允许任何整行或敏感列读取。
	int228AssertProjectionColumns(t, sqls(), "`bots`", "id,user_id,name,count(*)")

	// 汇总结果仍在：投影改造不改变功能结果。
	var daily model.BotUsageDaily
	if err := env.db.Where("bot_id = ? AND date = ? AND kind = ?",
		created.ID, usageDayAt(fixed, 1).Format("2006-01-02"), model.BotUsageDayMain).
		First(&daily).Error; err != nil {
		t.Fatalf("日汇总行应存在: %v", err)
	}
}

// TestInt228StartDoesNotWaitForNightlyScan Start 在夜间全扫描被阻塞时即已
// 返回：监听前的启动窗口不再被同步扫描拉长（进程可以在夜间扫描完成前开始
// 监听）。
func TestInt228StartDoesNotWaitForNightlyScan(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int228start")
	createBotRowAt(t, env, userID, "http://127.0.0.1:9", "fake-model")

	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// 失败分支也要放行阻塞回调，否则清理里的 Stop 等不到扫描收尾。
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	int228CaptureQueries(t, env.db, func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String())
		if !strings.Contains(sql, "coalesce(users.timezone") {
			return
		}
		once.Do(func() { close(blocked) })
		<-release
	})

	started := make(chan struct{})
	go func() {
		env.bots.Start()
		close(started)
	}()

	// 夜间扫描已在后台跑并被回调卡住。
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("夜间扫描候选查询未发生")
	}
	// Start 本身已经返回：它不等扫描完成。
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Start 被夜间扫描阻塞，未在扫描完成前返回")
	}

	close(release)
	<-started
	env.bots.Stop()
}

// TestInt228NightlyScanFallbackForUnsetTimezone 用户未声明时区时（投影拿
// 到空串），扫描按未声明回退部署配置，与旧的 FindByID 空时区口径一致。
func TestInt228NightlyScanFallbackForUnsetTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int228fallback")
	created := createBotRowAt(t, env, userID, "http://127.0.0.1:9", "fake-model")

	// UTC 19:00 已过 UTC 03:00：回退时区下应照常触发，日期取 UTC 当天。
	env.bots.setNowFn(func() time.Time {
		return time.Date(2026, 10, 16, 19, 0, 0, 0, time.UTC)
	})
	env.bots.scanNightlyOnce()

	var run model.BotMaintenanceRun
	waitForBot(t, "回退时区夜间运行建档", 10*time.Second, func() bool {
		return env.db.Where("bot_id = ? AND kind = ? AND trigger_key = ?",
			created.ID, model.BotMaintenanceNightly, "nightly:2026-10-16").
			First(&run).Error == nil
	})
}

// TestInt228ListBotOwnerTimezonesProjection 投影查询本身：带时区用户返回
// 声明值，未声明时区返回空串（回退由调用方做），顺序按 bot 创建序。
func TestInt228ListBotOwnerTimezonesProjection(t *testing.T) {
	env := setupBotTestEnv(t)
	shanghai := createBotTestUser(t, env.db, "int228proj-sh")
	plain := createBotTestUser(t, env.db, "int228proj-plain")
	setUserTimezone(t, env, shanghai, "Asia/Shanghai")
	b1 := createBotRowAt(t, env, shanghai, "http://127.0.0.1:9", "fake-model")
	b2 := createBotRowAt(t, env, plain, "http://127.0.0.1:9", "fake-model")

	rows, err := repository.NewBotMaintenanceRepository(env.db).ListBotOwnerTimezones()
	if err != nil {
		t.Fatalf("ListBotOwnerTimezones: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应返回全部 bot 候选，实得 %d: %+v", len(rows), rows)
	}
	if rows[0].BotID != b1.ID || rows[0].Timezone != "Asia/Shanghai" {
		t.Fatalf("首个候选应为带时区 bot: %+v", rows[0])
	}
	if rows[1].BotID != b2.ID || rows[1].Timezone != "" {
		t.Fatalf("未声明时区 bot 候选应为空串（由调用方回退）: %+v", rows[1])
	}
}

// TestInt228BotMetasByIDsChunksBatches IN 查询跨批次：批量投影按
// UsageQueryBatch 分块，bot 数超单批时不丟行（SQLite 变量数上限不退化）。
func TestInt228BotMetasByIDsChunksBatches(t *testing.T) {
	env := setupBotTestEnv(t)
	origBatch := repository.UsageQueryBatch
	repository.UsageQueryBatch = 1
	t.Cleanup(func() { repository.UsageQueryBatch = origBatch })

	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		userID := createBotTestUser(t, env.db, fmt.Sprintf("int228chunk-%d", i))
		bot := createBotRowAt(t, env, userID, "http://127.0.0.1:9", "fake-model")
		want[bot.ID] = true
	}

	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	rows, err := repository.NewBotRepository(env.db).BotMetasByIDs(ids)
	if err != nil {
		t.Fatalf("BotMetasByIDs: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("跨批次应取回全部投影行，实得 %d/%d: %+v", len(rows), len(want), rows)
	}
	for _, row := range rows {
		if !want[row.ID] {
			t.Fatalf("未知投影行: %+v", row)
		}
		if row.Name == "" || row.UserID == "" {
			t.Fatalf("投影行应带 name/user_id: %+v", row)
		}
	}
}
