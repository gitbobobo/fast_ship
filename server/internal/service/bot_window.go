package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 模型上下文窗口能力（INT-115 需求一）：按「API 地址 + 模型名」自动探测并
// 缓存模型元数据，解析顺序 manual（部署覆盖）> detected（/models 探测缓存）
// > fallback（默认窗口）。探测失败不阻塞每轮对话——只写日志，预算继续用当前
// 已解析的值；缺失字段保持 0，绝不当成 0 上限参与预算。

const (
	// botWindowDetectTimeout 是单次 /models 探测的超时；用独立 http.Client，
	// 不复用 AI/模型请求的客户端。
	botWindowDetectTimeout = 10 * time.Second
	// botWindowRefreshTTL 是成功缓存行的新鲜期。
	botWindowRefreshTTL = 24 * time.Hour
	// botWindowFailedTTL 是失败标记行的负缓存期：探测失败也落一行
	// ContextWindow=0 的标记，期内不再对「本来就不提供窗口字段」的上游白探，
	// 过期后允许重试。
	botWindowFailedTTL = 10 * time.Minute
)

// botWindowSyncProbeTimeout 是「缓存缺失且本轮马上要按窗口做压缩决策」时的
// 有界同步探测超时：只在这种时刻等网络，其余路径完全非阻塞。var 仅为测试
// 缩短等待（包内测试串行执行）。
var botWindowSyncProbeTimeout = 5 * time.Second

// botModelCapability 是一次模型能力解析的完整结果。Source 明确区分「已检测」
// 与「回退/猜测」：manual 是部署配置覆盖，detected 来自上游 /models，known
// 预留给内置已知模型表（当前未用），fallback 是默认窗口。展示层不得把回退
// 值说成已检测。
type botModelCapability struct {
	WindowTokens    int64
	MaxOutputTokens int64
	Source          string // manual | detected | known | fallback
	Detail          string
	Detected        bool // Source == "detected"
}

// botNormalizeBaseURL 规范化缓存键里的地址：TrimSpace + 去尾部 /。
func botNormalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// capabilityFor 按解析顺序（先命中先用）返回地址+模型的能力：
//  1. 部署覆盖 cfg.Bots.Context.ModelWindows[model] > 0 → manual
//  2. bot_model_windows 缓存行 ContextWindow > 0 → detected
//  3. cfg.Bots.Context.WindowTokens（默认 32768）→ fallback
//
// MaxOutputTokens 只可能来自第 2 步；manual 不写输出上限。
func (s *BotService) capabilityFor(baseURL, modelName string) botModelCapability {
	if w, ok := s.cfg.Bots.Context.ModelWindows[modelName]; ok && w > 0 {
		return botModelCapability{WindowTokens: w, Source: "manual", Detail: "部署配置覆盖"}
	}
	if row, err := s.windows.FindByAddrModel(botNormalizeBaseURL(baseURL), modelName); err == nil && row != nil && row.ContextWindow > 0 {
		cap := botModelCapability{
			WindowTokens:    row.ContextWindow,
			MaxOutputTokens: row.MaxOutputTokens,
			Source:          row.Source,
			Detail:          row.Detail,
		}
		cap.Detected = cap.Source == "detected"
		return cap
	}
	return botModelCapability{
		WindowTokens: s.cfg.Bots.Context.WindowTokensFor(modelName),
		Source:       "fallback",
		Detail:       "默认窗口（未探测到模型元数据）",
	}
}

// modelCapability 是 bot 视角的模型能力解析（地址取 bot 当前值；run 期间
// 调用方传入的 bot 已应用冻结地址）。
func (s *BotService) modelCapability(bot *model.Bot, modelName string) botModelCapability {
	baseURL := ""
	if bot != nil {
		baseURL = bot.BaseURL
	}
	return s.capabilityFor(baseURL, modelName)
}

// inputBudgetFor 返回考虑了输出空间后的输入预算：window - maxOutput
// （maxOutput<=0 或 >= window 时不保留）。窗口未知时即 cfg 默认窗口，与历史
// 行为一致——未装缓存时数字与 WindowTokensFor 完全相同（INT-102 测试兼容）。
func (s *BotService) inputBudgetFor(bot *model.Bot, modelName string) int64 {
	cap := s.modelCapability(bot, modelName)
	window := cap.WindowTokens
	if window <= 0 {
		// 解析链末端必有 fallback 默认值；防御性兜底不改语义。
		window = s.cfg.Bots.Context.WindowTokensFor(modelName)
	}
	if cap.MaxOutputTokens <= 0 || cap.MaxOutputTokens >= window {
		return window
	}
	return window - cap.MaxOutputTokens
}

// ---------- 探测 ----------

// botDetectClient 是探测专用的 HTTP 客户端：不复用模型请求客户端，也不带
// SDK 的隐藏重试。
var botDetectClient = &http.Client{Timeout: botWindowDetectTimeout}

