package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 定时任务手动触发（INT-121）服务层测试：fs_schedule run 的受理与执行、
// dry_run 只读预览、运行中再次触发的忙碌拒绝、trigger_source 区分 cron/manual。
// 工具集冻结基线（v1–v5 hash、v6 新动作 schema）见 bot_int103_test.go 的
// TestBotScheduleToolsetVersions。

// decodeRunReceipt 解 run 动作回执的公共字段。
func decodeRunReceipt(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("decode run receipt: %v %s", err, out)
	}
	return parsed
}

// ---------- 1. run 正常触发 ----------

// 手动触发产生 trigger_source=manual 的运行记录，走与 cron 完全相同的
// schedule 会话执行路径（同 wakeScheduleRun/botLoop），跑完落 completed；
// runs 动作与用户 REST 列表都能区分 manual/cron。
func TestBotInt121ManualRunAcceptedAndExecutes(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "手动执行完成：已核对昨日动态"},
		fakeModelTurn{Text: "[[SILENT]]"}, // 后面 cron 那次
	)
	userID := createBotTestUser(t, env.db, "schedrun121")
	bot := env.createBot(t, userID)
	// 停用任务：显式手动触发就是一次执行，不要求 enabled。
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.Instruction = "手动指令-QAZ 汇总昨日动态"
	})
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	out, err := botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	receipt := decodeRunReceipt(t, out)
	if receipt["accepted"] != true || receipt["ok"] != true {
		t.Fatalf("回执应为已受理: %s", out)
	}
	if receipt["status"] != string(model.BotRunStatusRunning) || receipt["trigger_source"] != "manual" {
		t.Fatalf("回执应记 running/manual: %s", out)
	}
	runID, _ := receipt["run_id"].(string)
	if runID == "" || receipt["task_id"] != task.ID {
		t.Fatalf("回执应带 run_id 与 task_id: %s", out)
	}
	if !strings.Contains(out, "action=runs") {
		t.Fatalf("回执应指引用 runs 查询终态: %s", out)
	}

	// 库里的 run 行：manual 来源、schedule 会话、任务引用正确、无计划时间。
	run := mustFindRun(t, env, runID)
	if run.TriggerSource != model.BotTriggerManual {
		t.Fatalf("trigger_source 应为 manual，实得 %s", run.TriggerSource)
	}
	if run.SessionKind != model.BotSessionKindSchedule {
		t.Fatalf("session_kind 应为 schedule，实得 %s", run.SessionKind)
	}
	if run.ScheduleTaskID == nil || *run.ScheduleTaskID != task.ID {
		t.Fatalf("schedule_task_id 应为任务 ID: %+v", run.ScheduleTaskID)
	}
	if run.ScheduledFor != nil {
		t.Fatalf("手动 run 的 scheduled_for 应为 nil: %+v", run.ScheduledFor)
	}
	if run.StartedAt == nil {
		t.Fatalf("手动 run 应记 started_at: %+v", run)
	}
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err != nil || snap.Model == "" {
		t.Fatalf("手动 run 应冻结配置快照: %q %v", run.ConfigSnapshotJSON, err)
	}

	done := waitForScheduleRunFinished(t, env, runID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("手动 run 应执行到 completed: %s %v", done.Status, done.ErrorMessage)
	}
	if done.Silent || !strings.Contains(done.Summary, "手动执行完成") {
		t.Fatalf("应按模型回复记录摘要: silent=%v summary=%q", done.Silent, done.Summary)
	}
	// 走的是 schedule 会话：请求含任务指令。
	req := env.fake.nextRequest(t, 10*time.Second)
	if !strings.Contains(string(req.Body), "手动指令-QAZ") {
		t.Fatalf("手动 run 应带任务指令进 schedule 会话: %.200s", req.Body)
	}

	// runs 动作：manual；用户 REST 列表共用同一投影。
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"runs","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("runs: %v", err)
	}
	var runsOut struct {
		Items []BotScheduleRunResponse `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &runsOut); err != nil || len(runsOut.Items) != 1 {
		t.Fatalf("runs 应返回 1 条记录: %v %s", err, out)
	}
	if runsOut.Items[0].TriggerSource != "manual" {
		t.Fatalf("runs 记录应标 manual: %+v", runsOut.Items[0])
	}
	restRuns, err := env.bots.ListScheduleTaskRuns(userID, bot.ID, task.ID, 20, 0)
	if err != nil || len(restRuns) != 1 || restRuns[0].TriggerSource != "manual" {
		t.Fatalf("REST 运行记录应标 manual: %v %+v", err, restRuns)
	}

	// 对照：cron 触发的记录标 cron，且 scheduled_for 为计划时间。
	due := time.Now().UTC().Add(-3 * time.Second)
	cronTask := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "对照任务"
		tk.NeedsReport = false
		tk.NextRunAt = &due
	})
	env.bots.scheduleTick(time.Now().UTC())
	var cronRun model.BotRun
	env.db.Where("schedule_task_id = ?", cronTask.ID).First(&cronRun)
	cronDone := waitForScheduleRunFinished(t, env, cronRun.ID)
	if cronDone.Status != model.BotRunStatusCompleted {
		t.Fatalf("cron run 应完成: %s %v", cronDone.Status, cronDone.ErrorMessage)
	}
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"runs","task_id":"`+cronTask.ID+`"}`)
	if err != nil {
		t.Fatalf("runs cron: %v", err)
	}
	if err := json.Unmarshal([]byte(out), &runsOut); err != nil || len(runsOut.Items) != 1 {
		t.Fatalf("runs cron 应返回 1 条: %v %s", err, out)
	}
	if runsOut.Items[0].TriggerSource != "cron" {
		t.Fatalf("cron 记录应标 cron: %+v", runsOut.Items[0])
	}
	if runsOut.Items[0].ScheduledFor == nil {
		t.Fatalf("cron 记录应有计划时间: %+v", runsOut.Items[0])
	}
}

