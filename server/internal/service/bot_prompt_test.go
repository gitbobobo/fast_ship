package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-133：默认人设（身份与说话方式）系统自动注入、拼在最前；用户人设追加
// 在后，可以覆盖默认语气。它替代了 INT-122 的「对话风格」段，规则 8/9/13 的
// 「告知/汇报」统一改为「简要说明结果」，升级通知限定为内部信息。
func TestBotSystemPromptStyleSection(t *testing.T) {
	if botPlatformPromptVersion != 12 {
		t.Fatalf("提示词版本应递增到 12: %d", botPlatformPromptVersion)
	}
	if botStylePromptVersion != 9 {
		t.Fatalf("说话方式版本应为 9: %d", botStylePromptVersion)
	}
	full := buildBotSystemPrompt("回复要更短。", "t-nonce")
	// 关键词锚点覆盖默认人设的每条原则，不钉全文。
	for _, kw := range []string{
		"说话方式：",
		"篇幅跟信息量匹配", "简单问题简短回答", "确实需要时可以展开",
		"先给结论",
		"只有内容确实需要时才用标题和表格",
		"只在需要用户决策时提问", "不为客套追问",
		"调用工具前不讲旁白",
		"不自我表功",
	} {
		if !strings.Contains(full, kw) {
			t.Fatalf("默认人设缺少关键词 %q", kw)
		}
	}
	// 默认人设只写原则、不写反例：旧风格段的长清单不应洇回来。
	for _, gone := range []string{"对话风格：", "像同事聊天", "A/B/C", "下一步可选项"} {
		if strings.Contains(full, gone) {
			t.Fatalf("默认人设不宜再出现旧句式 %q", gone)
		}
	}
	// 规则 9/13 的告知语义统一为「简要说明结果」；规则 8 的任务名告知
	// 条款随 INT-139 精简移出前缀，归 fs_schedule 工具描述（仍钉住）。
	for _, kw := range []string{
		"记忆写入成功后用一句话说明记住了什么",
		"并简要说明结果",
		"恢复后继续核实并说明结果",
	} {
		if !strings.Contains(full, kw) {
			t.Fatalf("规则文案缺少关键词 %q", kw)
		}
	}
	schedDesc := ""
	for _, tool := range botToolRegistryV10() {
		if tool.Name == "fs_schedule" {
			schedDesc = tool.Description
		}
	}
	if !strings.Contains(schedDesc, "告知用户任务名与执行时间") {
		t.Fatalf("fs_schedule 工具描述应保留「创建/修改后告知任务名与执行时间」: %q", schedDesc)
	}
	if !strings.Contains(full, "升级通知属于内部信息") {
		t.Fatalf("规则 14 应把升级通知限定为内部信息")
	}
	// 拼装顺序：身份与说话方式（默认人设）→ 平台规则 → 用户人设。
	iid := strings.Index(full, "你是 Fast Ship 内置机器人")
	is := strings.Index(full, "说话方式：")
	ir := strings.Index(full, "平台规则：")
	ip := strings.Index(full, "用户人设（追加，可以覆盖上面的默认语气）：")
	if iid < 0 || is < 0 || ir < 0 || ip < 0 || iid > is || is > ir || ir > ip {
		t.Fatalf("拼装顺序应为 默认人设→平台规则→用户人设: %d %d %d %d", iid, is, ir, ip)
	}
	// 无 persona 时不拼人设段，但默认人设保留。
	bare := buildBotSystemPrompt("   ", "t-nonce")
	if strings.Contains(bare, "用户人设（追加") {
		t.Fatalf("空白 persona 不应有人设段")
	}
	if !strings.Contains(bare, "说话方式：") {
		t.Fatalf("空白 persona 也应含默认人设段")
	}
}

