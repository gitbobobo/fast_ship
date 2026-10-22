package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
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
// dry_run 解析为布尔 true 时免确认（只读预览），否则需确认；enable（重新
// 武装一个用户已停用、运行内默认放行写操作的任务）与 delete（不可逆地抹掉
// 任务）也要确认。disable 保持同步：它是收紧权限、随时可逆（enable 要确认），
// 阻止它是净损失。返回的 error 是受理期参数校验，避免为一个必然执行失败的
// 调用弹确认卡。
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
	case "enable", "delete":
		return true, nil
	case "list", "runs", "state_get", "state_set", "disable", "status":
		// 只读动作与 disable（收紧权限、可逆）同步免确认；status 是 v2 冻结
		// 工具集里 list 的旧名，同样只读。
		return false, nil
	}
	// 未识别动作按「需要确认」处理（fail closed）：未来给工具集加动作却漏
	// 更新本表时，宁可多弹确认卡、由 worker 显式拒绝，也不能让新动作继承
	// 免确认默认值、静默放行持久化写。
	return true, nil
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
	case "list", "runs", "state_get", "state_set", "status":
		// 只读查询与本任务小状态是定时任务会话的正常能力；status 是 v2
		// 冻结工具集里 list 的旧名。
		return nil
	}
	// 管理动作（create/update/run/enable/disable/delete）一律拒绝；未识别
	// 动作同样拒绝（fail closed）：未来新动作默认不允许在定时任务会话里
	// 使用，等明确分类后再放行。
	return errs.New(errs.ErrInvalidParams.Code,
		"定时任务运行中不能创建/修改/启停/删除/手动运行定时任务（防止绕过写操作确认），请在主线对话里操作")
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
	// ModelInherited 标记生效模型来自「跟随机器人」而不是任务固定覆盖：
	// 卡片据此说明继承语义（机器人换模型后，下次运行会跟着变）。
	ModelInherited bool `json:"model_inherited,omitempty"`
	// TaskUpdatedAt 是受理时任务的 updated_at（RFC3339Nano，只对 run/enable/
	// delete 这类「按库里配置执行」的动作填）：确认卡批准的是当时看到的配置，
	// 执行器在事务内比对同一水位，期间任务被改过就拒绝执行（TOCTOU）。
	TaskUpdatedAt string `json:"task_updated_at,omitempty"`
}