var errModelWindowUnknown = errors.New("模型元数据中未找到可用上下文窗口")

// detectModelWindow 拉取当前 API 地址的模型元数据（GET {baseURL}/models，
// Bearer 鉴权）。网络/解析失败返回 error，调用方按「不阻塞每轮对话」处理；
// 找得到模型但缺失窗口字段时保持 0 并返回 errModelWindowUnknown。
func (s *BotService) detectModelWindow(ctx context.Context, baseURL, apiKey, modelName string) (*botModelCapability, error) {
	baseURL = botNormalizeBaseURL(baseURL)
	if baseURL == "" || modelName == "" {
		return nil, errors.New("探测目标缺失：base_url 与 model 必填")
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), botWindowDetectTimeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := botDetectClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("GET /models 返回 " + strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	meta, err := botParseModelsPayload(body, modelName)
	if err != nil {
		return nil, err
	}
	if meta.ContextWindow <= 0 {
		return nil, errModelWindowUnknown
	}
	return &botModelCapability{
		WindowTokens:    meta.ContextWindow,
		MaxOutputTokens: meta.MaxOutputTokens,
		Source:          "detected",
		Detail:          "GET /models",
		Detected:        true,
	}, nil
}

// botModelWindowMeta 是从 /models 载荷里挑出的目标模型元数据。
type botModelWindowMeta struct {
	ContextWindow   int64
	MaxOutputTokens int64
}

// botParseModelsPayload 宽容解析 /models 响应：顶层可以是 {"data":[...]}
// （OpenAI/DeepSeek 约定）、{"models":[...]} 或裸数组；逐项取模型名
// id/model/name，窗口与输出字段按候选顺序取第一个 >0 的值，数字或数字
// 字符串都接受。找不到指定模型返回 error（精确匹配优先，其次大小写不敏感）。
func botParseModelsPayload(body []byte, modelName string) (*botModelWindowMeta, error) {
	items, err := botModelListItems(body)
	if err != nil {
		return nil, err
	}
	var ci *botModelWindowMeta
	for _, item := range items {
		name := botModelItemName(item)
		if name == "" {
			continue
		}
		if name != modelName {
			if !strings.EqualFold(name, modelName) {
				continue
			}
			if ci == nil {
				ci = botModelItemMeta(item)
			}
			continue
		}
		// 精确匹配优先：命中即返回，不再看大小写变体。
		return botModelItemMeta(item), nil
	}
	if ci != nil {
		return ci, nil
	}
	return nil, errors.New("模型列表中未找到 " + modelName)
}

// botModelListItems 把三种顶层形状统一成条目数组。
func botModelListItems(body []byte) ([]map[string]json.RawMessage, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err == nil {
		for _, key := range []string{"data", "models"} {
			raw, ok := probe[key]
			if !ok {
				continue
			}
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &items); err == nil {
				return items, nil
			}
		}
		return nil, errors.New("模型列表载荷形状不认识")
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, errors.New("模型列表载荷不是 JSON 对象或数组")
	}
	return items, nil
}