// TestBotStyleUpgradeWatermarkColumn INT-133：风格升级的一次性压缩由持久水位
// bots.style_compaction_version 表达（不再挂在激活的瞬时返回值上）。新建
// 机器人水位直接落在当前说话方式版本；存量行水位为 0，激活推进平台版本
// 时不动水位——待办跨激活/重启保留。
func TestBotStyleUpgradeWatermarkColumn(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "styup9")
	bot := env.createBot(t, userID)
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if fresh.StyleCompactionVersion != botStylePromptVersion {
		t.Fatalf("新建机器人的风格压缩水位应为当前版本 %d，实得 %d",
			botStylePromptVersion, fresh.StyleCompactionVersion)
	}

	// 存量会话：水位 0 + 平台版本落后。激活只推进平台版本，不消费水位。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version":          botToolsetVersion,
		"platform_prompt_version":  botStylePromptVersion - 1,
		"style_compaction_version": 0,
	}).Error; err != nil {
		t.Fatalf("stale bot: %v", err)
	}
	stale, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if err := env.bots.activateBotPrompt(stale); err != nil {
		t.Fatalf("activate: %v", err)
	}
	after, _ := env.bots.bots.Get(bot.ID)
	if after.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("平台版本应推进到 %d，实得 %d", botPlatformPromptVersion, after.PlatformPromptVersion)
	}
	if after.StyleCompactionVersion != 0 {
		t.Fatalf("激活不得消费风格压缩水位: %d", after.StyleCompactionVersion)
	}
	if model.BotCompactionStyle != "style_upgrade" {
		t.Fatalf("风格升级压缩的 reason 应为 style_upgrade: %q", model.BotCompactionStyle)
	}
}

// TestBotPlatformPromptOnlyUpgrade 平台提示词单独升级的存量机器人：
// 工具集已是最新版本时，激活只重建提示词并恰好 +1 prompt_revision，工具集
// 与版本字节不动；激活按行幂等，重复调用不再递增。
func TestBotPlatformPromptOnlyUpgrade(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "ppup5")
	bot := env.createBot(t, userID)
	const oldPrompt = "旧平台提示：没有默认人设段落"
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

	revBefore := botRow.PromptRevision
	if err := env.bots.activateBotPrompt(botRow); err != nil {
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
	if !strings.Contains(fresh.SystemPromptText, "说话方式") || strings.Contains(fresh.SystemPromptText, oldPrompt) {
		t.Fatalf("系统提示词应重建为含默认人设的新文案")
	}

	// 激活幂等：版本已最新的第二次调用不写库、不再递增 revision。
	if err := env.bots.activateBotPrompt(fresh); err != nil {
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

// TestBotActivateRebuildsPromptFromLibraryRow 第 3 轮云端审查缺陷的回归：
// 调用方副本过期时，激活必须以事务内读到的库内行为准生成并持久化提示词，
// 而不是用副本上的人设/水位拼内容写库。场景：先持有一份人设 A 的旧副本，
// 再用 service 的 Update 保存人设 B（递增 revision），然后用旧副本走激活——
// 激活不得把 A 的提示词写回、不得让 revision 回退。断言对象是库内行，
// 不是副本。凭证/模型字段不属于激活语义，必须原样保留。
func TestBotActivateRebuildsPromptFromLibraryRow(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "librow")
	const personaA = "旧人设A_MARKER_DO_NOT_RESURRECT：回答保持简短。"
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "librow", PersonaPrompt: personaA,
		ProviderID: env.createProvider(t, userID, "sk-test-abcdef123456"), Model: "fake-model",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 库内行降级到 toolset v3 / platform v4：激活有活可干。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version":         3,
		"platform_prompt_version": 4,
	}).Error; err != nil {
		t.Fatalf("stale row: %v", err)
	}
	// 激活前加载的旧副本（人设 A、旧水位）。
	stale, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load stale copy: %v", err)
	}
	if stale.PersonaPrompt != personaA {
		t.Fatalf("旧副本应持人设 A: %q", stale.PersonaPrompt)
	}
	// 外部编辑：Update 写入人设 B 并递增 revision。
	const personaB = "新人设B_MARKER_MUST_WIN：回答带上示例。"
	updated, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{PersonaPrompt: strPtrBot(personaB)})
	if err != nil {
		t.Fatalf("update persona: %v", err)
	}
	revAfterUpdate := updated.PromptRevision

	// 用旧副本（人设 A）调激活：持久化内容必须来自事务内读到的 B 行。
	modelBefore, urlBefore := stale.Model, stale.BaseURL
	keyBefore := append([]byte(nil), stale.APIKeyEncrypted...)
	if err := env.bots.activateBotPrompt(stale); err != nil {
		t.Fatalf("activate: %v", err)
	}

	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load fresh: %v", err)
	}
	if fresh.PersonaPrompt != personaB {
		t.Fatalf("库内人设应保持 B: %q", fresh.PersonaPrompt)
	}
	if want := buildBotSystemPrompt(personaB, fresh.UntrustedNonce); fresh.SystemPromptText != want {
		t.Fatalf("库内系统提示词应按人设 B 重建，不得混入 A 的标记: got=%q", fresh.SystemPromptText)
	}
	if strings.Contains(fresh.SystemPromptText, "DO_NOT_RESURRECT") {
		t.Fatalf("旧副本的人设 A 不得复活进库内提示词")
	}
	if fresh.PromptRevision != revAfterUpdate+1 {
		t.Fatalf("prompt_revision 应为 Update 后 +1: want %d got %d", revAfterUpdate+1, fresh.PromptRevision)
	}
	if fresh.ToolsetVersion != botToolsetVersion || fresh.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("库内水位应升到最新: toolset=%d platform=%d", fresh.ToolsetVersion, fresh.PlatformPromptVersion)
	}
	// 副本五个字段与库内行一致，冻结出去的才是库内真实状态。
	if stale.PersonaPrompt != fresh.PersonaPrompt ||
		stale.SystemPromptText != fresh.SystemPromptText ||
		stale.PromptRevision != fresh.PromptRevision ||
		stale.PlatformPromptVersion != fresh.PlatformPromptVersion ||
		stale.ToolsetVersion != fresh.ToolsetVersion {
		t.Fatalf("激活后副本五字段应与库内行一致: %+v vs %+v", stale, fresh)
	}
	if stale.Model != modelBefore || stale.BaseURL != urlBefore || !bytes.Equal(stale.APIKeyEncrypted, keyBefore) {
		t.Fatalf("凭证/模型字段不属于激活语义，不得被改动")
	}
}

