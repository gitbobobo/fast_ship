package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/packages/param"
	"gorm.io/gorm"
)

// 上下文构建：逻辑分段顺序固定为
//
//	系统提示 → 记忆快照 → 工具定义 → 会话摘要 → 最近消息 → 本轮运行时上下文
//
// 但 Chat Completions 的工具定义实际在顶层 tools，不能伪造一条 tools 消息塞进
// messages。实现中 messages 数组按上述顺序拼装；当前时间、待处理输入等易变
// 内容只出现在末尾运行时上下文。哈希按「不可变前缀」与 tools 的实际字节分别
// 计算。压缩（INT-102）只改持久投影状态：摘要替换更早历史、折叠映射替换旧的
// 大工具结果——原始消息账本永不删改，普通请求的前缀逐字节只追加。

// botSessionContext 是一次构建用到的冻结会话上下文：工具集版本在机器人
// toolset_version 列上冻结（压缩/重启边界才升级），记忆注入快照按投影
// generation 冻结（压缩发布或进程重启才刷新，期间记忆写入立即可用工具读到
// 但不改变已注入前缀）。
type botSessionContext struct {
	Toolset *botToolsetHolder
	// MemoryInjection 是冻结的常驻记忆注入正文（含标题行；空为不注入）。
	MemoryInjection string
	MemoryVersions  map[string]int64
	// State 是构建时读到的持久投影状态。
	State *model.BotContextState
	// Folded 是消息 ID → 折叠引用正文的映射。
	Folded map[string]string
}

// botFrozenMemory 是缓存在 runtime 的冻结记忆注入。
type botFrozenMemory struct {
	Generation int64
	Text       string
	Versions   map[string]int64
}

// sessionContextOf 取该 bot 当前应使用的冻结会话上下文：读持久投影状态，
// 记忆注入按 generation 缓存冻结。工具集版本读 bots.toolset_version 活值
// （压缩发布会推进它，内存里的 bot 行可能滞后）。
func (s *BotService) sessionContextOf(botID string, toolsetVersion int64) (*botSessionContext, error) {
	state, err := repository.NewBotContextRepository(s.db).Find(botID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			state = &model.BotContextState{BotID: botID}
		} else {
			return nil, errs.ErrInternal
		}
	}
	var liveVersion int64
	if err := s.db.Raw(`SELECT toolset_version FROM bots WHERE id = ?`, botID).
		Scan(&liveVersion).Error; err == nil && liveVersion > 0 {
		toolsetVersion = liveVersion
	}
	folded := map[string]string{}
	if state.FoldedResultsJSON != "" {
		if err := json.Unmarshal([]byte(state.FoldedResultsJSON), &folded); err != nil {
			folded = map[string]string{}
		}
	}
	snap := s.frozenMemorySnapshot(botID, state.Generation)
	return &botSessionContext{
		Toolset:         botToolsetVersionOf(toolsetVersion),
		MemoryInjection: snap.Text,
		MemoryVersions:  snap.Versions,
		State:           state,
		Folded:          folded,
	}, nil
}

// frozenMemorySnapshot 按投影 generation 取冻结记忆注入：generation 变化
// （压缩发布）或进程重启（缓存为空）时重算，同一 generation 内即使记忆被
// 工具改写也保持注入字节不变（前缀稳定）。
func (s *BotService) frozenMemorySnapshot(botID string, generation int64) *botFrozenMemory {
	s.rt.memMu.Lock()
	cached, ok := s.rt.memSnaps[botID]
	s.rt.memMu.Unlock()
	if ok && cached.Generation == generation {
		return cached
	}
	var text string
	var versions map[string]int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		text, versions, txErr = s.botMemoryInjectionTx(tx, botID)
		return txErr
	})
	if err != nil {
		text, versions = "", map[string]int64{}
	}
	snap := &botFrozenMemory{Generation: generation, Text: text, Versions: versions}
	s.rt.memMu.Lock()
	s.rt.memSnaps[botID] = snap
	s.rt.memMu.Unlock()
	return snap
}

