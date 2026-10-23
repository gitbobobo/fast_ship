package service

// INT-138 测试（机器人时区与定时任务体验，后端第 1 块）：运行时上下文带
// user_timezone / current_time_local 且不影响前缀哈希；fs_schedule 创建任务
// 省略时区时默认用户时区；工具集 v8 的描述与帮助文本写明任务指令写法。

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// setUserTimezone 直接改用户时区列（模拟设置页/首登写入，绕过 handler）。
func setUserTimezone(t *testing.T, env *botTestEnv, userID, tz string) {
	t.Helper()
	if err := env.db.Model(&model.User{}).Where("id = ?", userID).Update("timezone", tz).Error; err != nil {
		t.Fatalf("set user timezone: %v", err)
	}
}

// botRuntimeContextPayload 去掉运行时上下文消息的平台前缀并返回正文。
// INT-136 起前缀带围栏 nonce（"[运行时上下文 nonce=…]"），旧渲染为
// "[运行时上下文]"；两种都要能解出正文。
func botRuntimeContextPayload(content string) (string, bool) {
	if !strings.HasPrefix(content, "[运行时上下文") {
		return "", false
	}
	end := strings.Index(content, "]\n")
	if end < 0 {
		return "", false
	}
	return content[end+2:], true
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
	payload, ok := botRuntimeContextPayload(wire.Content)
	if !ok {
		t.Fatalf("末条消息应为运行时上下文: %q", wire.Content)
	}
	var rt botRuntimeContext
	if err := json.Unmarshal([]byte(payload), &rt); err != nil {
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
	payload, ok := botRuntimeContextPayload(wire.Content)
	if !ok {
		t.Fatalf("末条消息应为运行时上下文: %q", wire.Content)
	}
	var rt botScheduleRuntimeContext
	if err := json.Unmarshal([]byte(payload), &rt); err != nil {
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
	// 任务时区与用户时区不同时，除了任务本地时间还要给用户当地时间：
	// 纽约 09:00 与上海 21:00 指向同一时刻，模型汇报时不必自己换算。
	var nyProjection struct {
		NextRunAt     string `json:"next_run_at"`
		NextRunAtUTC  string `json:"next_run_at_utc"`
		NextRunAtUser string `json:"next_run_at_user"`
		UserTimezone  string `json:"user_timezone"`
	}
	if err := json.Unmarshal([]byte(out), &nyProjection); err != nil {
		t.Fatalf("decode ny projection: %v %s", err, out)
	}
	if nyProjection.UserTimezone != "Asia/Shanghai" {
		t.Fatalf("任务投影应带用户时区，实得 %q: %s", nyProjection.UserTimezone, out)
	}
	// cron 是 0 9 * * *，任务时区的钟点恒为 09:00（与夏令时无关）；用户时区
	// 那一份按绝对时刻推出，避免把 EDT/EST 或上海 21:00/22:00 写死。
	if !strings.Contains(nyProjection.NextRunAt, "09:00:00") {
		t.Fatalf("next_run_at 应为任务时区（纽约）本地 09:00: %s", out)
	}
	utc, err := time.Parse(time.RFC3339, nyProjection.NextRunAtUTC)
	if err != nil {
		t.Fatalf("next_run_at_utc 应为 RFC3339: %v %s", err, out)
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load shanghai: %v", err)
	}
	if want := utc.In(shanghai).Format("2006-01-02 15:04:05"); !strings.HasPrefix(nyProjection.NextRunAtUser, want) {
		t.Fatalf("next_run_at_user 应为上海本地 %s，实得 %q", want, nyProjection.NextRunAtUser)
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

// 创建任务时读不到用户行（数据库故障，而不是「用户没声明时区」）必须失败：
// 把瞬时读库失败当成未声明，会把部署回退时区当成用户的选择永久写进任务，
// 之后用户改时区也不会带动这条任务（INT-138 复审）。
func TestInt138ScheduleCreateFailsWhenUserReadFails(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tzdberr")
	bot := env.createBot(t, userID)

	// 换成一个连接已关闭的仓储：bot 行仍读得到，用户行读会真实报错。
	broken, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "closed.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open broken db: %v", err)
	}
	sqlDB, err := broken.DB()
	if err != nil {
		t.Fatalf("broken sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close broken db: %v", err)
	}
	original := env.bots.users
	env.bots.users = repository.NewUserRepository(broken)
	t.Cleanup(func() { env.bots.users = original })

	_, err = botScheduleCall(t, env, bot.ID,
		`{"action":"create","name":"晨报","cron":"0 9 * * *","instruction":"检查新 issue"}`)
	if err == nil {
		t.Fatalf("读不到用户行时应拒绝创建任务，而不是回退成部署时区落库")
	}

	// 不能留下任务行（空时区等于 UTC，会被静默固化）。
	var count int64
	if err := env.db.Model(&model.BotScheduleTask{}).Where("bot_id = ?", bot.ID).Count(&count).Error; err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if count != 0 {
		t.Fatalf("创建失败时不应留下任务行，实得 %d 条", count)
	}
}

// 生效时区（用户设置 → 部署配置 → UTC）是 /auth/me 里的 effective_timezone，
// 设置页据此显示「跟随部署配置」时机器人实际用哪个时区。
func TestInt138EffectiveTimezoneName(t *testing.T) {
	cases := []struct {
		name     string
		userTZ   string
		deployTZ string
		want     string
	}{
		{"用户设置优先", "Asia/Shanghai", "Europe/Berlin", "Asia/Shanghai"},
		{"用户为空回落部署", "", "Europe/Berlin", "Europe/Berlin"},
		{"用户非法回落部署", "Asia/Shangha", "Europe/Berlin", "Europe/Berlin"},
		{"都为空回落 UTC", "", "", "UTC"},
		{"部署非法回落 UTC", "", "Not/AZone", "UTC"},
	}
	for _, tc := range cases {
		if got := effectiveTimezoneName(tc.userTZ, tc.deployTZ); got != tc.want {
			t.Fatalf("%s: effectiveTimezoneName(%q, %q) = %q, want %q",
				tc.name, tc.userTZ, tc.deployTZ, got, tc.want)
		}
	}

	// auth 投影带上生效时区；成员投影不填（成员列表不需要这个派生值）。
	svc := &AuthService{cfg: &config.Config{Bots: config.BotsConfig{
		Memory: config.BotsMemoryConfig{Timezone: "Asia/Tokyo"},
	}}}
	resp := svc.toUserResponse(&model.User{ID: "u1", Username: "n", Email: "e"})
	if resp.EffectiveTimezone == nil || *resp.EffectiveTimezone != "Asia/Tokyo" {
		t.Fatalf("/auth/me 应带生效时区 Asia/Tokyo，实得 %+v", resp.EffectiveTimezone)
	}
	member := newUserResponse(&model.User{ID: "u1", Username: "n", Email: "e"})
	if member.EffectiveTimezone != nil {
		t.Fatalf("成员投影不应带生效时区: %+v", member.EffectiveTimezone)
	}
}

// 模型可见文本的快照（INT-138 验收要求）：fs_help 的「定时任务」整行与 v8 的
// fs_schedule 工具描述逐字钉死。这两段话直接决定模型怎么写任务指令、怎么理解
// 时区默认值，改一个字都应该是有意识的决定——所以是等值断言，不是关键词包含。
const (
	botInt138V8ScheduleDescription = "管理本机器人绑定的定时任务：list/create/update/delete/runs/run/enable/disable，全部同步执行、立即生效。每次运行是独立会话，只带你的记忆与本任务指令，写操作默认放行（不弹确认）。用户只能查看与开关任务，创建/修改由你负责；创建或修改后要在对话里用中文告知用户任务名与执行时间。时区可选，省略即用户时区（不是 UTC），「每天早上 9 点」按用户所在地执行。run 立即手动执行一次任务：显式触发就是一次执行、不要求任务启用；回执只表示已受理，终态与产出用 runs 查询；同一任务已有未结束运行时会被明确拒绝（不排队）；dry_run=true 只预览、不执行也不建运行记录。state_get/state_set 读写本任务跨运行保留的小状态（≤8192 UTF-8 字节）：只能在定时任务运行中使用、由本次运行的任务隐式决定，省略 state 即清空。instruction 写「意图」而不是固定操作步骤：说清目标、什么情况才需要汇报、去重方式（用 state_get/state_set 记「上次已报的 ID」，只报新增）；没有需要告知的内容时只回复一行 [[SILENT]]；只有「新内容确实值得打断用户」的周期检查才开 needs_report=true。"
	botInt138HelpScheduleLine      = "- 定时任务：fs_schedule list/create/update/delete/runs/run/enable/disable。cron 是标准五字段（分 时 日 月 周），时区用独立 IANA 字段（可选，省略即用户时区，「每天 9 点」按用户所在地执行；update 不传时区=保持原值）。list/runs/disable 与只改 name/cron/timezone 的 update、dry_run=true 的 run 同步立即生效；create、改动 instruction/needs_report/model 的 update、run（dry_run=true 预览除外）、enable、delete 需要用户确认：先受理（回执 status=pending_confirmation），用户批准后在后台执行，结果以「平台后台结果」消息送达，不要把受理当成已执行；批准创建或修改任务等于授权该任务以后每次运行默认放行写操作，enable 等于重新启用一个以后每次运行默认放行写操作的任务，delete 不可逆。任务由你创建/修改/删除；用户只能查看与开关。run 立即手动执行一次：显式触发就是一次执行、不要求任务启用，回执只表示已受理（终态用 runs 查询，trigger_source 区分 cron/manual）；同一任务已有未结束运行时被拒绝；dry_run=true 只预览、不建运行记录。定时任务运行中不能管理任务（防止绕过写操作确认），请在主线对话里操作。每次运行是独立会话，只带你的记忆与任务指令，写操作默认放行；无话可说时只回复一行 [[SILENT]]。任务各有一块跨运行保留的小状态：state_get（无参数）读取、state_set 写入（state 为任意 JSON 值，≤8192 UTF-8 字节；省略或 null 即清空）；这两个动作只能在定时任务运行（schedule run）内使用，主线会话调用会被拒绝。当前状态已随运行时上下文的 task_state 字段给出（带 task_state_updated_at；是本次运行开始时的快照，省略表示当时为空，运行中变化后要最新值才调 state_get），开局不必调 state_get；写入与当前完全相同的内容会直接回 unchanged、不产生变更——没有变化就不要重复写。任务指令写「意图」而不是固定操作步骤：说清目标、什么情况才需要汇报、去重方式（用 state_get/state_set 记「上次已报的 ID」，只报新增），例如「每天 9 点检查新 issue，有新的才告诉我」；只有新内容确实值得打断用户的周期检查才开 needs_report=true。"
)

func TestBotInt138HelpAndToolDescriptionGuidance(t *testing.T) {
	// fs_help 的「定时任务」整行逐字快照（含时区默认与指令写法示例）。
	var helpLine string
	for _, line := range strings.Split(botHelpText, "\n") {
		if strings.HasPrefix(line, "- 定时任务：") {
			helpLine = line
			break
		}
	}
	if helpLine == "" {
		t.Fatalf("帮助文本缺少「定时任务」条目")
	}
	if helpLine != botInt138HelpScheduleLine {
		t.Fatalf("fs_help 定时任务条目偏离快照:\n  got: %s\n want: %s", helpLine, botInt138HelpScheduleLine)
	}

	// v8 的 fs_schedule 工具描述逐字快照。
	// v8 已冻结在 INT-138、v9 已冻结为 INT-148 的字节；当前发布版本是 v10（INT-136）。
	v8 := botToolsetVersionOf(8)
	sched := v8.byName["fs_schedule"]
	if sched == nil {
		t.Fatalf("v8 应含 fs_schedule")
	}
	if sched.Description != botInt138V8ScheduleDescription {
		t.Fatalf("v8 fs_schedule 描述偏离快照:\n  got: %s\n want: %s",
			sched.Description, botInt138V8ScheduleDescription)
	}

	// create 动作描述（不进 wire 字节，但模型会读到）只需说明时区默认。
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
	// v8 冻结在 INT-138，v9 已冻结为 INT-148 的字节；当前是 INT-160 的 v12
	//（v10/v11 也已冻结为各自发布基线）。
	current := botToolsetVersionOf(botToolsetVersion)
	v9 := botToolsetVersionOf(9)
	if current.version != 12 || current.hash == v8.hash || current.hash == v9.hash {
		t.Fatalf("botToolsetVersion 应指向 v12（v8–v11 已冻结），实得 v%d", current.version)
	}
	if v9.hash != botToolsetV9FrozenHash {
		t.Fatalf("v9 tools_hash 偏离 INT-148 冻结基线: got %s want %s", v9.hash, botToolsetV9FrozenHash)
	}
	if v8.hash != botToolsetV8FrozenHash {
		t.Fatalf("v8 tools_hash 偏离冻结基线: got %s want %s", v8.hash, botToolsetV8FrozenHash)
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
