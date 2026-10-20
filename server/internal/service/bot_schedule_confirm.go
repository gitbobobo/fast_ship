package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// fs_schedule 的确认受理（INT-136）：哪些动作需要确认、受理期校验、确认卡的
// 范围与预览、以及确认后的异步执行。集中在一个文件里，避免确认语义散进通用
// 工具表、域服务与会话收尾；其余 fs_schedule 生命周期仍在 bot_schedule.go。
//
// 规则不随工具集版本变化（冻结在 v3–v7 的会话同样生效）：RequiresConfirmation
// 与 Mode 都不在 wire 字节里，不影响任何已发布版本的 tools_hash。

// botScheduleConfirmation 判定 fs_schedule 动作是否需要用户确认，并据此把
// 调用改成异步受理（平台只有异步路径存在确认）。规则不随工具集版本变化：
// 冻结在 v3–v7 的会话同样生效，wire 字节里没有该信息，不影响 tools_hash。
// 规则：create 恒需确认；update 出现 instruction/needs_report/model 任一
// （含 null，出现即算）需确认，只改 name/cron/timezone 保持同步；run 在
// dry_run 解析为布尔 true 时免确认（只读预览），否则需确认。返回的 error 是
// 受理期参数校验（cron/时区），避免为一个必然执行失败的调用弹确认卡。
func botScheduleConfirmation(action string, raw map[string]json.RawMessage) (bool, error) {
	switch action {
	case "create":
		if err := botValidateScheduleAcceptArgs(raw); err != nil {
			return false, err
		}
		return true, nil
	case "update":
		sensitive := false
		for _, field := range []string{"instruction", "needs_report", "model"} {
			if _, ok := raw[field]; ok {
				sensitive = true
				break
			}
		}
		if !sensitive {
			return false, nil
		}
		if err := botValidateScheduleAcceptArgs(raw); err != nil {
			return false, err
		}
		return true, nil
	case "run":
		var dryRun bool
		if v, ok := raw["dry_run"]; ok {
			// 解析失败按非 true 处理：仍要确认，不会放行真实触发。
			_ = json.Unmarshal(v, &dryRun)
		}
		return !dryRun, nil
	}
	return false, nil
}

// botValidateScheduleAcceptArgs 是需要确认的 create/update 的受理期参数校验：
// cron 与执行层（parseScheduleCron）同一解析路径，时区必须是 IANA 名称。
// 未提供的字段不校验（由执行层按「未提供则保留原值」处理）。
func botValidateScheduleAcceptArgs(raw map[string]json.RawMessage) error {
	cronExpr := rawString(raw["cron"])
	timezone := rawString(raw["timezone"])
	if cronExpr != "" {
		if _, _, err := parseScheduleCron(cronExpr, timezone, time.Now().UTC()); err != nil {
			return err
		}
	} else if timezone != "" {
		if _, err := time.LoadLocation(timezone); err != nil {
			return errs.New(errs.ErrInvalidParams.Code, "时区无效（须为 IANA 名称，如 Asia/Shanghai）")
		}
	}
	// 长度上限与执行层同一来源（botScheduleTaskNameMaxLen / botScheduleInstructionMaxLen）：
	// 否则超长指令能过受理、弹卡、获批，worker 执行时才 40001 失败。
	if name := rawString(raw["name"]); utf8.RuneCountInString(name) > botScheduleTaskNameMaxLen {
		return errs.New(errs.ErrInvalidParams.Code, "任务名过长（最多 200 字符）")
	}
	if instruction := rawString(raw["instruction"]); utf8.RuneCountInString(instruction) > botScheduleInstructionMaxLen {
		return errs.New(errs.ErrInvalidParams.Code, "任务指令过长（最多 8000 字符）")
	}
	return nil
}

// botScheduleManageDenied 拒绝在定时任务会话里管理任务：schedule 会话默认放行
// 写操作，允许它建/改/手动跑任务等于允许无确认地造出一个以后每次运行都放行
// 写操作的会话；允许它 enable/disable/delete 等于能静默重新武装用户已经停用
// 的任务、或者抹掉全部任务。这些都属于「管理任务」，一律回主线操作。
// 由调用方在 schedule 会话的受理路径上调用。
func botScheduleManageDenied(prepared *botPreparedCall) error {
	if prepared.ToolName != "fs_schedule" {
		return nil
	}
	switch prepared.Action {
	case "create", "update", "run", "enable", "disable", "delete":
		return errs.New(errs.ErrInvalidParams.Code,
			"定时任务运行中不能创建/修改/启停/删除/手动运行定时任务（防止绕过写操作确认），请在主线对话里操作")
	}
	return nil
}

