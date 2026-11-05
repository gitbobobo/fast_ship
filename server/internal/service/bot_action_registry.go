package service

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
)

// 动作注册表（INT-195）：机器人每个工具动作（"工具/action" 字符串）的属性——
// 参数约束（必填/禁止字段）、执行入口、确认语义、写策略档位、围栏豁免、定时
// 任务会话放行与上下文投影、副作用标记、受理范围解析——在这里一处声明。新增
// 一个动作 = 加一条 spec，字段校验、同步/异步执行、确认、写策略、围栏、会话
// 与范围等行为系统立即正确识别；源码里不再保留按动作名分派的平行 switch/表。
//
// 各工具的 spec 组放在各自的适配文件里（bot_tools_project.go 等），本文件聚合
// 成包级注册表并提供查表入口。构建失败（重复注册、执行入口缺失等）按编程错误
// panic，与 schema 生成同口径——注册表错误不能带病启动。

// botConfirmKind 声明动作的确认分类。写动作（非 ReadOnly）必须显式声明，
// 构建时强制——这是旧版 botScheduleConfirmation「未分类动作默认需确认」
// fail-closed 护栏的结构化等价：漏声明在注册表构建时就 panic，而不是等到
// 运行时把一次未确认的写静默放行（INT-195 面板审查 R1）。
type botConfirmKind string

const (
	// confirmNever 同步免确认（如 disable 是收紧权限、可逆；记忆写靠乐观锁）。
	confirmNever botConfirmKind = "never"
	// confirmAlways 恒需确认：受理端按 RequiresConfirmation=true 走异步确认。
	confirmAlways botConfirmKind = "always"
	// confirmDynamic 按 Confirm 闭包按参数判定（fs_schedule update/run）。
	confirmDynamic botConfirmKind = "dynamic"
)

