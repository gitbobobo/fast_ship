package service

// INT-138 测试（机器人时区与定时任务体验，后端第 1 块）：运行时上下文带
// user_timezone / current_time_local 且不影响前缀哈希；fs_schedule 创建任务
// 省略时区时默认用户时区；工具集 v8 的描述与帮助文本写明任务指令写法。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// setUserTimezone 直接改用户时区列（模拟设置页/首登写入，绕过 handler）。
func setUserTimezone(t *testing.T, env *botTestEnv, userID, tz string) {
	t.Helper()
	if err := env.db.Model(&model.User{}).Where("id = ?", userID).Update("timezone", tz).Error; err != nil {
		t.Fatalf("set user timezone: %v", err)
	}
}

// decodeMainlineRuntimeContext 校验主线构建产物末条消息形态并解出运行时上下文。
func decodeMainlineRuntimeContext(t *testing.T, built *botBuiltContext) botRuntimeContext {
	t.Helper()
	if len(built.Request.Messages) == 0 {
		t.Fatalf("构建产物没有消息")
	}
	last := built.Request.Messages[len(built.Request.Messages)-1]
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatalf("marshal last message: %v", err)
	}
	var wire struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal last message: %v", err)
	}
	const prefix = "[运行时上下文]\n"
	if !strings.HasPrefix(wire.Content, prefix) {
		t.Fatalf("末条消息应为运行时上下文: %q", wire.Content)
	}
	var rt botRuntimeContext
	if err := json.Unmarshal([]byte(strings.TrimPrefix(wire.Content, prefix)), &rt); err != nil {
		t.Fatalf("unmarshal rt: %v", err)
	}
	return rt
}

func TestBotInt138MainlineRuntimeContextTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tzmain")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	run := botFixtureRun(t, env, botRow)

	// 用户未声明时区、部署无回退 → 有效时区为 UTC。
	built0, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build0: %v", err)
	}
	rt0 := decodeMainlineRuntimeContext(t, built0)
	if rt0.UserTimezone != "UTC" {
		t.Fatalf("未声明时区应回退 UTC，实得 %q", rt0.UserTimezone)
	}
	if _, err := time.Parse(time.RFC3339, rt0.CurrentTimeLocal); err != nil {
		t.Fatalf("current_time_local 应为 RFC3339: %v", err)
	}

	// 设置用户时区后重建：末条运行时上下文带用户时区与当地时钟（+08:00），
	// 但前缀哈希不变——新字段只出现在最后一条消息里。
	setUserTimezone(t, env, userID, "Asia/Shanghai")
	built1, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build1: %v", err)
	}
	rt1 := decodeMainlineRuntimeContext(t, built1)
	if rt1.UserTimezone != "Asia/Shanghai" {
		t.Fatalf("user_timezone 应为 Asia/Shanghai，实得 %q", rt1.UserTimezone)
	}
	local, err := time.Parse(time.RFC3339, rt1.CurrentTimeLocal)
	if err != nil {
		t.Fatalf("current_time_local 应为 RFC3339: %v", err)
	}
	if _, offset := local.Zone(); offset != 8*3600 {
		t.Fatalf("current_time_local 应带 +08:00 偏移，实得 %q", rt1.CurrentTimeLocal)
	}
	if built1.PrefixHash != built0.PrefixHash {
		t.Fatalf("设置用户时区后 prefix_hash 不得变化")
	}
	// 新字段不得渗进前缀：除末条外的消息不含 user_timezone。
	for i, msg := range built1.Request.Messages[:len(built1.Request.Messages)-1] {
		raw, _ := json.Marshal(msg)
		if strings.Contains(string(raw), "user_timezone") || strings.Contains(string(raw), "current_time_local") {
			t.Fatalf("第 %d 条前缀消息不得含时区字段: %s", i, raw)
		}
	}
	// 消息条数不变（不是多插了一条消息）。
	if len(built1.Request.Messages) != len(built0.Request.Messages) {
		t.Fatalf("消息条数不应变化: %d vs %d", len(built1.Request.Messages), len(built0.Request.Messages))
	}
}

func TestBotInt138ScheduleRuntimeContextTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tzsched")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Shanghai")
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	sess, err := env.bots.newScheduleSession(botRow, run)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	built, err := env.bots.buildScheduleRunContext(botRow, run, sess)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	last := built.Request.Messages[len(built.Request.Messages)-1]
	raw, _ := json.Marshal(last)
	var wire struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal last message: %v", err)
	}
	const prefix = "[运行时上下文]\n"
	if !strings.HasPrefix(wire.Content, prefix) {
		t.Fatalf("末条消息应为运行时上下文: %q", wire.Content)
	}
	var rt botScheduleRuntimeContext
	if err := json.Unmarshal([]byte(strings.TrimPrefix(wire.Content, prefix)), &rt); err != nil {
		t.Fatalf("unmarshal rt: %v", err)
	}
	if rt.UserTimezone != "Asia/Shanghai" {
		t.Fatalf("schedule 会话 user_timezone 应为 Asia/Shanghai，实得 %q", rt.UserTimezone)
	}
	local, err := time.Parse(time.RFC3339, rt.CurrentTimeLocal)
	if err != nil {
		t.Fatalf("current_time_local 应为 RFC3339: %v", err)
	}
	if _, offset := local.Zone(); offset != 8*3600 {
		t.Fatalf("current_time_local 应带 +08:00 偏移，实得 %q", rt.CurrentTimeLocal)
	}
}

func TestBotInt138ScheduleCreateDefaultsUserTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tzcreate")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Shanghai")

	// create 不带 timezone：默认用户时区，上海 09:00 = UTC 01:00。
	out, err := botScheduleCall(t, env, bot.ID,
		`{"action":"create","name":"晨报","cron":"0 9 * * *","instruction":"检查新 issue 并汇报"}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		ID           string `json:"id"`
		Timezone     string `json:"timezone"`
		NextRunAt    string `json:"next_run_at"`
		NextRunAtUTC string `json:"next_run_at_utc"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode create result: %v %s", err, out)
	}
	if created.Timezone != "Asia/Shanghai" {
		t.Fatalf("create 省略时区应默认用户时区，实得 %q: %s", created.Timezone, out)
	}
	if !strings.Contains(created.NextRunAt, "09:00:00") {
		t.Fatalf("next_run_at 应为用户本地 09:00: %s", out)
	}
	if !strings.Contains(created.NextRunAtUTC, "T01:00:00Z") {
		t.Fatalf("next_run_at_utc 应为 UTC 01:00: %s", out)
	}
	// 结果里的本地时间带时区缩写（MST 样式），模型可直接转述。
	if !strings.Contains(created.NextRunAt, "CST") {
		t.Fatalf("next_run_at 应带本地时区缩写: %s", out)
	}

	// 落库的任务行同样存用户时区。
	task, err := env.bots.schedTasks.FindByID(created.ID)
	if err != nil || task == nil {
		t.Fatalf("load task: %v", err)
	}
	if task.Timezone != "Asia/Shanghai" {
		t.Fatalf("任务行应存 Asia/Shanghai，实得 %q", task.Timezone)
	}

	// 对照：显式时区仍然原样采用（不被用户时区覆盖）。
	out, err = botScheduleCall(t, env, bot.ID,
		`{"action":"create","name":"纽约提醒","cron":"0 9 * * *","timezone":"America/New_York","instruction":"提醒"}`)
	if err != nil {
		t.Fatalf("create ny: %v", err)
	}
	if !strings.Contains(out, `"timezone":"America/New_York"`) {
		t.Fatalf("显式时区应原样采用: %s", out)
	}
	var nyTask struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &nyTask); err != nil || nyTask.ID == "" {
		t.Fatalf("decode ny task: %v %s", err, out)
	}

	// update 不传时区 = 保持原值（语义不变）。
	out, err = botScheduleCall(t, env, bot.ID,
		`{"action":"update","task_id":"`+nyTask.ID+`","cron":"30 10 * * *"}`)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, `"timezone":"America/New_York"`) {
		t.Fatalf("update 不传时区应保持原值: %s", out)
	}
}