// botBuiltContext 是一次模型请求的构建产物。
type botBuiltContext struct {
	Request botStreamRequest
	// InputThroughSeq 是本次请求消费到的消息水位。
	InputThroughSeq     int64
	PrefixHash          string
	ToolsHash           string
	RequestHash         string
	ToolsSchemaBytes    int64
	ToolsTokensEstimate int64
	TokenEstimator      string
	// ContextGeneration 是构建时读到的投影状态代数；InputTokensEstimate
	// 是发送前对整包输入的估算（缺 usage 时的压缩触发依据）。
	ContextGeneration   int64
	InputTokensEstimate int64
	InputEstimator      string
	// ToolsetVersion 是本请求实际使用的冻结工具集版本。
	ToolsetVersion int64
	// SummaryMessageIdx 是会话摘要消息在 Messages 中的下标（-1 为无摘要）。
	SummaryMessageIdx int
	// prefixRaw / runtimeRaw 是实际协议投影字节，供 usage 增量基线比较。
	prefixRaw  []byte
	runtimeRaw []byte
}

// botSummaryMessageText 是摘要投影消息的固定拼装；正文清空即 F 口径的
// 同一条消息（标识符附录保留）。
func botSummaryMessageText(summary, manifest string) string {
	s := "[会话摘要] 以下是对更早历史的压缩摘要；标识符附录是确定性生成的可查引用。\n" + summary
	if manifest != "" {
		s += "\n[标识符附录]\n" + manifest
	}
	return s
}

// botContextOverlay 在估算「折叠/摘要后的最终投影」时覆盖持久状态，不写库。
type botContextOverlay struct {
	SummaryText            string
	SummaryThroughSeq      int64
	IdentifierManifestJSON string
	Folded                 map[string]string
	MemoryInjection        *string
	Toolset                *botToolsetHolder
	SystemPromptText       *string
}

// botUsageBaseline 是进程内保存的最近一次成功主线请求投影基线。
type botUsageBaseline struct {
	PrefixJSON        []byte
	RuntimeJSON       []byte
	PromptTokens      int64
	InputThroughSeq   int64
	PromptRevision    int64
	ToolsetVersion    int64
	ContextGeneration int64
	ToolsHash         string
	Model             string
}

// botRuntimeContext 是末尾运行时上下文（易变内容只出现在这里）。
type botRuntimeContext struct {
	Note           string            `json:"note"`
	CurrentTimeUTC string            `json:"current_time_utc"`
	RunID          string            `json:"run_id"`
	PendingInputs  []botPendingInput `json:"pending_inputs,omitempty"`
}

type botPendingInput struct {
	Seq  int64  `json:"seq"`
	Kind string `json:"kind"`
}

const botHistoryPageSize = 256

// botTokensEstimatorBytes35 是按字节估算的固定标识：任何兼容模型没有统一
// tokenizer，缺失 usage 时统一按 UTF-8 字节 / 3.5 估算。
const botTokensEstimatorBytes35 = "bytes/3.5"

// botEstimateTokens 粗估文本的 token 数（UTF-8 字节 / 3.5，上取整）。
func botEstimateTokens(s string) int64 {
	return botEstimateBytes(int64(len(s)))
}

// botEstimateBytes 按字节数估算 token（bytes/3.5，上取整）。
func botEstimateBytes(n int64) int64 {
	return (n*2 + 6) / 7
}

// buildRunContext 把主线账本按持久投影状态投影为协议请求：摘要在历史之前
// 注入，折叠映射替换旧的大工具结果；保证 assistant 工具调用与 tool 回复块
// 完整后，再插入后来到达的用户/平台事件输入；不修改原始账本顺序。
func (s *BotService) buildRunContext(bot *model.Bot, run *model.BotRun) (*botBuiltContext, error) {
	return s.buildRunContextWith(bot, run, nil)
}

