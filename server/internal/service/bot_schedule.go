package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"github.com/openai/openai-go/v2"
	"gorm.io/gorm"
)

// 定时任务（INT-103）：机器人经 fs_schedule 工具维护任务，调度器按 cron 触发
// 独立 schedule 会话。本文件负责：schedule 会话的上下文构建（无压缩、确定性
// 折叠、静默哨兵）、运行事件进主线、工具执行与用户侧只读 REST 的服务层。

const (
	// botScheduleFoldChars 是 schedule 会话的折叠阈值：tool_reply 正文超过该
	// 字符数即替换为一行 fs_result.read 引用。与主线「压缩驱动折叠」不同，
	// 这里是无状态、确定性的——同一 run 内任何一次投影结果一致（前缀稳定），
	// 不写 bot_context_states。
	botScheduleFoldChars = 6000
	// botScheduleSentinel 是静默结果哨兵：无话可说时模型只回复这一行。
	botScheduleSentinel = "[[SILENT]]"
	// botScheduleSummaryMaxChars 限制运行摘要长度，避免事件与账本膨胀。
	botScheduleSummaryMaxChars = 2000

	botScheduleTaskNameMaxLen    = 200
	botScheduleInstructionMaxLen = 8000
	botScheduleRunsDefaultLimit  = 20
	botScheduleRunsMaxLimit      = 100
	botScheduleTasksListLimit    = 200
)

// botScheduleSession 是一次 schedule run 冻结的会话上下文：任务快照、工具集
// 版本（run 开始时钉死，不随主线激活变化）与本次运行的记忆注入（每次运行
// 现读一次，run 内冻结；schedule 不做压缩，不走 memSnaps 缓存）。
type botScheduleSession struct {
	task    *model.BotScheduleTask
	toolset *botToolsetHolder
	memory  string
}

// newScheduleSession 载入任务并冻结会话上下文；任务已删除时返回错误（孤儿
// run 显式失败，不静默空跑）。
func (s *BotService) newScheduleSession(bot *model.Bot, run *model.BotRun) (*botScheduleSession, error) {
	if run.ScheduleTaskID == nil {
		return nil, errs.New(errs.ErrInternal.Code, "schedule run 缺少任务引用")
	}
	task, err := s.schedTasks.FindByID(*run.ScheduleTaskID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.New(errs.ErrInternal.Code, "定时任务已删除，run 无法执行")
		}
		return nil, errs.ErrInternal
	}
	var memory string
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var txErr error
		memory, _, txErr = s.botMemoryInjectionTx(tx, bot.ID)
		return txErr
	})
	if err != nil {
		memory = ""
	}
	return &botScheduleSession{
		task:    task,
		toolset: botToolsetVersionOf(bot.ToolsetVersion),
		memory:  memory,
	}, nil
}

// botScheduleRuntimeContext 是 schedule 会话末尾的运行时上下文：任务指令、
// 静默约定与放行说明只出现在这里。
type botScheduleRuntimeContext struct {
	Note           string `json:"note"`
	CurrentTimeUTC string `json:"current_time_utc"`
	RunID          string `json:"run_id"`
	TaskID         string `json:"task_id"`
	TaskName       string `json:"task_name"`
	Instruction    string `json:"instruction"`
	Cron           string `json:"cron,omitempty"`
	Timezone       string `json:"timezone,omitempty"`
	ScheduledFor   string `json:"scheduled_for,omitempty"`
}

