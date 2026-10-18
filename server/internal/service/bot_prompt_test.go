package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-122：内置机器人对话风格收敛。提示词版本递增到 5，风格段排在平台
// 规则之后、人设之前；运行时上下文 note 按是否存在待处理输入分支。

func TestBotSystemPromptStyleSection(t *testing.T) {
	if botPlatformPromptVersion != 6 {
		t.Fatalf("提示词版本应递增到 6: %d", botPlatformPromptVersion)
	}
	full := buildBotSystemPrompt("回复要更短。")
	// 关键词锚点覆盖风格段每条要求，不钉全文。
	for _, kw := range []string{
		"对话风格",
		// 结论先行、篇幅收口、表格与标题约束。
		"像同事聊天", "先给结论", "1～3 句", "不写标题", "不做小节", "多于 3 行时才用表格",
		// 不复述/不评价/不列选项；低风险直接做。
		"不复述用户的话", "不评价用户", "下一步可选项", "直接去做", "确认按钮", "A/B/C",
		// 工具回合无旁白，核实不必说出口。
		"不讲旁白", "tool_calls", "不需要向用户说出来",
		// 闲聊不接任务；内部信息不外漏。
		"打招呼或闲聊", "只回问候", "不向用户转述运行时上下文",
		// 无新输入的唤醒默认安静。
		"没有新用户输入", "最多回一句", "失败或异常",
	} {
		if !strings.Contains(full, kw) {
			t.Fatalf("风格段缺少关键词 %q", kw)
		}
	}
	// 拼装顺序：平台规则 → 对话风格 → 人设。
	ir := strings.Index(full, "你是 Fast Ship 内置机器人")
	is := strings.Index(full, "对话风格：")
	ip := strings.Index(full, "人设（由机器人主人配置）：")
	if ir < 0 || is < 0 || ip < 0 || ir > is || is > ip {
		t.Fatalf("拼装顺序应为 平台规则→对话风格→人设: %d %d %d", ir, is, ip)
	}
	// 无 persona 时不拼人设段，但风格段保留。
	bare := buildBotSystemPrompt("   ")
	if strings.Contains(bare, "人设（由机器人主人配置）") {
		t.Fatalf("空白 persona 不应有人设段")
	}
	if !strings.Contains(bare, "对话风格：") {
		t.Fatalf("空白 persona 也应含对话风格段")
	}
}

// TestBotPlatformPromptOnlyUpgrade 平台提示词单独升级（v5）的存量机器人：
// 工具集已是最新版本时，重启激活只重建提示词并恰好 +1 prompt_revision，
// 工具集与版本字节不动；激活进程内幂等，重复调用不再递增。
func TestBotPlatformPromptOnlyUpgrade(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "ppup5")
	bot := env.createBot(t, userID)
	const oldPrompt = "旧平台提示：没有对话风格段落"
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version":         botToolsetVersion,
		"platform_prompt_version": 4,
		"system_prompt_text":      oldPrompt,
	}).Error; err != nil {
		t.Fatalf("stale bot: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	run := botFixtureRun(t, env, botRow)
	builtBefore, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build before: %v", err)
	}

	env.bots.rt.mu.Lock()
	delete(env.bots.rt.activated, bot.ID)
	env.bots.rt.mu.Unlock()

	revBefore := botRow.PromptRevision
	if err := env.bots.activateOnProcessStart(botRow); err != nil {
		t.Fatalf("activate: %v", err)
	}
	fresh, _ := env.bots.bots.Get(bot.ID)
	if fresh.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("平台提示词版本应升到 %d，实得 %d", botPlatformPromptVersion, fresh.PlatformPromptVersion)
	}
	if fresh.ToolsetVersion != botToolsetVersion {
		t.Fatalf("工具集版本应保持 %d，实得 %d", botToolsetVersion, fresh.ToolsetVersion)
	}
	if fresh.PromptRevision != revBefore+1 {
		t.Fatalf("prompt_revision 应恰好 +1: %d -> %d", revBefore, fresh.PromptRevision)
	}
	if !strings.Contains(fresh.SystemPromptText, "对话风格") || strings.Contains(fresh.SystemPromptText, oldPrompt) {
		t.Fatalf("系统提示词应重建为含对话风格的新文案")
	}

	// 激活进程内幂等：再次调用不再递增 revision。
	if err := env.bots.activateOnProcessStart(fresh); err != nil {
		t.Fatalf("activate again: %v", err)
	}
	again, _ := env.bots.bots.Get(bot.ID)
	if again.PromptRevision != revBefore+1 {
		t.Fatalf("重复激活不得递增 prompt_revision: %d", again.PromptRevision)
	}

	// 只有提示词变了：工具集未动，升级前后构建的 tools 哈希一致。
	builtAfter, err := env.bots.buildRunContext(again, run)
	if err != nil {
		t.Fatalf("build after: %v", err)
	}
	if builtAfter.ToolsHash != builtBefore.ToolsHash {
		t.Fatalf("仅升级平台提示词不得改变工具集: %s vs %s", builtBefore.ToolsHash, builtAfter.ToolsHash)
	}
}