func (s *BotService) buildRunContextWith(bot *model.Bot, run *model.BotRun, over *botContextOverlay) (*botBuiltContext, error) {
	sess, err := s.sessionContextOf(bot.ID, bot.ToolsetVersion)
	if err != nil {
		return nil, err
	}
	if over != nil {
		sess.State.SummaryText = over.SummaryText
		sess.State.SummaryThroughSeq = over.SummaryThroughSeq
		sess.State.IdentifierManifestJSON = over.IdentifierManifestJSON
		if over.Folded != nil {
			sess.Folded = over.Folded
		}
		if over.MemoryInjection != nil {
			sess.MemoryInjection = *over.MemoryInjection
		}
		if over.Toolset != nil {
			sess.Toolset = over.Toolset
		}
	}
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 32)
	// builtSummaryIdx 记录摘要消息的下标（-1 为无摘要）：压缩验收在同一个
	// 物化投影上仅清空该条消息正文复算 F 口径，不重建投影。
	builtSummaryIdx := -1

	// 1. 系统提示（bot.system_prompt_text，已拼好人设与平台规则）。
	sysPrompt := bot.SystemPromptText
	if over != nil && over.SystemPromptText != nil {
		sysPrompt = *over.SystemPromptText
	}
	messages = append(messages, openai.SystemMessage(sysPrompt))

	// 2. 记忆快照（按投影 generation 冻结的常驻文件注入）。
	if sess.MemoryInjection != "" {
		messages = append(messages, openai.SystemMessage("[记忆快照]\n"+sess.MemoryInjection))
	}

	// 3. 工具定义在顶层 tools，由冻结工具集提供；这里仅记哈希。

	// 4. 会话摘要与标识符附录（摘要水位 = state.SummaryThroughSeq）。
	fromSeq := sess.State.SummaryThroughSeq
	if fromSeq > 0 {
		messages = append(messages, openai.SystemMessage(
			botSummaryMessageText(sess.State.SummaryText, sess.State.IdentifierManifestJSON)))
		builtSummaryIdx = len(messages) - 1
	}

	// 5. 历史投影：从摘要水位分页读到账本末尾，不把未摘要内容当已覆盖。
	rawRows, err := s.listMessagesThrough(bot.ID, model.BotSessionKindMain, "", fromSeq, 0)
	if err != nil {
		return nil, errs.ErrInternal
	}
	projected, rows, _, err := botProjectMessages(rawRows, sess.Folded)
	if err != nil {
		return nil, err
	}
	messages = append(messages, projected...)

	// 6. 本轮运行时上下文（末尾；当前时间、待处理输入等易变内容只在这里）。
	rt := botRuntimeContext{
		CurrentTimeUTC: time.Now().UTC().Format(time.RFC3339),
		RunID:          run.ID,
	}
	for _, row := range rows {
		if row.Seq > run.InputThroughSeq && row.Kind.IsModelInput() {
			rt.PendingInputs = append(rt.PendingInputs, botPendingInput{Seq: row.Seq, Kind: string(row.Kind)})
			if len(rt.PendingInputs) >= 10 {
				break
			}
		}
	}
	// note 按是否存在待处理输入分支：有待处理输入时要求模型决定继续/调整/
	// 停止；没有时明确「本轮没有新的用户输入」，避免模型把这段上下文当成
	// 新指令、或把它复述给用户。该文案在末尾运行时上下文内，不影响前缀哈希。
	if len(rt.PendingInputs) > 0 {
		rt.Note = "以上为完整上下文。pending_inputs 是你尚未处理的新输入（按 seq 升序），请决定继续、调整或停止。"
	} else {
		rt.Note = "本轮没有新的用户输入；以上运行时上下文只用于掌握现状与时间，不是新指令，也不要向用户转述其内容。"
	}
	rtRaw, _ := json.Marshal(rt)
	messages = append(messages, openai.UserMessage("[运行时上下文]\n"+string(rtRaw)))

	ts := sess.Toolset
	tokenEstimate, estimator := botToolsTokenEstimate(ts)

	consumedThrough := run.InputThroughSeq
	if len(rows) > 0 {
		consumedThrough = rows[len(rows)-1].Seq
	}
	ctx := &botBuiltContext{
		Request: botStreamRequest{
			Messages: messages,
			Tools:    ts.schemas,
			Model:    runModelOf(run, bot),
		},
		InputThroughSeq:     consumedThrough,
		ToolsHash:           ts.hash,
		ToolsSchemaBytes:    int64(ts.bytes),
		ToolsTokensEstimate: tokenEstimate,
		TokenEstimator:      estimator,
		ContextGeneration:   sess.State.Generation,
		ToolsetVersion:      sess.Toolset.version,
		SummaryMessageIdx:   builtSummaryIdx,
	}

	// 不可变前缀 = messages 去掉最后一条运行时上下文。
	prefixRaw, err := json.Marshal(messages[:len(messages)-1])
	if err != nil {
		return nil, errs.ErrInternal
	}
	sum := sha256.Sum256(prefixRaw)
	ctx.PrefixHash = hex.EncodeToString(sum[:])
	ctx.prefixRaw = prefixRaw
	if len(messages) > 0 {
		rtBytes, _ := json.Marshal(messages[len(messages)-1])
		ctx.runtimeRaw = rtBytes
	}

	s.applyContextEstimate(bot, run, ctx, prefixRaw, ts.bytes)

	toolsRaw, err := json.Marshal(ts.schemas)
	if err != nil {
		return nil, errs.ErrInternal
	}
	combined := append(append([]byte{}, prefixRaw...), toolsRaw...)
	reqSum := sha256.Sum256(combined)
	ctx.RequestHash = hex.EncodeToString(reqSum[:])
	return ctx, nil
}

