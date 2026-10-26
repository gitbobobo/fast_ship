package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/option"
	"github.com/openai/openai-go/v2/packages/param"
	"github.com/openai/openai-go/v2/packages/respjson"
	"github.com/openai/openai-go/v2/shared"
	"go.uber.org/zap"
)

// bot_openai.go 是官方 openai-go 的适配层：客户端构造、流式聚合、usage 提取
// 与连接探测。任意 base URL 保留（http/https、自托管、内网），不追加 /v1；
// SDK 重试显式关零——连接测试与被中断的请求不允许产生隐藏的重复调用。

const botProbeTimeout = 30 * time.Second

// botNewClient 按运行配置构造客户端；重试关零是显式决策。
func botNewClient(baseURL, apiKey string) openai.Client {
	return openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0),
	)
}

// botModelReply 是一次流式模型请求的聚合结果。
type botModelReply struct {
	Text      string
	ToolCalls []botModelToolCall
	// SawOutput 报告本流是否出现过任何正文或工具调用片段（含未拼齐的半截
	// 调用）：聚合处置位，不依赖 id/name 是否齐全。有过输出的失败不在重试
	// 与强制压缩重试之列——上游可能已计费，重放还可能重复触发副作用（INT-137）。
	SawOutput bool
	// ProviderRequestID 是响应里的请求身份；拿不到为空。
	ProviderRequestID string
	FinishReason      string
	// FirstDeltaAt 是首个非空正文片段的到达时刻（INT-115 指标口径：
	// GenerationMs 的起点）；纯工具调用或失败请求为 nil。
	FirstDeltaAt *time.Time
	// Usage 可空：缺失与零必须区分，不伪造统计。
	UsageComplete    bool
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CachedTokens     int64
	// ReasoningTokens 是 completion_tokens_details.reasoning_tokens（供应商
	// 可选返回）：未返回为 nil。reasoning ⊆ completion 已计费，这里只记账，
	// 不改计价公式。
	ReasoningTokens *int64
	// ProviderCost 是供应商 usage 返回的实际金额，只收置信 USD 的值
	// （解析见 botParseProviderCost）；币别不明或非用户实收的金额保留在
	// ProviderCostRaw（原始字段名与值的 JSON，只记录不展示，INT-164）。
	ProviderCost    *float64
	ProviderCostRaw *string
	// SynthesizedToolCallIDs 是聚合时为缺失 id 的流式工具调用合成的平台
	// id 个数（INT-139 R12）：部分兼容服务流式不给 id，配对键是我们账本
	// 里的 provider_tool_call_id，本地合成不影响协议配对。
	SynthesizedToolCallIDs int
	// DroppedToolCalls 是聚合时被丢弃的缺名工具调用个数：缺 name 无法合成
	// 语义，只能丢，但必须可观测（调用方据此记 warn）。
	DroppedToolCalls int
}

type botModelToolCall struct {
	ID        string
	Name      string
	Arguments string // 原始 JSON 字符串（跨 chunk 拼接）
}

// botStreamRequest 是 contextBuilder 的产物：消息数组 + 固定工具数组。
type botStreamRequest struct {
	Messages []openai.ChatCompletionMessageParamUnion
	Tools    []openai.ChatCompletionToolUnionParam
	Model    string
	// MaxCompletionTokens 是辅助请求的输出预算；0 表示不限制（主线不加人为上限）。
	MaxCompletionTokens int64
}

// botStreamTimeouts 是流式看门狗阈值：发出请求到首个片段、相邻片段之间的
// 空闲上限。由调用方从运行配置注入，适配层不读配置；非正阈值不启用对应
// 看门狗（配置层已保证正数，这里只作防御）。
type botStreamTimeouts struct {
	FirstChunk time.Duration
	Idle       time.Duration
}