// TestBotUpdateRenameKeepsPromptState Update 事务化后的人设列保护：激活把
// 库内提示词推进到最新后，仅改名/换模型的保存不得把提示词四列
// （persona_prompt/system_prompt_text/prompt_revision/platform_prompt_version）
// 一并写回——读行-改-写在同一事务里按列跳过未变的提示词状态，反向撤销
// 并发激活的路径被关闭。
func TestBotUpdateRenameKeepsPromptState(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "renamerev")
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "before-rename", PersonaPrompt: "人设保持不变。",
		ProviderID: env.createProvider(t, userID, "sk-test-abcdef123456"), Model: "fake-model",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 降级后激活：库内提示词状态被推进（重建文案、+1 revision）。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"toolset_version":         3,
		"platform_prompt_version": 4,
	}).Error; err != nil {
		t.Fatalf("stale row: %v", err)
	}
	rowBefore, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load before: %v", err)
	}
	if err := env.bots.activateBotPrompt(rowBefore); err != nil {
		t.Fatalf("activate: %v", err)
	}
	activated, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load activated: %v", err)
	}
	if activated.ToolsetVersion != botToolsetVersion {
		t.Fatalf("前置：激活应把工具集推到最新, got %d", activated.ToolsetVersion)
	}

	// 仅改名保存一次。
	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{Name: strPtrBot("after-rename")}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	after, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load after: %v", err)
	}
	if after.Name != "after-rename" {
		t.Fatalf("改名应生效: %q", after.Name)
	}
	if after.SystemPromptText != activated.SystemPromptText ||
		after.PromptRevision != activated.PromptRevision ||
		after.PlatformPromptVersion != activated.PlatformPromptVersion ||
		after.PersonaPrompt != activated.PersonaPrompt {
		t.Fatalf("改名不得回写提示词状态: before(rev=%d plat=%d) after(rev=%d plat=%d)",
			activated.PromptRevision, activated.PlatformPromptVersion,
			after.PromptRevision, after.PlatformPromptVersion)
	}
	if after.ToolsetVersion != activated.ToolsetVersion {
		t.Fatalf("改名不得回写工具集水位: %d vs %d", after.ToolsetVersion, activated.ToolsetVersion)
	}

	// 换模型同样不碰提示词列。
	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{Model: strPtrBot("other-model")}); err != nil {
		t.Fatalf("change model: %v", err)
	}
	afterModel, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load after model: %v", err)
	}
	if afterModel.Model != "other-model" {
		t.Fatalf("换模型应生效: %q", afterModel.Model)
	}
	if afterModel.SystemPromptText != activated.SystemPromptText || afterModel.PromptRevision != activated.PromptRevision {
		t.Fatalf("换模型不得回写提示词状态: rev %d vs %d", afterModel.PromptRevision, activated.PromptRevision)
	}
}