// TestBotActivateRefreshesConcurrentStaleCopy 并发重复激活：两个 run 同时启动
// 时各持一份激活前的独立 bot 副本，首个 run 完成激活后，后走
// activateOnProcessStart 的副本必须在「已激活」早退分支从库里刷新提示词与
// 工具集字段，否则会带着旧系统提示词与旧工具集冻结整个 run。凭证/模型字段
// 不属于本次刷新（冻结语义在 captureRunFreeze），必须原样保留。
func TestBotActivateRefreshesConcurrentStaleCopy(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "concact")
	bot := env.createBot(t, userID)
	const oldPrompt = "旧激活前文案：不含对话风格段落"
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version":         3,
		"platform_prompt_version": 4,
		"system_prompt_text":      oldPrompt,
	}).Error; err != nil {
		t.Fatalf("stale bot: %v", err)
	}

	env.bots.rt.mu.Lock()
	delete(env.bots.rt.activated, bot.ID)
	env.bots.rt.mu.Unlock()

	// 两个并发 run 各自在激活前加载的独立副本。
	a, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load a: %v", err)
	}
	b, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load b: %v", err)
	}
	if a == b {
		t.Fatalf("两份副本必须是独立加载的")
	}

	// a 完成激活（写库 + 置 rt.activated）。
	if err := env.bots.activateOnProcessStart(a); err != nil {
		t.Fatalf("activate a: %v", err)
	}
	revAfterA := a.PromptRevision

	// b 走早退分支：提示词/工具集字段应从库里刷新。
	modelBefore, urlBefore := b.Model, b.BaseURL
	keyBefore := append([]byte(nil), b.APIKeyEncrypted...)
	if err := env.bots.activateOnProcessStart(b); err != nil {
		t.Fatalf("activate b: %v", err)
	}
	if !strings.Contains(b.SystemPromptText, "对话风格") || strings.Contains(b.SystemPromptText, oldPrompt) {
		t.Fatalf("b 的系统提示词应刷新为含对话风格的新文案")
	}
	if b.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("b 的平台提示词版本应刷新到 %d，实得 %d", botPlatformPromptVersion, b.PlatformPromptVersion)
	}
	if b.ToolsetVersion != botToolsetVersion {
		t.Fatalf("b 的工具集版本应刷新到 %d，实得 %d", botToolsetVersion, b.ToolsetVersion)
	}
	if b.PromptRevision != revAfterA {
		t.Fatalf("b 的 prompt_revision 应与 a 激活后一致: %d vs %d", b.PromptRevision, revAfterA)
	}
	if b.Model != modelBefore || b.BaseURL != urlBefore || !bytes.Equal(b.APIKeyEncrypted, keyBefore) {
		t.Fatalf("凭证/模型字段不得被刷新改动")
	}

	// 用 b 构建请求：冻结出去的系统消息必须是新提示词、工具集是新版本。
	run := botFixtureRun(t, env, b)
	built, err := env.bots.buildRunContext(b, run)
	if err != nil {
		t.Fatalf("build run context: %v", err)
	}
	raw, err := json.Marshal(built.Request.Messages[0])
	if err != nil {
		t.Fatalf("marshal system message: %v", err)
	}
	var wire struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal system message: %v", err)
	}
	if wire.Role != "system" || !strings.Contains(wire.Content, "对话风格") || strings.Contains(wire.Content, oldPrompt) {
		t.Fatalf("冻结出去的系统消息应为含对话风格的新提示词: role=%s", wire.Role)
	}
	if built.ToolsetVersion != botToolsetVersion {
		t.Fatalf("构建出的工具集版本应为 %d，实得 %d", botToolsetVersion, built.ToolsetVersion)
	}
}

