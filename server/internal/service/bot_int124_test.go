package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-124 测试：只读记忆动作对「不存在」返回空结果、夜间整理触发键含未消费
// 内容签名（可补跑）、静默存档按 seq 水位幂等。

// int124MemoryCall 直接执行一条 fs_memory 工具调用。
func int124MemoryCall(t *testing.T, env *botTestEnv, botID, args string) string {
	t.Helper()
	out, err := env.bots.execMemoryTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, ToolName: "fs_memory",
		ArgumentsJSON: args,
	})
	if err != nil {
		t.Fatalf("fs_memory %s: %v", args, err)
	}
	return out
}

// int124DecodeRead 解析 read 返回体。
func int124DecodeRead(t *testing.T, out string) struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Deleted    bool   `json:"deleted"`
	Content    string `json:"content"`
	Version    int64  `json:"version"`
	TotalChars int64  `json:"total_chars"`
	Offset     int64  `json:"offset"`
	HasMore    bool   `json:"has_more"`
} {
	t.Helper()
	var page struct {
		Path       string `json:"path"`
		Exists     bool   `json:"exists"`
		Deleted    bool   `json:"deleted"`
		Content    string `json:"content"`
		Version    int64  `json:"version"`
		TotalChars int64  `json:"total_chars"`
		Offset     int64  `json:"offset"`
		HasMore    bool   `json:"has_more"`
	}
	if err := json.Unmarshal([]byte(out), &page); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	return page
}