// ---------- 2. dry_run 只读预览 ----------

func TestBotInt121DryRunHasNoSideEffects(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "scheddry121")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Instruction = "预览指令-WSX 每日整理"
	})
	// 停用任务也能预览（gorm 建行对布尔零值走默认值，停用走开关服务）。
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	out, err := botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`","dry_run":true}`)
	if err != nil {
		t.Fatalf("dry_run: %v", err)
	}
	parsed := decodeRunReceipt(t, out)
	if parsed["dry_run"] != true || parsed["ok"] != true {
		t.Fatalf("回执应标 dry_run: %s", out)
	}
	if parsed["instruction"] != "预览指令-WSX 每日整理" {
		t.Fatalf("预览应含指令全文: %s", out)
	}
	if parsed["name"] != task.Name || parsed["cron_expression"] != task.CronExpression {
		t.Fatalf("预览应含任务名与 cron: %s", out)
	}
	if parsed["enabled"] != false {
		t.Fatalf("预览应含 enabled 现状: %s", out)
	}
	// 下次计划从 now 起算（停用任务的 next_run_at 是空的，这里仍给出）。
	if _, ok := parsed["next_run_at_utc"].(string); !ok {
		t.Fatalf("预览应含解析出的下次计划时间: %s", out)
	}
	if parsed["busy"] != false {
		t.Fatalf("空闲任务 busy 应为 false: %s", out)
	}

	// 零副作用：不建任何运行记录。
	var runCount int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", task.ID).Count(&runCount)
	if runCount != 0 {
		t.Fatalf("dry_run 不应产生运行记录，实得 %d", runCount)
	}

	// 任务忙碌时 busy=true（dry_run 仍只读）。
	seedScheduleRun(t, env, bot.ID, task.ID)
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`","dry_run":true}`)
	if err != nil {
		t.Fatalf("dry_run busy: %v", err)
	}
	if got := decodeRunReceipt(t, out)["busy"]; got != true {
		t.Fatalf("忙碌任务 dry_run 应标 busy=true: %s", out)
	}
}

// ---------- 3. 忙碌拒绝 ----------

// 同一任务已有未结束运行（cron 或手动）时，run 明确拒绝（40930），不排队、
// 不建新 run；dry_run 不受影响。
func TestBotInt121ManualRunRejectedWhileBusy(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedbusy121")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	existing := seedScheduleRun(t, env, bot.ID, task.ID)

	_, err := botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`"}`)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrBotScheduleRunBusy.Code {
		t.Fatalf("运行中手动触发应返回 40930，实得 %v", err)
	}
	// 拒绝即拒绝：没有第二条 run 行。
	var count int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", task.ID).Count(&count)
	if count != 1 {
		t.Fatalf("忙碌拒绝不应建新 run，实得 %d", count)
	}
	// 既有 run 不受影响。
	fresh := mustFindRun(t, env, existing.ID)
	if fresh.FinishedAt != nil {
		t.Fatalf("既有 run 不应被影响: %+v", fresh)
	}
	// dry_run 仍可读（busy=true）。
	out, err := botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`","dry_run":true}`)
	if err != nil {
		t.Fatalf("busy dry_run: %v", err)
	}
	if got := decodeRunReceipt(t, out)["busy"]; got != true {
		t.Fatalf("dry_run 应标 busy=true: %s", out)
	}

	// 运行结束后可再次手动触发：第二次受理并执行到终态。
	env.db.Model(&model.BotRun{}).Where("id = ?", existing.ID).
		Update("finished_at", time.Now().UTC())
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("运行结束后再触发应受理: %v", err)
	}
	secondID, _ := decodeRunReceipt(t, out)["run_id"].(string)
	if secondID == "" {
		t.Fatalf("第二次触发应返回 run_id: %s", out)
	}
	second := waitForScheduleRunFinished(t, env, secondID)
	if second.Status != model.BotRunStatusCompleted {
		t.Fatalf("第二次 run 应执行完成: %s %v", second.Status, second.ErrorMessage)
	}
}