// TestBotToolsetFrozenWithinRun 工具集纳入 run 冻结：主线 run 入口把库内
// toolset_version 钉进 freeze 后，外部（并发 schedule 激活）把库推到新版本，
// 本 run 构建上下文仍用冻结版本；只有压缩边界（acceptCompactionBoundary）
// 携带的新快照才允许换。sessionContextOf 不再活读 bots.toolset_version。
func TestBotToolsetFrozenWithinRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tsfreeze")
	bot := env.createBot(t, userID)
	// 入口时库内是 v3：入口激活按行把水位与副本同步到 v3 行的语义由
	// activateBotPrompt 覆盖；这里直接取 v3 行的副本模拟已钉死的 run。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Update("toolset_version", 3).Error; err != nil {
		t.Fatalf("downgrade: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if botRow.ToolsetVersion != 3 {
		t.Fatalf("前置：副本应钉在 v3, got %d", botRow.ToolsetVersion)
	}
	run := botFixtureRun(t, env, botRow)
	freeze := captureRunFreeze(botRow, run)
	applyRunCredentialFreeze(botRow, freeze)
	applyRunPromptFreeze(botRow, freeze)

	// 外部把库推到 v4（模拟并发 schedule 激活直接写库）。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("toolset_version", botToolsetVersion).Error; err != nil {
		t.Fatalf("external activation: %v", err)
	}
	// run 内每次迭代按 freeze 恢复副本，再构建：工具集必须仍是 3。
	applyRunPromptFreeze(botRow, freeze)
	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.ToolsetVersion != 3 {
		t.Fatalf("run 内工具集应冻结在 3，实得 %d", built.ToolsetVersion)
	}

	// 压缩边界：快照带 v4，接受后才换，且 freeze 一并推进。
	snap := &botBoundarySnapshot{
		Toolset:               botToolsetVersionOf(botToolsetVersion),
		ToolsetVersion:        botToolsetVersion,
		SystemPromptText:      botRow.SystemPromptText,
		PromptRevision:        botRow.PromptRevision,
		PlatformPromptVersion: botRow.PlatformPromptVersion,
		PersonaPrompt:         botRow.PersonaPrompt,
		Generation:            1,
		MemoryVersions:        map[string]int64{},
	}
	env.bots.acceptCompactionBoundary(botRow, &freeze, snap)
	if botRow.ToolsetVersion != botToolsetVersion || freeze.ToolsetVersion != botToolsetVersion {
		t.Fatalf("压缩边界后工具集应升级到 %d: bot=%d freeze=%d",
			botToolsetVersion, botRow.ToolsetVersion, freeze.ToolsetVersion)
	}
	builtAfter, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build after boundary: %v", err)
	}
	if builtAfter.ToolsetVersion != botToolsetVersion {
		t.Fatalf("压缩边界后构建应使用新工具集，实得 %d", builtAfter.ToolsetVersion)
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
		prefix := botPlatformPrefix(botProjectionNonce(botRow), "运行时上下文") + "\n"
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

// TestBotStyleUpgradeCompactsAtNextBoundary INT-133：风格压缩水位落后的存量
// 会话即使输入未达压缩阈值，也在下一个主线 run 边界强制压缩一次
// （reason=style_upgrade），水位推进后不再重复（一次性语义 d）。
func TestBotStyleUpgradeCompactsAtNextBoundary(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stybound9")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 60000
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"platform_prompt_version":  botStylePromptVersion - 1,
		"toolset_version":          botToolsetVersion,
		"style_compaction_version": 0,
	}).Error; err != nil {
		t.Fatalf("stale bot: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}

	// 历史体积落在 (压缩目标, 触发阈值) 之间：不达阈值不会自然触发压缩，
	// 但确实有值得收进摘要的长内容。
	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	probeRun := botFixtureRun(t, env, botRow)
	base := env.bots.estimateProjectionTokens(botRow, probeRun, nil)
	env.db.Where("id = ?", probeRun.ID).Delete(&model.BotRun{})
	window := env.bots.inputBudgetFor(botRow, "fake-model")
	trigger := botFloorRatio(window, env.cfg.Bots.Context.CompressionRatioV())
	goal := botFloorRatio(trigger, env.cfg.Bots.Context.TargetRatioV())
	bigTokens := goal + (trigger-goal)/2 - base
	if bigTokens < 1000 {
		t.Fatalf("几何异常：base=%d goal=%d trigger=%d", base, goal, trigger)
	}
	seedBotUserMessage(t, env, bot.ID, strings.Repeat("很长的历史内容。", int(bigTokens*7/48)+1))
	seedBotUserMessage(t, env, bot.ID, "保护轮A")
	seedBotUserMessage(t, env, bot.ID, "保护轮B")
	seedBotUserMessage(t, env, bot.ID, "保护轮C")
	markHistoryConsumed(t, env, botRow, nil)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "sty-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run 收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })

	var comp model.BotCompaction
	if err := env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&comp).Error; err != nil {
		t.Fatalf("升级后的边界应产生一次压缩: %v", err)
	}
	if comp.Reason != model.BotCompactionStyle {
		t.Fatalf("压缩 reason 应为 %q，实得 %q", model.BotCompactionStyle, comp.Reason)
	}
	if comp.Status == model.BotCompactionFailed {
		t.Fatalf("风格升级压缩不应失败: %s", comp.Status)
	}
	// 尝试后水位落库为当前版本（一次性语义的持久凭据）。
	var wm int64
	if err := env.db.Raw("SELECT style_compaction_version FROM bots WHERE id = ?", bot.ID).Scan(&wm).Error; err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if wm != botStylePromptVersion {
		t.Fatalf("压缩尝试后水位应推进到 %d，实得 %d", botStylePromptVersion, wm)
	}

	// 一次性：下一次 run 不再产生 style_upgrade 压缩。
	var before int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ?", bot.ID).Count(&before)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "再来一次", ClientMessageID: "sty-2",
	}); err != nil {
		t.Fatalf("send again: %v", err)
	}
	waitForBot(t, "第二轮收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })
	var after int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ? AND reason = ?", bot.ID, model.BotCompactionStyle).Count(&after)
	if after != 1 {
		t.Fatalf("风格升级压缩只应发生一次，实得 %d 次", after)
	}
}