func TestBotInt138HelpAndToolDescriptionGuidance(t *testing.T) {
	// 帮助文本：时区默认用户时区 + 指令写法示例。
	for _, kw := range []string{
		"省略即用户时区",
		"每天 9 点检查新 issue，有新的才告诉我",
		"去重",
		"[[SILENT]]",
		"needs_report",
	} {
		if !strings.Contains(botHelpText, kw) {
			t.Fatalf("帮助文本应包含 %q", kw)
		}
	}
	if strings.Contains(botHelpText, "（默认 UTC）") {
		t.Fatalf("帮助文本不应再声称默认 UTC")
	}

	// v8 工具描述：时区默认 + 指令写法四要素。
	v8 := botToolsetVersionOf(botToolsetVersion)
	sched := v8.byName["fs_schedule"]
	if sched == nil {
		t.Fatalf("v8 应含 fs_schedule")
	}
	for _, kw := range []string{
		"省略即用户时区", "不是 UTC",
		"意图", "目标", "汇报", "去重", "state_get/state_set", "[[SILENT]]", "needs_report",
	} {
		if !strings.Contains(sched.Description, kw) {
			t.Fatalf("v8 fs_schedule 描述应包含 %q: %s", kw, sched.Description)
		}
	}
	createAction, ok := v8.byAction["fs_schedule/create"]
	if !ok {
		t.Fatalf("v8 fs_schedule 应有 create 动作")
	}
	if !strings.Contains(createAction.Description, "省略即用户时区") {
		t.Fatalf("v8 create 动作描述应说明时区默认: %s", createAction.Description)
	}
}

func TestBotInt138ToolsetV8Baseline(t *testing.T) {
	v7 := botToolsetVersionOf(7)
	v8 := botToolsetVersionOf(8)
	if len(v8.schemas) != 11 {
		t.Fatalf("v8 工具数应为 11，实得 %d", len(v8.schemas))
	}
	if v8.hash == v7.hash {
		t.Fatalf("v8 与 v7 的 tools_hash 不应相同")
	}
	if v8.hash == botToolsetV7FrozenHash {
		t.Fatalf("v8 不得复用 v7 冻结 hash")
	}
	// 当前发布版本路由到 v8。
	if botToolsetVersionOf(botToolsetVersion).hash != v8.hash {
		t.Fatalf("botToolsetVersion 应指向 v8")
	}

	// v8 schema 的 timezone 描述为新口径；v7 冻结 schema 保持旧口径。
	getTZDesc := func(ts *botToolsetHolder) string {
		t.Helper()
		raw, _ := json.Marshal(ts.byName["fs_schedule"].Parameters)
		var schema struct {
			Properties struct {
				Timezone struct {
					Description string `json:"description"`
				} `json:"timezone"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("decode schema: %v", err)
		}
		return schema.Properties.Timezone.Description
	}
	if tz := getTZDesc(v8); !strings.Contains(tz, "用户时区") || strings.Contains(tz, "默认 UTC") {
		t.Fatalf("v8 timezone 描述应为用户时区口径: %s", tz)
	}
	if tz := getTZDesc(v7); !strings.Contains(tz, "默认 UTC") {
		t.Fatalf("v7 冻结 timezone 描述应保持默认 UTC: %s", tz)
	}

	// 升级通知级联：7→8 只讲 v8 内容；6→8 级联补 v7 的小状态说明。
	note78 := botToolsetChangeNote(7, 8)
	if !strings.Contains(note78, "用户时区") || !strings.Contains(note78, "[[SILENT]]") {
		t.Fatalf("7→8 升级通知应说明 v8 变更: %s", note78)
	}
	if strings.Contains(note78, "任务跨运行保留的小状态") {
		t.Fatalf("7→8 升级通知不应级联 v7 内容: %s", note78)
	}
	note68 := botToolsetChangeNote(6, 8)
	if !strings.Contains(note68, "任务跨运行保留的小状态") {
		t.Fatalf("6→8 升级通知应级联 v7 的小状态说明: %s", note68)
	}
}