// 任务不存在时 run/dry_run 都报「定时任务不存在」（40429）。
func TestBotInt121RunMissingTaskNotFound(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedrun404")
	bot := env.createBot(t, userID)
	for _, args := range []string{
		`{"action":"run","task_id":"missing"}`,
		`{"action":"run","task_id":"missing","dry_run":true}`,
	} {
		_, err := botScheduleCall(t, env, bot.ID, args)
		var appErr *errs.AppError
		if !errors.As(err, &appErr) || appErr.Code != errs.ErrBotScheduleTaskNotFound.Code {
			t.Fatalf("%s 应返回 40429，实得 %v", args, err)
		}
	}
}

// ---------- 4. 受理路径（v6 工具集） ----------

// run 在 v6 受理路径要求 task_id；v5 冻结会话不接受 run（基线与 schema 断言
// 见 bot_int103_test.go）。INT-136 起确认判定在受理端、版本无关：非 dry_run
// 的 run 受理即改判异步待确认，dry_run 预览保持同步。
func TestBotInt121RunPrepareRequiresTaskID(t *testing.T) {
	v6 := botToolsetVersionOf(6)
	if _, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"run"}`); err == nil {
		t.Fatalf("run 缺 task_id 应被拒绝")
	}
	prepared, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"run","task_id":"t1"}`)
	if err != nil || prepared.Action != "run" || prepared.Mode != model.BotExecutionAsync {
		t.Fatalf("run 应异步待确认受理: %v %+v", err, prepared)
	}
	if !prepared.RequiresConfirmation {
		t.Fatalf("run 应要求确认（INT-136）")
	}
	prepared, err = botPrepareToolCall(v6, "fs_schedule", `{"action":"run","task_id":"t1","dry_run":true}`)
	if err != nil || prepared.Mode != model.BotExecutionSync || prepared.RequiresConfirmation {
		t.Fatalf("dry_run 预览应保持同步: %v %+v", err, prepared)
	}
}

// ---------- 5. dry_run 仅限 run 动作（INT-121 审查修正） ----------