// botStreamChat 发起流式请求并聚合：finish_reason 之后继续读取可能随后到达
// 的 usage chunk；文本增量经 onDelta 透出（由调用方合并批量落 SSE）。
// baseURL 是供应商身份来源（与 client 构造同值），用于识别 OpenRouter 语义
// 的实际金额字段；其余字段只看响应形状。
// 看门狗（INT-137）：首包与片段间空闲超时经派生 context 只杀本条流，以
// *botStreamTimeoutError 返回；父 ctx 的取消（用户停止）不改写成超时。
// 失败时同样返回已聚合的部分回复（正文与工具调用片段），供调用方判断
// 「本次尝试是否已有输出」。
func botStreamChat(ctx context.Context, client *openai.Client, baseURL string, req botStreamRequest, timeouts botStreamTimeouts, onDelta func(text string)) (*botModelReply, error) {
	params := openai.ChatCompletionNewParams{
		Model:    req.Model,
		Messages: req.Messages,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	}
	if req.MaxCompletionTokens > 0 {
		params.MaxCompletionTokens = param.NewOpt(req.MaxCompletionTokens)
	}
	if len(req.Tools) > 0 {
		params.Tools = req.Tools
		// 声明串行偏好，但不依赖兼容服务一定遵守；循环按安全点收编并行调用。
		params.ParallelToolCalls = param.NewOpt(false)
	}

	// 看门狗：独立派生 context，首个片段前按首包阈值计时，之后每次收到
	// 片段重置为空闲阈值；超时只取消本条流，父 ctx（用户停止）不受影响。
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	idle := timeouts.Idle
	if idle <= 0 {
		idle = timeouts.FirstChunk
	}
	var watchdog *time.Timer
	if timeouts.FirstChunk > 0 {
		watchdog = time.AfterFunc(timeouts.FirstChunk, cancelStream)
		defer watchdog.Stop()
	}

	stream := client.Chat.Completions.NewStreaming(streamCtx, params)
	defer stream.Close()

	reply := &botModelReply{}
	// 流式工具调用按 index 分片拼接。
	parts := map[int64]*botModelToolCall{}
	order := []int64{}
	firstChunkSeen := false

	for stream.Next() {
		if watchdog != nil {
			firstChunkSeen = true
			watchdog.Reset(idle)
		}
		chunk := stream.Current()
		if chunk.ID != "" {
			reply.ProviderRequestID = chunk.ID
		}
		if chunk.JSON.Usage.Valid() {
			reply.UsageComplete = true
			reply.PromptTokens = chunk.Usage.PromptTokens
			reply.CompletionTokens = chunk.Usage.CompletionTokens
			reply.TotalTokens = chunk.Usage.TotalTokens
			reply.CachedTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			// 推理 token 与供应商实际金额都是可选字段：SDK 未建模的部分在
			// ExtraFields 里按原始 JSON 解析；未返回保持 nil，缺失 ≠ 0。
			if d := chunk.Usage.CompletionTokensDetails; chunk.Usage.JSON.CompletionTokensDetails.Valid() &&
				d.JSON.ReasoningTokens.Valid() {
				v := d.ReasoningTokens
				reply.ReasoningTokens = &v
			}
			// 分多次回 usage 的上游只在其中一个 chunk 带金额：空解析不覆写，
			// 否则后到的空 chunk 会把已收到的实收抹掉（与 reasoning_tokens
			// 「有才写」同口径）。
			if cost, raw := botParseProviderCost(
				chunk.Usage.JSON.ExtraFields, botIsOpenRouterBase(baseURL)); cost != nil {
				reply.ProviderCost, reply.ProviderCostRaw = cost, raw
			} else if raw != nil && reply.ProviderCost == nil {
				reply.ProviderCostRaw = raw
			}
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				reply.FinishReason = choice.FinishReason
			}
			if text := choice.Delta.Content; text != "" {
				reply.SawOutput = true
				if reply.FirstDeltaAt == nil {
					// 只记首个非空正文片段：GenerationMs 衡量正文生成耗时。
					now := time.Now().UTC()
					reply.FirstDeltaAt = &now
				}
				reply.Text += text
				if onDelta != nil {
					onDelta(text)
				}
			}
			if len(choice.Delta.ToolCalls) > 0 {
				reply.SawOutput = true
			}
			for _, tc := range choice.Delta.ToolCalls {
				part, ok := parts[tc.Index]
				if !ok {
					part = &botModelToolCall{}
					parts[tc.Index] = part
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					part.ID = tc.ID
				}
				if tc.Function.Name != "" {
					part.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					part.Arguments += tc.Function.Arguments
				}
			}
		}
	}
	sErr := stream.Err()
	// 无论成败都先拼装已收到的工具调用片段：调用方按「本次尝试是否已有
	// 输出」决定能否重试，半截的工具调用必须可见。
	for _, idx := range order {
		part := parts[idx]
		// 缺 name 无法合成语义：丢弃。干净结束时计入缺名异常——断流残片
		// 的缺名是截断的后果而非上游协议畸形，不计入避免观测噪音
		// （INT-139 R12 / 云端复审）。
		if part.Name == "" {
			if sErr == nil {
				reply.DroppedToolCalls++
			}
			continue
		}
		// 缺 id 的合成平台 id：部分兼容服务流式不返回 id，静默丢弃会让
		// 调用丢失、本轮无结果。配对键 provider_tool_call_id 存在我们账本
		// 里、回放什么就按什么配对，本地合成不破坏协议闭合。
		if part.ID == "" {
			part.ID = fmt.Sprintf("call_%d_%s", idx, uuid.NewString())
			reply.SynthesizedToolCallIDs++
		}
		reply.ToolCalls = append(reply.ToolCalls, *part)
	}
	if err := sErr; err != nil {
		// 只有看门狗取消（父 ctx 仍存活且错误为取消）才改写为超时错误：
		// 用户停止 / 关停导致的取消保持原样，HTTP 与网络错误也原样透出。
		if watchdog != nil && ctx.Err() == nil && errors.Is(err, context.Canceled) {
			phase, took := "first_chunk", timeouts.FirstChunk
			if firstChunkSeen {
				phase, took = "idle", idle
			}
			return reply, &botStreamTimeoutError{Phase: phase, Timeout: took}
		}
		return reply, err
	}
	// 正常结束但协议没有完成信号（无 finish_reason 也无 usage）：上游在输出
	// 中途关闭了连接，SDK 把普通 HTTP EOF 当成正常结束。半截回复、尤其是
	// 半截摘要不能当成功提交（摘要会推进水位，丢掉未总结的内容），统一按
	// 断流失败处理；已输出片段时调用方不会重试（INT-137）。
	if reply.FinishReason == "" && !reply.UsageComplete {
		return reply, io.ErrUnexpectedEOF
	}
	return reply, nil
}