// TestInt124MemoryReadMissingReturnsEmpty 只读动作对不存在的文件成功返回空
// 结果（不再 e40001）；墓碑读取显 deleted=true 且不吐正文；显式历史读取与
// 写入类动作对缺失文件的报错保持现状。
func TestInt124MemoryReadMissingReturnsEmpty(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int124read")
	bot := env.createBot(t, userID)

	// 新建 bot：read / history 成功且为空。
	page := int124DecodeRead(t, int124MemoryCall(t, env, bot.ID, `{"action":"read","path":"MEMORY.md"}`))
	if page.Path != "MEMORY.md" || page.Exists || page.Deleted || page.Content != "" ||
		page.Version != 0 || page.TotalChars != 0 || page.Offset != 0 || page.HasMore {
		t.Fatalf("缺失文件读取应为空结果: %+v", page)
	}
	var hist struct {
		Path           string            `json:"path"`
		Exists         bool              `json:"exists"`
		CurrentVersion int64             `json:"current_version"`
		Deleted        bool              `json:"deleted"`
		Versions       []json.RawMessage `json:"versions"`
		HasMore        bool              `json:"has_more"`
	}
	out := int124MemoryCall(t, env, bot.ID, `{"action":"history","path":"MEMORY.md"}`)
	if err := json.Unmarshal([]byte(out), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if hist.Path != "MEMORY.md" || hist.Exists || hist.Deleted || hist.CurrentVersion != 0 ||
		len(hist.Versions) != 0 || hist.HasMore {
		t.Fatalf("缺失文件 history 应为空结果: %+v", hist)
	}

	// 显式读历史版本：缺失路径仍是非法操作。
	if _, err := env.bots.execMemoryTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_memory",
		ArgumentsJSON: `{"action":"read","path":"MEMORY.md","version":1}`,
	}); err == nil {
		t.Fatalf("read version=1 对不存在文件应报错")
	}

	// 写入类动作对缺失文件保持报错。
	for _, args := range []string{
		`{"action":"replace","path":"MEMORY.md","content":"x"}`,
		`{"action":"append","path":"MEMORY.md","content":"x"}`,
		`{"action":"delete","path":"MEMORY.md"}`,
		`{"action":"restore","path":"MEMORY.md","version":1}`,
	} {
		if _, err := env.bots.execMemoryTool(&model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_memory", ArgumentsJSON: args,
		}); err == nil {
			t.Fatalf("写入类动作对缺失文件应报错: %s", args)
		}
	}

	// 建文件后读取：exists=true、deleted=false，其余字段向后兼容。
	int124MemoryCall(t, env, bot.ID, `{"action":"create","path":"MEMORY.md","content":"项目天狼星"}`)
	page = int124DecodeRead(t, int124MemoryCall(t, env, bot.ID, `{"action":"read","path":"MEMORY.md"}`))
	if !page.Exists || page.Deleted || page.Content != "项目天狼星" || page.Version != 1 || page.TotalChars != 5 {
		t.Fatalf("存在文件读取应向后兼容并带 exists: %+v", page)
	}
	out = int124MemoryCall(t, env, bot.ID, `{"action":"history","path":"MEMORY.md"}`)
	if err := json.Unmarshal([]byte(out), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if !hist.Exists || hist.Deleted || hist.CurrentVersion != 1 || len(hist.Versions) != 1 {
		t.Fatalf("存在文件 history 应带 exists=true: %+v", hist)
	}

	// 删除成墓碑：read 成功返回 exists=false + deleted=true，不吐墓碑正文。
	ev := int64(1)
	if _, _, err := repository.NewBotMemoryRepository(env.db).WriteVersionTx(env.db, bot.ID, "MEMORY.md",
		model.BotMemoryOpDelete, "", true, &ev, model.BotMemoryWriterMain, "", "", "int124-del",
		time.Now().UTC()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	page = int124DecodeRead(t, int124MemoryCall(t, env, bot.ID, `{"action":"read","path":"MEMORY.md"}`))
	if page.Exists || !page.Deleted || page.Content != "" || page.Version != 0 || page.TotalChars != 0 {
		t.Fatalf("墓碑读取应为 exists=false + deleted=true 且正文为空: %+v", page)
	}
	// 墓碑的显式历史版本保持可读（现状不变）。
	out = int124MemoryCall(t, env, bot.ID, `{"action":"read","path":"MEMORY.md","version":1}`)
	if !strings.Contains(out, "项目天狼星") {
		t.Fatalf("墓碑历史版本读取应保持可用: %s", out)
	}
	// 墓碑 history：exists 与 read 同义记 false + deleted=true，历史版本仍可查。
	out = int124MemoryCall(t, env, bot.ID, `{"action":"history","path":"MEMORY.md"}`)
	if err := json.Unmarshal([]byte(out), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if hist.Exists || !hist.Deleted || len(hist.Versions) < 1 {
		t.Fatalf("墓碑 history 应为 exists=false + deleted=true 且版本仍可见: %+v", hist)
	}
}

// int124SeedMaintRun 落一条 running 维护运行（归档/夜间的账本外键）。
func int124SeedMaintRun(t *testing.T, env *botTestEnv, botID, triggerKey string) *model.BotMaintenanceRun {
	t.Helper()
	now := time.Now().UTC()
	mr := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID, Kind: model.BotMaintenanceCompaction,
		TriggerKey: triggerKey, Status: model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(mr).Error; err != nil {
		t.Fatalf("seed maint run: %v", err)
	}
	return mr
}

func int124ArchiveRequests(t *testing.T, env *botTestEnv, maintID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotMaintenanceRequest{}).
		Where("maintenance_run_id = ? AND purpose = ?", maintID, model.BotPurposeArchive).
		Count(&n).Error; err != nil {
		t.Fatalf("count archive requests: %v", err)
	}
	return n
}

// TestInt124SilentArchiveWatermark 同一 seq 区间重复触发静默存档：daily 只
// 追加一次、水位只前进到段内最大 seq、重复周期不再发归档请求。
func TestInt124SilentArchiveWatermark(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int124arch")
	bot := env.createBot(t, userID)
	m1 := seedBotUserMessage(t, env, bot.ID, "第一条：用户叫 Bobo")
	seedBotUserMessage(t, env, bot.ID, "第二条：项目代号天狼星")
	m3 := seedBotUserMessage(t, env, bot.ID, "第三条：偏好简洁回复")
	var seg []model.BotMessage
	if err := env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&seg).Error; err != nil {
		t.Fatalf("load segment: %v", err)
	}
	plan := &botCompactionPlan{Segment: seg}

	env.fake.mu.Lock()
	env.fake.turns = []fakeModelTurn{{
		Text: fmt.Sprintf(`{"items":[{"text":"用户叫 Bobo，项目代号天狼星","source_seq":%d}]}`, m1.Seq),
	}}
	env.fake.mu.Unlock()
	env.fake.strict = true

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}

	// 第一轮：正常归档——daily 落库、水位推进到段内最大 seq。
	mr1 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g0:s-int124-1")
	env.bots.runSilentArchive(context.Background(), botRow, mr1, plan)
	dailyPath := env.bots.botDailyPathFor(time.Now())
	f, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil || !strings.Contains(f.Content, "用户叫 Bobo") {
		t.Fatalf("第一轮应写入 daily: %v", err)
	}
	if f.CurrentVersion != 1 {
		t.Fatalf("daily 应只写一版，实得 v%d", f.CurrentVersion)
	}
	if n := int124ArchiveRequests(t, env, mr1.ID); n != 1 {
		t.Fatalf("第一轮应恰好 1 次归档请求，实得 %d", n)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m3.Seq {
		t.Fatalf("水位应推进到段内最大 seq %d，实得 %d", m3.Seq, fresh.MemoryArchiveSeq)
	}

	// 同一 seq 区间再次触发（新维护运行）：already_archived，不发请求、
	// 不追加 daily。
	mr2 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g1:s-int124-2")
	env.bots.runSilentArchive(context.Background(), botRow, mr2, plan)
	row2, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr2.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row2.ResultJSON, "already_archived") {
		t.Fatalf("第二轮应记 already_archived: %s", row2.ResultJSON)
	}
	if n := int124ArchiveRequests(t, env, mr2.ID); n != 0 {
		t.Fatalf("already_archived 不应发归档请求，实得 %d", n)
	}
	f2, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil || f2.CurrentVersion != 1 || f2.Content != f.Content {
		t.Fatalf("同一区间重复归档不得追加 daily: %v", err)
	}
}

