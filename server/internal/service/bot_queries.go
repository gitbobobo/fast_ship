package service

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// 只读查询与运行控制入口：全部仅 JWT，按机器人归属取用户身份。

func (s *BotService) checkOwnership(userID, botID string) error {
	if _, err := s.bots.FindByIDAndUser(botID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrBotNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

func (s *BotService) ListRuns(userID, botID string, limit int) ([]BotRunResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.runs.ListByBot(botID, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotRunResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botRunResponseOf(&rows[i]))
	}
	return out, nil
}

func (s *BotService) GetRun(userID, botID, runID string) (*BotRunResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	run, err := s.runs.FindByID(runID)
	if err != nil || run.BotID != botID {
		return nil, errs.ErrBotRunNotFound
	}
	resp := botRunResponseOf(run)
	return &resp, nil
}

func (s *BotService) ListCalls(userID, botID string, statuses []string, limit int) ([]BotToolCallResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.calls.ListByBot(botID, statuses, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotToolCallResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botToolCallResponseOf(&rows[i]))
	}
	return out, nil
}

func (s *BotService) findOwnCall(userID, botID, callID string) (*model.BotToolCall, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	call, err := s.calls.FindByID(callID)
	if err != nil || call.BotID != botID {
		return nil, errs.ErrBotCallNotFound
	}
	return call, nil
}

func (s *BotService) GetCall(userID, botID, callID string) (*BotToolCallResponse, error) {
	call, err := s.findOwnCall(userID, botID, callID)
	if err != nil {
		return nil, err
	}
	resp := botToolCallResponseOf(call)
	return &resp, nil
}

// CallResultResponse 是完整结果的读取响应（UI 查看；模型走 fs_result.read）。
type CallResultResponse struct {
	CallID      string          `json:"call_id"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result"`
	Truncated   bool            `json:"truncated"`
	ResultChars int64           `json:"result_chars"`
}

func (s *BotService) GetCallResult(userID, botID, callID string) (*CallResultResponse, error) {
	call, err := s.findOwnCall(userID, botID, callID)
	if err != nil {
		return nil, err
	}
	resp := &CallResultResponse{
		CallID:      call.ID,
		Status:      string(call.Status),
		Truncated:   call.Truncated,
		ResultChars: call.ResultChars,
	}
	if call.ResultJSON != nil {
		resp.Result = json.RawMessage(*call.ResultJSON)
	}
	return resp, nil
}

// CancelCall 取消后台调用；终态调用幂等返回现状。
func (s *BotService) CancelCall(userID, botID, callID string) (*BotToolCallResponse, error) {
	call, err := s.findOwnCall(userID, botID, callID)
	if err != nil {
		return nil, err
	}
	if call.Status.IsTerminal() {
		resp := botToolCallResponseOf(call)
		return &resp, nil
	}
	fresh := call
	for attempt := 0; attempt < 3; attempt++ {
		if fresh.Status.IsTerminal() {
			break
		}
		next, _ := s.cancelBotCall(fresh)
		if next == nil {
			return nil, errs.ErrInternal
		}
		// 状态没动（含 queued CAS 被 worker 认领抢走的竞速重读）就停手；
		// 状态推进了（queued→running 等）则用新状态继续取消意图。
		if next.Status == fresh.Status {
			fresh = next
			break
		}
		fresh = next
	}
	resp := botToolCallResponseOf(fresh)
	return &resp, nil
}

// ---------- SSE 支撑 ----------

// BotRequestMetrics 是最近一次主线模型请求的单次口径。
type BotRequestMetrics struct {
	RequestID           string  `json:"request_id"`
	RunID               string  `json:"run_id"`
	Ordinal             int64   `json:"ordinal"`
	Status              string  `json:"status"` // running|succeeded|failed
	PromptTokens        *int64  `json:"prompt_tokens"`
	CompletionTokens    *int64  `json:"completion_tokens"`
	CachedTokens        *int64  `json:"cached_tokens"`
	UsageComplete       bool    `json:"usage_complete"`
	InputTokensEstimate int64   `json:"input_tokens_estimate"`
	InputEstimator      string  `json:"input_estimator"`
	StartedAt           string  `json:"started_at"`
	FinishedAt          *string `json:"finished_at"`
	// GenerationMs 是首个流式正文片段到请求结束的耗时；0 表示不可用
	//（首个正文与结束时间必须都有效且差值为正）。
	GenerationMs int64 `json:"generation_ms"`
}