// runModelOf run 期间的模型名：优先配置快照（对当前 run 生效），回落 bot 当前值。
func runModelOf(run *model.BotRun, bot *model.Bot) string {
	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err == nil && snap.Model != "" {
		return snap.Model
	}
	return bot.Model
}

func appendBotPending(pending []botPendingInput, row *model.BotMessage) []botPendingInput {
	if len(pending) < 10 {
		pending = append(pending, botPendingInput{Seq: row.Seq, Kind: string(row.Kind)})
	}
	return pending
}

// botAssistantUnion 把账本里的 assistant 内容投回协议消息（含工具调用数组）。
func botAssistantUnion(content *botAssistantContent) *openai.ChatCompletionMessageParamUnion {
	text := content.Text
	union := &openai.ChatCompletionMessageParamUnion{
		OfAssistant: &openai.ChatCompletionAssistantMessageParam{
			Content: openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: param.NewOpt(text),
			},
		},
	}
	if len(content.ToolCalls) > 0 {
		calls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(content.ToolCalls))
		for _, tc := range content.ToolCalls {
			calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: tc.ProviderToolCallID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      tc.ToolName,
						Arguments: tc.Arguments,
					},
				},
			})
		}
		union.OfAssistant.ToolCalls = calls
	}
	return union
}

// botFoldedReference 是折叠后替代原大结果的一行引用。
func botFoldedReference(callID string, items, chars int64) string {
	raw, _ := json.Marshal(map[string]interface{}{
		"folded":      true,
		"call_id":     callID,
		"items":       items,
		"total_chars": chars,
		"read":        "fs_result action=read call_id=" + callID + " offset=0",
	})
	return string(raw)
}