// TestInt124SilentArchiveFailureKeepsWatermark 失败路径（bad_output）不推进
// 水位、不写 daily，保持重试语义。
func TestInt124SilentArchiveFailureKeepsWatermark(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int124archfail")
	bot := env.createBot(t, userID)
	seedBotUserMessage(t, env, bot.ID, "用户叫 Bobo")
	var seg []model.BotMessage
	if err := env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&seg).Error; err != nil {
		t.Fatalf("load segment: %v", err)
	}
	plan := &botCompactionPlan{Segment: seg}
	env.fake.mu.Lock()
	env.fake.turns = []fakeModelTurn{{Text: `不是 JSON`}}
	env.fake.mu.Unlock()
	env.fake.strict = true
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	mr := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g0:s-int124-bad")
	env.bots.runSilentArchive(context.Background(), botRow, mr, plan)
	row, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row.ResultJSON, "bad_output") {
		t.Fatalf("坏输出应记 bad_output: %s", row.ResultJSON)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != 0 {
		t.Fatalf("失败路径不得推进水位，实得 %d", fresh.MemoryArchiveSeq)
	}
	var n int64
	env.db.Model(&model.BotMemoryFile{}).Where("bot_id = ? AND path LIKE ?", bot.ID, "daily/%").Count(&n)
	if n != 0 {
		t.Fatalf("失败路径不得写 daily")
	}
}

// TestInt124SilentArchiveSkipsBelowWatermark 模型重放水位以下消息的旧事实
// 时按 seq 过滤：只写水位之上的新条目。
func TestInt124SilentArchiveSkipsBelowWatermark(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int124archmix")
	bot := env.createBot(t, userID)
	m1 := seedBotUserMessage(t, env, bot.ID, "旧消息：用户叫 Bobo")
	m2 := seedBotUserMessage(t, env, bot.ID, "新消息：项目代号天狼星")
	// 此前已归档到 m1.Seq。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("memory_archive_seq", m1.Seq).Error; err != nil {
		t.Fatalf("set watermark: %v", err)
	}
	var seg []model.BotMessage
	if err := env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&seg).Error; err != nil {
		t.Fatalf("load segment: %v", err)
	}
	plan := &botCompactionPlan{Segment: seg}
	env.fake.mu.Lock()
	env.fake.turns = []fakeModelTurn{{
		Text: fmt.Sprintf(`{"items":[{"text":"旧事实：用户叫 Bobo","source_seq":%d},`+
			`{"text":"新事实：项目代号天狼星","source_seq":%d}]}`, m1.Seq, m2.Seq),
	}}
	env.fake.mu.Unlock()
	env.fake.strict = true
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	mr := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g0:s-int124-mix")
	env.bots.runSilentArchive(context.Background(), botRow, mr, plan)
	dailyPath := env.bots.botDailyPathFor(time.Now())
	f, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if !strings.Contains(f.Content, "新事实：项目代号天狼星") {
		t.Fatalf("水位之上的新条目应写入 daily: %q", f.Content)
	}
	if strings.Contains(f.Content, "旧事实") {
		t.Fatalf("水位以下的旧条目不得重复写入: %q", f.Content)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m2.Seq {
		t.Fatalf("水位应推进到 %d，实得 %d", m2.Seq, fresh.MemoryArchiveSeq)
	}
}