// resolveScheduleScope 建卡前解析 fs_schedule 写操作的范围与确认卡预览。
// create 的范围是本机器人的任务集合（ScopeID=bot.ID）；update/run 的范围是
// 单个任务，任务读不到就保持 ScopeID 为空（主线不建卡，按无法确定目标失败；
// 受理与执行之间任务被删的，执行期会以 40429 失败）。update 的预览按「未提供
// 则保留原值」合并 cron/timezone/model 后计算，让用户在卡上看到的是生效后的
// 配置而不是增量。list/delete/runs/enable/disable 等同步动作不设范围与预览。
func (s *BotService) resolveScheduleScope(bot *model.Bot, prepared *botPreparedCall, raw map[string]json.RawMessage) error {
	switch prepared.Action {
	case "create":
		prepared.ScopeKind = botScopeScheduleTaskCollection
		prepared.ScopeID = bot.ID
		prepared.FieldsConstraint = presentFields(raw, "name", "cron", "timezone", "instruction", "model", "needs_report")
		preview, err := s.schedulePreviewFromArgs(bot, raw)
		if err != nil {
			// 读不到用户有效时区就不建卡：执行期同样会在这里失败，先把卡片
			// 建出来只会得到一张指向必然失败调用的确认卡。
			return err
		}
		if preview == nil {
			return errs.New(errs.ErrInvalidParams.Code,
				"无法计算任务的生效配置，请检查 cron 与时区后再操作")
		}
		prepared.SchedulePreview = preview
	case "update":
		prepared.ScopeKind = botScopeScheduleTask
		prepared.FieldsConstraint = presentFields(raw, "name", "cron", "timezone", "instruction", "model", "needs_report")
		prepared.ScopeID = rawString(raw["task_id"])
		if !prepared.RequiresConfirmation {
			// 免确认 update（只改 name/cron/timezone）受理期不读库内任务：
			// 存量非法配置不该挡住这类同步修改（执行层会返回具体字段错误），
			// 没有确认卡的路径也不能记修订水位——否则受理到同步执行之间
			// 任务被 REST 启停或另一路写碰过 updated_at，同步 update 会以
			// 「任务在确认期间被修改」失败，而这条路径根本没有确认期间。
			return nil
		}
		task, err := s.scheduleTargetTask(bot, prepared.ScopeID)
		if err != nil {
			return err
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
		// 合并/校验失败时没有生效配置：不建确认卡（没有配置的确认等于没有确认，
		// 修订比对也会因为缺少受理水位被跳过）。
		if prepared.SchedulePreview == nil {
			return errs.New(errs.ErrInvalidParams.Code,
				"无法计算任务的生效配置（现有配置不合法），请先修正任务后再操作")
		}
	case "run", "enable", "delete":
		// 这三个动作执行时只按 task_id 读库里的任务，确认卡展示的是那一刻的
		// 配置；目标读不到（含瞬时读库错误）就当场失败，不留一张指向未知
		// 目标、或把瞬时错误说成「任务不存在」的确认卡。
		prepared.ScopeKind = botScopeScheduleTask
		task, err := s.scheduleTargetTask(bot, rawString(raw["task_id"]))
		if err != nil {
			return err
		}
		prepared.ScopeID = task.ID
		prepared.SchedulePreview = s.schedulePreviewFromTask(bot, schedulePreviewInput{}, task)
		if prepared.SchedulePreview == nil {
			if prepared.Action == "enable" {
				// enable 要按 cron 重算下次计划：配置不合法就必须先修。
				return errs.New(errs.ErrInvalidParams.Code,
					"无法计算任务的生效配置（现有配置不合法），请先修正任务后再操作")
			}
			// run/delete 的执行不解析 cron（run 只冻结任务快照、delete 只按 id
			// 删除），存量非法配置时仍允许受理；确认卡如实展示库里那一整份
			// 配置——只剩 task_id 的匿名卡会让用户在看不到指令的情况下批准
			// 一次「默认放行写操作」的执行，正是确认卡要防的盲批。
			prepared.SchedulePreview = scheduleRawTaskPreview(bot, task)
		}
	}
	return nil
}

// scheduleTargetTask 取确认动作的目标任务：不存在回 40429，其它读库错误回
// 内部错误（不合并成「目标不存在」）。
func (s *BotService) scheduleTargetTask(bot *model.Bot, taskID string) (*model.BotScheduleTask, error) {
	task, err := s.schedTasks.FindByIDAndBot(taskID, bot.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotScheduleTaskNotFound
		}
		return nil, errs.ErrInternal
	}
	return task, nil
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

// schedulePreviewFromArgs 为 create 计算预览。时区解析与执行期同一口径：
// 省略时区 = 归属用户的有效时区，且读库失败按失败关闭处理（不用回退时区
// 顶替）——回退值一旦写进快照，卡片上的下次运行和真正落地的不在同一个时区。
func (s *BotService) schedulePreviewFromArgs(bot *model.Bot, raw map[string]json.RawMessage) (*botScheduleCallPreview, error) {
	timezone := rawString(raw["timezone"])
	if strings.TrimSpace(timezone) == "" {
		resolved, err := s.botOwnerTimezoneOfErr(bot)
		if err != nil {
			return nil, err
		}
		timezone = resolved.name
	}
	return s.schedulePreviewFromTask(bot, schedulePreviewInput{
		Name:           rawString(raw["name"]),
		Cron:           rawString(raw["cron"]),
		Timezone:       timezone,
		Instruction:    rawString(raw["instruction"]),
		ModelPtr:       rawStringPtr(raw["model"]),
		NeedsReportPtr: rawBoolPtr(raw["needs_report"]),
	}, nil), nil
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
		ModelInherited: strings.TrimSpace(merged.Model) == "",
	}
	if task != nil {
		preview.TaskID = task.ID
		preview.TaskUpdatedAt = task.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if _, next, parseErr := parseScheduleCron(merged.CronExpression, merged.Timezone, time.Now().UTC()); parseErr == nil {
		loc := timeOrUTC(merged.Timezone)
		preview.NextRunAt = next.In(loc).Format("2006-01-02 15:04:05 MST")
		preview.NextRunAtUTC = next.UTC().Format(time.RFC3339)
	}
	return preview
}

// scheduleRawTaskPreview 用任务行的原始字段组兜底预览：合并/校验失败（存量
// 配置不合法）时 run/delete 仍允许受理，但确认卡必须如实展示将要执行/删除的
// 那一份配置，而不是一张只剩 task_id 的匿名卡。next_run_at 取库里存的计划
// （不再解析 cron——它可能本来就解析不了，那也是配置不合法的一部分）。
func scheduleRawTaskPreview(bot *model.Bot, task *model.BotScheduleTask) *botScheduleCallPreview {
	effective := strings.TrimSpace(task.Model)
	if effective == "" {
		effective = bot.Model
	}
	report := task.NeedsReport
	preview := &botScheduleCallPreview{
		TaskID: task.ID, Name: task.Name,
		Cron: task.CronExpression, Timezone: task.Timezone,
		Instruction: task.Instruction, NeedsReport: &report,
		EffectiveModel: effective,
		ModelInherited: strings.TrimSpace(task.Model) == "",
		TaskUpdatedAt:  task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if task.NextRunAt != nil {
		loc := timeOrUTC(task.Timezone)
		preview.NextRunAt = task.NextRunAt.In(loc).Format("2006-01-02 15:04:05 MST")
		preview.NextRunAtUTC = task.NextRunAt.UTC().Format(time.RFC3339)
	}
	return preview
}

// botScheduleAcceptedRevision 取受理时记下的任务修订水位（确认卡上展示的那一版）；
// 没有（旧调用或非确认动作）时为空串，表示不做修订比对。
func botScheduleAcceptedRevision(call *model.BotToolCall) string {
	var execCtx botCallExecContext
	if call.ExecutionContextJSON == "" {
		return ""
	}
	if err := json.Unmarshal([]byte(call.ExecutionContextJSON), &execCtx); err != nil {
		return ""
	}
	if execCtx.Schedule == nil {
		return ""
	}
	return execCtx.Schedule.TaskUpdatedAt
}

// botScheduleRevisionChanged 在事务内比对任务修订：确认卡批准的是受理时看到
// 的配置，run/enable/delete 执行时任务已被改过就拒绝（TOCTOU，与 fs_issue 的
// expected_updated_at 同一口径），让用户重新看一眼再决定，而不是静默按新配置
// 执行已批准的旧卡。
func botScheduleRevisionChanged(task *model.BotScheduleTask, expected string) error {
	if expected == "" || task == nil {
		return nil
	}
	if task.UpdatedAt.UTC().Format(time.RFC3339Nano) != expected {
		return errs.New(errs.ErrInvalidParams.Code,
			"任务在确认期间被修改，本次未执行：请重新查看任务配置后再操作")
	}
	return nil
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
	case "create", "update", "run", "enable", "delete":
	default:
		return nil, errs.New(errs.ErrInvalidParams.Code,
			"fs_schedule 只有 create/update/run/enable/delete 走异步确认路径: "+call.Action)
	}
	out, err := s.execScheduleTool(call)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