func TestBotRuntimeContextNoteBranches(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botnote")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}
	msgs := repository.NewBotMessageRepository(env.db)
	appendUser := func(text string) *model.BotMessage {
		t.Helper()
		raw, err := json.Marshal(botUserContent{Text: text})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		seq, err := env.bots.bots.NextMessageSeqTx(env.db, bot.ID)
		if err != nil {
			t.Fatalf("seq: %v", err)
		}
		msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: seq,
			Kind: model.BotMessageKindUser, ContentJSON: string(raw), CreatedAt: time.Now().UTC()}
		if err := msgs.AppendTx(env.db, msg); err != nil {
			t.Fatalf("append: %v", err)
		}
		return msg
	}
	seedRun := func(id string, through int64) *model.BotRun {
		t.Helper()
		now := time.Now().UTC()
		run := &model.BotRun{
			ID: id, BotID: bot.ID,
			TriggerSource: model.BotTriggerUserMessage, TriggerKey: id,
			TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
			InputThroughSeq:    through,
			ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		}
		if err := env.bots.runs.CreateTx(env.db, run); err != nil {
			t.Fatalf("seed run: %v", err)
		}
		return run
	}
	parseRuntime := func(built *botBuiltContext) botRuntimeContext {
		t.Helper()
		raw, err := json.Marshal(built.Request.Messages[len(built.Request.Messages)-1])
		if err != nil {
			t.Fatalf("marshal last message: %v", err)
		}
		var wire struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal message: %v", err)
		}
		if wire.Role != "user" {
			t.Fatalf("末条消息应为 user: %s", wire.Role)
		}
		const prefix = "[运行时上下文]\n"
		if !strings.HasPrefix(wire.Content, prefix) {
			t.Fatalf("末条消息应以运行时上下文开头: %q", wire.Content)
		}
		var rt botRuntimeContext
		if err := json.Unmarshal([]byte(strings.TrimPrefix(wire.Content, prefix)), &rt); err != nil {
			t.Fatalf("unmarshal runtime context: %v", err)
		}
		return rt
	}

	// 分支一：有待处理输入（水位 0，user 未消费）→ 保留 pending 说明。
	first := appendUser("待处理的问题")
	built, err := env.bots.buildRunContext(botRow, seedRun("run-note-pending", 0))
	if err != nil {
		t.Fatalf("build pending: %v", err)
	}
	rt := parseRuntime(built)
	if len(rt.PendingInputs) == 0 || rt.PendingInputs[0].Seq != first.Seq {
		t.Fatalf("应有待处理输入且 seq 相符: %+v", rt.PendingInputs)
	}
	for _, kw := range []string{"pending_inputs", "继续、调整或停止"} {
		if !strings.Contains(rt.Note, kw) {
			t.Fatalf("有待处理输入时 note 应含 %q: %q", kw, rt.Note)
		}
	}

	// 分支二：无待处理输入（水位覆盖全部账本）→ 明确「没有新的用户输入」，
	// 不再出现 pending 说明。
	appendUser("已消费的问题")
	builtQuiet, err := env.bots.buildRunContext(botRow, seedRun("run-note-quiet", first.Seq+1))
	if err != nil {
		t.Fatalf("build quiet: %v", err)
	}
	rtQuiet := parseRuntime(builtQuiet)
	if len(rtQuiet.PendingInputs) != 0 {
		t.Fatalf("不应有待处理输入: %+v", rtQuiet.PendingInputs)
	}
	if !strings.Contains(rtQuiet.Note, "没有新的用户输入") {
		t.Fatalf("无待处理输入时 note 应明确没有新输入: %q", rtQuiet.Note)
	}
	if strings.Contains(rtQuiet.Note, "pending_inputs") {
		t.Fatalf("无待处理输入时 note 不应提 pending_inputs: %q", rtQuiet.Note)
	}
}