// botActionSpec 是当前工具集动作的单一声明点。
type botActionSpec struct {
	Tool, Action string
	// Description 是当前（v14）动作描述：生成 botToolDef.Actions 用，不进
	// wire 字节（schema 由参数结构体生成）。
	Description string
	Mode        model.BotToolExecutionMode
	// ReadOnly 标记纯读取动作：新输入到达时读类调用允许执行完，只有未开始的
	// 写操作才补 not_executed（usage 总览的读/写归组同源）。参数决定语义的
	// 只读（fs_schedule/run 的 dry_run=true 预览）用 ReadOnlyIf 表达。
	ReadOnly bool
	// Confirmation 是确认分类（写动作必填，见 botConfirmKind）；v14 的
	// botActionDef.RequiresConfirmation 由 confirmAlways 推导。confirmDynamic
	// 的动作 Confirm 必填，运行时按参数动态判定（命中时受理端改判异步待
	// 确认）；Confirm 返回的 error 是受理期参数校验，避免为一个必然执行失败
	// 的调用弹确认卡。
	Confirmation botConfirmKind
	Confirm      func(raw map[string]json.RawMessage) (bool, error)
	// SyncExecForbidden 显式声明「本动作不得有同步执行入口」，构建期强制
	//（SyncExec 非 nil 即 panic）。confirmAlways 的动作已由确认分类自动禁止；
	// 这个标志服务 confirmDynamic + 恒真闭包的动作（fs_schedule 的 create/
	// enable/delete）：它们运行时恒改判异步，但静态分类上与 update/run 同为
	// dynamic，护栏无法从分类推断——用标志把「不给绕过受理直接写留第二把
	// 钥匙」从约定变成可校验的声明（fast-ship-bot R1 Note）。
	SyncExecForbidden bool
	Required          []botRequiredField
	Forbidden         []string
	// SyncExec/AsyncExec 是执行入口，至少一者非 nil（fs_schedule update/run
	// 两者都有：确认分流是真实形态——受理端按参数动态改判异步，同步分支服务
	// 免确认参数；确认恒真的动作只声明 AsyncExec，同步分派保持旧白名单的
	// 拒绝语义，不给「绕过受理直接写」留第二把钥匙）。SyncExec 带 *model.Bot：
	// fs_memory/status 与 fs_schedule/status（v2 墓碑动作）的能力查询需要
	// bot.ToolsetVersion。注册表用方法表达式直接指向单动作方法（如
	// (*BotService).execIssueGet）。
	SyncExec  func(*BotService, *model.Bot, context.Context, *model.BotToolCall) (string, error)
	AsyncExec func(*BotService, context.Context, *model.BotToolCall) (interface{}, error)
	// 写策略：PolicyTier 是非 tier1 候选的基础档（空 = tier2）；PolicyRule 是
	// 覆盖本动作的 tier1 规则 ID；CandidateFunc 是动态候选（fs_thread/send 的
	// 「继续」模板），与 PolicyRule 并存——候选判定用 CandidateFunc，规则归属
	// （误判检测）用 PolicyRule。
	PolicyTier    string
	PolicyRule    string
	CandidateFunc func(*botPreparedCall) string
	// FenceExempt 豁免出不可信围栏：只有平台完全自有的元数据/回执才豁免
	//（记忆的路径与版本列表、帮助、停止/取消回执、state_set 的 ack）；带正文
	// 的读取与一切未声明动作默认进围栏（fail closed）。
	FenceExempt bool
	// ScheduleOK 放行定时任务会话（管理动作=false，默认拒绝）；ScheduleContext
	// 把动作纳入定时任务会话的上下文投影（state_get、fs_thread list）。
	ScheduleOK      bool
	ScheduleContext bool
	// NoSideEffect 标记无写副作用的动作（wait）：取消竞速与服务重启恢复据此
	// 不标「可能已写出」，结果带结构化中断 outcome。目前仅 wait 声明；中断
	// 结果构造（botWaitInterruptedOutcome）是 wait 模块专属——新增无副作用
	// 动作时需自带中断结果构造或扩展该 helper，不能盲用 wait 形态的 payload。
	NoSideEffect bool
	// MemoryWrite 标记记忆写动作：重启恢复据此判定「效果未知/已落库」并
	// 给出专用中断提示（fast-ship-bot R16，替代按动作名的平行表）。
	MemoryWrite bool
	// AcceptanceOnly 标记「仅受理钩子执行」的动作（wait）：受理即 running、
	// 由领域评估器收尾，永不进 worker 队列——无需 AsyncExec 与 PolicyTier
	//（占位假入口会让误入队的调用回一条与工具面矛盾的参数错误，快船 bot
	// R19）。校验：Mode=async、无任何执行入口、confirmNever。
	AcceptanceOnly bool
	// ReadOnlyIf 是参数相关的只读判定（fs_schedule/run 的 dry_run=true）。
	ReadOnlyIf func(*model.BotToolCall) bool
	// Scope 是受理范围解析（异步写动作声明；含 fs_thread 的执行上下文回填）。
	Scope func(*BotService, *model.Bot, *botPreparedCall, map[string]json.RawMessage, *botCallExecContext) error
}

// botActionSpecGroups 按工具聚合各适配文件声明的 spec 组；组内顺序即 v14 的
// 动作声明顺序（沿用既有发布顺序，不重排）。做成函数而非包级 var：工具面
// 构建（botToolRegistryV14）与执行器在同一依赖网里，var 形式会构成初始化
// 循环；注册表本体在首次访问时构建，NewBotService 启动期显式预构建
// （见 botActionSpecsAll），规格错误在进程启动即暴露。
func botActionSpecGroupsAll() map[string][]*botActionSpec {
	return map[string][]*botActionSpec{
		"fs_project":       botProjectActionSpecs(),
		"fs_issue":         botIssueActionSpecs(),
		"fs_thread":        botThreadActionSpecs(),
		"fs_environment":   botEnvironmentActionSpecs(),
		"fs_schedule":      botScheduleActionSpecs(),
		"fs_memory":        botMemoryActionSpecs(),
		"fs_memory_search": botMemorySearchActionSpecs(),
		"fs_help":          botHelpActionSpecs(),
		"fs_result":        botResultActionSpecs(),
		"fs_stop":          botStopActionSpecs(),
		"fs_cancel":        botCancelActionSpecs(),
	}
}