// seedLegacyStyleBot 把机器人回退成 INT-133 升级前的存量会话：平台提示词版本
// 与风格压缩水位都落回旧值（水位 0 = 待压缩）。
func seedLegacyStyleBot(t *testing.T, env *botTestEnv, botID string) {
	t.Helper()
	if err := env.db.Model(&model.Bot{}).Where("id = ?", botID).Updates(map[string]interface{}{
		"platform_prompt_version":  botPlatformPromptVersion - 1,
		"toolset_version":          botToolsetVersion,
		"style_compaction_version": 0,
	}).Error; err != nil {
		t.Fatalf("legacy bot: %v", err)
	}
}

// styleCompactionCount 统计该机器人的 style_upgrade 压缩次数。
func styleCompactionCount(env *botTestEnv, botID string) int64 {
	var n int64
	env.db.Model(&model.BotCompaction{}).
		Where("bot_id = ? AND reason = ?", botID, model.BotCompactionStyle).Count(&n)
	return n
}

// TestBotStyleCompactionSurvivesScheduleRun INT-133(a)：存量会话先跑一次
// schedule run——它推进平台版本但完全忽略风格水位（不压缩、不推进水位）；
// 之后主线发消息仍发生恰好一次 style_upgrade 压缩。
func TestBotStyleCompactionSurvivesScheduleRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stysched")
	bot := env.createBot(t, userID)
	seedLegacyStyleBot(t, env, bot.ID)
	seedCompactableLedger(t, env, bot.ID, "最早的问题")
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	markHistoryConsumed(t, env, botRow, nil)

	// 先跑一次 schedule run：到期任务派发一个 schedule 会话的 run。
	due := time.Now().UTC().Add(-5 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NextRunAt = &due })
	env.bots.scheduleTick(time.Now().UTC())
	var runs []model.BotRun
	if err := env.db.Where("schedule_task_id = ?", task.ID).Find(&runs).Error; err != nil || len(runs) != 1 {
		t.Fatalf("应派发一个 schedule run: runs=%d err=%v", len(runs), err)
	}
	waitForScheduleRunFinished(t, env, runs[0].ID)

	// schedule run 内的激活推进了平台版本，但风格水位与压缩账本都没有被碰。
	mid, _ := env.bots.bots.Get(bot.ID)
	if mid.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("schedule 激活应推进平台版本到 %d，实得 %d", botPlatformPromptVersion, mid.PlatformPromptVersion)
	}
	if mid.StyleCompactionVersion != 0 {
		t.Fatalf("schedule run 不得消费风格压缩水位: %d", mid.StyleCompactionVersion)
	}
	var compN int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ?", bot.ID).Count(&compN)
	if compN != 0 {
		t.Fatalf("schedule run 不得产生压缩，实得 %d", compN)
	}

	// 主线发消息：水位落后 → 恰好一次 style_upgrade 压缩。注意库里已有
	// 一条 schedule run，botSettled 的「至少一个 run」前提天然满足——必须
	// 等主线 run 真的出现并收尾。
	waitMain := func() bool {
		var mainN int64
		env.db.Model(&model.BotRun{}).
			Where("bot_id = ? AND session_kind = ?", bot.ID, model.BotSessionKindMain).Count(&mainN)
		return mainN >= 1 && env.activeRunOrNothing(bot.ID) == nil
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "sty-sched-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线收尾", 30*time.Second, waitMain)
	if n := styleCompactionCount(env, bot.ID); n != 1 {
		t.Fatalf("schedule 先行后主线应发生一次风格压缩，实得 %d", n)
	}
	fresh, _ := env.bots.bots.Get(bot.ID)
	if fresh.StyleCompactionVersion != botStylePromptVersion {
		t.Fatalf("尝试后水位应推进到 %d，实得 %d", botStylePromptVersion, fresh.StyleCompactionVersion)
	}
}

