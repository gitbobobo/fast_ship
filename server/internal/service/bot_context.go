package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/packages/param"
)

// 上下文构建：逻辑分段顺序固定为
//
//	系统提示 → 记忆快照 → 工具定义 → 会话摘要 → 最近消息 → 本轮运行时上下文
//
// 但 Chat Completions 的工具定义实际在顶层 tools，不能伪造一条 tools 消息塞进
// messages。实现中工具数组固定（进程内一次生成、不按机器人权限删减排序），
// messages 数组按上述顺序拼装；当前时间、待处理输入等易变内容只出现在末尾
// 运行时上下文。哈希按「不可变前缀」与 tools 的实际字节分别计算。

// botMemorySnapshot 首期返回空快照（预留 MemorySnapshotProvider 接缝）。
func botMemorySnapshot(botID string) string {
	return ""
}

// botConversationSummary 首期返回空摘要与完整历史下界 0
// （预留 ConversationSummaryProvider 接缝；不做自动压缩/丢弃/改写）。
func botConversationSummary(botID string) (summary string, fromSeq int64) {
	return "", 0
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

const botHistoryFetchLimit = 100000

// buildRunContext 把主线账本投影为协议请求；保证 assistant 工具调用与 tool
// 回复块完整后，再插入后来到达的用户/平台事件输入；不修改原始账本顺序。
func (s *BotService) buildRunContext(bot *model.Bot, run *model.BotRun) (*botBuiltContext, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 32)

	// 1. 系统提示（bot.system_prompt_text，已拼好人设与平台规则）。
	messages = append(messages, openai.SystemMessage(bot.SystemPromptText))

	// 2. 记忆快照（首期为空）。
	if snap := botMemorySnapshot(bot.ID); snap != "" {
		messages = append(messages, openai.SystemMessage("[记忆快照]\n"+snap))
	}

	// 3. 工具定义在顶层 tools，由 botToolsetOf 提供；这里仅记哈希。

	// 4. 会话摘要（首期为空，历史下界 0 = 全量）。
	_, fromSeq := botConversationSummary(bot.ID)

	// 5. 历史投影：窗口取最近的 botHistoryFetchLimit 条。固定从 0 起读在
	// 账本超过上限后会永远停在第一页——新输入进不来、run 永远在空转重建
	// 同一页。窗口滑动后，更早的历史归摘要/记忆层（预留接缝）。
	maxSeq, err := s.messages.MaxSeq(bot.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	windowStart := fromSeq - 1
	if maxSeq > int64(botHistoryFetchLimit) {
		windowStart = maxSeq - int64(botHistoryFetchLimit)
		if windowStart < fromSeq-1 {
			windowStart = fromSeq - 1
		}
	}
	rows, err := s.messages.ListAfterSeq(bot.ID, windowStart, botHistoryFetchLimit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	// 窗口起点对齐协议块：滑窗起点可能落在一个 tool 回复块中间（assistant
	// 行在窗外），丢弃开头成串的孤儿 tool 回复——裸 tool 角色消息会被
	// OpenAI 兼容端拒绝。回复一定排在 assistant 之后，assistant 行在窗内
	// 时它的回复不会落在窗外。
	for len(rows) > 0 && rows[0].Kind == model.BotMessageKindToolReply {
		rows = rows[1:]
	}

	// 预取调用状态：assistant 消息里的工具调用列表带平台状态说明。
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
				return nil, errs.ErrInternal
			}
			pending = appendBotPending(pending, row)
			if len(openToolCalls) > 0 {
				deferred = append(deferred, openai.UserMessage(content.Text))
				continue
			}
			flushAssistant()
			messages = append(messages, openai.UserMessage(content.Text))
		case model.BotMessageKindAssistant:
			flushAssistant()
			flushDeferred()
			var content botAssistantContent
			if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
				return nil, errs.ErrInternal
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
				return nil, errs.ErrInternal
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
				return nil, errs.ErrInternal
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
				return nil, errs.ErrInternal
			}
			raw, _ := json.Marshal(content)
			msg := openai.UserMessage("[平台事件] 以下是平台生命周期事件（非用户发言）：\n" + string(raw))
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
		return nil, errs.New(errs.ErrInternal.Code,
			"上下文构建失败：存在未闭合的工具调用块，需恢复补齐协议回复后重试")
	}

	// 6. 本轮运行时上下文（末尾；当前时间、待处理输入等易变内容只在这里）。
	rt := botRuntimeContext{
		Note:           "以上为完整上下文。pending_inputs 是你尚未处理的新输入（按 seq 升序），请决定继续、调整或停止。",
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
	rtRaw, _ := json.Marshal(rt)
	messages = append(messages, openai.UserMessage("[运行时上下文]\n"+string(rtRaw)))

	ts := botToolsetOf()
	tokenEstimate, estimator := botToolsTokenEstimate()

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
	}

	// 不可变前缀 = messages 去掉最后一条运行时上下文。
	prefixRaw, err := json.Marshal(messages[:len(messages)-1])
	if err != nil {
		return nil, errs.ErrInternal
	}
	sum := sha256.Sum256(prefixRaw)
	ctx.PrefixHash = hex.EncodeToString(sum[:])

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