// botLegacyActionSpecs 是遗留动作墓碑：已不在当前工具集、但仍能被冻结工具集
// （v1–v2）走到行为分派的动作，按今日各表的原值声明，保证冻结会话的校验、
// 执行、确认与会话放行行为不变。墓碑不进 v14 的动作生成。
func botLegacyActionSpecs() []*botActionSpec {
	return []*botActionSpec{
		// fs_schedule/status 是 v2 冻结工具集里 list 的旧名（v3 起被真实任务管理
		// 动作取代，改名提示见 botRenamedActions）：ReadOnly、ScheduleOK、能力
		// 查询执行入口照抄今日各表；围栏豁免表里没有它，不豁免。
		{
			Tool: "fs_schedule", Action: "status",
			Description: "查询调度能力是否可用",
			Mode:        model.BotExecutionSync, ReadOnly: true, ScheduleOK: true,
			Forbidden: []string{"dry_run", "state"},
			SyncExec:  (*BotService).execCapabilityStatusAction,
		},
	}
}

var (
	botActionSpecsOnce sync.Once
	botActionSpecs     map[string]*botActionSpec
)

// botActionSpecsAll 返回包级注册表（键 "tool/action"），首次访问时构建；
// NewBotService 在启动路径显式调用一次，保证规格错误在进程启动即 panic
// 重复注册、键不完整或执行入口全空都按编程错误 panic（与 schema 生成同
// 口径，不能带病启动）。
func botActionSpecsAll() map[string]*botActionSpec {
	botActionSpecsOnce.Do(func() {
		botActionSpecs = botBuildActionSpecs()
	})
	return botActionSpecs
}

// botRequiresConfirmationOf 报告动作的静态确认标记（v14 生成 botActionDef
// 用）：confirmAlways 为 true，其余（never/dynamic/读动作）为 false。
// confirmDynamic 的动作运行时由 Confirm 在受理端动态改判。
func botRequiresConfirmationOf(spec *botActionSpec) bool {
	return spec.Confirmation == confirmAlways
}

// botValidateActionSpecEntry 是构建与测试注入共用的单条入口校验：键完整、
// 执行入口存在、内部一致性与交叉约束（botValidateActionSpec）。重复注册属
// 注册表聚合语义，仍由 botBuildActionSpecs 查表时检查。
func botValidateActionSpecEntry(spec *botActionSpec) {
	key := spec.Tool + "/" + spec.Action
	if spec.Tool == "" || spec.Action == "" {
		panic("bot action registry: spec 键不完整: " + key)
	}
	if spec.SyncExec == nil && spec.AsyncExec == nil && !spec.AcceptanceOnly {
		panic("bot action registry: 执行入口缺失 " + key)
	}
	botValidateActionSpec(spec)
}