// botModelItemName 取条目的模型名：id / model / name 中第一个非空字符串。
func botModelItemName(item map[string]json.RawMessage) string {
	for _, key := range []string{"id", "model", "name"} {
		raw, ok := item[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// botWindowFieldNames 是窗口/输出字段的候选名（按序取第一个 >0 的值）。
var (
	botContextFieldNames = []string{"context_window", "context_length", "max_context_tokens", "max_input_tokens"}
	botOutputFieldNames  = []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "output_token_limit"}
)

// botModelItemMeta 按候选字段名提取窗口与输出上限。
func botModelItemMeta(item map[string]json.RawMessage) *botModelWindowMeta {
	return &botModelWindowMeta{
		ContextWindow:   botRawMessageInt64(item, botContextFieldNames),
		MaxOutputTokens: botRawMessageInt64(item, botOutputFieldNames),
	}
}

// botRawMessageInt64 按候选顺序取第一个 >0 的字段；数字与数字字符串都接受。
func botRawMessageInt64(item map[string]json.RawMessage, names []string) int64 {
	for _, name := range names {
		raw, ok := item[name]
		if !ok {
			continue
		}
		if v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			if v > 0 {
				return v
			}
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && v > 0 {
				return v
			}
		}
	}
	return 0
}

// persistModelWindow 把一份已探测的能力写入窗口缓存。TestConnection 与
// refreshModelWindow 共用同一入口，保证「响应里的结果」与「缓存里的结果」
// 同源，不再出现二次探测或两者不一致。
func (s *BotService) persistModelWindow(baseURL, modelName string, cap *botModelCapability) error {
	now := s.now()
	return s.windows.Upsert(&model.BotModelWindow{
		ID:              uuid.NewString(),
		BaseURL:         botNormalizeBaseURL(baseURL),
		Model:           modelName,
		ContextWindow:   cap.WindowTokens,
		MaxOutputTokens: cap.MaxOutputTokens,
		Source:          "detected",
		Detail:          cap.Detail,
		FetchedAt:       now,
		UpdatedAt:       now,
	})
}

// persistModelWindowFailure 落一行探测失败标记（ContextWindow=0；解析层只认
// >0，capabilityFor 天然跳过）：给注定失败的上游一个短 TTL 负缓存，避免每次
// run、每次 Create/Update/TestConnection 都对同一地址白探一次。Detail 截断
// 保存失败原因，仅供排查展示。
func (s *BotService) persistModelWindowFailure(baseURL, modelName string, cause error) error {
	detail := ""
	if cause != nil {
		detail = cause.Error()
		if len(detail) > 200 {
			detail = detail[:200]
		}
	}
	now := s.now()
	return s.windows.Upsert(&model.BotModelWindow{
		ID:              uuid.NewString(),
		BaseURL:         botNormalizeBaseURL(baseURL),
		Model:           modelName,
		ContextWindow:   0,
		MaxOutputTokens: 0,
		Source:          "detect_failed",
		Detail:          detail,
		FetchedAt:       now,
		UpdatedAt:       now,
	})
}

// refreshModelWindow 探测并持久化：成功写结果行，失败写短 TTL 失败标记。
// 两种行都让 ensureModelWindow 的 TTL 判断在期内不再打网络。
func (s *BotService) refreshModelWindow(ctx context.Context, baseURL, apiKey, modelName string) error {
	cap, err := s.detectModelWindow(ctx, baseURL, apiKey, modelName)
	if err != nil {
		if perr := s.persistModelWindowFailure(baseURL, modelName, err); perr != nil {
			s.logger.Info("模型窗口失败标记写入失败",
				zap.String("model", modelName), zap.Error(perr))
		}
		return err
	}
	return s.persistModelWindow(baseURL, modelName, cap)
}

// refreshBotModelWindowAsync 后台探测并缓存该机器人当前模型的窗口元数据。
// 结果只写缓存与日志：失败不冒泡为业务失败，也不阻塞调用方（创建/更新响应、
// run 开始都不等待网络）。
func (s *BotService) refreshBotModelWindowAsync(botID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), botWindowDetectTimeout)
		defer cancel()
		bot, err := s.bots.Get(botID)
		if err != nil {
			return
		}
		key, err := s.decryptAPIKey(bot)
		if err != nil {
			return
		}
		if err := s.refreshModelWindow(ctx, bot.BaseURL, key, bot.Model); err != nil {
			s.logger.Info("模型窗口探测未成功",
				zap.String("bot_id", botID), zap.String("model", bot.Model), zap.Error(err))
		}
	}()
}

// ensureModelWindow 是「窗口新鲜度」的唯一入口：只有本轮确实要按窗口做压缩
// 决策时才等网络。
//   - 缓存行新鲜（成功行 24h、失败标记 10min）→ 直接返回，不打网络；
//   - 缓存缺失/过期且输入估算低于回退触发阈值（本轮不依赖窗口）→ 后台异步
//     刷新，立即返回；
//   - 缓存缺失/过期且估算已达回退触发阈值（升级后的存量机器人正属此类：本轮
//     马上要按默认小窗口做压缩判断）→ 有界同步探测。成功即落缓存，本轮的
//     inputBudgetFor 随即用探测值；失败/超时继续用回退窗口，不冒泡为错误——
//     网络故障不阻塞对话，也不能因探测失败把输入判死。
func (s *BotService) ensureModelWindow(bot *model.Bot, run *model.BotRun, apiKey string, inputEstimate int64) {
	baseURL := botNormalizeBaseURL(runBaseURLOf(run, bot))
	modelName := runModelOf(run, bot)
	if row, err := s.windows.FindByAddrModel(baseURL, modelName); err == nil && row != nil {
		ttl := botWindowFailedTTL
		if row.ContextWindow > 0 {
			ttl = botWindowRefreshTTL
		}
		if s.now().Sub(row.FetchedAt) < ttl {
			return
		}
	}
	// 回退（当前已解析）预算的触发阈值即「本轮会不会依赖窗口」的分界：低于
	// 它时压缩判断与窗口无关，探测退回后台。
	fallbackTrigger := botFloorRatio(s.inputBudgetFor(bot, modelName), s.cfg.Bots.Context.CompressionRatioV())
	if inputEstimate < fallbackTrigger {
		s.refreshBotModelWindowAsync(bot.ID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), botWindowSyncProbeTimeout)
	defer cancel()
	if err := s.refreshModelWindow(ctx, baseURL, apiKey, modelName); err != nil {
		s.logger.Info("模型窗口同步探测未成功，本轮继续用回退窗口",
			zap.String("bot_id", bot.ID), zap.String("model", modelName), zap.Error(err))
	}
}