// TestInt124NightlyCatchUp 有未消费 daily 时触发键带内容签名并可补跑；同一
// 内容状态不重复入队；消费后回到日期键。
func TestInt124NightlyCatchUp(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: `{"user_md":"用户叫 Bobo","memory_md":"项目天狼星","sources":[{"path":"daily/2026-10-15.md","version":1}],"note":"ok"}`})
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "int124night")
	bot := env.createBot(t, userID)
	env.bots.nowFn = func() time.Time {
		return time.Date(2026, 10, 16, 4, 0, 0, 0, time.UTC)
	}
	const date = "2026-10-16"
	const baseKey = "nightly:" + date

	// 无未消费内容：日期键。
	if key := env.bots.nightlyTriggerKey(bot.ID, date); key != baseKey {
		t.Fatalf("无未消费内容应为日期键: %s", key)
	}

	// 固定时刻的扫描先跑一次：nothing_to_consume。
	env.bots.scanNightlyOnce()
	var run1 model.BotMaintenanceRun
	waitForBot(t, "首次夜间运行建档", 10*time.Second, func() bool {
		return env.db.Where("bot_id = ? AND kind = ? AND trigger_key = ?",
			bot.ID, model.BotMaintenanceNightly, baseKey).First(&run1).Error == nil
	})
	// 收尾 wait 直接把最新行读回 run1：建档 wait 留下的是「刚建档、
	// ResultJSON 仍是 {}」的快照，run 完成晚于建档轮询时后续断言会读到陈旧值。
	waitForBot(t, "首次夜间运行收尾", 10*time.Second, func() bool {
		if err := env.db.Where("id = ?", run1.ID).First(&run1).Error; err != nil {
			return false
		}
		return run1.Status == model.BotMaintenanceSucceeded || run1.Status == model.BotMaintenanceFailed
	})
	if !strings.Contains(run1.ResultJSON, "nothing_to_consume") {
		t.Fatalf("无内容应记 nothing_to_consume: %s", run1.ResultJSON)
	}

	// 04:25 daily 才写出来：签名出现、键变化且形态为 16 hex。
	writeBotMemory(t, env, bot.ID, "daily/2026-10-15.md", "- 用户叫 Bobo\n- 项目天狼星", "int124-d")
	sigKey := env.bots.nightlyTriggerKey(bot.ID, date)
	if !regexp.MustCompile(`^nightly:2026-10-16:[0-9a-f]{16}$`).MatchString(sigKey) {
		t.Fatalf("有未消费内容应带 16 hex 内容签名: %s", sigKey)
	}
	if again := env.bots.nightlyTriggerKey(bot.ID, date); again != sigKey {
		t.Fatalf("同一内容状态签名应稳定: %s vs %s", again, sigKey)
	}

	// 下一次扫描用新键补跑：整理消费该 daily。
	env.bots.scanNightlyOnce()
	var run2 model.BotMaintenanceRun
	waitForBot(t, "补跑运行建档", 10*time.Second, func() bool {
		return env.db.Where("bot_id = ? AND kind = ? AND trigger_key = ?",
			bot.ID, model.BotMaintenanceNightly, sigKey).First(&run2).Error == nil
	})
	// 同理重读进 run2，Status / ResultJSON 断言用的才是收尾后的最新行。
	waitForBot(t, "补跑收尾", 10*time.Second, func() bool {
		if err := env.db.Where("id = ?", run2.ID).First(&run2).Error; err != nil {
			return false
		}
		return run2.Status == model.BotMaintenanceSucceeded || run2.Status == model.BotMaintenanceFailed
	})
	if run2.Status != model.BotMaintenanceSucceeded {
		t.Fatalf("补跑应成功: %s %v", run2.Status, run2.ErrorCode)
	}
	mem, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, "MEMORY.md")
	if err != nil || !strings.Contains(mem.Content, "天狼星") {
		t.Fatalf("补跑应整理进 MEMORY.md: %v", err)
	}
	if !strings.Contains(run2.ResultJSON, "daily/2026-10-15.md") {
		t.Fatalf("补跑应记录已消费来源: %s", run2.ResultJSON)
	}

	// 内容已消费：签名消失回到日期键（已存在不重建）；再扫也不产生第三条。
	if key := env.bots.nightlyTriggerKey(bot.ID, date); key != baseKey {
		t.Fatalf("消费后应回到日期键: %s", key)
	}
	env.bots.scanNightlyOnce()
	env.bots.scanNightlyOnce()
	time.Sleep(200 * time.Millisecond)
	var total int64
	env.db.Model(&model.BotMaintenanceRun{}).
		Where("bot_id = ? AND kind = ?", bot.ID, model.BotMaintenanceNightly).Count(&total)
	if total != 2 {
		t.Fatalf("同一内容状态与空内容不得重复入队，实得 %d 条夜间运行", total)
	}
}