// botValidateActionSpec 校验单条 spec 的内部一致性（编程错误直接 panic，
// 不能带病启动）：
//   - 写动作（非 ReadOnly）必须显式声明确认分类，零值不再等价于免确认
//     （旧 botScheduleConfirmation 的 fail-closed 护栏）；
//   - confirmDynamic 与 Confirm 闭包一一对应；
//   - 只读动作不得声明恒需确认（矛盾语义）。
func botValidateActionSpec(spec *botActionSpec) {
	key := spec.Tool + "/" + spec.Action
	switch spec.Confirmation {
	case "":
		if !spec.ReadOnly {
			panic("bot action registry: 写动作未声明确认分类 " + key)
		}
		if spec.Confirm != nil {
			panic("bot action registry: 未分类动作不得携带 Confirm 闭包 " + key)
		}
	case confirmDynamic:
		if spec.Confirm == nil {
			panic("bot action registry: confirmDynamic 缺少 Confirm 闭包 " + key)
		}
		if spec.ReadOnly {
			// 读动作永不确认：误声明的只读动作不得被 Confirm 改判异步待确认
			//（快船 bot R16，护栏覆盖「读动作永不确认」的另一半）。
			panic("bot action registry: 只读动作不得声明 confirmDynamic " + key)
		}
		// dynamic 意味着运行时可能改判异步：AsyncExec 必须存在，否则确认卡
		// 批准后 worker 才报「没有异步实现」（快船 bot R9 Note，与 Mode 零值
		// 同一故障家族的最后一个入口）。
		if spec.AsyncExec == nil {
			panic("bot action registry: confirmDynamic 缺少 AsyncExec 入口 " + key)
		}
	case confirmNever, confirmAlways:
		if spec.Confirm != nil {
			panic("bot action registry: 静态确认分类不得携带 Confirm 闭包 " + key)
		}
	default:
		panic("bot action registry: 未知确认分类 " + string(spec.Confirmation) + " " + key)
	}
	if spec.ReadOnly && spec.Confirmation == confirmAlways {
		panic("bot action registry: 只读动作不得恒需确认 " + key)
	}
	// confirmAlways 的动作不得携带 SyncExec：恒需确认意味着受理端必须走
	// 异步确认，同步直达入口就是绕过确认的第二把钥匙（面板 R2 指出的护栏缝）。
	// confirmDynamic + SyncExec 是合法形态（fs_schedule update/run 的免确认
	// 参数同步分支），不在禁止之列。
	if spec.Confirmation == confirmAlways && spec.SyncExec != nil {
		panic("bot action registry: 恒需确认的动作不得带同步执行入口 " + key)
	}
	if spec.SyncExecForbidden && spec.SyncExec != nil {
		panic("bot action registry: 已声明禁止同步入口的动作带了 SyncExec " + key)
	}
	// Mode 与执行入口/确认分类交叉校验（快船 bot R5）：错配不再等到运行时——
	// async 动作没有 AsyncExec 会在批准后的 worker 里才报「没有异步实现」；
	// confirmAlways 配 Mode=sync 会以同步形态落账、污染 closeUnstarted 语义。
	if spec.Mode == model.BotExecutionAsync && spec.AsyncExec == nil && !spec.AcceptanceOnly {
		panic("bot action registry: 异步动作缺少 AsyncExec 入口 " + key)
	}
	if spec.AcceptanceOnly {
		// 仅受理钩子动作的完整形态（wait）：running 由受理分支落，异步与
		// 同步执行入口、策略档都是死数据；worker 认领这类行是编程错误，
		// 构建期就拒绝其存在形态。
		if spec.AsyncExec != nil || spec.SyncExec != nil {
			panic("bot action registry: 仅受理钩子动作不得带执行入口 " + key)
		}
		if spec.Mode != model.BotExecutionAsync {
			panic("bot action registry: 仅受理钩子动作必须是异步形态 " + key)
		}
		if spec.Confirmation != confirmNever {
			panic("bot action registry: 仅受理钩子动作只能免确认 " + key)
		}
		if spec.PolicyTier != "" {
			panic("bot action registry: 仅受理钩子动作不得声明 PolicyTier（策略评估不可达） " + key)
		}
	}
	if spec.Confirmation == confirmAlways && spec.Mode != model.BotExecutionAsync {
		panic("bot action registry: 恒需确认的动作必须是异步形态 " + key)
	}
	// dynamic + sync 形态必须声明同步出路：Confirm 可能返回 false 的动作若
	// 既无 SyncExec 又未声明 SyncExecForbidden，免确认参数会在受理后执行时
	// 才报「同步工具不执行该动作」——同一错配的启动期前移（快船 bot R15）。
	if spec.Confirmation == confirmDynamic && spec.Mode == model.BotExecutionSync &&
		spec.SyncExec == nil && !spec.SyncExecForbidden {
		panic("bot action registry: dynamic 动作的同步形态必须声明 SyncExec 或 SyncExecForbidden " + key)
	}
	// Mode 必须显式声明且属枚举（快船 bot R8 Note）：零值受理后会凭空生成
	// 确认卡、批准后才在 worker 里报「没有异步实现」，错误暴露点后移。
	if spec.Mode != model.BotExecutionSync && spec.Mode != model.BotExecutionAsync {
		panic("bot action registry: Mode 缺失或未知 " + key)
	}
	// PolicyTier 只被异步受理的写策略评估消费（快船 bot R8）：async 动作必须
	// 显式声明 tier2/tier3——空值静默等价 tier2 会让新写动作在定时任务会话从
	// 「同步拒绝」降为「默认放行并自动执行」（fail open）；SyncExec-only 的动作
	// 永不进策略评估，声明 tier 反而是死数据，一并拒绝。
	switch {
	case spec.AsyncExec != nil:
		if spec.PolicyTier != "tier2" && spec.PolicyTier != "tier3" {
			panic("bot action registry: 异步动作必须显式声明的 PolicyTier（tier2/tier3） " + key)
		}
	default:
		if spec.PolicyTier != "" {
			panic("bot action registry: 仅同步的动作不得声明 PolicyTier（策略评估不可达） " + key)
		}
	}
}