// ---------- fs_schedule 确认受理（INT-136）：范围、确认卡预览与异步执行 ----------

// 授权范围类型：create 的范围是本机器人的任务集合（ScopeID=bot.ID）；update/run
// 的范围是单个任务（ScopeID=任务 ID）——run 的「始终允许」按任务绑定，用户
// 此前确认过该任务才可能授权。
const (
	botScopeScheduleTask           = "schedule_task"
	botScopeScheduleTaskCollection = "schedule_task_collection"
)

// botScheduleCallPreview 是 fs_schedule 确认卡的服务端预览：受理时算好下次
// 运行时间与生效模型，并带任务完整配置快照（update 按「未提供则保留原值」
// 合并后的生效值），前端直接展示，不重复实现 cron 解析。
type botScheduleCallPreview struct {
	// TaskID 是目标任务 ID；create 时任务尚不存在，为空。
	TaskID string `json:"task_id,omitempty"`
	Name   string `json:"name,omitempty"`
	// Cron/Timezone/Instruction/NeedsReport 是生效后的完整任务配置：update
	// 与 run 让用户在卡上看到的是合并后配置而不是增量。
	Cron        string `json:"cron,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	Instruction string `json:"instruction,omitempty"`
	NeedsReport *bool  `json:"needs_report,omitempty"`
	// NextRunAt 是任务时区的可读时间，格式 "2006-01-02 15:04:05 MST"（与
	// scheduleTaskSlim 一致）。
	NextRunAt string `json:"next_run_at,omitempty"`
	// NextRunAtUTC 是同一时刻的 RFC3339 投影。
	NextRunAtUTC string `json:"next_run_at_utc,omitempty"`
	// EffectiveModel 是任务覆盖优先、否则机器人当前模型。
	EffectiveModel string `json:"effective_model,omitempty"`
}

// resolveScheduleScope 建卡前解析 fs_schedule 写操作的范围与确认卡预览。
// create 的范围是本机器人的任务集合（ScopeID=bot.ID）；update/run 的范围是
// 单个任务，任务读不到就保持 ScopeID 为空（主线不建卡，按无法确定目标失败；
// 受理与执行之间任务被删的，执行期会以 40429 失败）。update 的预览按「未提供
// 则保留原值」合并 cron/timezone/model 后计算，让用户在卡上看到的是生效后的
// 配置而不是增量。list/delete/runs/enable/disable 等同步动作不设范围与预览。
func (s *BotService) resolveScheduleScope(bot *model.Bot, prepared *botPreparedCall, raw map[string]json.RawMessage) {
	switch prepared.Action {
	case "create":
		prepared.ScopeKind = botScopeScheduleTaskCollection
		prepared.ScopeID = bot.ID
		prepared.FieldsConstraint = presentFields(raw, "name", "cron", "timezone", "instruction", "model", "needs_report")
		prepared.SchedulePreview = s.schedulePreviewFromArgs(bot, raw)
	case "update":
		prepared.ScopeKind = botScopeScheduleTask
		prepared.FieldsConstraint = presentFields(raw, "name", "cron", "timezone", "instruction", "model", "needs_report")
		task, err := s.schedTasks.FindByIDAndBot(rawString(raw["task_id"]), bot.ID)
		if err != nil {
			return
		}
		prepared.ScopeID = task.ID
		prepared.SchedulePreview = s.schedulePreviewFromTask(bot, schedulePreviewInput{
			Name:           rawString(raw["name"]),
			Cron:           rawString(raw["cron"]),
			Timezone:       rawString(raw["timezone"]),
			Instruction:    rawString(raw["instruction"]),
			ModelPtr:       rawStringPtr(raw["model"]),
			NeedsReportPtr: rawBoolPtr(raw["needs_report"]),
		}, task)
	case "run":
		prepared.ScopeKind = botScopeScheduleTask
		task, err := s.schedTasks.FindByIDAndBot(rawString(raw["task_id"]), bot.ID)
		if err != nil {
			return
		}
		prepared.ScopeID = task.ID
		prepared.SchedulePreview = s.schedulePreviewFromTask(bot, schedulePreviewInput{}, task)
	}
}

// schedulePreviewInput 是 create/update 预览的参数增量：空串表示未提供（保留
// 原值），ModelPtr/NeedsReportPtr 非 nil 表示显式提供（model 空串=清除覆盖，
// 回到机器人当前模型）。
type schedulePreviewInput struct {
	Name           string
	Cron           string
	Timezone       string
	Instruction    string
	ModelPtr       *string
	NeedsReportPtr *bool
}

// schedulePreviewFromArgs 为 create 计算预览：cron/timezone 已在受理期校验，
// 解析失败时只省略下次运行时间，不阻断受理。
func (s *BotService) schedulePreviewFromArgs(bot *model.Bot, raw map[string]json.RawMessage) *botScheduleCallPreview {
	return s.schedulePreviewFromTask(bot, schedulePreviewInput{
		Name:           rawString(raw["name"]),
		Cron:           rawString(raw["cron"]),
		Timezone:       rawString(raw["timezone"]),
		Instruction:    rawString(raw["instruction"]),
		ModelPtr:       rawStringPtr(raw["model"]),
		NeedsReportPtr: rawBoolPtr(raw["needs_report"]),
	}, nil)
}

// schedulePreviewFromTask 用执行器同一份纯函数（applyScheduleTaskUpdate）合并
// 配置：确认卡展示的「生效配置」与实际执行值不会漂移（新增字段只改一处）。
// task 为 nil 表示 create（全部取增量）；合并或校验失败时不给预览（调用已被
// 受理期校验拦住，这里只是兜底）。生效模型：任务覆盖非空优先，否则机器人当前
// 模型（与建 run 时的冻结优先级一致）。
func (s *BotService) schedulePreviewFromTask(bot *model.Bot, in schedulePreviewInput, task *model.BotScheduleTask) *botScheduleCallPreview {
	base := model.BotScheduleTask{}
	if task != nil {
		base = *task
	}
	merged, err := applyScheduleTaskUpdate(&base, in.Name, in.Cron, in.Timezone, in.Instruction, in.ModelPtr, in.NeedsReportPtr)
	if err != nil {
		return nil
	}
	effective := strings.TrimSpace(merged.Model)
	if effective == "" {
		effective = bot.Model
	}
	report := merged.NeedsReport
	preview := &botScheduleCallPreview{
		Name: merged.Name, Cron: merged.CronExpression, Timezone: merged.Timezone,
		Instruction: merged.Instruction, NeedsReport: &report,
		EffectiveModel: effective,
	}
	if task != nil {
		preview.TaskID = task.ID
	}
	if _, next, parseErr := parseScheduleCron(merged.CronExpression, merged.Timezone, time.Now().UTC()); parseErr == nil {
		loc := timeOrUTC(merged.Timezone)
		preview.NextRunAt = next.In(loc).Format("2006-01-02 15:04:05 MST")
		preview.NextRunAtUTC = next.UTC().Format(time.RFC3339)
	}
	return preview
}

// rawStringPtr 解出字符串指针：字段未出现返回 nil（「未提供」与「显式空串」
// 语义不同，创建时指定的模型要能被显式清空）。
func rawStringPtr(raw json.RawMessage) *string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	return &s
}

// rawBoolPtr 解出布尔指针：未提供返回 nil（needs_report 的「未提供」与
// 「显式 false」语义不同，update 用指针区分）。
func rawBoolPtr(raw json.RawMessage) *bool {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil
	}
	return &b
}

// runScheduleWrite 在 worker 内执行需要确认的 fs_schedule 动作（create/update
// 敏感字段/run）。确认在受理端判定，这里只负责执行并把结果交给后台结果通道；
// 返回 json.RawMessage 让 botBuildOutcome 原样透传 execScheduleTool 的结果。
func (s *BotService) runScheduleWrite(ctx context.Context, call *model.BotToolCall) (interface{}, error) {
	switch call.Action {
	case "create", "update", "run":
	default:
		return nil, errs.New(errs.ErrInvalidParams.Code,
			"fs_schedule 只有 create/update/run 走异步确认路径: "+call.Action)
	}
	out, err := s.execScheduleTool(call)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