// buildScheduleRunContext 把 schedule run 投影为协议请求：同一份系统提示与
// 冻结记忆注入 + 冻结工具集 + 只含本次 run 的消息 + 任务指令运行时上下文。
// 不读 bot_context_states（无摘要、无压缩折叠），折叠用确定性规则。
func (s *BotService) buildScheduleRunContext(bot *model.Bot, run *model.BotRun, sess *botScheduleSession) (*botBuiltContext, error) {
	rows, err := s.listMessagesThrough(bot.ID, model.BotSessionKindSchedule, run.ID, 0, 0)
	if err != nil {
		return nil, errs.ErrInternal
	}
	projected, projRows, _, err := botProjectMessages(rows, botScheduleFoldMap(rows))
	if err != nil {
		return nil, err
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(projected)+4)
	messages = append(messages, openai.SystemMessage(bot.SystemPromptText))
	if sess.memory != "" {
		messages = append(messages, openai.SystemMessage("[记忆快照]\n"+sess.memory))
	}
	messages = append(messages, projected...)

	rt := botScheduleRuntimeContext{
		Note: "以上为本次定时任务的完整上下文。这是一次 schedule run：独立会话，" +
			"结束后不会与用户对话，用户只会看到你在最终回复里汇报的内容。" +
			"若没有需要告知用户的内容，只回复一行 " + botScheduleSentinel + "；" +
			"否则用简洁中文汇报执行结果。本次运行继承用户权限，业务写操作默认放行（不弹确认）。",
		CurrentTimeUTC: time.Now().UTC().Format(time.RFC3339),
		RunID:          run.ID,
		TaskID:         sess.task.ID,
		TaskName:       sess.task.Name,
		Instruction:    sess.task.Instruction,
		Cron:           sess.task.CronExpression,
		Timezone:       sess.task.Timezone,
	}
	if run.ScheduledFor != nil {
		rt.ScheduledFor = run.ScheduledFor.UTC().Format(time.RFC3339)
	}
	rtRaw, _ := json.Marshal(rt)
	messages = append(messages, openai.UserMessage("[运行时上下文]\n"+string(rtRaw)))

	ts := sess.toolset
	tokenEstimate, estimator := botToolsTokenEstimate(ts)

	consumedThrough := run.InputThroughSeq
	if len(projRows) > 0 {
		consumedThrough = projRows[len(projRows)-1].Seq
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
		// schedule 不参与压缩：投影代数恒 0、无摘要消息、估算按整包字节。
		ContextGeneration: 0,
		SummaryMessageIdx: -1,
		ToolsetVersion:    ts.version,
	}

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
	msgRaw, _ := json.Marshal(messages)
	ctx.InputTokensEstimate = botEstimateBytes(int64(len(msgRaw) + ts.bytes))
	ctx.InputEstimator = botTokensEstimatorBytes35

	toolsRaw, err := json.Marshal(ts.schemas)
	if err != nil {
		return nil, errs.ErrInternal
	}
	combined := append(append([]byte{}, prefixRaw...), toolsRaw...)
	reqSum := sha256.Sum256(combined)
	ctx.RequestHash = hex.EncodeToString(reqSum[:])
	return ctx, nil
}

// botScheduleFoldMap 是 schedule 会话的确定性折叠：超阈值的 tool_reply 正文
// 替换为一行 fs_result.read 引用。无状态、只依赖账本内容——同一 run 内任何
// 一次重放结果一致（前缀稳定），原文仍在账本与调用结果里。
func botScheduleFoldMap(rows []model.BotMessage) map[string]string {
	folded := map[string]string{}
	for i := range rows {
		row := &rows[i]
		if row.Kind != model.BotMessageKindToolReply || row.CallID == nil {
			continue
		}
		var content botToolReplyContent
		if err := json.Unmarshal([]byte(row.ContentJSON), &content); err != nil {
			continue
		}
		chars := utf8Chars(content.Content)
		if chars <= botScheduleFoldChars {
			continue
		}
		folded[row.ID] = fmt.Sprintf(
			"[已折叠] 结果过大（%d 字符），用 fs_result.read(call_id=%s) 读取", chars, *row.CallID)
	}
	return folded
}

// botSilentSummaryOf 取 run 最后一条 assistant 正文，判定静默哨兵并生成结果
// 摘要。哨兵匹配允许尾部空白与句号差异；静默 run 的 summary 记空串。
func (s *BotService) botSilentSummaryOf(botID, runID string) (silent bool, summary string) {
	msg, err := s.messages.LastAssistantOfRun(botID, runID)
	if err != nil {
		return false, ""
	}
	var content botAssistantContent
	if err := json.Unmarshal([]byte(msg.ContentJSON), &content); err != nil {
		return false, ""
	}
	text := strings.TrimSpace(content.Text)
	stripped := strings.TrimSpace(strings.TrimRight(text, ".。"))
	if stripped == botScheduleSentinel {
		return true, ""
	}
	return false, truncateRunes(text, botScheduleSummaryMaxChars)
}