// BotMetricsResponse 是聊天页指标区的一致读取：窗口能力 + 最近一次主线模型
// 请求的口径。usage_complete 取「请求落过 usage 计数列」（缺失与零严格区分，
// 计数列为 NULL 即视为不可用）。
type BotMetricsResponse struct {
	ContextWindow   int64  `json:"context_window"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	ContextSource   string `json:"context_source"` // manual|detected|known|fallback
	ContextDetail   string `json:"context_detail,omitempty"`
	// Request 是最近一次主线模型请求（进行中或已结束）；没有则为 nil。
	Request *BotRequestMetrics `json:"request,omitempty"`
}

// BotMaintenanceProgress 是当前活动主线 run 最近一条维护进度（压缩阶段/失败
// 原因）。进度事件只经 SSE 推送且在快照游标之前不会重放，快照随取随带一份，
// 刷新/重开聊天后运行栏不丢反馈；run 结束后不再返回。
type BotMaintenanceProgress struct {
	RunID   string `json:"run_id,omitempty"`
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// BotChatSnapshot 是聊天页的初始读取：消息、调用、run 与事件水位取自同一
// 读取点。订阅游标（next_event_seq - 1）与快照内容严格衔接——事件既不重放
// 快照已含的内容，也不漏掉快照之后的内容（先订阅后补读兜住中间窗口）。
type BotChatSnapshot struct {
	Bot          BotResponse           `json:"bot"`
	Messages     []BotMessageResponse  `json:"messages"`
	Calls        []BotToolCallResponse `json:"calls"`
	Runs         []BotRunResponse      `json:"runs"`
	NextEventSeq int64                 `json:"next_event_seq"`
	// Metrics 是运行指标（INT-115）：窗口能力 + 最近一次主线模型请求；
	// 没有主线请求时 Request 缺省，指标区始终可见已解析的窗口能力。
	Metrics *BotMetricsResponse `json:"metrics,omitempty"`
	// MaintenanceProgress 是活动主线 run 的最近一条维护进度；无活动 run 或
	// 尚无进度事件时缺省（已结束 run 的进度不应再显示）。
	MaintenanceProgress *BotMaintenanceProgress `json:"maintenance_progress,omitempty"`
}

// listAllUnfinishedCalls 分页取全本机器人未终态调用（页大小 200，按
// created_at/id 游标推进）。
func listAllUnfinishedCalls(tx *gorm.DB, botID string) ([]model.BotToolCall, error) {
	repo := repository.NewBotToolCallRepository(tx)
	all := make([]model.BotToolCall, 0, 8)
	for {
		page, err := repo.ListUnfinishedByBotPage(botID, len(all), 200)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < 200 {
			return all, nil
		}
	}
}

// latestMaintenanceProgressTx 在事务内取该 run 最近一条维护进度事件（delta +
// kind=maintenance_progress，payload {phase,message}），按 seq 倒序首个即最新。
// LIKE 只做候选预过滤（维护进度远稀于流式正文），归属与形状由解包后的 kind
// 判定；找不到或解析失败返回 nil，调用方按「尚无进度」处理。
func latestMaintenanceProgressTx(tx *gorm.DB, run *model.BotRun) *BotMaintenanceProgress {
	if run == nil {
		return nil
	}
	var rows []model.BotEvent
	if err := tx.Where("bot_id = ? AND run_id = ? AND event_type = ? AND payload_json LIKE ?",
		run.BotID, run.ID, model.BotEventDelta, "%maintenance_progress%").
		Order("seq DESC").Limit(5).Find(&rows).Error; err != nil {
		return nil
	}
	for i := range rows {
		var env botEventEnvelope
		if json.Unmarshal([]byte(rows[i].PayloadJSON), &env) != nil || env.Kind != "maintenance_progress" {
			continue
		}
		payload, _ := env.Payload.(map[string]interface{})
		phase, _ := payload["phase"].(string)
		message, _ := payload["message"].(string)
		return &BotMaintenanceProgress{RunID: run.ID, Phase: phase, Message: message}
	}
	return nil
}

// GetChatSnapshot 单事务一致读；messageLimit 是消息尾部页大小。bot 投影与
// 指标里的窗口能力解析要走事务外的池根连接（单连接纪律），所以事务内只收
// 集原始行，投影放在提交之后。
func (s *BotService) GetChatSnapshot(userID, botID string, messageLimit int) (*BotChatSnapshot, error) {
	if messageLimit <= 0 || messageLimit > 200 {
		messageLimit = 50
	}
	snap := &BotChatSnapshot{}
	var botRow *model.Bot
	var latestReq *model.BotModelRequest
	var latestRun *model.BotRun
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		bot, err := repository.NewBotRepository(tx).FindByIDAndUser(botID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrBotNotFound
			}
			return err
		}
		botRow = bot
		snap.NextEventSeq = bot.NextEventSeq

		msgs, err := repository.NewBotMessageRepository(tx).ListMainline(botID, 0, 0, messageLimit)
		if err != nil {
			return err
		}
		snap.Messages = make([]BotMessageResponse, 0, len(msgs))
		for i := range msgs {
			snap.Messages = append(snap.Messages, botMessageResponseOf(&msgs[i]))
		}

		callRows, err := repository.NewBotToolCallRepository(tx).ListByBot(botID, nil, 100)
		if err != nil {
			return err
		}
		snap.Calls = make([]BotToolCallResponse, 0, len(callRows))
		for i := range callRows {
			snap.Calls = append(snap.Calls, botToolCallResponseOf(&callRows[i]))
		}

		// 未终态调用必须全量收录：待确认卡片必须可操作。分页取全（数量
		// 无数据库上界，但实际有界且只读）。
		unfinished, err := listAllUnfinishedCalls(tx, botID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, c := range unfinished {
			if !seen[c.ID] {
				seen[c.ID] = true
				snap.Calls = append(snap.Calls, botToolCallResponseOf(&c))
			}
		}

		runRows, err := repository.NewBotRunRepository(tx).ListByBot(botID, 50)
		if err != nil {
			return err
		}
		snap.Runs = make([]BotRunResponse, 0, len(runRows))
		for i := range runRows {
			snap.Runs = append(snap.Runs, botRunResponseOf(&runRows[i]))
		}

		if req, err := repository.NewBotModelRequestRepository(tx).LatestMainByBot(botID); err == nil {
			latestReq = req
			if run, err := repository.NewBotRunRepository(tx).FindByID(req.RunID); err == nil {
				latestRun = run
			}
		}

		// 活动主线 run 的最近一条维护进度：进度事件在快照游标之前不会经 SSE
		// 重放，刷新/重开聊天后由快照补齐。run 已结束（含失败收尾）则不返回
		// ——已终结 run 的进度不应再显示为进行中。
		if activeRun, err := repository.NewBotRunRepository(tx).FindActiveByBot(botID); err == nil {
			snap.MaintenanceProgress = latestMaintenanceProgressTx(tx, activeRun)
		}
		return nil
	})
	if txErr != nil {
		if errors.Is(txErr, errs.ErrBotNotFound) {
			return nil, txErr
		}
		return nil, errs.ErrInternal
	}
	snap.Bot = s.botResponseOf(botRow)
	// 指标始终产出（INT-115）：窗口能力即使还没有任何主线请求（全新 bot、
	// 只有失败 build）也要可见；Request 为 nil 时前端渲染「—」。
	snap.Metrics = s.botMetricsOf(botRow, latestRun, latestReq)
	return snap, nil
}

// botMetricsOf 把最近一次主线模型请求与窗口能力投影成指标响应。req 为 nil 时
// 只给窗口能力（Request 缺省）。能力解析的目录归属/模型/地址成组取 run 的
// 配置快照（机器人切 provider 后，旧请求的窗口必须按它发起时的目录与地址
// 解析，不能拿当前 provider 的窗口除旧请求的估算），取不到回退 bot 当前值。
func (s *BotService) botMetricsOf(bot *model.Bot, run *model.BotRun, req *model.BotModelRequest) *BotMetricsResponse {
	if bot == nil {
		return nil
	}
	baseURL := bot.BaseURL
	modelName := bot.Model
	catalogID := bot.CatalogID
	if run != nil {
		baseURL = runBaseURLOf(run, bot)
		modelName = runModelOf(run, bot)
		catalogID = runCatalogIDOf(run, bot)
	}
	cap := s.capabilityFor(catalogID, baseURL, modelName)
	out := &BotMetricsResponse{
		ContextWindow:   cap.WindowTokens,
		MaxOutputTokens: cap.MaxOutputTokens,
		ContextSource:   cap.Source,
		ContextDetail:   cap.Detail,
	}
	if req == nil {
		return out
	}
	out.Request = &BotRequestMetrics{
		RequestID:           req.ID,
		RunID:               req.RunID,
		Ordinal:             req.Ordinal,
		Status:              string(req.Status),
		PromptTokens:        req.PromptTokens,
		CompletionTokens:    req.CompletionTokens,
		CachedTokens:        req.CachedTokens,
		UsageComplete:       req.PromptTokens != nil,
		InputTokensEstimate: req.InputTokensEstimate,
		InputEstimator:      req.InputEstimator,
		StartedAt:           req.StartedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt:          rfc3339(req.FinishedAt),
	}
	if req.FinishedAt != nil && req.FirstDeltaAt != nil {
		// 首片与结束时间都有效且为正才给出生成耗时。
		if ms := req.FinishedAt.Sub(*req.FirstDeltaAt).Milliseconds(); ms > 0 {
			out.Request.GenerationMs = ms
		}
	}
	return out
}

// BotEventResponse 是事件的 API 投影（重放用）。
type BotEventResponse struct {
	Seq       int64           `json:"seq"`
	EventType string          `json:"event_type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

func botEventResponseOf(e *model.BotEvent) BotEventResponse {
	return BotEventResponse{
		Seq:       e.Seq,
		EventType: e.EventType,
		Payload:   json.RawMessage(e.PayloadJSON),
		CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// ListEvents 重放：seq 大于 after 的全部事件（正序）。
func (s *BotService) ListEvents(userID, botID string, after int64, limit int) ([]BotEventResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.events.ListAfterSeq(botID, after, limit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotEventResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botEventResponseOf(&rows[i]))
	}
	return out, nil
}

// SubscribeEvents 注册 SSE 订阅；先订阅后补读之间不漏事件。返回的取消函数
// 只结束订阅，不影响 run。
func (s *BotService) SubscribeEvents(botID string) (<-chan int64, func()) {
	return s.streamHub.subscribe(botID)
}