// TestInt124PlatformPromptProactiveMemory 平台提示词 v7：规则 9 明确稳定偏好
// 与重要事实应主动沉淀；规则 10 的守门文案保持不变。
func TestInt124PlatformPromptProactiveMemory(t *testing.T) {
	if botPlatformPromptVersion != 7 {
		t.Fatalf("平台提示词版本应为 7: %d", botPlatformPromptVersion)
	}
	full := buildBotSystemPrompt("")
	for _, kw := range []string{"主动写入", "不必等用户要求", "稳定偏好"} {
		if !strings.Contains(full, kw) {
			t.Fatalf("规则 9 应含主动沉淀指引 %q", kw)
		}
	}
	for _, kw := range []string{"禁止把工具结果", "不要把密钥"} {
		if !strings.Contains(full, kw) {
			t.Fatalf("规则 10 守门应保持: %q", kw)
		}
	}
}

// TestInt124SerializeSegmentSentPrefix serializeSegment 返回实际写进请求的
// 最大 seq：预算截尾只丢尾部，正文与 max seq 对得上；空段返回 0。
func TestInt124SerializeSegmentSentPrefix(t *testing.T) {
	mk := func(seq int64, text string) model.BotMessage {
		raw, _ := json.Marshal(botUserContent{Text: text})
		return model.BotMessage{Seq: seq, Kind: model.BotMessageKindUser, ContentJSON: string(raw)}
	}
	rows := []model.BotMessage{mk(1, "第一条"), mk(2, "第二条"), mk(3, "第三条")}

	// 不限预算：整段发出，max seq = 3。
	full, maxSeq := serializeSegment(rows, 0)
	if maxSeq != 3 || !strings.Contains(full, "第三条") {
		t.Fatalf("不限预算应整段发出: maxSeq=%d full=%q", maxSeq, full)
	}
	// 每条序列化约 24 字节（"[seq N] user: 第一条\n"）：40 字节预算越过阈值时
	// 第二条已写入、第三条未写入——发出的最大 seq 是 2。
	text, sent := serializeSegment(rows, 40)
	if sent != 2 || !strings.Contains(text, "第二条") || strings.Contains(text, "第三条") {
		t.Fatalf("40 字节预算应发出前两条: sent=%d text=%q", sent, text)
	}
	text, sent = serializeSegment(rows, 20)
	if sent != 1 || !strings.Contains(text, "第一条") || strings.Contains(text, "第二条") {
		t.Fatalf("20 字节预算应只发出第一条: sent=%d text=%q", sent, text)
	}
	if text, sent := serializeSegment(nil, 100); text != "" || sent != 0 {
		t.Fatalf("空段应返回空正文与 0: %q %d", text, sent)
	}
}