// TestBotStyleCompactionSurvivesActivation INT-133(b)：激活推进版本后进程
// 重启（等价场景：激活早已消费过，水位仍挂在库里），下一条主线消息仍
// 触发一次风格压缩——待办不随激活的瞬时返回值丢失。
func TestBotStyleCompactionSurvivesActivation(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "styrst")
	bot := env.createBot(t, userID)
	seedLegacyStyleBot(t, env, bot.ID)
	seedCompactableLedger(t, env, bot.ID, "最早的问题")
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	// 模拟「升级后激活已发生、随后重启」：直接激活推进平台版本，
	// 水位留在 0（重启后瞬时 pending 无从谈起，持久水位仍在）。
	if err := env.bots.activateBotPrompt(botRow); err != nil {
		t.Fatalf("activate: %v", err)
	}
	mid, _ := env.bots.bots.Get(bot.ID)
	if mid.PlatformPromptVersion != botPlatformPromptVersion || mid.StyleCompactionVersion != 0 {
		t.Fatalf("激活后版本/水位异常: %+v", mid)
	}
	markHistoryConsumed(t, env, mid, nil)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "重启后的第一条", ClientMessageID: "sty-rst-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })
	if n := styleCompactionCount(env, bot.ID); n != 1 {
		t.Fatalf("激活后重启的主线仍应发生一次风格压缩，实得 %d", n)
	}
}

// TestBotStyleCompactionNewBotSkips INT-133(c)：新建机器人水位直接落在当前
// 版本，第一条主线消息不产生 style_upgrade 压缩（没有旧风格历史可收）。
func TestBotStyleCompactionNewBotSkips(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stynew")
	bot := env.createBot(t, userID)
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if fresh.StyleCompactionVersion != botStylePromptVersion {
		t.Fatalf("新建机器人水位应为 %d，实得 %d", botStylePromptVersion, fresh.StyleCompactionVersion)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "你好", ClientMessageID: "sty-new-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线收尾", 30*time.Second, func() bool { return env.botSettled(bot.ID) })
	var n int64
	env.db.Model(&model.BotCompaction{}).Where("bot_id = ?", bot.ID).Count(&n)
	if n != 0 {
		t.Fatalf("新建机器人不应产生任何压缩，实得 %d", n)
	}
}