// botScheduleEventContent 是 schedule_event 消息的正文：追加到主线末尾，让
// 机器人判断是否通知用户。
type botScheduleEventContent struct {
	TaskID       string `json:"task_id"`
	TaskName     string `json:"task_name"`
	RunID        string `json:"run_id"`
	Status       string `json:"status"`
	Summary      string `json:"summary,omitempty"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// appendScheduleEventTx 在调用方事务内向主线会话追加一条定时任务运行事件
// （run_id=NULL，不与任何会话关联；schedule_event 是主线模型输入）。
func (s *BotService) appendScheduleEventTx(tx *gorm.DB, botID string, task *model.BotScheduleTask, run *model.BotRun, status model.BotRunStatus, summary, errorCode, errorMessage string, now time.Time) error {
	content := botScheduleEventContent{
		TaskID: task.ID, TaskName: task.Name, RunID: run.ID,
		Status: string(status), Summary: summary,
		ErrorCode: errorCode, ErrorMessage: errorMessage,
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	seq, err := s.bots.NextMessageSeqTx(tx, botID)
	if err != nil {
		return err
	}
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		Kind: model.BotMessageKindScheduleEvent, ContentJSON: string(raw),
		SessionKind: model.BotSessionKindMain, CreatedAt: now,
	}
	if err := repository.NewBotMessageRepository(tx).AppendTx(tx, msg); err != nil {
		return err
	}
	return s.appendEventTx(tx, botID, nil, "", model.BotEventDelta, botEventEnvelope{
		Kind:      "schedule_event",
		MessageID: msg.ID,
		Payload:   botMessageResponseOf(msg),
	})
}

// ---------- 任务校验与维护（工具与 REST 共用） ----------

// botValidateScheduleTaskFields 校验任务字段：cron 五字段（与调度器同一解析
// 路径）、时区 IANA 名称、名称与指令非空。
func botValidateScheduleTaskFields(name, cronExpr, timezone, instruction string) error {
	if strings.TrimSpace(name) == "" {
		return errs.New(errs.ErrInvalidParams.Code, "任务名不能为空")
	}
	if utf8.RuneCountInString(name) > botScheduleTaskNameMaxLen {
		return errs.New(errs.ErrInvalidParams.Code, "任务名过长（最多 200 字符）")
	}
	if strings.TrimSpace(instruction) == "" {
		return errs.New(errs.ErrInvalidParams.Code, "任务指令不能为空")
	}
	if utf8.RuneCountInString(instruction) > botScheduleInstructionMaxLen {
		return errs.New(errs.ErrInvalidParams.Code, "任务指令过长（最多 8000 字符）")
	}
	_, _, err := parseAutomationCron(cronExpr, timezone, time.Now().UTC())
	return err
}

// createScheduleTask 校验并创建任务；next_run_at 取 now 之后的下一次。
func (s *BotService) createScheduleTask(botID, name, cronExpr, timezone, instruction, mdl string, needsReport bool) (*model.BotScheduleTask, error) {
	name = strings.TrimSpace(name)
	instruction = strings.TrimSpace(instruction)
	timezone = strings.TrimSpace(timezone)
	if err := botValidateScheduleTaskFields(name, cronExpr, timezone, instruction); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_, next, err := parseAutomationCron(cronExpr, timezone, now)
	if err != nil {
		return nil, err
	}
	task := &model.BotScheduleTask{
		ID: uuid.NewString(), BotID: botID,
		Name: name, CronExpression: strings.TrimSpace(cronExpr), Timezone: timezone,
		Instruction: instruction, Model: strings.TrimSpace(mdl),
		NeedsReport: needsReport, Enabled: true, NextRunAt: &next,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.schedTasks.Create(task); err != nil {
		return nil, errs.ErrInternal
	}
	return task, nil
}

// updateScheduleTask 在事务内重新读取任务后按提供（非空）的字段更新；
// cron/时区变化且任务启用时重算 next_run_at 为 now 之后的下一次。读改写走
// 同一事务：与用户启停（setScheduleTaskEnabled）在同一连接上串行化，避免
// 「读到启用 → 期间被停用 → 把 next_run_at 又写回去」的竞态。
func (s *BotService) updateScheduleTask(botID, taskID, name, cronExpr, timezone, instruction string, mdl *string, needsReport *bool) (*model.BotScheduleTask, error) {
	var out *model.BotScheduleTask
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		repo := repository.NewBotScheduleTaskRepository(tx)
		task, err := repo.FindByIDAndBot(taskID, botID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrBotScheduleTaskNotFound
			}
			return err
		}
		updated, err := applyScheduleTaskUpdate(task, name, cronExpr, timezone, instruction, mdl, needsReport)
		if err != nil {
			return err
		}
		cols := map[string]interface{}{
			"name": updated.Name, "cron_expression": updated.CronExpression,
			"timezone": updated.Timezone, "instruction": updated.Instruction,
			"model": updated.Model, "needs_report": updated.NeedsReport,
			"next_run_at": updated.NextRunAt, "updated_at": time.Now().UTC(),
		}
		if ok, err := repo.UpdateColumns(updated.ID, nil, cols); err != nil || !ok {
			return errs.ErrInternal
		}
		out = updated
		return nil
	})
	if txErr != nil {
		if appErr, ok := txErr.(*errs.AppError); ok {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}
	return out, nil
}

// applyScheduleTaskUpdate 是纯字段应用与校验（不碰数据库），供事务内调用。
// mdl 为 nil 表示未提供（保留原值），指向空串表示显式清除模型覆盖。
func applyScheduleTaskUpdate(task *model.BotScheduleTask, name, cronExpr, timezone, instruction string, mdl *string, needsReport *bool) (*model.BotScheduleTask, error) {
	cronChanged := false
	if strings.TrimSpace(name) != "" {
		if utf8.RuneCountInString(strings.TrimSpace(name)) > botScheduleTaskNameMaxLen {
			return nil, errs.New(errs.ErrInvalidParams.Code, "任务名过长（最多 200 字符）")
		}
		task.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(cronExpr) != "" {
		task.CronExpression = strings.TrimSpace(cronExpr)
		cronChanged = true
	}
	if strings.TrimSpace(timezone) != "" {
		if _, err := time.LoadLocation(strings.TrimSpace(timezone)); err != nil {
			return nil, errs.New(errs.ErrInvalidParams.Code, "时区无效（须为 IANA 名称，如 Asia/Shanghai）")
		}
		task.Timezone = strings.TrimSpace(timezone)
		cronChanged = true
	}
	if strings.TrimSpace(instruction) != "" {
		if utf8.RuneCountInString(strings.TrimSpace(instruction)) > botScheduleInstructionMaxLen {
			return nil, errs.New(errs.ErrInvalidParams.Code, "任务指令过长（最多 8000 字符）")
		}
		task.Instruction = strings.TrimSpace(instruction)
	}
	if mdl != nil {
		task.Model = strings.TrimSpace(*mdl)
	}
	if needsReport != nil {
		task.NeedsReport = *needsReport
	}
	if err := botValidateScheduleTaskFields(task.Name, task.CronExpression, task.Timezone, task.Instruction); err != nil {
		return nil, err
	}
	if cronChanged {
		if !task.Enabled {
			// 停用的任务保持「无下次计划」：改 cron/时区不该让接口与界面显示
			// 一个永远不会触发的未来时间（重新启用时才重算）。
			task.NextRunAt = nil
		} else {
			_, next, err := parseAutomationCron(task.CronExpression, task.Timezone, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			task.NextRunAt = &next
		}
	}
	return task, nil
}

// setScheduleTaskEnabled 开关任务：重新启用时 next_run_at 重算为 now 之后的
// 下一次（不补跑过期计划）；停用后不再触发。
func (s *BotService) setScheduleTaskEnabled(botID, taskID string, enabled bool) (*model.BotScheduleTask, error) {
	now := time.Now().UTC()
	var task *model.BotScheduleTask
	txErr := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		task, err = repository.NewBotScheduleTaskRepository(tx).FindByIDAndBot(taskID, botID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.ErrBotScheduleTaskNotFound
			}
			return err
		}
		cols := map[string]interface{}{"enabled": enabled, "updated_at": now}
		if enabled {
			_, next, parseErr := parseAutomationCron(task.CronExpression, task.Timezone, now)
			if parseErr != nil {
				return parseErr
			}
			cols["next_run_at"] = next
		} else {
			cols["next_run_at"] = nil
		}
		res := tx.Model(&model.BotScheduleTask{}).Where("id = ?", taskID).Updates(cols)
		if res.Error != nil {
			return res.Error
		}
		task.Enabled = enabled
		if enabled {
			if next, ok := cols["next_run_at"].(time.Time); ok {
				v := next
				task.NextRunAt = &v
			}
		} else {
			task.NextRunAt = nil
		}
		return nil
	})
	if txErr != nil {
		if appErr, ok := txErr.(*errs.AppError); ok {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}
	return task, nil
}

// deleteScheduleTask 删除任务；运行记录保留（账本供排查）。
func (s *BotService) deleteScheduleTask(botID, taskID string) error {
	if err := s.schedTasks.Delete(botID, taskID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.ErrBotScheduleTaskNotFound
		}
		return errs.ErrInternal
	}
	return nil
}

// ---------- API 投影（形状已与前端冻结，不要改字段名） ----------

// BotScheduleRunResponse 是定时任务运行记录的 API 投影。
type BotScheduleRunResponse struct {
	ID           string  `json:"id"`
	TaskID       *string `json:"task_id"`
	Status       string  `json:"status"`
	Summary      string  `json:"summary"`
	Silent       bool    `json:"silent"`
	ErrorCode    *string `json:"error_code,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
	ScheduledFor *string `json:"scheduled_for"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	DurationMS   *int64  `json:"duration_ms"`
	PromptTokens *int64  `json:"prompt_tokens,omitempty"`
	Completion   *int64  `json:"completion_tokens,omitempty"`
	TotalTokens  *int64  `json:"total_tokens,omitempty"`
}

func botScheduleRunResponseOf(run *model.BotRun) BotScheduleRunResponse {
	resp := BotScheduleRunResponse{
		ID:           run.ID,
		TaskID:       run.ScheduleTaskID,
		Status:       string(run.Status),
		Summary:      run.Summary,
		Silent:       run.Silent,
		ErrorCode:    run.ErrorCode,
		ErrorMessage: run.ErrorMessage,
		ScheduledFor: rfc3339(run.ScheduledFor),
		StartedAt:    rfc3339(run.StartedAt),
		FinishedAt:   rfc3339(run.FinishedAt),
		PromptTokens: run.PromptTokens,
		Completion:   run.CompletionTokens,
		TotalTokens:  run.TotalTokens,
	}
	if run.StartedAt != nil && run.FinishedAt != nil {
		ms := run.FinishedAt.Sub(*run.StartedAt).Milliseconds()
		resp.DurationMS = &ms
	}
	return resp
}

// BotScheduleTaskResponse 是定时任务的 API 投影；last_run 是最近一次运行。
type BotScheduleTaskResponse struct {
	ID             string                  `json:"id"`
	BotID          string                  `json:"bot_id"`
	Name           string                  `json:"name"`
	CronExpression string                  `json:"cron_expression"`
	Timezone       string                  `json:"timezone"`
	Instruction    string                  `json:"instruction"`
	Model          string                  `json:"model"`
	NeedsReport    bool                    `json:"needs_report"`
	Enabled        bool                    `json:"enabled"`
	NextRunAt      *string                 `json:"next_run_at"`
	CreatedAt      string                  `json:"created_at"`
	UpdatedAt      string                  `json:"updated_at"`
	LastRun        *BotScheduleRunResponse `json:"last_run"`
}

func (s *BotService) scheduleTaskResponseOf(task *model.BotScheduleTask) BotScheduleTaskResponse {
	resp := BotScheduleTaskResponse{
		ID: task.ID, BotID: task.BotID, Name: task.Name,
		CronExpression: task.CronExpression, Timezone: task.Timezone,
		Instruction: task.Instruction, Model: task.Model,
		NeedsReport: task.NeedsReport, Enabled: task.Enabled,
		NextRunAt: rfc3339(task.NextRunAt),
		CreatedAt: task.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if run, err := s.runs.LatestByScheduleTask(task.BotID, task.ID); err == nil {
		out := botScheduleRunResponseOf(run)
		resp.LastRun = &out
	}
	return resp
}

// ---------- 用户侧只读服务（唯一可写：enabled 开关） ----------

func (s *BotService) ListScheduleTasks(userID, botID string) ([]BotScheduleTaskResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	rows, err := s.schedTasks.ListByBot(botID, botScheduleTasksListLimit)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotScheduleTaskResponse, 0, len(rows))
	for i := range rows {
		out = append(out, s.scheduleTaskResponseOf(&rows[i]))
	}
	return out, nil
}

func (s *BotService) GetScheduleTask(userID, botID, taskID string) (*BotScheduleTaskResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	task, err := s.schedTasks.FindByIDAndBot(taskID, botID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotScheduleTaskNotFound
		}
		return nil, errs.ErrInternal
	}
	resp := s.scheduleTaskResponseOf(task)
	return &resp, nil
}

func (s *BotService) ListScheduleTaskRuns(userID, botID, taskID string, limit, offset int) ([]BotScheduleRunResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	if _, err := s.schedTasks.FindByIDAndBot(taskID, botID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotScheduleTaskNotFound
		}
		return nil, errs.ErrInternal
	}
	if limit <= 0 || limit > botScheduleRunsMaxLimit {
		limit = botScheduleRunsDefaultLimit
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.runs.ListByScheduleTask(botID, taskID, limit, offset)
	if err != nil {
		return nil, errs.ErrInternal
	}
	out := make([]BotScheduleRunResponse, 0, len(rows))
	for i := range rows {
		out = append(out, botScheduleRunResponseOf(&rows[i]))
	}
	return out, nil
}

// SetScheduleTaskEnabled 是用户对任务唯一的写操作。
func (s *BotService) SetScheduleTaskEnabled(userID, botID, taskID string, enabled bool) (*BotScheduleTaskResponse, error) {
	if err := s.checkOwnership(userID, botID); err != nil {
		return nil, err
	}
	task, err := s.setScheduleTaskEnabled(botID, taskID, enabled)
	if err != nil {
		return nil, err
	}
	resp := s.scheduleTaskResponseOf(task)
	return &resp, nil
}

// SetScheduleTaskEnabledRequest 是开关请求体：enabled 是唯一可写字段，
// 其它字段在 handler 层拒绝（400）。
type SetScheduleTaskEnabledRequest struct {
	Enabled *bool `json:"enabled"`
}

// ---------- fs_schedule 工具执行（全部同步、不弹确认） ----------

func (s *BotService) execScheduleTool(call *model.BotToolCall) (string, error) {
	var args botScheduleTaskArgs
	if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
		return "", errs.ErrInvalidParams
	}
	switch args.Action {
	case "list":
		return s.execScheduleList(call, &args)
	case "create":
		return s.execScheduleCreate(call, &args)
	case "update":
		return s.execScheduleUpdate(call, &args)
	case "delete":
		return s.execScheduleDelete(call, &args)
	case "runs":
		return s.execScheduleRuns(call, &args)
	case "enable", "disable":
		return s.execScheduleEnable(call, &args, args.Action == "enable")
	}
	return "", errs.New(errs.ErrInvalidParams.Code, "不支持的 fs_schedule 动作: "+args.Action)
}

// execScheduleList 分页列出任务：total 是真实总数，has_more 提示还有更旧的
// 任务未返回——把截断结果当成总量会让旧任务变得不可达、也就无从管理。
func (s *BotService) execScheduleList(call *model.BotToolCall, args *botScheduleTaskArgs) (string, error) {
	limit := args.Limit
	if limit <= 0 {
		limit = botScheduleTasksListLimit
	}
	if limit > botScheduleTasksListLimit {
		limit = botScheduleTasksListLimit
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	tasks, err := s.schedTasks.ListByBotPage(call.BotID, offset, limit)
	if err != nil {
		return "", errs.ErrInternal
	}
	total, err := s.schedTasks.CountByBot(call.BotID)
	if err != nil {
		return "", errs.ErrInternal
	}
	items := make([]map[string]interface{}, 0, len(tasks))
	for i := range tasks {
		items = append(items, s.scheduleTaskSlim(&tasks[i]))
	}
	out := map[string]interface{}{
		"items": items, "total": total, "limit": limit, "offset": offset,
	}
	if int64(offset+len(items)) < total {
		out["has_more"] = true
	}
	return botMarshalResult(out)
}

// scheduleTaskSlim 是工具结果里的任务投影：带 next_run_at 与 cron/时区原文，
// 让模型能在回复里用中文说明执行时间。
func (s *BotService) scheduleTaskSlim(task *model.BotScheduleTask) map[string]interface{} {
	out := map[string]interface{}{
		"id": task.ID, "name": task.Name,
		"cron_expression": task.CronExpression, "timezone": task.Timezone,
		"instruction": task.Instruction, "needs_report": task.NeedsReport,
		"enabled": task.Enabled,
	}
	if task.Model != "" {
		out["model"] = task.Model
	}
	if task.NextRunAt != nil {
		loc := timeOrUTC(task.Timezone)
		out["next_run_at"] = task.NextRunAt.In(loc).Format("2006-01-02 15:04:05 MST")
		out["next_run_at_utc"] = task.NextRunAt.UTC().Format(time.RFC3339)
	} else {
		out["next_run_at"] = nil
	}
	return out
}

func timeOrUTC(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(name); err == nil {
		return loc
	}
	return time.UTC
}

func (s *BotService) execScheduleCreate(call *model.BotToolCall, args *botScheduleTaskArgs) (string, error) {
	mdl := ""
	if args.Model != nil {
		mdl = *args.Model
	}
	task, err := s.createScheduleTask(call.BotID, args.Name, args.Cron, args.Timezone, args.Instruction, mdl, args.NeedsReportBool())
	if err != nil {
		return "", err
	}
	out := s.scheduleTaskSlim(task)
	out["ok"] = true
	out["note"] = "任务已创建并启用；请在对话里用中文告知用户任务名与执行时间"
	return botMarshalResult(out)
}

func (s *BotService) execScheduleUpdate(call *model.BotToolCall, args *botScheduleTaskArgs) (string, error) {
	updated, err := s.updateScheduleTask(call.BotID, strings.TrimSpace(args.TaskID),
		args.Name, args.Cron, args.Timezone, args.Instruction, args.Model, args.NeedsReport)
	if err != nil {
		return "", err
	}
	out := s.scheduleTaskSlim(updated)
	out["ok"] = true
	out["note"] = "任务已更新；请在对话里用中文告知用户任务名与新的执行时间"
	return botMarshalResult(out)
}

func (s *BotService) execScheduleDelete(call *model.BotToolCall, args *botScheduleTaskArgs) (string, error) {
	if err := s.deleteScheduleTask(call.BotID, strings.TrimSpace(args.TaskID)); err != nil {
		return "", err
	}
	return botMarshalResult(map[string]interface{}{
		"ok": true, "task_id": strings.TrimSpace(args.TaskID),
		"note": "任务已删除；请在对话里用中文告知用户",
	})
}

func (s *BotService) execScheduleRuns(call *model.BotToolCall, args *botScheduleTaskArgs) (string, error) {
	taskID := strings.TrimSpace(args.TaskID)
	if _, err := s.schedTasks.FindByIDAndBot(taskID, call.BotID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.ErrBotScheduleTaskNotFound
		}
		return "", errs.ErrInternal
	}
	limit := args.Limit
	if limit <= 0 {
		limit = botScheduleRunsDefaultLimit
	}
	if limit > botScheduleRunsMaxLimit {
		limit = botScheduleRunsMaxLimit
	}
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	rows, err := s.runs.ListByScheduleTask(call.BotID, taskID, limit, offset)
	if err != nil {
		return "", errs.ErrInternal
	}
	items := make([]BotScheduleRunResponse, 0, len(rows))
	for i := range rows {
		items = append(items, botScheduleRunResponseOf(&rows[i]))
	}
	return botMarshalResult(map[string]interface{}{
		"items": items, "task_id": taskID, "limit": limit, "offset": offset,
	})
}

func (s *BotService) execScheduleEnable(call *model.BotToolCall, args *botScheduleTaskArgs, enabled bool) (string, error) {
	task, err := s.setScheduleTaskEnabled(call.BotID, strings.TrimSpace(args.TaskID), enabled)
	if err != nil {
		return "", err
	}
	out := s.scheduleTaskSlim(task)
	out["ok"] = true
	out["note"] = "任务已" + map[bool]string{true: "启用", false: "停用"}[enabled] +
		"；请在对话里用中文告知用户"
	return botMarshalResult(out)
}