// TestInt124SilentArchiveSentPrefixWatermark 预算截尾后水位只推进到实际发出
// 的最大 seq：未发出的段尾保持未归档，同一 plan 重试不是 already_archived、
// 只补发段尾（从假服务的请求体断言），水位最终继续前进。
func TestInt124SilentArchiveSentPrefixWatermark(t *testing.T) {
	env := setupBotTestEnv(t)
	// 测试配置里窗口是 fallback 默认 32768，整段装得下；调小到 1000 后输入
	// 预算进入「窗口−包装−输出」的小窗口分支，恒为 128 token（≈445 字节），
	// 而每条消息序列化约 265 字节——第二条越过预算、第三条发不出去。归档
	// 路径不探测窗口，/models 探测默认 404 也不落成功缓存行，无探测干扰。
	env.cfg.Bots.Context.WindowTokens = 1000
	userID := createBotTestUser(t, env.db, "int124trunc")
	bot := env.createBot(t, userID)
	pad := strings.Repeat("p", 250)
	m1 := seedBotUserMessage(t, env, bot.ID, "alpha"+pad)
	m2 := seedBotUserMessage(t, env, bot.ID, "bravo"+pad)
	m3 := seedBotUserMessage(t, env, bot.ID, "charlie"+pad)
	var seg []model.BotMessage
	if err := env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&seg).Error; err != nil {
		t.Fatalf("load segment: %v", err)
	}
	plan := &botCompactionPlan{Segment: seg}
	env.fake.mu.Lock()
	env.fake.turns = []fakeModelTurn{
		{Text: fmt.Sprintf(`{"items":[{"text":"归档事实一","source_seq":%d}]}`, m1.Seq)},
		{Text: fmt.Sprintf(`{"items":[{"text":"归档事实三","source_seq":%d}]}`, m3.Seq)},
	}
	env.fake.mu.Unlock()
	env.fake.strict = true
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	dailyPath := env.bots.botDailyPathFor(time.Now())

	// 第一轮：请求体只含发出前缀 m1+m2，段尾 m3 根本没进请求。
	mr1 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g0:s-int124-t1")
	env.bots.runSilentArchive(context.Background(), botRow, mr1, plan)
	body1 := string(env.fake.nextRequest(t, 5*time.Second).Body)
	if !strings.Contains(body1, "alpha") || !strings.Contains(body1, "bravo") ||
		strings.Contains(body1, "charlie") {
		t.Fatalf("第一轮请求应只含前缀 m1+m2，不含段尾 m3: %s", body1)
	}
	row1, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr1.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row1.ResultJSON, `"archive":"ok"`) {
		t.Fatalf("第一轮应正常归档: %s", row1.ResultJSON)
	}
	f1, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil || f1.CurrentVersion != 1 || !strings.Contains(f1.Content, "归档事实一") {
		t.Fatalf("第一轮应写入 daily: %v", err)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m2.Seq {
		t.Fatalf("水位应停在发出前缀 m2.Seq=%d，实得 %d", m2.Seq, fresh.MemoryArchiveSeq)
	}
	if fresh.MemoryArchiveSeq >= m3.Seq {
		t.Fatalf("水位不得越过未发出的段尾 m3.Seq=%d: %d", m3.Seq, fresh.MemoryArchiveSeq)
	}

	// 第二轮（同一 plan）：不是 already_archived，只补发段尾 m3。
	mr2 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g1:s-int124-t2")
	env.bots.runSilentArchive(context.Background(), botRow, mr2, plan)
	body2 := string(env.fake.nextRequest(t, 5*time.Second).Body)
	if !strings.Contains(body2, "charlie") || strings.Contains(body2, "alpha") ||
		strings.Contains(body2, "bravo") {
		t.Fatalf("第二轮请求应只含未发出的段尾 m3: %s", body2)
	}
	if n := int124ArchiveRequests(t, env, mr2.ID); n != 1 {
		t.Fatalf("第二轮应恰好 1 次归档请求，实得 %d", n)
	}
	row2, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr2.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row2.ResultJSON, `"archive":"ok"`) ||
		strings.Contains(row2.ResultJSON, "already_archived") {
		t.Fatalf("第二轮应补发归档而不是 already_archived: %s", row2.ResultJSON)
	}
	f2, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil || f2.CurrentVersion != 2 || !strings.Contains(f2.Content, "归档事实三") {
		t.Fatalf("第二轮应追加段尾事实: %v", err)
	}
	fresh, err = env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m3.Seq {
		t.Fatalf("水位应推进到 m3.Seq=%d，实得 %d", m3.Seq, fresh.MemoryArchiveSeq)
	}
}