// botProjectMessages 把账本行投影为协议消息：折叠映射替换大结果、assistant
// 工具调用与 tool 回复块保持连续、未闭合块拒绝发请求。只依赖入参，主线与
// 定时任务（INT-103）构建共用同一段投影，保证输出形状一致。返回投影后的
// 消息数组、（裁剪孤儿回复后的）原始行与待处理输入列表。
func botProjectMessages(rows []model.BotMessage, folded map[string]string) (
	[]openai.ChatCompletionMessageParamUnion, []model.BotMessage, []botPendingInput, error,
) {
	// 窗口起点对齐协议块：滑窗起点可能落在一个 tool 回复块中间（assistant
	// 行在窗外），丢弃开头成串的孤儿 tool 回复——裸 tool 角色消息会被
	// OpenAI 兼容端拒绝。回复一定排在 assistant 之后，assistant 行在窗内
	// 时它的回复不会落在窗外。
	for len(rows) > 0 && rows[0].Kind == model.BotMessageKindToolReply {
		rows = rows[1:]
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(rows)+4)
	pending := make([]botPendingInput, 0, 4)
	var openAssistant *openai.ChatCompletionMessageParamUnion
	// openToolCalls 是尚未见到协议回复的 provider_tool_call_id 集合：块未闭合
	// 前到达的用户/事件输入延后插入，保证 assistant 工具调用与 tool 回复块
	// 在协议里完整。
	openToolCalls := map[string]bool{}
	deferred := []openai.ChatCompletionMessageParamUnion{}
	flushAssistant := func() {
		if openAssistant != nil {
			messages = append(messages, *openAssistant)
			openAssistant = nil
		}
	}
	flushDeferred := func() {
		messages = append(messages, deferred...)
		deferred = nil
	}

	for i := range rows {
		row := &rows[i]
		switch row.Kind {
		case model.BotMessageKindUser:
			var content botUserContent
			if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
				return nil, nil, nil, errs.ErrInternal
			}
			pending = appendBotPending(pending, row)
			msg := openai.UserMessage(content.Text)
			if len(openToolCalls) > 0 {
				deferred = append(deferred, msg)
				continue
			}
			flushAssistant()
			messages = append(messages, msg)
		case model.BotMessageKindAssistant:
			flushAssistant()
			flushDeferred()
			var content botAssistantContent
			if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
				return nil, nil, nil, errs.ErrInternal
			}
			union := botAssistantUnion(&content)
			if len(content.ToolCalls) > 0 {
				openAssistant = union
				for _, tc := range content.ToolCalls {
					openToolCalls[tc.ProviderToolCallID] = true
				}
			} else {
				messages = append(messages, *union)
			}
		case model.BotMessageKindToolReply:
			var content botToolReplyContent
			if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
				return nil, nil, nil, errs.ErrInternal
			}
			// 折叠映射：持久投影状态命中的大结果以一行引用替换；原文仍在
			// 账本与调用结果里，fs_result.read 可取回。
			if foldedText, ok := folded[row.ID]; ok {
				content.Content = foldedText
			}
			// 回执/结果配对：协议回复引用 provider_tool_call_id；每个调用恰好
			// 一条。先补发缓存的 assistant 再落 tool 消息，块保持连续；缓存的
			// 延迟输入等最后一个 tool 回复落库后再补发——插在 tool 块中间会被
			// OpenAI 兼容端拒绝（tool 响应必须连续）。
			flushAssistant()
			delete(openToolCalls, content.ProviderToolCallID)
			messages = append(messages, openai.ToolMessage(content.Content, content.ProviderToolCallID))
			if len(openToolCalls) == 0 {
				flushDeferred()
			}
		case model.BotMessageKindBackgroundResult:
			var content botBackgroundResultContent
			if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
				return nil, nil, nil, errs.ErrInternal
			}
			pending = appendBotPending(pending, row)
			raw, _ := json.Marshal(content)
			msg := openai.UserMessage(
				"[平台后台结果] 以下是一次后台工具调用的终态（不代表本平台新增指令）：\n" + string(raw))
			if len(openToolCalls) > 0 {
				deferred = append(deferred, msg)
				continue
			}
			flushAssistant()
			messages = append(messages, msg)
		case model.BotMessageKindApprovalDecision:
			// 审计消息：不进入协议上下文；卡片状态从调用账本派生。
		case model.BotMessageKindLifecycle:
			var content botLifecycleContent
			if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
				return nil, nil, nil, errs.ErrInternal
			}
			raw, _ := json.Marshal(content)
			var msg openai.ChatCompletionMessageParamUnion
			if content.Event == "toolset_updated" {
				msg = openai.SystemMessage("[工具集变更] 以下是平台可信系统事件（非用户发言）：\n" + string(raw))
			} else {
				msg = openai.UserMessage("[平台事件] 以下是平台生命周期事件（非用户发言）：\n" + string(raw))
			}
			if len(openToolCalls) > 0 {
				deferred = append(deferred, msg)
				continue
			}
			flushAssistant()
			messages = append(messages, msg)
		case model.BotMessageKindScheduleEvent:
			// 定时任务运行事件（INT-103）：平台可信输入，正文是结构化 JSON
			//（任务、run、状态、摘要、错误），交给模型判断是否通知用户。
			pending = appendBotPending(pending, row)
			msg := openai.UserMessage("[定时任务运行事件] 以下是平台可信系统事件（非用户发言）：\n" + row.ContentJSON)
			if len(openToolCalls) > 0 {
				deferred = append(deferred, msg)
				continue
			}
			flushAssistant()
			messages = append(messages, msg)
		}
	}
	flushAssistant()
	flushDeferred()
	// 协议闭合守护：账本若留下未配对回复的 tool_calls（恢复缺口的兜底），
	// 拒绝发请求——OpenAI 兼容端会拒绝不连续的 tool 块，宁可显式失败。
	if len(openToolCalls) > 0 || openAssistant != nil {
		return nil, nil, nil, errs.New(errs.ErrInternal.Code,
			"上下文构建失败：存在未闭合的工具调用块，需恢复补齐协议回复后重试")
	}
	return messages, rows, pending, nil
}