// ---------- usage 里的供应商实际金额 ----------

// botIsOpenRouterBase 按 base_url host 识别 OpenRouter（host 含 openrouter.ai，
// 按域名边界判定）：只有它的 usage.cost / cost_details.upstream_inference_cost
// 是官方语义的美元实收金额，别家同名字段币别不明。
func botIsOpenRouterBase(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}

// botParseProviderCost 从 usage 的非标准字段（ExtraFields）解析供应商实际
// 金额。返回两个通道：置信 USD 的值走 provider_cost（本列可安全求和）；
// 币别不明或非用户实收的金额走 provider_cost_raw（{"field","value"} 的
// JSON 记录，只记录不展示）。金额可能以数字或字符串到达，解析失败按缺失
// 处理；多字段并存按固定优先级取首个可解析值，同一响应解析结果稳定：
//  1. cost / total_cost —— 裸名不带币别声明，仅 OpenRouter 语义按 USD
//     展示，其余地址币别不明只进 raw；
//  2. 名字以 _usd 结尾且为总额形态的字段 —— 字段名自身声明币别，按
//     约定视为 USD；
//  3. cost_details.upstream_inference_cost —— OpenRouter 付给上游的金额
//     而非用户实收，币别虽是 USD 也只进 raw。
func botParseProviderCost(extra map[string]respjson.Field, openrouter bool) (*float64, *string) {
	// ExtraFields 里的字段没经过类型化解码，SDK 一律记为 invalid——Valid()
	// 恒 false（「无对应模型字段」与「值坏掉」同状态）；存在性按原始文本判，
	// 缺失是空串、显式 null 是 "null"。
	field := func(raw respjson.Field) (*float64, bool) {
		v := raw.Raw()
		if v == "" || v == "null" {
			return nil, false
		}
		return botParseCostText(v)
	}
	rawRecord := func(name string, raw respjson.Field) *string {
		b, err := json.Marshal(map[string]string{"field": name, "value": raw.Raw()})
		if err != nil {
			return nil
		}
		s := string(b)
		return &s
	}
	// rawNote 只记一条：多个非 USD 字段并存时按声明优先级留更早的那个。
	var rawNote *string
	if v, ok := field(extra["cost"]); ok {
		if openrouter {
			// 豁免审计：OpenRouter 的 cost 是唯一被文档锚定 USD 的字段，
			// 来源无歧义——raw 只为「哪个字段胜出」存在，这里答案恒为 cost。
			return v, nil
		}
		// 非 OpenRouter 的 cost 币别不明：记 raw 但继续按优先级找置信
		// USD 字段，不早退——同响应带 cost+*_usd 时 _usd 应胜出。
		rawNote = rawRecord("cost", extra["cost"])
	}
	var usd *float64
	usdField := ""
	if v, ok := field(extra["total_cost"]); ok {
		if openrouter {
			usd = v
			usdField = "total_cost"
		} else if rawNote == nil {
			// 与裸 cost 同一口径：字段名不带币别声明，非 OpenRouter 地址
			// 币别不明只记录不置信（积分/分/其他币种会静默错价）。
			rawNote = rawRecord("total_cost", extra["total_cost"])
		}
	}
	// *_usd 字段只置信「总额」形态的名字：分项（input_/output_）与单价
	// （price_per_）类 *_usd 不是用户实收总额，置信托付会系统性错价。
	// 总额候选按名字排序取首个可解析的：map 遍历无序，不稳定会让同一
	// 响应两次解析选出不同字段。
	if usd == nil {
		totals := make([]string, 0, len(extra))
		others := make([]string, 0, len(extra))
		for name := range extra {
			if !strings.HasSuffix(strings.ToLower(name), "_usd") {
				continue
			}
			if botUSDTotalField(name) {
				totals = append(totals, name)
			} else {
				others = append(others, name)
			}
		}
		sort.Strings(totals)
		for _, name := range totals {
			if v, ok := field(extra[name]); ok {
				usd = v
				usdField = name
				break
			}
		}
		// 分项/单价类 *_usd 有实值但不敢当总额：留一条 raw 证据备查。
		if usd == nil && rawNote == nil {
			sort.Strings(others)
			for _, name := range others {
				if _, ok := field(extra[name]); ok {
					rawNote = rawRecord(name, extra[name])
					break
				}
			}
		}
	}
	// 置信命中时把胜出字段名与原值写进 raw，事后可审计 provider_cost 出自
	// 哪个字段（歧义证据让位于实际采用的来源）。
	if usd != nil {
		rawNote = rawRecord(usdField, extra[usdField])
	}
	if rawNote == nil {
		if raw, ok := extra["cost_details"]; ok && raw.Raw() != "" && raw.Raw() != "null" {
			var details struct {
				UpstreamInferenceCost json.RawMessage `json:"upstream_inference_cost"`
			}
			if err := json.Unmarshal([]byte(raw.Raw()), &details); err == nil &&
				len(details.UpstreamInferenceCost) > 0 {
				if _, ok := botParseCostText(string(details.UpstreamInferenceCost)); ok {
					rawNote = rawRecord("cost_details.upstream_inference_cost", raw)
				}
			}
		}
	}
	return usd, rawNote
}