// TestInt124SilentArchiveBacklogAcrossCycles 跨周期积压回归：第一轮预算截尾
// 只发出段前缀、水位停在发出边界，摘要却覆盖到整段切点——下一周期的 Segment
// 从更晚的消息开始，不再含被截掉的段尾。归档积压必须独立于摘要水位：第二轮
// 从账本读「水位之后、切点之内」，把被截掉的段尾重新入队并最终归档，而不是
// already_archived 永久漏掉。
func TestInt124SilentArchiveBacklogAcrossCycles(t *testing.T) {
	env := setupBotTestEnv(t)
	// 与 TestInt124SilentArchiveSentPrefixWatermark 同一手法：窗口 1000 使输入
	// 预算恒为 128 token（≈445 字节），每条消息序列化约 270 字节——第二条越过
	// 预算即停，只发出前缀。
	env.cfg.Bots.Context.WindowTokens = 1000
	userID := createBotTestUser(t, env.db, "int124backlog")
	bot := env.createBot(t, userID)
	pad := strings.Repeat("p", 250)
	m1 := seedBotUserMessage(t, env, bot.ID, "alpha"+pad)
	m2 := seedBotUserMessage(t, env, bot.ID, "bravo"+pad)
	m3 := seedBotUserMessage(t, env, bot.ID, "charlie"+pad)
	m4 := seedBotUserMessage(t, env, bot.ID, "delta"+pad)
	var seg []model.BotMessage
	if err := env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&seg).Error; err != nil {
		t.Fatalf("load segment: %v", err)
	}
	plan1 := &botCompactionPlan{Segment: seg, ThroughSeq: m4.Seq}
	env.fake.mu.Lock()
	env.fake.turns = []fakeModelTurn{
		{Text: fmt.Sprintf(`{"items":[{"text":"归档事实一","source_seq":%d}]}`, m1.Seq)},
		{Text: fmt.Sprintf(`{"items":[{"text":"归档事实三","source_seq":%d}]}`, m3.Seq)},
	}
	env.fake.mu.Unlock()
	env.fake.strict = true
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	dailyPath := env.bots.botDailyPathFor(time.Now())

	// 第一轮：Segment 含 m1..m4、切点 m4.Seq；预算截尾只发出 m1+m2，水位停在
	// m2.Seq，m3/m4 保持未归档。
	mr1 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g0:s-int124-b1")
	env.bots.runSilentArchive(context.Background(), botRow, mr1, plan1)
	body1 := string(env.fake.nextRequest(t, 5*time.Second).Body)
	if !strings.Contains(body1, "alpha") || !strings.Contains(body1, "bravo") ||
		strings.Contains(body1, "charlie") || strings.Contains(body1, "delta") {
		t.Fatalf("第一轮请求应只含前缀 m1+m2，不含段尾: %s", body1)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m2.Seq {
		t.Fatalf("第一轮水位应停在 m2.Seq=%d，实得 %d", m2.Seq, fresh.MemoryArchiveSeq)
	}

	// 第二个周期：摘要已覆盖到切点（ThroughSeq=m4.Seq），Segment 只从更晚的
	// m4 开始——被截掉的段尾 m3 不在任何 Segment 里。
	plan2 := &botCompactionPlan{Segment: seg[len(seg)-1:], ThroughSeq: m4.Seq}
	mr2 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g1:s-int124-b2")
	env.bots.runSilentArchive(context.Background(), botRow, mr2, plan2)
	body2 := string(env.fake.nextRequest(t, 5*time.Second).Body)
	// 积压被重新纳入：请求体包含被截掉的段尾 m3，且不含水位以下的 m1/m2。
	if !strings.Contains(body2, "charlie") || strings.Contains(body2, "alpha") ||
		strings.Contains(body2, "bravo") {
		t.Fatalf("第二轮请求应含被截掉的段尾 m3 且不含水位以下消息: %s", body2)
	}
	if n := int124ArchiveRequests(t, env, mr2.ID); n != 1 {
		t.Fatalf("第二轮应恰好 1 次归档请求，实得 %d", n)
	}
	row2, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr2.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row2.ResultJSON, `"archive":"ok"`) ||
		strings.Contains(row2.ResultJSON, "already_archived") {
		t.Fatalf("第二轮应补发积压而不是 already_archived: %s", row2.ResultJSON)
	}
	f2, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil || f2.CurrentVersion != 2 || !strings.Contains(f2.Content, "归档事实三") {
		t.Fatalf("第二轮应追加段尾事实到 daily: %v", err)
	}
	fresh, err = env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m4.Seq {
		t.Fatalf("水位应继续推进到切点 m4.Seq=%d，实得 %d", m4.Seq, fresh.MemoryArchiveSeq)
	}
}

// TestInt124SilentArchiveAllDuplicate 归档条目全部与既有 daily 判重：记
// all_duplicate 并在同一事务推进水位——同段重试 already_archived，不再重复
// 发归档请求。
func TestInt124SilentArchiveAllDuplicate(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int124dup")
	bot := env.createBot(t, userID)
	m1 := seedBotUserMessage(t, env, bot.ID, "用户叫 Bobo")
	dailyPath := env.bots.botDailyPathFor(time.Now())
	if _, _, err := repository.NewBotMemoryRepository(env.db).WriteVersionTx(env.db, bot.ID, dailyPath,
		model.BotMemoryOpCreate, "- 用户叫 Bobo", false, nil, model.BotMemoryWriterMain,
		"", "", "int124-pre", time.Now().UTC()); err != nil {
		t.Fatalf("seed daily: %v", err)
	}
	var seg []model.BotMessage
	if err := env.db.Where("bot_id = ?", bot.ID).Order("seq ASC").Find(&seg).Error; err != nil {
		t.Fatalf("load segment: %v", err)
	}
	plan := &botCompactionPlan{Segment: seg}
	env.fake.mu.Lock()
	env.fake.turns = []fakeModelTurn{{
		Text: fmt.Sprintf(`{"items":[{"text":"用户叫 Bobo","source_seq":%d}]}`, m1.Seq),
	}}
	env.fake.mu.Unlock()
	env.fake.strict = true
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}

	mr := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g0:s-int124-dup")
	env.bots.runSilentArchive(context.Background(), botRow, mr, plan)
	row, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row.ResultJSON, "all_duplicate") {
		t.Fatalf("全判重应记 all_duplicate: %s", row.ResultJSON)
	}
	f, err := repository.NewBotMemoryRepository(env.db).FindFile(bot.ID, dailyPath)
	if err != nil || f.CurrentVersion != 1 || f.Content != "- 用户叫 Bobo" {
		t.Fatalf("全判重不得追加 daily: %v", err)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("reload bot: %v", err)
	}
	if fresh.MemoryArchiveSeq != m1.Seq {
		t.Fatalf("全判重也应推进水位到 %d，实得 %d", m1.Seq, fresh.MemoryArchiveSeq)
	}

	// 水位已覆盖：同段再跑 already_archived、不发请求。
	mr2 := int124SeedMaintRun(t, env, bot.ID, "compaction:threshold:g1:s-int124-dup2")
	env.bots.runSilentArchive(context.Background(), botRow, mr2, plan)
	row2, err := repository.NewBotMaintenanceRepository(env.db).FindByID(mr2.ID)
	if err != nil {
		t.Fatalf("reload maint: %v", err)
	}
	if !strings.Contains(row2.ResultJSON, "already_archived") {
		t.Fatalf("同段重试应 already_archived: %s", row2.ResultJSON)
	}
	if n := int124ArchiveRequests(t, env, mr2.ID); n != 0 {
		t.Fatalf("同段重试不应发归档请求，实得 %d", n)
	}
}