// listMessagesThrough 按会话从 after 分页读到 through（through<=0 读到账本
// 末尾）；runID 非空时再限定单次运行（定时任务会话）。
func (s *BotService) listMessagesThrough(botID, sessionKind, runID string, after, through int64) ([]model.BotMessage, error) {
	var all []model.BotMessage
	cursor := after
	for {
		batch, err := s.messages.ListSeqRange(botID, sessionKind, runID, cursor, through, botHistoryPageSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		cursor = batch[len(batch)-1].Seq
		if through > 0 && cursor >= through {
			break
		}
		if len(batch) < botHistoryPageSize {
			break
		}
	}
	return all, nil
}

// utf8Chars 返回 Unicode 字符数（折叠阈值按字符计，避免按字节截断）。
func utf8Chars(s string) int64 {
	return int64(utf8.RuneCountInString(s))
}

// estimateProjectionTokens 用实际请求投影（messages+tools 序列化字节/3.5）估算体积。
func (s *BotService) estimateProjectionTokens(bot *model.Bot, run *model.BotRun, over *botContextOverlay) int64 {
	if bot == nil || run == nil {
		return 0
	}
	built, err := s.buildRunContextWith(bot, run, over)
	if err != nil {
		return 0
	}
	msgRaw, _ := json.Marshal(built.Request.Messages)
	return botEstimateBytes(int64(len(msgRaw) + int(built.ToolsSchemaBytes)))
}

func (s *BotService) rememberUsageBaseline(bot *model.Bot, run *model.BotRun, built *botBuiltContext, reply *botModelReply) {
	if bot == nil || built == nil {
		return
	}
	if reply == nil || !reply.UsageComplete {
		s.clearUsageBaseline(bot.ID)
		return
	}
	s.rt.usageMu.Lock()
	s.rt.usageBase[bot.ID] = &botUsageBaseline{
		PrefixJSON:        append([]byte(nil), built.prefixRaw...),
		RuntimeJSON:       append([]byte(nil), built.runtimeRaw...),
		PromptTokens:      reply.PromptTokens,
		InputThroughSeq:   built.InputThroughSeq,
		PromptRevision:    bot.PromptRevision,
		ToolsetVersion:    built.ToolsetVersion,
		ContextGeneration: built.ContextGeneration,
		ToolsHash:         built.ToolsHash,
		Model:             runModelOf(run, bot),
	}
	s.rt.usageMu.Unlock()
}

func (s *BotService) clearUsageBaseline(botID string) {
	s.rt.usageMu.Lock()
	delete(s.rt.usageBase, botID)
	s.rt.usageMu.Unlock()
}

func (s *BotService) usageBaselineOf(botID string) *botUsageBaseline {
	s.rt.usageMu.Lock()
	defer s.rt.usageMu.Unlock()
	return s.rt.usageBase[botID]
}

// protocolPrefixAddedBytes 核对 curr 协议消息数组是 prev 的追加，返回新增消息序列化字节。
func protocolPrefixAddedBytes(prev, curr []byte) (int64, bool) {
	var prevMsgs, currMsgs []json.RawMessage
	if json.Unmarshal(prev, &prevMsgs) != nil || json.Unmarshal(curr, &currMsgs) != nil {
		return 0, false
	}
	if len(currMsgs) < len(prevMsgs) {
		return 0, false
	}
	for i := range prevMsgs {
		if !bytes.Equal(prevMsgs[i], currMsgs[i]) {
			return 0, false
		}
	}
	return int64(len(curr) - len(prev)), true
}