// botUSDTotalField 判定 *_usd 字段名是否声明「总额」：词根为裸金额词
// （cost/amount/charge/price/spend/fee/total/response_cost）或以 total_
// 开头的字段视作总额；带其他限定的 *_usd（input_/output_ 分项、
// price_per_ 单价、upstream 上游成本）不是用户实收总额。
func botUSDTotalField(name string) bool {
	stem := strings.TrimSuffix(strings.ToLower(name), "_usd")
	switch stem {
	case "cost", "amount", "charge", "price", "spend", "fee",
		"total", "response_cost":
		return true
	}
	return strings.HasPrefix(stem, "total_")
}

// botParseCostText 解析金额文本：裸数字（respjson 原始值）或 JSON 字符串
// （部分供应商以字符串回金额）都接受，去掉两侧引号后交给 ParseFloat。
func botParseCostText(s string) (*float64, bool) {
	f, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(s), `"`), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil, false
	}
	return &f, true
}

// ---------- 连接探测 ----------

// botProbeStreaming 最小流式探测：一条 user 消息，读到任一片段或正常结束即通过。
func botProbeStreaming(baseURL, modelName, apiKey string) (bool, string) {
	client := botNewClient(baseURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), botProbeTimeout)
	defer cancel()

	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:               modelName,
		Messages:            []openai.ChatCompletionMessageParamUnion{openai.UserMessage("ping")},
		MaxCompletionTokens: param.NewOpt(int64(16)),
	})
	defer stream.Close()
	sawChunk := false
	for stream.Next() {
		sawChunk = true
		break
	}
	if err := stream.Err(); err != nil {
		return false, botProbeErrText(err)
	}
	if !sawChunk {
		return false, "服务未返回任何流式片段"
	}
	return true, ""
}