// 非 run 动作携带 dry_run（true 或 false）在受理层就是参数错误：预览标记被
// 其它动作的分支忽略时，模型会误以为带了标记就没副作用。
func TestBotInt121DryRunRejectedOnNonRunActions(t *testing.T) {
	v6 := botToolsetVersionOf(6)
	cases := []struct {
		name string
		tmpl string
	}{
		{"list", `{"action":"list","dry_run":%s}`},
		{"create", `{"action":"create","name":"x","cron":"0 9 * * *","instruction":"y","dry_run":%s}`},
		{"update", `{"action":"update","task_id":"t1","dry_run":%s}`},
		{"delete", `{"action":"delete","task_id":"t1","dry_run":%s}`},
		{"runs", `{"action":"runs","task_id":"t1","dry_run":%s}`},
		{"enable", `{"action":"enable","task_id":"t1","dry_run":%s}`},
		{"disable", `{"action":"disable","task_id":"t1","dry_run":%s}`},
	}
	for _, tc := range cases {
		// null 也算「携带」：归一化会把它变回未提供，受理层必须按原始参数拒。
		for _, flag := range []string{"true", "false", "null"} {
			args := fmt.Sprintf(tc.tmpl, flag)
			_, err := botPrepareToolCall(v6, "fs_schedule", args)
			var appErr *errs.AppError
			if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
				t.Fatalf("%s 携带 dry_run=%s 应参数错误，实得 %v", tc.name, flag, err)
			}
		}
	}
	// status 是 v1/v2 占位动作：规则表虽列了 Forbidden，但 v6 受理层先回改名
	// 提示（同样拒绝）；v5 及更早的冻结参数结构体没有 dry_run 字段、按未知参数
	// 拒绝——都到不了字段校验，这里只断言 v6 侧不放大。
	if _, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"status","dry_run":true}`); err == nil {
		t.Fatalf("status 携带 dry_run 应被拒绝")
	}
}

// 执行器直接调用（绕过受理层）同样拒绝：delete/update/enable/disable 带
// dry_run 后任务原样保留，create 不建任务。
func TestBotInt121DryRunDirectExecHasNoWrites(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "scheddryguard")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "守卫任务"
	})

	// null 也拒：执行器按原始参数判断字段是否存在。
	for _, flag := range []string{"true", "false", "null"} {
		for _, args := range []string{
			`{"action":"delete","task_id":"` + task.ID + `","dry_run":` + flag + `}`,
			`{"action":"update","task_id":"` + task.ID + `","name":"不该改名","dry_run":` + flag + `}`,
			`{"action":"enable","task_id":"` + task.ID + `","dry_run":` + flag + `}`,
			`{"action":"disable","task_id":"` + task.ID + `","dry_run":` + flag + `}`,
			`{"action":"create","name":"不该被建出来","cron":"0 9 * * *","instruction":"x","dry_run":` + flag + `}`,
			`{"action":"runs","task_id":"` + task.ID + `","dry_run":` + flag + `}`,
			`{"action":"list","dry_run":` + flag + `}`,
		} {
			_, err := botScheduleCall(t, env, bot.ID, args)
			var appErr *errs.AppError
			if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
				t.Fatalf("执行器直接调用 %s 应参数错误，实得 %v", args, err)
			}
		}
	}

	// 任务原样保留：还在、名字没改、仍是启用。
	fresh, err := env.bots.schedTasks.FindByIDAndBot(task.ID, bot.ID)
	if err != nil {
		t.Fatalf("带 dry_run 的 delete/update 不应删掉或改掉任务: %v", err)
	}
	if fresh.Name != "守卫任务" || !fresh.Enabled {
		t.Fatalf("任务应原样保留: name=%q enabled=%v", fresh.Name, fresh.Enabled)
	}
	tasks, err := env.bots.schedTasks.ListByBot(bot.ID, botScheduleTasksListLimit)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("写动作不应生效，任务数应仍为 1: %v %+v", err, tasks)
	}
}

// ---------- 6. dry_run 预览的有效模型 ----------

// 预览始终返回有效模型：任务覆盖优先，否则继承机器人当前模型——真实运行
// 会按同一优先级在建 run 时冻结模型，调用方要能核对会用哪个。两种情况都
// 不建运行记录。
func TestBotInt121DryRunEffectiveModel(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "scheddrymodel")
	bot := env.createBot(t, userID) // Model: fake-model

	inherited := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "继承模型"
		tk.Model = ""
	})
	out, err := botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+inherited.ID+`","dry_run":true}`)
	if err != nil {
		t.Fatalf("dry_run inherited: %v", err)
	}
	if got := decodeRunReceipt(t, out)["model"]; got != "fake-model" {
		t.Fatalf("无覆盖时应继承机器人模型 fake-model，实得 %v: %s", got, out)
	}

	overridden := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "覆盖模型"
		tk.Model = "task-override-model"
	})
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+overridden.ID+`","dry_run":true}`)
	if err != nil {
		t.Fatalf("dry_run override: %v", err)
	}
	if got := decodeRunReceipt(t, out)["model"]; got != "task-override-model" {
		t.Fatalf("有覆盖时应返回任务模型，实得 %v: %s", got, out)
	}

	// 零副作用：两种预览都不建任何运行记录。
	var runCount int64
	env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&runCount)
	if runCount != 0 {
		t.Fatalf("dry_run 不应产生运行记录，实得 %d", runCount)
	}
}

// ---------- 7. 并发手动触发 ----------

// 两个手动触发竞争：忙碌检查与建 run 在同一事务里（MaxOpenConns=1 下事务
// 串行化），恰好一条受理、另一条 40930，不排队、不重复建 run。
func TestBotInt121ConcurrentManualRunSingleWinner(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedrace121")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	const racers = 2
	type result struct {
		out string
		err error
	}
	results := make(chan result, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := botScheduleCall(t, env, bot.ID, `{"action":"run","task_id":"`+task.ID+`"}`)
			results <- result{out: out, err: err}
		}()
	}
	wg.Wait()
	close(results)

	accepted, busy := 0, 0
	winnerID := ""
	for r := range results {
		if r.err == nil {
			accepted++
			winnerID, _ = decodeRunReceipt(t, r.out)["run_id"].(string)
			continue
		}
		var appErr *errs.AppError
		if errors.As(r.err, &appErr) && appErr.Code == errs.ErrBotScheduleRunBusy.Code {
			busy++
			continue
		}
		t.Fatalf("并发触发应只出现受理或 40930，实得 %v", r.err)
	}
	if accepted != 1 || busy != 1 {
		t.Fatalf("应恰好 1 受理 1 拒绝，实得 accepted=%d busy=%d", accepted, busy)
	}
	if winnerID == "" {
		t.Fatalf("受理方应返回 run_id")
	}
	var runCount int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", task.ID).Count(&runCount)
	if runCount != 1 {
		t.Fatalf("竞争后应只有 1 条运行记录，实得 %d", runCount)
	}
	done := waitForScheduleRunFinished(t, env, winnerID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("胜出的 run 应正常执行到终态: %s %v", done.Status, done.ErrorMessage)
	}
}