// TestBotStyleCompactionSummarizesBelowGoal INT-133(4)：风格升级压缩在历史
// 低于压缩目标时也必须发出摘要请求——否则旧长回复原样留在投影里。断言
// 摘要请求发生、summary_through_seq 前进、旧 assistant 正文不再出现在
// 压缩后的请求投影里。
func TestBotStyleCompactionSummarizesBelowGoal(t *testing.T) {
	env := setupBotTestEnv(t, archiveAndSummaryTurns("（摘要：旧长回复已收编）")...)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "stybelow")
	bot := env.createBot(t, userID)

	// 可摘要段 = [最早的问题, 旧风格长回复]；保护窗为最近 3 轮 user+assistant。
	const marker = "OLDSTYLE_MARKER 这段旧回复非常啰嗦"
	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	seedBotAssistantText(t, env, bot.ID, marker+" "+strings.Repeat("展开解释很多内容。", 40))
	for i := 1; i <= 3; i++ {
		seedBotUserMessage(t, env, bot.ID, "保护轮")
		seedBotAssistantToolBlock(t, env, bot.ID, "call-p", "fs_issue", "小结果")
	}
	botRow, _ := env.bots.bots.Get(bot.ID)
	run := botFixtureRun(t, env, botRow)
	markHistoryConsumed(t, env, botRow, run)

	plan, err := env.bots.planCompaction(botRow, run, model.BotCompactionStyle)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Segment) == 0 {
		t.Fatalf("应有可摘要段")
	}
	if plan.BeforeTokens >= plan.GoalTokens {
		t.Fatalf("场景要求历史低于目标：before=%d goal=%d", plan.BeforeTokens, plan.GoalTokens)
	}
	if !plan.NeedSummary {
		t.Fatalf("风格升级压缩低于目标也应强制摘要")
	}
	outcome, err := env.bots.runCompactionCycle(context.Background(), botRow, run, model.BotCompactionStyle)
	if err != nil {
		t.Fatalf("style 压缩不应致命: %v", err)
	}
	if outcome == nil || !outcome.Published {
		t.Fatalf("应发布压缩投影: %+v", outcome)
	}
	var comp model.BotCompaction
	if err := env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&comp).Error; err != nil {
		t.Fatalf("compaction row: %v", err)
	}
	_, summaryN := countMaintByPurpose(env.db, comp.MaintenanceRunID)
	if summaryN < 1 {
		t.Fatalf("应发出摘要请求")
	}
	state, err := repository.NewBotContextRepository(env.db).Find(bot.ID)
	if err != nil || state.SummaryThroughSeq <= 0 {
		t.Fatalf("summary_through_seq 应前进: %+v err=%v", state, err)
	}
	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wire, _ := json.Marshal(built.Request.Messages)
	if strings.Contains(string(wire), "OLDSTYLE_MARKER") {
		t.Fatalf("旧 assistant 正文不应再出现在压缩后的请求投影里")
	}
}

// TestBotCreateProviderCheckedInSameTx INT-133(2)：provider 归属校验与 bot
// 插入在同一事务里执行。回归断言走「先删 provider 再 Create」的非事务
// 受控路径——若校验还游离在插入事务之外，这条用例会先放行校验再插入出
// 悬空 bot；校验收进事务后，删掉的 provider 必然查不到并返回
// ErrAIProviderNotFound。
func TestBotCreateProviderCheckedInSameTx(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "creatx")
	providerID := env.createProvider(t, userID, "sk-gone-0000")
	providerSvc := newProviderTestService(t, env)
	if err := providerSvc.Delete(userID, providerID); err != nil {
		t.Fatalf("delete provider: %v", err)
	}
	if _, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "dangling", ProviderID: providerID, Model: "fake-model",
	}); err != errs.ErrAIProviderNotFound {
		t.Fatalf("provider 已删除时创建应 40433，实得 %v", err)
	}
	var n int64
	env.db.Model(&model.Bot{}).Where("name = ?", "dangling").Count(&n)
	if n != 0 {
		t.Fatalf("失败创建不得留下 bot 行")
	}
}