func botBuildActionSpecs() map[string]*botActionSpec {
	out := make(map[string]*botActionSpec, 96)
	groups := make([][]*botActionSpec, 0, len(botActionSpecGroupsAll())+1)
	for _, group := range botActionSpecGroupsAll() {
		groups = append(groups, group)
	}
	groups = append(groups, botLegacyActionSpecs())
	for _, group := range groups {
		for _, spec := range group {
			key := spec.Tool + "/" + spec.Action
			if _, dup := out[key]; dup {
				panic("bot action registry: 重复注册 " + key)
			}
			botValidateActionSpecEntry(spec)
			out[key] = spec
		}
	}
	return out
}

// botActionSpecOf 查动作 spec；未注册动作返回 nil，各调用方按各自口径 fail
// closed（进围栏、按写处理、定时任务会话拒绝等），与旧平行表的 map miss
// 行为一致。
func botActionSpecOf(tool, action string) *botActionSpec {
	return botActionSpecsAll()[tool+"/"+action]
}

// 测试如需临时扩展注册表，直接写 botActionSpecsAll() 返回的 map 并在测试
// 结束时恢复原值（helper 在 bot_action_registry_test.go，带保存/恢复语义）；
// 生产代码不提供改表入口。

// botActionReadOnlyOf 是动作的静态只读标记；未知动作按写处理（fail closed）。
func botActionReadOnlyOf(tool, action string) bool {
	spec := botActionSpecOf(tool, action)
	return spec != nil && spec.ReadOnly
}

// botActionNoSideEffectOf 报告调用是否属于无写副作用的动作（wait）：取消竞速
// 与服务重启恢复据此不标「可能已写出」。
func botActionNoSideEffectOf(call *model.BotToolCall) bool {
	spec := botActionSpecOf(call.ToolName, call.Action)
	return spec != nil && spec.NoSideEffect
}

// botApplyActionConfirmation 在受理端应用注册表确认语义（botPrepareToolCall
// 调用）：Confirm 非 nil 的动作按参数动态判定，命中时改判异步待确认——平台
// 只有异步路径存在确认，执行结果以后台结果送达模型。error 是受理期参数校验。
// 规则不随工具集版本变化：RequiresConfirmation/Mode 不在 wire 字节里，冻结在
// 旧版本的会话同样立即生效。
func botApplyActionConfirmation(prepared *botPreparedCall, spec *botActionSpec, raw map[string]json.RawMessage) error {
	if spec == nil || spec.Confirm == nil {
		return nil
	}
	needs, err := spec.Confirm(raw)
	if err != nil {
		return err
	}
	if needs {
		prepared.Mode = model.BotExecutionAsync
		prepared.RequiresConfirmation = true
	}
	return nil
}

// botExecSyncAction 是同步执行的注册表分派：botExecuteSyncTool（主线循环）与
// 各聚合兼容入口共用；action 为空时退回 call.Action（与拆分前各 switch 的取
// 键口径一致）。无 spec 或动作没有同步实现时返回调用方给定的既有错误文案。
func (s *BotService) botExecSyncAction(ctx context.Context, bot *model.Bot, call *model.BotToolCall, action, legacyErr string) (string, error) {
	if action == "" {
		action = call.Action
	}
	spec := botActionSpecOf(call.ToolName, action)
	if spec == nil || spec.SyncExec == nil {
		return "", errs.New(errs.ErrInvalidParams.Code, legacyErr)
	}
	return spec.SyncExec(s, bot, ctx, call)
}