// botProbeToolCall 工具调用兼容性探测：带一个极小工具发起流式请求，检查
// 服务接受 tools 字段并能正常结束（不强制模型必须调用该工具）。
func botProbeToolCall(baseURL, modelName, apiKey string) (bool, string) {
	client := botNewClient(baseURL, apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), botProbeTimeout)
	defer cancel()

	tools := []openai.ChatCompletionToolUnionParam{{
		OfFunction: &openai.ChatCompletionFunctionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        "fs_probe_echo",
				Description: param.NewOpt("连通性探测工具，返回 pong。"),
				Parameters: shared.FunctionParameters{
					"type":                 "object",
					"properties":           map[string]interface{}{},
					"additionalProperties": false,
				},
			},
		},
	}}
	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:               modelName,
		Messages:            []openai.ChatCompletionMessageParamUnion{openai.UserMessage("ping")},
		Tools:               tools,
		MaxCompletionTokens: param.NewOpt(int64(64)),
	})
	defer stream.Close()
	sawFinish := false
	for stream.Next() {
		chunk := stream.Current()
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				sawFinish = true
			}
		}
	}
	if err := stream.Err(); err != nil {
		return false, botProbeErrText(err)
	}
	if !sawFinish {
		return false, "服务未报告 finish_reason"
	}
	return true, ""
}

// botIsContextLimitError 判定上游「上下文长度超限」类错误：这是唯一允许
// 触发强制压缩后重试一次的失败；普通 400 参数错误与 429 限流不在此列。
// 判定靠结构化 status/code 与常见报错文案的组合——不依赖单一供应商文案。
func botIsContextLimitError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *openai.Error
	status := 0
	code := ""
	msg := err.Error()
	if errors.As(err, &apiErr) {
		status = apiErr.StatusCode
		code = apiErr.Code
	}
	if code == "context_length_exceeded" || code == "context_window_exceeded" {
		return true
	}
	// 仅 400/413 体量的文案匹配；429/500 永不视为上下文超限。
	if status != 0 && status != 400 && status != 413 {
		return false
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many requests") {
		return false
	}
	// 只匹配明确的超窗含义；「不支持 max_tokens」等普通 400 不得触发强制压缩。
	markers := []string{
		"context_length_exceeded",
		"context_window_exceeded",
		"maximum context length",
		"maximum context window",
		"context window is",
		"prompt is too long",
		"please reduce the length of the messages",
		"reduce the length of the messages",
		"this model's maximum context",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// warnToolCallAnomalies 上报流式聚合里的工具调用异常（INT-139 R12）：
// 为缺 id 的调用合成平台 id、丢弃缺 name 的调用。两者都必须可观测——
// 静默丢弃会让一次模型意图彻底消失、本轮无结果且无人知晓。
func (s *BotService) warnToolCallAnomalies(reply *botModelReply, fields ...zap.Field) {
	if reply == nil || (reply.SynthesizedToolCallIDs == 0 && reply.DroppedToolCalls == 0) {
		return
	}
	s.logger.Warn("流式工具调用存在异常片段",
		append(fields,
			zap.Int("synthesized_ids", reply.SynthesizedToolCallIDs),
			zap.Int("dropped_no_name", reply.DroppedToolCalls))...)
}

// botProbeErrText 截断上游错误文本（错误不透传密钥，也不整段透传响应体）。
func botProbeErrText(err error) string {
	msg := err.Error()
	// 同 botErrMessage：按字符截断，不劈多字节字符（INT-139 R7）。
	if rs := []rune(msg); len(rs) > 300 {
		msg = string(rs[:300])
	}
	return msg
}