// TestInt124MemoryReadVersionStatus 显式读历史版本：exists 与 head/history
// 同义（当前有存活文件），deleted 是被读版本自身的删除标记；version<=0
// 先于文件查找判非法参数，不再落进「记忆文件不存在」。
func TestInt124MemoryReadVersionStatus(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int124ver")
	bot := env.createBot(t, userID)

	int124MemoryCall(t, env, bot.ID, `{"action":"create","path":"MEMORY.md","content":"正文第一版"}`)
	int124MemoryCall(t, env, bot.ID, `{"action":"delete","path":"MEMORY.md","expected_version":1}`)

	// head 读取：墓碑 → exists=false + deleted=true，不吐正文。
	head := int124DecodeRead(t, int124MemoryCall(t, env, bot.ID, `{"action":"read","path":"MEMORY.md"}`))
	if head.Exists || !head.Deleted || head.Content != "" || head.Version != 0 {
		t.Fatalf("墓碑 head 读取应为 exists=false + deleted=true: %+v", head)
	}
	// read version=1：当前无存活文件（exists=false），但 v1 自身不是删除版本
	// （deleted=false），正文仍可读。
	p1 := int124DecodeRead(t, int124MemoryCall(t, env, bot.ID,
		`{"action":"read","path":"MEMORY.md","version":1}`))
	if p1.Exists || p1.Deleted || p1.Version != 1 || !strings.Contains(p1.Content, "正文第一版") {
		t.Fatalf("read version=1 应为 exists=false + deleted=false 且正文可读: %+v", p1)
	}
	// read version=2：删除版本自身 deleted=true，正文保留删除前内容。
	p2 := int124DecodeRead(t, int124MemoryCall(t, env, bot.ID,
		`{"action":"read","path":"MEMORY.md","version":2}`))
	if p2.Exists || !p2.Deleted || p2.Version != 2 || !strings.Contains(p2.Content, "正文第一版") {
		t.Fatalf("read version=2 应为 exists=false + deleted=true 且保留删除前正文: %+v", p2)
	}

	// version<=0：存在路径与缺失路径（USER.md 是白名单内但本 bot 未建）同类
	// 非法参数。
	for _, args := range []string{
		`{"action":"read","path":"MEMORY.md","version":0}`,
		`{"action":"read","path":"MEMORY.md","version":-1}`,
		`{"action":"read","path":"USER.md","version":0}`,
	} {
		if _, err := env.bots.execMemoryTool(&model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_memory", ArgumentsJSON: args,
		}); err == nil || err.Error() != errs.ErrInvalidParams.Error() {
			t.Fatalf("version<=0 应为非法参数: %s → %v", args, err)
		}
	}
	// 缺失路径 + 合法 version 保持「记忆文件不存在」（现状不变）。
	if _, err := env.bots.execMemoryTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_memory",
		ArgumentsJSON: `{"action":"read","path":"USER.md","version":1}`,
	}); err == nil || !strings.Contains(err.Error(), "记忆文件不存在") {
		t.Fatalf("缺失路径 + version=1 应报记忆文件不存在: %v", err)
	}
}