// TestBotStyleWatermarkSurvivesThresholdCompaction INT-133（Codex P2）：阈值压缩
// 可能是 fold-only、旧长回复仍在投影里，因此它不得消费风格水位；只有真正走过
// style_upgrade 的那一轮才推进水位，后续低负载 run 仍能补上风格摘要。
func TestBotStyleWatermarkSurvivesThresholdCompaction(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stythresh9")
	bot := env.createBot(t, userID)
	env.cfg.Bots.Context.WindowTokens = 60000
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"platform_prompt_version":  botStylePromptVersion - 1,
		"toolset_version":          botToolsetVersion,
		"style_compaction_version": 0,
	}).Error; err != nil {
		t.Fatalf("stale bot: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}

	// 首轮历史落在压缩阈值之上：run 里必然先走 threshold 压缩。
	seedBotUserMessage(t, env, bot.ID, "最早的问题")
	probeRun := botFixtureRun(t, env, botRow)
	base := env.bots.estimateProjectionTokens(botRow, probeRun, nil)
	env.db.Where("id = ?", probeRun.ID).Delete(&model.BotRun{})
	window := env.bots.inputBudgetFor(botRow, "fake-model")
	trigger := botFloorRatio(window, env.cfg.Bots.Context.CompressionRatioV())
	bigTokens := trigger + (window-trigger)/2 - base
	if bigTokens < 1000 {
		t.Fatalf("几何异常：base=%d trigger=%d window=%d", base, trigger, window)
	}
	seedBotUserMessage(t, env, bot.ID, strings.Repeat("很长的历史内容。", int(bigTokens*7/48)+1))
	seedBotUserMessage(t, env, bot.ID, "保护轮A")
	seedBotUserMessage(t, env, bot.ID, "保护轮B")
	seedBotUserMessage(t, env, bot.ID, "保护轮C")
	markHistoryConsumed(t, env, botRow, nil)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "继续", ClientMessageID: "sty-th-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "阈值 run 收尾", 30*time.Second, func() bool {
		return completedMainRuns(t, env, bot.ID) >= 1 && env.botSettled(bot.ID)
	})

	var first model.BotCompaction
	if err := env.db.Where("bot_id = ?", bot.ID).Order("created_at DESC").First(&first).Error; err != nil {
		t.Fatalf("首轮应产生压缩记录: %v", err)
	}
	if first.Reason != model.BotCompactionThreshold {
		t.Fatalf("首轮压缩 reason 应为 %q，实得 %q", model.BotCompactionThreshold, first.Reason)
	}
	if wm := styleWatermarkOf(t, env, bot.ID); wm != 0 {
		t.Fatalf("阈值压缩不得消费风格水位，实得 %d", wm)
	}

	// 第二轮负载已降下来：风格水位仍欠账，必须补一次 style_upgrade。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "再来一次", ClientMessageID: "sty-th-2",
	}); err != nil {
		t.Fatalf("send again: %v", err)
	}
	// 后台 run 先创建再收尾：以「完成的 run 数 + 无活动 run」为界，避免抢跑。
	waitForBot(t, "第二轮收尾", 30*time.Second, func() bool {
		return completedMainRuns(t, env, bot.ID) >= 2 && env.botSettled(bot.ID)
	})
	var styleCount int64
	if err := env.db.Model(&model.BotCompaction{}).
		Where("bot_id = ? AND reason = ?", bot.ID, model.BotCompactionStyle).Count(&styleCount).Error; err != nil {
		t.Fatalf("count style compactions: %v", err)
	}
	if styleCount != 1 {
		t.Fatalf("阈值压缩后仍应补一次风格压缩，实得 %d 次", styleCount)
	}
	if wm := styleWatermarkOf(t, env, bot.ID); wm != botStylePromptVersion {
		t.Fatalf("风格压缩后水位应推进到 %d，实得 %d", botStylePromptVersion, wm)
	}
}

// completedMainRuns 统计该机器人已完成的主线 run 数（用于等待后台 run 收尾）。
func completedMainRuns(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotRun{}).
		Where("bot_id = ? AND session_kind = ? AND finished_at IS NOT NULL", botID, model.BotSessionKindMain).
		Count(&n).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

// styleWatermarkOf 读取库内的风格压缩水位。
func styleWatermarkOf(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var wm int64
	if err := env.db.Raw("SELECT style_compaction_version FROM bots WHERE id = ?", botID).Scan(&wm).Error; err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	return wm
}
