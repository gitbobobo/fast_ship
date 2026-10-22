package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/invopop/jsonschema"
	"github.com/openai/openai-go/v2"
	"github.com/openai/openai-go/v2/packages/param"
	"github.com/openai/openai-go/v2/shared"
)

// 白名单工具：v3 固定 11 个工具、44 个 action（v2 为 11/38，v1 为 10/29，
// 均为独立冻结副本供存量会话在压缩边界前使用）。业务写操作为
// async + 用户确认/已有授权；同步读取与 fs_schedule 的同步动作（INT-103 起
// 记账，INT-136 起 create/敏感字段 update/非 dry_run run 改走异步确认）也记
// 调用账本。不做反射调用 service、不提供任意 RPC。参数使用专用结构体，
// action 在 schema 中列 enum，服务端再校验该 action 的必填、禁止字段。

type botActionDef struct {
	Name                 string
	Description          string
	Mode                 model.BotToolExecutionMode
	RequiresConfirmation bool
	// ReadOnly 标记纯读取动作：新输入到达时读类调用允许执行完，只有未开始的
	// 写操作才补 not_executed。同步执行的写动作（fs_memory 写、fs_schedule 管理）
	// 也属于写，不能只按 Mode 判断。
	ReadOnly bool
}

type botToolDef struct {
	Name        string
	Description string
	// ArgsType 生成 Parameters；进程内一次，不按机器人权限动态删减或排序。
	ArgsType   reflect.Type
	Parameters shared.FunctionParameters
	Actions    []botActionDef
}

// botArgsHelp / botArgsResult / botArgsStop / botArgsCancel 等零字段工具
// 共用空参数对象。
type botNoArgs struct{}

// botScheduleArgs 是 fs_schedule/fs_memory 在 v1/v2 冻结注册表里的占位参数
// 结构体；v3 起 fs_schedule 使用独立的参数结构体（v3/v4 冻结副本与 v5 当前
// 版本见 bot_args_frozen.go 与下方），此结构体只为存量会话的前缀稳定保留，
// 不得复用或修改。
type botScheduleArgs struct {
	Action string `json:"action" jsonschema:"enum=status,required"`
}

// botScheduleTaskArgs 是当前（v9）fs_schedule 的参数超集：schema 描述并集，
// 服务端按 action 校验必填/禁止字段。全部动作同步执行、立即生效。INT-121 起
// 新增 run（手动立即执行一次）与 dry_run（只预览）；INT-123 起新增
// state_get/state_set 与 state 参数（任务小状态，只有 state_set 接受它）；
// INT-138 起 timezone 的描述改为「省略即用户时区」（行为本身由服务端
// createScheduleTask 兜底，schema 只是同一事实的文字投影）。
// v7 及更早的冻结副本在 bot_args_frozen.go（botScheduleTaskArgsV5/V6/V7）与
// botScheduleTaskArgsFrozen。
type botScheduleTaskArgs struct {
	Action string `json:"action" jsonschema:"enum=list,enum=create,enum=update,enum=delete,enum=runs,enum=run,enum=enable,enum=disable,enum=state_get,enum=state_set,required"`
	// TaskID 是任务 ID（update/delete/runs/run/enable/disable 必填；create 禁止）。
	TaskID string `json:"task_id,omitempty" jsonschema:"description=任务 ID（update/delete/runs/run/enable/disable 必填）"`
	// Name 任务名（create 必填；update 提供即改）。
	Name string `json:"name,omitempty" jsonschema:"description=任务名（create 必填；update 可改）"`
	// Cron 标准五字段表达式 分 时 日 月 周（create 必填；update 提供即改）。
	Cron string `json:"cron,omitempty" jsonschema:"description=cron 表达式，标准五字段（分 时 日 月 周；create 必填；update 提供即改）"`
	// Timezone 是 IANA 名称；create 省略即用户时区（INT-138），update 传空
	// 保持原值，显式换时区传 UTC。
	Timezone string `json:"timezone,omitempty" jsonschema:"description=时区，IANA 名称如 Asia/Shanghai（可选，省略即用户时区；update 提供即改、不传保持原值）"`
	// Instruction 是每次运行交给机器人的任务指令（create 必填；update 提供即改）。
	Instruction string `json:"instruction,omitempty" jsonschema:"description=任务指令：每次运行交给你的目标说明（create 必填；update 可改）"`
	// Model 可选：省略则保持原值（update）/用机器人当前模型（create）；
	// 显式传空串清除覆盖，回到机器人当前模型。用指针区分「未提供」与
	// 「显式清空」，否则创建时指定的模型永远无法撤掉。
	Model *string `json:"model,omitempty" jsonschema:"description=运行使用的模型名（可选；省略则保持不变或跟随机器人当前模型，传空串清除覆盖）"`
	// NeedsReport 标记成功且非静默的运行是否也追加事件进主线。
	NeedsReport *bool `json:"needs_report,omitempty" jsonschema:"description=运行是否需要汇报（可选；true 时成功且非静默的运行也会通知主线）"`
	// DryRun 只配合 run 使用：true 时只预览任务与下次计划、不建运行记录不执行；
	// 任务忙碌时回执标 busy=true。其它动作不接受该字段（显式 false 也拒绝），
	// 避免「带预览标记仍写入」的误用。
	DryRun *bool `json:"dry_run,omitempty" jsonschema:"description=只配合 run：true 时只预览（任务信息、指令与下次计划），不建运行记录、不执行；任务已有未结束运行时 busy=true；其它动作携带 dry_run 即被拒绝（显式 false / null 亦然）"`
	// State 只配合 state_set（INT-123）：要写入任务小状态的任意 JSON 值，
	// 省略或 null 表示清空；其它动作不接受该字段。原始 JSON 字节在归一化后
	// 原样保留，避免大整数精度丢失。
	State *json.RawMessage `json:"state,omitempty" jsonschema:"description=只配合 state_set：要写入任务小状态的任意 JSON 值（≤8192 UTF-8 字节）；省略或 null 表示清空；其它动作携带 state 即被拒绝"`
	// Limit/Offset 是 runs/list 的分页参数。
	Limit  int `json:"limit,omitempty" jsonschema:"description=条数上限（runs 默认 20 上限 100；list 默认且最多 200）"`
	Offset int `json:"offset,omitempty" jsonschema:"description=分页偏移（runs/list，默认 0）"`
}

// NeedsReportBool 是 needs_report 的缺省读取（缺省 false）。
func (a *botScheduleTaskArgs) NeedsReportBool() bool {
	return a.NeedsReport != nil && *a.NeedsReport
}

// DryRunBool 是 dry_run 的缺省读取（缺省 false）。
func (a *botScheduleTaskArgs) DryRunBool() bool {
	return a.DryRun != nil && *a.DryRun
}

// botMemoryArgs 是 fs_memory 的参数超集（v2 起可用）。
type botMemoryArgs struct {
	Action  string  `json:"action" jsonschema:"enum=status,enum=list,enum=read,enum=create,enum=replace,enum=append,enum=delete,enum=history,enum=restore,required"`
	Path    string  `json:"path,omitempty" jsonschema:"description=记忆文件路径（USER.md / MEMORY.md / daily/YYYY-MM-DD.md；list 之外必填）"`
	Prefix  string  `json:"prefix,omitempty" jsonschema:"description=路径前缀过滤（list 可选，如 daily/）"`
	Content *string `json:"content,omitempty" jsonschema:"description=正文（create/replace/append 必填；提供空串即写空内容）"`
	// ExpectedVersion 是乐观并发版本（replace/append/delete/restore 必填）。
	ExpectedVersion *int64 `json:"expected_version,omitempty" jsonschema:"description=期望当前版本号（replace/append/delete/restore 必填）"`
	Version         *int64 `json:"version,omitempty" jsonschema:"description=目标版本号（restore 必填；read 可选读旧版本）"`
	// Offset/Limit 以 Unicode 字符计。
	Offset int `json:"offset,omitempty" jsonschema:"description=起始字符偏移（read，默认 0）"`
	Limit  int `json:"limit,omitempty" jsonschema:"description=条数或字符数上限（read 默认 4000 上限 16000；history 默认 20 上限 50）"`
}

// botMemorySearchArgs 是 fs_memory_search 的参数（v2 新增）。
type botMemorySearchArgs struct {
	Action     string `json:"action" jsonschema:"enum=search,required"`
	Query      string `json:"query" jsonschema:"description=检索关键词（必填；≥3 字符走全文索引，1-2 字符走子串匹配）"`
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"description=路径前缀过滤（可选，如 daily/）"`
	Limit      int    `json:"limit,omitempty" jsonschema:"description=命中上限（默认 10，最大 50）"`
	Offset     int    `json:"offset,omitempty" jsonschema:"description=分页偏移（默认 0）"`
}

type botHelpArgs struct{}

type botResultArgs struct {
	Action string `json:"action" jsonschema:"enum=read,required"`
	CallID string `json:"call_id" jsonschema:"description=要读取的调用 ID（必填，required）"`
	// Offset/Limit 以 Unicode 字符数计，避免切断 UTF-8。
	Offset int `json:"offset,omitempty" jsonschema:"description=起始字符偏移（默认 0）"`
	Limit  int `json:"limit,omitempty" jsonschema:"description=本次读取的字符数（默认 4000，上限 16000）"`
}

type botStopArgs struct{}

type botCancelArgs struct {
	Action string `json:"action" jsonschema:"enum=cancel,required"`
	CallID string `json:"call_id" jsonschema:"description=要取消的本机器人后台调用 ID（必填，required）"`
}

type botProjectArgs struct {
	Action        string  `json:"action" jsonschema:"enum=list,enum=get,enum=create,enum=update,required"`
	ID            string  `json:"id,omitempty" jsonschema:"description=项目 ID（get/update 必填）"`
	Page          int     `json:"page,omitempty" jsonschema:"description=页码（从 1 起，list 用）"`
	PageSize      int     `json:"page_size,omitempty" jsonschema:"description=页长（1-100，list 用）"`
	Name          string  `json:"name,omitempty" jsonschema:"description=项目名（create 必填；update 可改）"`
	Description   *string `json:"description,omitempty" jsonschema:"description=项目描述；update 提供空串即清空"`
	RepositoryURL string  `json:"repository_url,omitempty" jsonschema:"description=仓库 URL（http/ssh）"`
}

// botIssueArgs 是 fs_issue 的参数超集：schema 描述并集，服务端按 action
// 校验必填/禁止字段（botActionRules）。
// v3 起 action 枚举用 `enum=a,enum=b` 语法逐项声明（`enum=a,b` 只会生成
// 第一个取值）；v1/v2 的旧字节由 bot_args_frozen.go 的冻结结构体保留。
type botIssueArgs struct {
	Action string `json:"action" jsonschema:"enum=list,enum=get,enum=comments,enum=timeline,enum=create,enum=update,enum=set_workflow,enum=replace_checklist,enum=comment,required"`
	// list / create 用
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=Fast Ship 项目 ID（list/create 必填）"`
	// 读取与写操作共用
	IssueID  string `json:"issue_id,omitempty" jsonschema:"description=Issue ID（get/comments/timeline 及全部写操作必填）"`
	Page     int    `json:"page,omitempty" jsonschema:"description=页码（从 1 起）"`
	PageSize int    `json:"page_size,omitempty" jsonschema:"description=页长（1-100）"`
	State    string `json:"state,omitempty" jsonschema:"description=状态过滤（open/closed）或目标状态（update）"`
	Query    string `json:"query,omitempty" jsonschema:"description=关键词过滤"`
	Label    string `json:"label,omitempty" jsonschema:"description=标签过滤"`
	Source   string `json:"source,omitempty" jsonschema:"description=internal/github（list 过滤或 create 来源；create 缺省 internal）"`
	Workflow string `json:"workflow,omitempty" jsonschema:"description=工作流状态过滤"`
	Sort     string `json:"sort,omitempty" jsonschema:"description=排序"`
	// create / update 用
	Title       string    `json:"title,omitempty" jsonschema:"description=标题（create 必填；update 提供即替换）"`
	Body        *string   `json:"body,omitempty" jsonschema:"description=正文（Markdown）；create 缺省为空，update 提供即替换、省略即保留"`
	Labels      *[]string `json:"labels,omitempty" jsonschema:"description=标签整组替换（仅 GitHub）"`
	StateReason *string   `json:"state_reason,omitempty" jsonschema:"description=completed/not_planned/reopened（state=closed 时）"`
	// set_workflow 用
	WorkflowStatus string `json:"workflow_status,omitempty" jsonschema:"description=工作流状态（set_workflow 必填）"`
	// replace_checklist 用：整组替换，确认卡片会明确展示
	Items []IssueChecklistItemInput `json:"items,omitempty" jsonschema:"description=完整清单（replace_checklist 必填，整组替换）"`
	// comment 用
	CommentBody string `json:"comment_body,omitempty" jsonschema:"description=评论正文（comment 必填）"`
	// 写入保护（update）：先 get 读回的 issue.updated_at（RFC3339）。与当前值不符
	// 说明已被他人改动，服务端拒绝静默覆盖并把最新内容回给模型；省略即不检查。
	ExpectedUpdatedAt string `json:"expected_updated_at,omitempty" jsonschema:"description=写入前读到的 issue.updated_at（RFC3339，update 可选）；不符即拒绝静默覆盖"`
	// 写入保护（replace_checklist）：先 get 读回的 internal_meta.checklist_updated_at。
	// 省略即不检查。
	ExpectedChecklistUpdatedAt string `json:"expected_checklist_updated_at,omitempty" jsonschema:"description=写入前读到的 internal_meta.checklist_updated_at（RFC3339，replace_checklist 可选）；不符即拒绝静默覆盖"`
}

// botThreadArgs 是 fs_thread 的参数超集。v5（INT-120）起 list 真正支持
// env_id/query 过滤；v4 的旧标签由 bot_args_frozen.go 的 botThreadArgsV4 冻结。
type botThreadArgs struct {
	Action    string `json:"action" jsonschema:"enum=list,enum=get,enum=item,enum=composer_options,enum=workspace_options,enum=launch,enum=send,enum=stop,required"`
	EnvID     string `json:"env_id,omitempty" jsonschema:"description=T3 环境 ID（get/item/composer_options/workspace_options/launch/send/stop 必填；list 可选，只保留该环境的线程）"`
	ThreadID  string `json:"thread_id,omitempty" jsonschema:"description=T3 线程 ID（get/item/send/stop 必填）"`
	ItemID    string `json:"item_id,omitempty" jsonschema:"description=T3 线程条目 ID（item 必填；条目 ID 由 get 返回的 items[].id 提供）"`
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=T3 项目 ID（workspace_options/launch 必填；list 可选，只保留该项目的线程）"`
	Query     string `json:"query,omitempty" jsonschema:"description=关键词过滤（list 可选：线程 id/标题/项目名/项目 id/环境名，不区分大小写包含匹配；workspace_options 为引用名过滤）"`
	Cursor    *int   `json:"cursor,omitempty" jsonschema:"description=上一页返回的 next_cursor（workspace_options）"`
	Text      string `json:"text,omitempty" jsonschema:"description=消息正文（launch/send 必填）"`
	Title     string `json:"title,omitempty" jsonschema:"description=线程标题（launch 可选，缺省由 T3 生成）"`
	// list 过滤与分页：默认只看未归档线程（归档线程不属于巡检范围）。
	IncludeArchived *bool  `json:"include_archived,omitempty" jsonschema:"description=list 可选；true 时连归档线程一起返回（默认 false，只返回未归档线程）"`
	Settled         *bool  `json:"settled,omitempty" jsonschema:"description=list 可选；按 settled 过滤（true 只看已收进历史区的线程）"`
	Status          string `json:"status,omitempty" jsonschema:"description=list 可选；按运行态过滤（running/idle/waiting/archived/error 等，服务端原样比较）"`
	UpdatedAfter    string `json:"updated_after,omitempty" jsonschema:"description=list 可选；只返回 updated_at 晚于该 RFC3339 时间的线程"`
	Page            int    `json:"page,omitempty" jsonschema:"description=页码（从 1 起，list 用；过滤后稳定排序再切片）"`
	PageSize        int    `json:"page_size,omitempty" jsonschema:"description=页长（1-100，list 用，默认 50；服务端强制上限，不允许一次拉全量）"`
	// get 的条目分页：详情默认只回最近若干条，完整历史按 offset 向前读。
	ItemLimit  int `json:"item_limit,omitempty" jsonschema:"description=get 可选；返回的线程条目条数（默认 50，上限 200）"`
	ItemOffset int `json:"item_offset,omitempty" jsonschema:"description=get 可选；条目偏移（从 0 起，配合 item_limit 向前翻完整历史）"`
}

type botEnvironmentArgs struct {
	Action string `json:"action" jsonschema:"enum=list,enum=projects,required"`
}

// 各版本注册表按固定顺序独立冻结为字面量；顺序即协议 tools 数组顺序。
// v1/v2 是历史版本的冻结副本——v3 变化（含 fs_schedule 及任何共享工具的
// 描述调整）不得影响它们，否则存量会话的前缀/tools_hash 失稳。v3/v4/v5
// 都从 botToolRegistryV4Literal 构造（每次调用返回新分配的对象），差异只在
// fs_thread 的参数结构体。

// botToolRegistryV4Literal 是工具面字面量：v3/v4/v5 共用同一份工具、动作与
// 描述，差异只在 fs_thread 的参数结构体（由各版本包装函数决定）。fs_schedule
// 自 INT-103 起开放 list/create/update/delete/runs/enable/disable，全部同步
// 执行；INT-110 修正 action 枚举并新增线程/Issue 字段。不要在此改工具级
// Description（它进 wire 字节，会让 v3/v4 的 tools_hash 失稳）。
func botToolRegistryV4Literal() []*botToolDef {
	return []*botToolDef{
		{
			Name:        "fs_project",
			Description: "操作 Fast Ship 项目：list/get 读取，create/update 为需确认的写操作（首期只开放名称、描述、仓库 URL）。",
			ArgsType:    reflect.TypeOf(botProjectArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出当前用户项目", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取项目详情", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建项目", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新项目（名称/描述/仓库 URL）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_issue",
			Description: "操作 Fast Ship Issue：list/get/comments/timeline 读取，create/update/set_workflow/replace_checklist/comment 为需确认的写操作。replace_checklist 是整组替换。",
			ArgsType:    reflect.TypeOf(botIssueArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "按项目列出 Issue", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取 Issue 详情", Mode: model.BotExecutionSync},
				{Name: "comments", Description: "读取 Issue 评论", Mode: model.BotExecutionSync},
				{Name: "timeline", Description: "读取 Issue 时间线", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建 Issue（内部或 GitHub）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新 Issue 标题/正文/状态/标签", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "set_workflow", Description: "设置工作流状态", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "replace_checklist", Description: "整组替换验收清单", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "comment", Description: "发表 Issue 评论", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_thread",
			Description: "操作已连接 T3 环境里的线程：list/get/item/composer_options/workspace_options 读取，launch/send/stop 为需确认的写操作。launch 只表示启动 RPC 受理，不代表远端完成。",
			ArgsType:    reflect.TypeOf(botThreadArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出全部环境的线程", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取线程详情", Mode: model.BotExecutionSync},
				{Name: "item", Description: "读取线程条目", Mode: model.BotExecutionSync},
				{Name: "composer_options", Description: "读取启动器选项", Mode: model.BotExecutionSync},
				{Name: "workspace_options", Description: "读取工作区/引用选项", Mode: model.BotExecutionSync},
				{Name: "launch", Description: "在新线程中启动 T3 任务", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "send", Description: "向既有线程发消息", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "stop", Description: "停止远端线程的当前运行", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_environment",
			Description: "读取已连接的 T3 环境与其项目。首期不开放配对、密钥代答与环境删除。",
			ArgsType:    reflect.TypeOf(botEnvironmentArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出已连接环境", Mode: model.BotExecutionSync},
				{Name: "projects", Description: "发现各环境的 T3 项目", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_schedule",
			Description: "管理本机器人绑定的定时任务：list/create/update/delete/runs/enable/disable，全部同步执行、立即生效。每次运行是独立会话，只带你的记忆与本任务指令，写操作默认放行（不弹确认）。用户只能查看与开关任务，创建/修改由你负责；创建或修改后要在对话里用中文告知用户任务名与执行时间。",
			ArgsType:    reflect.TypeOf(botScheduleTaskArgsV5{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出本机器人的定时任务", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建定时任务（name/cron/instruction 必填；时区可选默认 UTC）", Mode: model.BotExecutionSync},
				{Name: "update", Description: "修改定时任务（task_id 必填；提供即改）", Mode: model.BotExecutionSync},
				{Name: "delete", Description: "删除定时任务（task_id 必填；运行记录保留）", Mode: model.BotExecutionSync},
				{Name: "runs", Description: "查看任务最近运行记录（task_id 必填）", Mode: model.BotExecutionSync},
				{Name: "enable", Description: "启用任务并重算下次运行时间（task_id 必填）", Mode: model.BotExecutionSync},
				{Name: "disable", Description: "停用任务，不再触发（task_id 必填）", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_memory",
			Description: "操作本机器人的长期记忆文件：USER.md（用户偏好）、MEMORY.md（长期事实与决策）、daily/YYYY-MM-DD.md（每日笔记）。写入立即持久生效；list/read/history/restore 可查看与恢复历史版本。",
			ArgsType:    reflect.TypeOf(botMemoryArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询记忆能力状态与注入上限", Mode: model.BotExecutionSync},
				{Name: "list", Description: "列出记忆文件元数据（prefix 可选）", Mode: model.BotExecutionSync},
				{Name: "read", Description: "读取当前正文或指定版本（offset/limit 按 Unicode 字符）", Mode: model.BotExecutionSync},
				{Name: "create", Description: "新建记忆文件（已存在则冲突；墓碑可重建）", Mode: model.BotExecutionSync},
				{Name: "replace", Description: "整体替换正文（需 expected_version）", Mode: model.BotExecutionSync},
				{Name: "append", Description: "追加正文（需 expected_version）", Mode: model.BotExecutionSync},
				{Name: "delete", Description: "删除文件（写墓碑版本，历史保留；需 expected_version）", Mode: model.BotExecutionSync},
				{Name: "history", Description: "查看最近版本元数据", Mode: model.BotExecutionSync},
				{Name: "restore", Description: "把指定旧版本恢复为新当前版本（需 expected_version）", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_memory_search",
			Description: "在本机器人记忆文件中做关键词全文检索（SQLite FTS5 trigram）；返回路径、版本、片段与排名，正文用 fs_memory.read 取回。",
			ArgsType:    reflect.TypeOf(botMemorySearchArgs{}),
			Actions: []botActionDef{
				{Name: "search", Description: "关键词全文检索本机器人记忆", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_help",
			Description: "读取嵌入程序的工具使用说明。",
			ArgsType:    reflect.TypeOf(botHelpArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取使用说明", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_result",
			Description: "按 call_id 分页读取被截断的完整工具结果（offset/limit 以 Unicode 字符计）。",
			ArgsType:    reflect.TypeOf(botResultArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取完整结果", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_stop",
			Description: "结束当前主线运行（等效用户点击停止）。",
			ArgsType:    reflect.TypeOf(botStopArgs{}),
			Actions: []botActionDef{
				{Name: "stop", Description: "停止当前 run", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_cancel",
			Description: "取消本机器人的指定后台调用。",
			ArgsType:    reflect.TypeOf(botCancelArgs{}),
			Actions: []botActionDef{
				{Name: "cancel", Description: "取消后台调用", Mode: model.BotExecutionSync},
			},
		},
	}
}

// botToolRegistryV6 是 INT-123 起冻结的 v6 注册表：在 v5（INT-120）之上，
// fs_schedule 自 INT-121 起新增 run（手动立即执行一次，支持 dry_run 预览），
// 全部同步执行。v5 及更早版本是冻结副本——本注册表的任何变化（含共享工具的
// 描述调整）不得影响它们，否则存量会话的前缀/tools_hash 失稳。fs_schedule
// 参数用冻结副本 botScheduleTaskArgsV6：INT-123 的 state_get/state_set 只进
// v7，本版 wire 字节逐字节保留。
func botToolRegistryV6() []*botToolDef {
	tools := botToolRegistryV4Literal()
	for _, tool := range tools {
		if tool.Name != "fs_schedule" {
			continue
		}
		tool.Description = "管理本机器人绑定的定时任务：list/create/update/delete/runs/run/enable/disable，全部同步执行、立即生效。每次运行是独立会话，只带你的记忆与本任务指令，写操作默认放行（不弹确认）。用户只能查看与开关任务，创建/修改由你负责；创建或修改后要在对话里用中文告知用户任务名与执行时间。run 立即手动执行一次任务：显式触发就是一次执行、不要求任务启用；回执只表示已受理，终态与产出用 runs 查询；同一任务已有未结束运行时会被明确拒绝（不排队）；dry_run=true 只预览、不执行也不建运行记录。"
		tool.ArgsType = reflect.TypeOf(botScheduleTaskArgsV6{})
		tool.Actions = []botActionDef{
			{Name: "list", Description: "列出本机器人的定时任务", Mode: model.BotExecutionSync},
			{Name: "create", Description: "创建定时任务（name/cron/instruction 必填；时区可选默认 UTC）", Mode: model.BotExecutionSync},
			{Name: "update", Description: "修改定时任务（task_id 必填；提供即改）", Mode: model.BotExecutionSync},
			{Name: "delete", Description: "删除定时任务（task_id 必填；运行记录保留）", Mode: model.BotExecutionSync},
			{Name: "runs", Description: "查看任务最近运行记录（task_id 必填；trigger_source 区分 cron/manual）", Mode: model.BotExecutionSync},
			{Name: "run", Description: "立即手动执行一次任务（task_id 必填；不要求任务启用；回执只表示已受理；已有未结束运行时拒绝；dry_run=true 只预览）", Mode: model.BotExecutionSync},
			{Name: "enable", Description: "启用任务并重算下次运行时间（task_id 必填）", Mode: model.BotExecutionSync},
			{Name: "disable", Description: "停用任务，不再触发（task_id 必填）", Mode: model.BotExecutionSync},
		}
	}
	return tools
}

// botToolRegistryV7 是 INT-138 起冻结的 v7 注册表（INT-123）：在 v6 之上，
// fs_schedule 新增 state_get/state_set（任务跨运行保留的小状态）与 state 参数、
// 更新工具描述，共 10 个动作，全部同步执行。任务小状态只在 schedule run 内
// 可用（执行器按本次 run 判别，不收 task_id 参数）。v6 及更早版本是冻结副本。
// INT-138 的 timezone 描述变更只进 v8：参数结构体换用冻结副本
// botScheduleTaskArgsV7，本版 wire 字节逐字节保留（基线见 bot_int103_test.go）。
func botToolRegistryV7() []*botToolDef {
	tools := botToolRegistryV4Literal()
	for _, tool := range tools {
		if tool.Name != "fs_schedule" {
			continue
		}
		tool.Description = "管理本机器人绑定的定时任务：list/create/update/delete/runs/run/enable/disable，全部同步执行、立即生效。每次运行是独立会话，只带你的记忆与本任务指令，写操作默认放行（不弹确认）。用户只能查看与开关任务，创建/修改由你负责；创建或修改后要在对话里用中文告知用户任务名与执行时间。run 立即手动执行一次任务：显式触发就是一次执行、不要求任务启用；回执只表示已受理，终态与产出用 runs 查询；同一任务已有未结束运行时会被明确拒绝（不排队）；dry_run=true 只预览、不执行也不建运行记录。state_get/state_set 读写本任务跨运行保留的小状态（≤8192 UTF-8 字节）：只能在定时任务运行中使用、由本次运行的任务隐式决定，省略 state 即清空，适合存「上次已报的 ID」这类去重集合。"
		tool.ArgsType = reflect.TypeOf(botScheduleTaskArgsV7{})
		tool.Actions = []botActionDef{
			{Name: "list", Description: "列出本机器人的定时任务", Mode: model.BotExecutionSync},
			{Name: "create", Description: "创建定时任务（name/cron/instruction 必填；时区可选默认 UTC）", Mode: model.BotExecutionSync},
			{Name: "update", Description: "修改定时任务（task_id 必填；提供即改）", Mode: model.BotExecutionSync},
			{Name: "delete", Description: "删除定时任务（task_id 必填；运行记录保留）", Mode: model.BotExecutionSync},
			{Name: "runs", Description: "查看任务最近运行记录（task_id 必填；trigger_source 区分 cron/manual）", Mode: model.BotExecutionSync},
			{Name: "run", Description: "立即手动执行一次任务（task_id 必填；不要求任务启用；回执只表示已受理；已有未结束运行时拒绝；dry_run=true 只预览）", Mode: model.BotExecutionSync},
			{Name: "enable", Description: "启用任务并重算下次运行时间（task_id 必填）", Mode: model.BotExecutionSync},
			{Name: "disable", Description: "停用任务，不再触发（task_id 必填）", Mode: model.BotExecutionSync},
			{Name: "state_get", Description: "读取本任务跨运行保留的小状态（无参数；只能在定时任务运行中使用）", Mode: model.BotExecutionSync},
			{Name: "state_set", Description: "写入本任务的小状态（state 为任意 JSON 值，省略或 null 表示清空；只能在定时任务运行中使用）", Mode: model.BotExecutionSync},
		}
	}
	return tools
}

// botToolRegistryV8 是 INT-138 起冻结的 v8 注册表：在 v7 之上只改 fs_schedule
// 的描述——创建任务省略时区即用户时区（不再默认 UTC），并写明任务指令的
// 写法（目标、汇报条件、去重方式、无变化回静默哨兵）。动作集与参数字段不变；
// 参数结构体的 timezone 描述演进只进 v8（v7 用冻结副本 botScheduleTaskArgsV7，
// wire 字节不变）。v7 及更早版本是冻结副本——本注册表的任何变化（含共享工具
// 的描述调整）不得影响它们，否则存量会话的前缀/tools_hash 失稳。
func botToolRegistryV8() []*botToolDef {
	tools := botToolRegistryV7()
	for _, tool := range tools {
		if tool.Name != "fs_schedule" {
			continue
		}
		tool.Description = "管理本机器人绑定的定时任务：list/create/update/delete/runs/run/enable/disable，全部同步执行、立即生效。每次运行是独立会话，只带你的记忆与本任务指令，写操作默认放行（不弹确认）。用户只能查看与开关任务，创建/修改由你负责；创建或修改后要在对话里用中文告知用户任务名与执行时间。时区可选，省略即用户时区（不是 UTC），「每天早上 9 点」按用户所在地执行。run 立即手动执行一次任务：显式触发就是一次执行、不要求任务启用；回执只表示已受理，终态与产出用 runs 查询；同一任务已有未结束运行时会被明确拒绝（不排队）；dry_run=true 只预览、不执行也不建运行记录。state_get/state_set 读写本任务跨运行保留的小状态（≤8192 UTF-8 字节）：只能在定时任务运行中使用、由本次运行的任务隐式决定，省略 state 即清空。instruction 写「意图」而不是固定操作步骤：说清目标、什么情况才需要汇报、去重方式（用 state_get/state_set 记「上次已报的 ID」，只报新增）；没有需要告知的内容时只回复一行 [[SILENT]]；只有「新内容确实值得打断用户」的周期检查才开 needs_report=true。"
		// v8 用活跃参数结构体：timezone 的 jsonschema 描述随本版更新
		//（「默认 UTC」→「省略即用户时区」）；v7 的 wire 字节由冻结副本保留。
		tool.ArgsType = reflect.TypeOf(botScheduleTaskArgs{})
		tool.Actions = []botActionDef{
			{Name: "list", Description: "列出本机器人的定时任务", Mode: model.BotExecutionSync},
			{Name: "create", Description: "创建定时任务（name/cron/instruction 必填；时区可选，省略即用户时区）", Mode: model.BotExecutionSync},
			{Name: "update", Description: "修改定时任务（task_id 必填；提供即改）", Mode: model.BotExecutionSync},
			{Name: "delete", Description: "删除定时任务（task_id 必填；运行记录保留）", Mode: model.BotExecutionSync},
			{Name: "runs", Description: "查看任务最近运行记录（task_id 必填；trigger_source 区分 cron/manual）", Mode: model.BotExecutionSync},
			{Name: "run", Description: "立即手动执行一次任务（task_id 必填；不要求任务启用；回执只表示已受理；已有未结束运行时拒绝；dry_run=true 只预览）", Mode: model.BotExecutionSync},
			{Name: "enable", Description: "启用任务并重算下次运行时间（task_id 必填）", Mode: model.BotExecutionSync},
			{Name: "disable", Description: "停用任务，不再触发（task_id 必填）", Mode: model.BotExecutionSync},
			{Name: "state_get", Description: "读取本任务跨运行保留的小状态（无参数；只能在定时任务运行中使用）", Mode: model.BotExecutionSync},
			{Name: "state_set", Description: "写入本任务的小状态（state 为任意 JSON 值，省略或 null 表示清空；只能在定时任务运行中使用）", Mode: model.BotExecutionSync},
		}
	}
	return tools
}

// botToolRegistryV9 是当前发布注册表（INT-136）：在 v8 之上只改 fs_schedule
// 的工具与动作描述，说明确认语义——create、改动 instruction/needs_report/model
// 的 update、非 dry_run 的 run 需要用户确认、异步受理、结果以后台结果送达；
// 批准创建或修改任务等于授权该任务以后每次运行默认放行写操作；定时任务运行中
// 不能 create/update/run。动作清单与参数结构体不变（仍 10 个动作、共用
// botScheduleTaskArgs）。确认判定的事实源是 botScheduleConfirmation（版本
// 无关、在受理端生效），动作定义里的 Mode/RequiresConfirmation 不进 wire
// 字节、保持原样，v8 及更早版本是冻结副本——本注册表的任何变化（含共享工具
// 的描述调整）不得影响它们，否则存量会话的前缀/tools_hash 失稳。
func botToolRegistryV9() []*botToolDef {
	tools := botToolRegistryV8()
	for _, tool := range tools {
		if tool.Name != "fs_schedule" {
			continue
		}
		tool.Description = "管理本机器人绑定的定时任务：list/runs/disable 与只改 name/cron/timezone 的 update、dry_run=true 的 run 同步执行、立即生效；create、update 中改动 instruction/needs_report/model、run（dry_run=true 预览除外）、enable、delete 需要用户确认：调用先受理（回执 status=pending_confirmation），用户批准后在后台执行，结果以「平台后台结果」消息送达，不要把受理当成已执行。批准创建或修改任务等于授权该任务以后每次运行默认放行写操作；enable 等于重新启用一个以后每次运行都默认放行写操作的任务，delete 不可逆。定时任务运行中不能管理任务（create/update/run/enable/disable/delete 都会拒绝），请在主线对话里操作。每次运行是独立会话，只带你的记忆与本任务指令，写操作默认放行（不弹确认）。用户只能查看与开关任务，创建/修改由你负责；创建或修改后要在对话里用中文告知用户任务名与执行时间。时区可选，省略即用户时区（不是 UTC），「每天早上 9 点」按用户所在地执行。run 立即手动执行一次任务：显式触发就是一次执行、不要求任务启用；非 dry_run 的 run 同样需要确认，回执只表示已受理，终态与产出用 runs 查询；同一任务已有未结束运行时会被明确拒绝（不排队）；dry_run=true 只预览、不执行也不建运行记录。state_get/state_set 读写本任务跨运行保留的小状态（≤8192 UTF-8 字节）：只能在定时任务运行中使用、由本次运行的任务隐式决定，省略 state 即清空。instruction 写「意图」而不是固定操作步骤：说清目标、什么情况才需要汇报、去重方式（用 state_get/state_set 记「上次已报的 ID」，只报新增）；没有需要告知的内容时只回复一行 [[SILENT]]；只有「新内容确实值得打断用户」的周期检查才开 needs_report=true。"
		// v9 用活跃参数结构体：timezone 的 jsonschema 描述随 v8 更新，v7 的
		// wire 字节由冻结副本 botScheduleTaskArgsV7 保留。
		tool.ArgsType = reflect.TypeOf(botScheduleTaskArgs{})
		tool.Actions = []botActionDef{
			{Name: "list", Description: "列出本机器人的定时任务", Mode: model.BotExecutionSync},
			{Name: "create", Description: "创建定时任务（name/cron/instruction 必填；时区可选，省略即用户时区）。需要用户确认：受理后等待批准，批准即授权该任务以后每次运行默认放行写操作", Mode: model.BotExecutionSync},
			{Name: "update", Description: "修改定时任务（task_id 必填；提供即改）。只改 name/cron/timezone 同步生效；改动 instruction/needs_report/model 需要用户确认并异步受理，批准即授权该任务以后每次运行默认放行写操作", Mode: model.BotExecutionSync},
			{Name: "delete", Description: "删除定时任务（task_id 必填；运行记录保留）。需要用户确认：删除不可逆", Mode: model.BotExecutionSync},
			{Name: "runs", Description: "查看任务最近运行记录（task_id 必填；trigger_source 区分 cron/manual）", Mode: model.BotExecutionSync},
			{Name: "run", Description: "立即手动执行一次任务（task_id 必填；不要求任务启用；已有未结束运行时拒绝）。dry_run=true 只预览（同步返回）；非 dry_run 需要用户确认、异步受理，结果以后台结果送达", Mode: model.BotExecutionSync},
			{Name: "enable", Description: "启用任务并重算下次运行时间（task_id 必填）。需要用户确认：启用等于重新武装一个以后每次运行默认放行写操作的任务", Mode: model.BotExecutionSync},
			{Name: "disable", Description: "停用任务，不再触发（task_id 必填）", Mode: model.BotExecutionSync},
			{Name: "state_get", Description: "读取本任务跨运行保留的小状态（无参数；只能在定时任务运行中使用）", Mode: model.BotExecutionSync},
			{Name: "state_set", Description: "写入本任务的小状态（state 为任意 JSON 值，省略或 null 表示清空；只能在定时任务运行中使用）", Mode: model.BotExecutionSync},
		}
	}
	return tools
}

// botToolRegistryV3 是 INT-103 冻结的 v3 注册表。工具/动作/描述与 v4 完全
// 相同，只有参数结构体不同：用冻结副本，保证已发布 v3 会话的 tools_hash 不
// 因 INT-110 的 schema 修正而回退。
func botToolRegistryV3() []*botToolDef {
	tools := botToolRegistryV4Literal()
	botUseFrozenArgs(3, tools)
	return tools
}

// botToolRegistryV4 是 INT-120 起冻结的 v4 注册表：工具/动作与 v5 相同，
// fs_thread 用 INT-110 时的冻结参数结构体（env_id/query 的 list 语义与描述
// 是 v5 才修正的），保证已发布 v4 会话的 tools_hash 不回退。
func botToolRegistryV4() []*botToolDef {
	tools := botToolRegistryV4Literal()
	botUseFrozenArgs(4, tools)
	return tools
}

// botToolRegistryV5 是当前发布注册表（INT-120）：工具/动作与 v4 相同，只有
// fs_thread 的参数结构体演进为最新形态（env_id/query 支持 list 过滤、描述
// 修正）。v4 自己的冻结副本由 botUseFrozenArgs(4) 换上，两者互不影响。
func botToolRegistryV5() []*botToolDef {
	return botToolRegistryV4Literal()
}

// botToolRegistryV2 是 INT-102 冻结的 v2 注册表：fs_schedule 仍是 status
// 占位（available:false），fs_memory 全量开放并新增 fs_memory_search。
// 存量会话在压缩边界/重启前的构建必须使用与本版完全一致的字节。
func botToolRegistryV2() []*botToolDef {
	tools := botToolRegistryV2Literal()
	botUseFrozenArgs(2, tools)
	return tools
}

func botToolRegistryV2Literal() []*botToolDef {
	return []*botToolDef{
		{
			Name:        "fs_project",
			Description: "操作 Fast Ship 项目：list/get 读取，create/update 为需确认的写操作（首期只开放名称、描述、仓库 URL）。",
			ArgsType:    reflect.TypeOf(botProjectArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出当前用户项目", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取项目详情", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建项目", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新项目（名称/描述/仓库 URL）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_issue",
			Description: "操作 Fast Ship Issue：list/get/comments/timeline 读取，create/update/set_workflow/replace_checklist/comment 为需确认的写操作。replace_checklist 是整组替换。",
			ArgsType:    reflect.TypeOf(botIssueArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "按项目列出 Issue", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取 Issue 详情", Mode: model.BotExecutionSync},
				{Name: "comments", Description: "读取 Issue 评论", Mode: model.BotExecutionSync},
				{Name: "timeline", Description: "读取 Issue 时间线", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建 Issue（内部或 GitHub）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新 Issue 标题/正文/状态/标签", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "set_workflow", Description: "设置工作流状态", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "replace_checklist", Description: "整组替换验收清单", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "comment", Description: "发表 Issue 评论", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_thread",
			Description: "操作已连接 T3 环境里的线程：list/get/item/composer_options/workspace_options 读取，launch/send/stop 为需确认的写操作。launch 只表示启动 RPC 受理，不代表远端完成。",
			ArgsType:    reflect.TypeOf(botThreadArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出全部环境的线程", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取线程详情", Mode: model.BotExecutionSync},
				{Name: "item", Description: "读取线程条目", Mode: model.BotExecutionSync},
				{Name: "composer_options", Description: "读取启动器选项", Mode: model.BotExecutionSync},
				{Name: "workspace_options", Description: "读取工作区/引用选项", Mode: model.BotExecutionSync},
				{Name: "launch", Description: "在新线程中启动 T3 任务", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "send", Description: "向既有线程发消息", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "stop", Description: "停止远端线程的当前运行", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_environment",
			Description: "读取已连接的 T3 环境与其项目。首期不开放配对、密钥代答与环境删除。",
			ArgsType:    reflect.TypeOf(botEnvironmentArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出已连接环境", Mode: model.BotExecutionSync},
				{Name: "projects", Description: "发现各环境的 T3 项目", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_schedule",
			Description: "定时调度能力状态查询（首期不可用）。",
			ArgsType:    reflect.TypeOf(botScheduleArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询调度能力是否可用", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_memory",
			Description: "操作本机器人的长期记忆文件：USER.md（用户偏好）、MEMORY.md（长期事实与决策）、daily/YYYY-MM-DD.md（每日笔记）。写入立即持久生效；list/read/history/restore 可查看与恢复历史版本。",
			ArgsType:    reflect.TypeOf(botMemoryArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询记忆能力状态与注入上限", Mode: model.BotExecutionSync},
				{Name: "list", Description: "列出记忆文件元数据（prefix 可选）", Mode: model.BotExecutionSync},
				{Name: "read", Description: "读取当前正文或指定版本（offset/limit 按 Unicode 字符）", Mode: model.BotExecutionSync},
				{Name: "create", Description: "新建记忆文件（已存在则冲突；墓碑可重建）", Mode: model.BotExecutionSync},
				{Name: "replace", Description: "整体替换正文（需 expected_version）", Mode: model.BotExecutionSync},
				{Name: "append", Description: "追加正文（需 expected_version）", Mode: model.BotExecutionSync},
				{Name: "delete", Description: "删除文件（写墓碑版本，历史保留；需 expected_version）", Mode: model.BotExecutionSync},
				{Name: "history", Description: "查看最近版本元数据", Mode: model.BotExecutionSync},
				{Name: "restore", Description: "把指定旧版本恢复为新当前版本（需 expected_version）", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_memory_search",
			Description: "在本机器人记忆文件中做关键词全文检索（SQLite FTS5 trigram）；返回路径、版本、片段与排名，正文用 fs_memory.read 取回。",
			ArgsType:    reflect.TypeOf(botMemorySearchArgs{}),
			Actions: []botActionDef{
				{Name: "search", Description: "关键词全文检索本机器人记忆", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_help",
			Description: "读取嵌入程序的工具使用说明。",
			ArgsType:    reflect.TypeOf(botHelpArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取使用说明", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_result",
			Description: "按 call_id 分页读取被截断的完整工具结果（offset/limit 以 Unicode 字符计）。",
			ArgsType:    reflect.TypeOf(botResultArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取完整结果", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_stop",
			Description: "结束当前主线运行（等效用户点击停止）。",
			ArgsType:    reflect.TypeOf(botStopArgs{}),
			Actions: []botActionDef{
				{Name: "stop", Description: "停止当前 run", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_cancel",
			Description: "取消本机器人的指定后台调用。",
			ArgsType:    reflect.TypeOf(botCancelArgs{}),
			Actions: []botActionDef{
				{Name: "cancel", Description: "取消后台调用", Mode: model.BotExecutionSync},
			},
		},
	}
}

// botToolRegistryV1 是 INT-101 冻结的 v1 注册表：fs_memory/fs_schedule 均为
// 占位（available:false），无 fs_memory_search。存量会话在压缩边界/重启前
// 的构建必须使用与本版完全一致的字节（前缀稳定）。
func botToolRegistryV1() []*botToolDef {
	tools := botToolRegistryV1Literal()
	botUseFrozenArgs(1, tools)
	return tools
}

func botToolRegistryV1Literal() []*botToolDef {
	return []*botToolDef{
		{
			Name:        "fs_project",
			Description: "操作 Fast Ship 项目：list/get 读取，create/update 为需确认的写操作（首期只开放名称、描述、仓库 URL）。",
			ArgsType:    reflect.TypeOf(botProjectArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出当前用户项目", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取项目详情", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建项目", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新项目（名称/描述/仓库 URL）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_issue",
			Description: "操作 Fast Ship Issue：list/get/comments/timeline 读取，create/update/set_workflow/replace_checklist/comment 为需确认的写操作。replace_checklist 是整组替换。",
			ArgsType:    reflect.TypeOf(botIssueArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "按项目列出 Issue", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取 Issue 详情", Mode: model.BotExecutionSync},
				{Name: "comments", Description: "读取 Issue 评论", Mode: model.BotExecutionSync},
				{Name: "timeline", Description: "读取 Issue 时间线", Mode: model.BotExecutionSync},
				{Name: "create", Description: "创建 Issue（内部或 GitHub）", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "update", Description: "更新 Issue 标题/正文/状态/标签", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "set_workflow", Description: "设置工作流状态", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "replace_checklist", Description: "整组替换验收清单", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "comment", Description: "发表 Issue 评论", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_thread",
			Description: "操作已连接 T3 环境里的线程：list/get/item/composer_options/workspace_options 读取，launch/send/stop 为需确认的写操作。launch 只表示启动 RPC 受理，不代表远端完成。",
			ArgsType:    reflect.TypeOf(botThreadArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出全部环境的线程", Mode: model.BotExecutionSync},
				{Name: "get", Description: "读取线程详情", Mode: model.BotExecutionSync},
				{Name: "item", Description: "读取线程条目", Mode: model.BotExecutionSync},
				{Name: "composer_options", Description: "读取启动器选项", Mode: model.BotExecutionSync},
				{Name: "workspace_options", Description: "读取工作区/引用选项", Mode: model.BotExecutionSync},
				{Name: "launch", Description: "在新线程中启动 T3 任务", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "send", Description: "向既有线程发消息", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
				{Name: "stop", Description: "停止远端线程的当前运行", Mode: model.BotExecutionAsync, RequiresConfirmation: true},
			},
		},
		{
			Name:        "fs_environment",
			Description: "读取已连接的 T3 环境与其项目。首期不开放配对、密钥代答与环境删除。",
			ArgsType:    reflect.TypeOf(botEnvironmentArgs{}),
			Actions: []botActionDef{
				{Name: "list", Description: "列出已连接环境", Mode: model.BotExecutionSync},
				{Name: "projects", Description: "发现各环境的 T3 项目", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_schedule",
			Description: "定时调度能力状态查询（首期不可用）。",
			ArgsType:    reflect.TypeOf(botScheduleArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询调度能力是否可用", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_memory",
			Description: "跨会话记忆能力状态查询（首期不可用）。",
			ArgsType:    reflect.TypeOf(botScheduleArgs{}),
			Actions: []botActionDef{
				{Name: "status", Description: "查询记忆能力是否可用", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_help",
			Description: "读取嵌入程序的工具使用说明。",
			ArgsType:    reflect.TypeOf(botHelpArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取使用说明", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_result",
			Description: "按 call_id 分页读取被截断的完整工具结果（offset/limit 以 Unicode 字符计）。",
			ArgsType:    reflect.TypeOf(botResultArgs{}),
			Actions: []botActionDef{
				{Name: "read", Description: "读取完整结果", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_stop",
			Description: "结束当前主线运行（等效用户点击停止）。",
			ArgsType:    reflect.TypeOf(botStopArgs{}),
			Actions: []botActionDef{
				{Name: "stop", Description: "停止当前 run", Mode: model.BotExecutionSync},
			},
		},
		{
			Name:        "fs_cancel",
			Description: "取消本机器人的指定后台调用。",
			ArgsType:    reflect.TypeOf(botCancelArgs{}),
			Actions: []botActionDef{
				{Name: "cancel", Description: "取消后台调用", Mode: model.BotExecutionSync},
			},
		},
	}
}

var (
	botToolsetsMu   sync.Mutex
	botToolsetsByID = map[int64]*botToolsetHolder{}
)

type botToolsetHolder struct {
	version  int64
	tools    []*botToolDef
	byName   map[string]*botToolDef
	byAction map[string]botActionDef
	schemas  []openai.ChatCompletionToolUnionParam
	// hash / bytes 是线上完整 tools 数组实际字节的 SHA-256 与长度——
	// tools_hash 必须对发送给模型的 tools 数组序列化结果计算，不能只对
	// 逐工具参数 schema 计算（版本号也不能代替内容哈希）。
	hash  string
	bytes int
}

// botToolsetOf 懒加载进程级工具集（当前发布版本）；schema 生成失败属于
// 编程错误，直接 panic。
func botToolsetOf() *botToolsetHolder {
	return botToolsetVersionOf(botToolsetVersion)
}

// botToolsetVersionOf 取指定发布版本的工具集；未知版本回退当前版本。
// 冻结版本供存量会话在压缩边界/重启前继续构建相同字节。
func botToolsetVersionOf(version int64) *botToolsetHolder {
	if version <= 0 || version > botToolsetVersion {
		version = botToolsetVersion
	}
	botToolsetsMu.Lock()
	defer botToolsetsMu.Unlock()
	if holder, ok := botToolsetsByID[version]; ok {
		return holder
	}
	var tools []*botToolDef
	switch version {
	case 1:
		tools = botToolRegistryV1()
	case 2:
		tools = botToolRegistryV2()
	case 3:
		tools = botToolRegistryV3()
	case 4:
		tools = botToolRegistryV4()
	case 5:
		tools = botToolRegistryV5()
	case 6:
		tools = botToolRegistryV6()
	case 7:
		tools = botToolRegistryV7()
	case 8:
		tools = botToolRegistryV8()
	default:
		tools = botToolRegistryV9()
	}
	holder := botBuildToolset(version, tools)
	botToolsetsByID[version] = holder
	return holder
}

func botBuildToolset(version int64, tools []*botToolDef) *botToolsetHolder {
	holder := &botToolsetHolder{
		version:  version,
		tools:    tools,
		byName:   map[string]*botToolDef{},
		byAction: map[string]botActionDef{},
	}
	reflector := &jsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
		ExpandedStruct: true,
	}
	for _, tool := range tools {
		schema := reflector.ReflectFromType(tool.ArgsType)
		schema.ID = ""
		schema.Version = ""
		schema.Definitions = nil
		schema.Description = tool.Description
		raw, err := schema.MarshalJSON()
		if err != nil {
			panic(fmt.Sprintf("bot tool schema %s: %v", tool.Name, err))
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			panic(fmt.Sprintf("bot tool schema %s: %v", tool.Name, err))
		}
		if version >= botConditionalSchemaVersion {
			botAnnotateActionRequirements(tool, parsed)
		}
		tool.Parameters = shared.FunctionParameters(parsed)
		holder.byName[tool.Name] = tool
		for i := range tool.Actions {
			action := tool.Actions[i]
			action.ReadOnly = botReadOnlyActions[tool.Name+"/"+action.Name]
			tool.Actions[i] = action
			holder.byAction[tool.Name+"/"+action.Name] = action
		}
		holder.schemas = append(holder.schemas, openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        tool.Name,
					Description: param.NewOpt(tool.Description),
					Parameters:  tool.Parameters,
				},
			},
		})
	}
	wire, err := json.Marshal(holder.schemas)
	if err != nil {
		panic(fmt.Sprintf("bot toolset wire marshal: %v", err))
	}
	sum := sha256.Sum256(wire)
	holder.hash = hex.EncodeToString(sum[:])
	holder.bytes = len(wire)
	return holder
}

// botUseFrozenArgs 把已发布版本的 schema 参数换回冻结结构体：活跃版本演进
// 后（v4 修正枚举、v5 修正 fs_thread 的 list 语义与描述），冻结副本保证已
// 发布版本的 tools_hash 不回退。v1 的 fs_memory 是 status 占位（用
// botScheduleArgs），不在此替换；v5 是当前版本，直接用最新结构体。
func botUseFrozenArgs(version int64, tools []*botToolDef) {
	frozen := map[string]reflect.Type{
		"fs_project":     reflect.TypeOf(botProjectArgsFrozen{}),
		"fs_issue":       reflect.TypeOf(botIssueArgsFrozen{}),
		"fs_thread":      reflect.TypeOf(botThreadArgsFrozen{}),
		"fs_environment": reflect.TypeOf(botEnvironmentArgsFrozen{}),
	}
	switch version {
	case 2:
		frozen["fs_memory"] = reflect.TypeOf(botMemoryArgsFrozen{})
	case 3:
		frozen["fs_memory"] = reflect.TypeOf(botMemoryArgsFrozen{})
		frozen["fs_schedule"] = reflect.TypeOf(botScheduleTaskArgsFrozen{})
	case 4:
		// v4 已含 INT-110 修正后的枚举与字段，其余结构体与 v5 一致，只冻结
		// v5 改过标签的 fs_thread。再往后冻结时要在上面各自的 case 里显式
		// 列出与当前版本的差异——这里没有 default 兜底。
		frozen = map[string]reflect.Type{"fs_thread": reflect.TypeOf(botThreadArgsV4{})}
	}
	for _, tool := range tools {
		if typ, ok := frozen[tool.Name]; ok {
			tool.ArgsType = typ
		}
	}
}

// botConditionalSchemaVersion 是从此版本起把 per-action 必填字段写进 schema
// 描述的工具集版本。v1/v2/v3 的 wire 字节已冻结，不能注入。
const botConditionalSchemaVersion int64 = 4

// botReadOnlyActions 标记纯读取动作；未列出的动作按写处理。
var botReadOnlyActions = map[string]bool{
	"fs_project/list": true, "fs_project/get": true,
	"fs_issue/list": true, "fs_issue/get": true, "fs_issue/comments": true, "fs_issue/timeline": true,
	"fs_thread/list": true, "fs_thread/get": true, "fs_thread/item": true,
	"fs_thread/composer_options": true, "fs_thread/workspace_options": true,
	"fs_environment/list": true, "fs_environment/projects": true,
	"fs_schedule/list": true, "fs_schedule/runs": true, "fs_schedule/status": true,
	// state_get 是任务小状态的只读动作（INT-123）；state_set 是同步写，不列。
	"fs_schedule/state_get": true,
	"fs_memory/status":      true, "fs_memory/list": true, "fs_memory/read": true, "fs_memory/history": true,
	"fs_memory_search/search": true,
	"fs_help/read":            true, "fs_result/read": true,
}

// botAnnotateActionRequirements 把各动作的必填字段写进 action 字段的 schema
// 描述里（服务端校验的事实源是 botActionRules，这里只是同一份规则的机器可读
// 投影）。只注解该工具当前声明的动作：botActionRules 是全局事实源、会先于
// schema 发布新动作（如 v5 的 fs_schedule/run），冻结版本声明里没有的动作
// 不能被注出来，否则其 wire 字节失稳。不用 allOf/if-then 的原因：不少
// OpenAI 兼容后端根本不校验 JSON Schema，条件分支只带来每请求约 700 token
// 的固定开销，没有实际强制力；真正的强制在服务端。描述按 action 名排序，
// 保证 wire 字节稳定（tools_hash）。
func botAnnotateActionRequirements(tool *botToolDef, schema map[string]interface{}) {
	prefix := tool.Name + "/"
	declared := map[string]bool{}
	for i := range tool.Actions {
		declared[tool.Actions[i].Name] = true
	}
	actions := make([]string, 0, 8)
	for key, rule := range botActionRules {
		if !strings.HasPrefix(key, prefix) || len(rule.Required) == 0 {
			continue
		}
		if action := strings.TrimPrefix(key, prefix); declared[action] {
			actions = append(actions, action)
		}
	}
	if len(actions) == 0 {
		return
	}
	sort.Strings(actions)
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		names := make([]string, 0, len(botActionRules[prefix+action].Required))
		for _, field := range botActionRules[prefix+action].Required {
			names = append(names, field.Name)
		}
		parts = append(parts, action+"="+strings.Join(names, "\u3001"))
	}
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return
	}
	actionField, ok := properties["action"].(map[string]interface{})
	if !ok {
		return
	}
	actionField["description"] = "必填字段（服务端强制）：" + strings.Join(parts, "；")
}

// botToolsTokenEstimate 工具定义的 token 占用估算：chars/4（估算值，标注估算方式）。
func botToolsTokenEstimate(ts *botToolsetHolder) (int64, string) {
	return int64(ts.bytes) / 4, "chars/4"
}

// ---------- 调用准备：解析、规范化、校验、范围解析 ----------

// botRenamedActionError 是改名/移除动作过渡期错误：旧调用不偷偷转发到新
// 动作，回结构化提示让模型改用新名（帮助入口见 fs_help.read）。
type botRenamedActionError struct {
	Tool      string
	Old       string
	RenamedTo string
}

func (e *botRenamedActionError) Error() string {
	return fmt.Sprintf("工具 %s 的动作 %s 已改名为 %s，请改用新动作",
		e.Tool, e.Old, e.RenamedTo)
}

// botRenamedActions 是改名/移除动作的静态过渡映射；条目至少保留两个后续
// 发布版本。映射键为 tool/action 全名。
var botRenamedActions = map[string]string{
	// v2 起草期曾把检索放在 fs_memory 下，发布形态是独立工具；
	// 旧叫法返回明确改名提示而不是静默转发。
	"fs_memory/search": "fs_memory_search/search",
	// v3 起 fs_schedule 的 status 占位被真实任务管理动作取代。
	"fs_schedule/status": "fs_schedule/list",
}

// botToolActionErrorReply 面向模型的结构化错误正文（改名提示含目标与帮助入口）。
func botToolActionErrorReply(err error) string {
	var renamed *botRenamedActionError
	if errors.As(err, &renamed) {
		raw, _ := json.Marshal(map[string]interface{}{
			"error":      "action_renamed",
			"tool":       renamed.Tool,
			"old_action": renamed.Old,
			"renamed_to": renamed.RenamedTo,
			"help":       "fs_help action=read",
		})
		return string(raw)
	}
	return botFailureReplyText(err)
}

// botPreparedCall 是模型工具调用经服务端校验后的落库形态。
type botPreparedCall struct {
	ToolName             string
	Action               string
	ArgumentsJSON        string
	ArgumentsHash        string
	Mode                 model.BotToolExecutionMode
	RequiresConfirmation bool
	// Scope 是确认卡片与授权规则的骨架。
	ScopeKind     string
	ScopeID       string
	EnvironmentID *string
	// FieldsConstraint 是 update 类动作允许写入的字段集合（写进授权约束）。
	FieldsConstraint []string
	// ParamConstraint 是参数值级约束（如 source=github）：同一动作不同参数
	// 值的副作用不同，「总是允许」必须按值绑定，不能只看字段集合。
	ParamConstraint string
	// SchedulePreview 是 fs_schedule 确认卡的服务端预览（create/update/run
	// 受理时算好下次运行与生效模型）；resolveCallScope 把它镜像进执行上下文，
	// 不进 wire 字节。
	SchedulePreview *botScheduleCallPreview
}

// botPrepareToolCall 解析并校验一次模型工具调用；ts 是该会话冻结的工具集
// （不同会话版本可能不同）。任何失败都返回 AppError 或 botRenamedActionError，
// 由调用方以同步失败回复模型（不建确认卡片）。
func botPrepareToolCall(ts *botToolsetHolder, toolName, rawArguments string) (*botPreparedCall, error) {
	tool, ok := ts.byName[toolName]
	if !ok {
		return nil, errs.New(errs.ErrInvalidParams.Code, "未知工具: "+toolName)
	}

	// 拒绝未知字段：schema additionalProperties=false，这里做服务端兜底。
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawArguments), &rawMap); err != nil {
		return nil, errs.New(errs.ErrInvalidParams.Code, "工具参数不是有效 JSON 对象")
	}
	// 重复键必须在任何解析之前拒绝：map 解析取末次值、结构体解析对 null
	// 做 no-op 还原，同一载荷会得到两份不一致的解释（确认卡按末次 null
	// 显示「未提供」、执行按先前值生效），展示与执行必须同源。
	if err := botJSONTopLevelKeysUnique(rawArguments); err != nil {
		return nil, err
	}
	// 改名/移除动作过渡期：action 从原始参数探出（参数形态可能已不属于
	// 本工具声明的字段集，字段白名单会先一步拒绝），先查静态映射回结构
	// 化提示，不偷执行新动作。冻结工具集里仍存在的动作不受映射影响
	//（如 v2 会话的 fs_schedule/status）。
	var actionProbe struct {
		Action string `json:"action"`
	}
	if raw := rawMap["action"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &actionProbe.Action)
	}
	probedAction := strings.TrimSpace(actionProbe.Action)
	if target, renamed := botRenamedActions[toolName+"/"+probedAction]; renamed {
		if _, stillAvailable := ts.byAction[toolName+"/"+probedAction]; !stillAvailable {
			return nil, &botRenamedActionError{Tool: toolName, Old: probedAction, RenamedTo: target}
		}
	}
	knownKeys := botArgKeys(tool.ArgsType)
	for key := range rawMap {
		if !knownKeys[key] {
			return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("工具 %s 不接受参数 %s", toolName, key))
		}
	}

	// 规范化：重新按结构体序列化，字段顺序与缺省值固定。
	args := reflect.New(tool.ArgsType).Interface()
	if err := json.Unmarshal([]byte(rawArguments), args); err != nil {
		return nil, errs.New(errs.ErrInvalidParams.Code, "工具参数类型不符: "+err.Error())
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		return nil, errs.ErrInternal
	}
	sum := sha256.Sum256(normalized)

	prepared := &botPreparedCall{
		ToolName:      toolName,
		ArgumentsJSON: string(normalized),
		ArgumentsHash: hex.EncodeToString(sum[:]),
	}

	// 解出 action 并校验。
	actionVal := reflect.ValueOf(args).Elem().FieldByName("Action")
	if !actionVal.IsValid() {
		// 无 action 字段的工具：action 即工具唯一动作。
		if len(tool.Actions) != 1 {
			return nil, errs.ErrInternal
		}
		prepared.Action = tool.Actions[0].Name
	} else {
		prepared.Action = strings.TrimSpace(actionVal.String())
	}
	meta, ok := ts.byAction[toolName+"/"+prepared.Action]
	if !ok {
		// 改名/移除动作过渡期：先查静态映射回结构化提示，不偷执行新动作。
		if target, renamed := botRenamedActions[toolName+"/"+prepared.Action]; renamed {
			return nil, &botRenamedActionError{Tool: toolName, Old: prepared.Action, RenamedTo: target}
		}
		return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("工具 %s 不支持 action %s", toolName, prepared.Action))
	}
	prepared.Mode = meta.Mode
	prepared.RequiresConfirmation = meta.RequiresConfirmation

	if err := botValidateActionFields(toolName, prepared.Action, rawMap); err != nil {
		return nil, err
	}
	if toolName == "fs_schedule" {
		// 受理端判定 fs_schedule 确认（INT-136）：规则不随工具集版本变化，
		// 冻结在 v3–v7 的会话同样立即生效（RequiresConfirmation/Mode 不在
		// wire 字节里，不影响已冻结的 tools_hash）。
		needs, err := botScheduleConfirmation(prepared.Action, rawMap)
		if err != nil {
			return nil, err
		}
		if needs {
			// 平台只有异步路径存在确认：改判为异步待确认，执行结果以后台
			// 结果送达模型。
			prepared.Mode = model.BotExecutionAsync
			prepared.RequiresConfirmation = true
		}
	}
	return prepared, nil
}

// botJSONTopLevelKeysUnique 拒绝顶层重复键：Go 的 map 解析取末次值，
// 结构体解析按出现顺序赋值且对 null 做 no-op 还原——`{"x":"a","x":null}`
// 在 map 里是 null、在结构体里是 "a"，受理判定/确认卡按 map 解释、执行
// 按结构体解释，两份解释必须一致。非对象形态留给参数校验兜底。
func botJSONTopLevelKeysUnique(raw string) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil
	}
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return errs.New(errs.ErrInvalidParams.Code, "工具参数不是有效 JSON 对象")
		}
		key, _ := keyTok.(string)
		if seen[key] {
			return errs.New(errs.ErrInvalidParams.Code,
				fmt.Sprintf("工具参数包含重复字段 %s", key))
		}
		seen[key] = true
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return errs.New(errs.ErrInvalidParams.Code, "工具参数不是有效 JSON 对象")
		}
	}
	return nil
}

// botArgKeys 收集参数结构体声明的 JSON 键。
func botArgKeys(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" {
			out[name] = true
		}
	}
	return out
}

// botRequiredField 是一个必填字段；AllowEmpty 为 true 时只要求字段出现
// （如记忆正文允许写空串）。
type botRequiredField struct {
	Name       string
	AllowEmpty bool
}

// botActionRule 描述一个工具动作的字段约束：Required 是服务端校验与 v3
// 起的 schema 描述共同的事实源；Forbidden 是明确禁止出现的字段——只要出现在
// 原始参数里就拒（包括显式 null：归一化会把 null 变回「未提供」，不能让带预览
// 标记的调用在归一化后看起来没带）。
type botActionRule struct {
	Required  []botRequiredField
	Forbidden []string
}

func req(names ...string) []botRequiredField {
	out := make([]botRequiredField, 0, len(names))
	for _, name := range names {
		out = append(out, botRequiredField{Name: name})
	}
	return out
}

func reqAllowEmpty(names ...string) []botRequiredField {
	out := make([]botRequiredField, 0, len(names))
	for _, name := range names {
		out = append(out, botRequiredField{Name: name, AllowEmpty: true})
	}
	return out
}

// botActionRules 是「工具/动作 → 字段约束」的单一事实源：服务端校验与 v3
// 起的 schema 描述都由它生成，避免中文描述与机器校验各说各话。列出的动作
// 才被接受；未列出的动作一律返回 40001。
var botActionRules = map[string]botActionRule{
	"fs_project/list":   {},
	"fs_project/get":    {Required: req("id")},
	"fs_project/update": {Required: req("id")},
	"fs_project/create": {Required: req("name"), Forbidden: []string{"id"}},

	"fs_issue/list":              {Required: req("project_id")},
	"fs_issue/get":               {Required: req("issue_id")},
	"fs_issue/comments":          {Required: req("issue_id")},
	"fs_issue/timeline":          {Required: req("issue_id")},
	"fs_issue/update":            {Required: req("issue_id")},
	"fs_issue/set_workflow":      {Required: req("issue_id", "workflow_status")},
	"fs_issue/replace_checklist": {Required: req("issue_id", "items")},
	"fs_issue/comment":           {Required: req("issue_id", "comment_body")},
	"fs_issue/create":            {Required: req("project_id", "title")},

	"fs_thread/list":              {},
	"fs_thread/get":               {Required: req("env_id", "thread_id")},
	"fs_thread/item":              {Required: req("env_id", "thread_id", "item_id")},
	"fs_thread/composer_options":  {Required: req("env_id")},
	"fs_thread/workspace_options": {Required: req("env_id", "project_id")},
	"fs_thread/launch":            {Required: req("env_id", "project_id", "text")},
	"fs_thread/send":              {Required: req("env_id", "thread_id", "text")}, "fs_thread/stop": {Required: req("env_id", "thread_id")},

	"fs_environment/list":     {},
	"fs_environment/projects": {},

	// dry_run 只配合 run（INT-121 审查修正）：预览标记被其它动作的分支忽略
	// 时，模型会误以为带了标记就没副作用。显式 false 也拒绝——语义是「这个
	// 动作不接受 dry_run」，而不是「按不预览执行」。
	// state 只配合 state_set（INT-123）：其它动作携带即拒（含 null），避免
	// 「带状态参数仍被当成管理动作」的误用；state_get/state_set 的任务由本次
	// schedule run 隐式决定，不收 task_id。
	// run 只接受 task_id/dry_run（INT-136 审查）：执行器按 task_id 读库里的
	// 任务配置，模型传的 name/instruction/needs_report 等一概不生效——若放行，
	// 确认卡可以展示一份无害指令、实际跑的却是任务里存的另一份。
	"fs_schedule/status": {Forbidden: []string{"dry_run", "state"}},
	"fs_schedule/list":   {Forbidden: []string{"dry_run", "state"}},
	"fs_schedule/create": {Required: req("name", "cron", "instruction"), Forbidden: []string{"task_id", "dry_run", "state"}},
	"fs_schedule/update": {Required: req("task_id"), Forbidden: []string{"dry_run", "state", "limit", "offset"}},
	// delete 与 run/enable 同一口径：执行器只按 task_id 删，配置字段不生效，
	// 放行会让确认卡的原始参数与审计账本出现一段从未生效的「指令」。
	"fs_schedule/delete": {Required: req("task_id"), Forbidden: []string{
		"dry_run", "state", "limit", "offset", "name", "cron", "timezone", "instruction", "model", "needs_report",
	}},
	"fs_schedule/runs": {Required: req("task_id"), Forbidden: []string{"dry_run", "state"}},
	"fs_schedule/run": {Required: req("task_id"), Forbidden: []string{
		"state", "limit", "offset", "name", "cron", "timezone", "instruction", "model", "needs_report",
	}},
	// enable/delete 只按 task_id 改开关或删除，配置字段一概不生效——放行会让
	// 确认卡展示一份无害配置、实际按库里的另一份执行（与 run 同一理由）。
	"fs_schedule/enable": {Required: req("task_id"), Forbidden: []string{
		"dry_run", "state", "limit", "offset", "name", "cron", "timezone", "instruction", "model", "needs_report",
	}},
	// disable 与 run/enable/delete 同一口径：执行器只按 task_id 改开关，其它配置
	// 字段一律拒绝（放行会被静默丢弃，模型却以为改成功了）。
	"fs_schedule/disable": {Required: req("task_id"), Forbidden: []string{
		"dry_run", "state", "limit", "offset", "name", "cron", "timezone", "instruction", "model", "needs_report",
	}},
	"fs_schedule/state_get": {Forbidden: []string{"task_id", "dry_run", "state", "limit", "offset"}},
	// state_set 只收 state（其余字段——含分页——一律视为误用）。
	"fs_schedule/state_set": {Forbidden: []string{"task_id", "dry_run", "limit", "offset"}},

	"fs_memory/status":  {},
	"fs_memory/list":    {},
	"fs_memory/read":    {Required: req("path")},
	"fs_memory/history": {Required: req("path")},
	"fs_memory/create":  {Required: append(req("path"), reqAllowEmpty("content")...)},
	"fs_memory/replace": {Required: append(append(req("path"), reqAllowEmpty("content")...), req("expected_version")...)},
	"fs_memory/append":  {Required: append(append(req("path"), reqAllowEmpty("content")...), req("expected_version")...)},
	"fs_memory/delete":  {Required: req("path", "expected_version")},
	"fs_memory/restore": {Required: req("path", "version", "expected_version")},

	"fs_memory_search/search": {Required: req("query")},

	"fs_help/read":     {},
	"fs_stop/stop":     {},
	"fs_result/read":   {Required: req("call_id")},
	"fs_cancel/cancel": {Required: req("call_id")},
}

// botValidateActionFields 按工具+action 校验必填/禁止字段（botActionRules 是
// 唯一事实源；同一份规则也生成 action 字段的必填描述）。
func botValidateActionFields(toolName, action string, raw map[string]json.RawMessage) error {
	key := toolName + "/" + action
	rule, ok := botActionRules[key]
	if !ok {
		return errs.New(errs.ErrInvalidParams.Code, "不支持的工具动作: "+key)
	}
	has := func(field string) bool {
		v, ok := raw[field]
		return ok && string(v) != "null"
	}
	// isEmptyString 只对真正的字符串值判空：数字/数组/对象等必填字段只要求
	// 出现（否则 items、expected_version 这类非字符串字段会被误判为空）。
	isEmptyString := func(field string) bool {
		v, ok := raw[field]
		if !ok || string(v) == "null" {
			return true
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return false
		}
		return strings.TrimSpace(s) == ""
	}
	bad := func(msg string) error {
		return errs.New(errs.ErrInvalidParams.Code, msg)
	}
	for _, field := range rule.Required {
		if !has(field.Name) || (!field.AllowEmpty && isEmptyString(field.Name)) {
			return bad(toolName + " " + action + " 需要 " + field.Name)
		}
	}
	for _, field := range rule.Forbidden {
		// 存在即拒（含 null）：Forbidden 表达的是「这个动作不接受该字段」，
		// 不是「该字段不得有值」。归一化会丢弃 null，按 has() 判断会把
		// {"dry_run":null} 这类调用放行成真实写入。
		if _, ok := raw[field]; ok {
			return bad(toolName + " " + action + " 不接受 " + field)
		}
	}
	return nil
}

// ---------- 执行 ----------

// botExecuteSyncTool 在主线循环内执行同步工具：读取类、帮助、结果续读、停止
// 与取消。能力查询（fs_*/status）由 executeSyncCall 分派：available 需要本
// run 冻结的工具集水位，通用分派里拿不到。返回进入协议回复的内容（JSON 字符串）。
func (s *BotService) botExecuteSyncTool(ctx context.Context, call *model.BotToolCall) (string, error) {
	switch call.ToolName + "/" + call.Action {
	case "fs_project/list", "fs_project/get":
		return s.execProjectRead(ctx, call)
	case "fs_issue/list", "fs_issue/get", "fs_issue/comments", "fs_issue/timeline":
		return s.execIssueRead(ctx, call)
	case "fs_thread/list", "fs_thread/get", "fs_thread/item", "fs_thread/composer_options", "fs_thread/workspace_options":
		return s.execThreadRead(ctx, call)
	case "fs_environment/list", "fs_environment/projects":
		return s.execEnvironmentRead(ctx, call)
	case "fs_schedule/list", "fs_schedule/update",
		"fs_schedule/runs", "fs_schedule/disable",
		"fs_schedule/state_get", "fs_schedule/state_set":
		// 定时任务管理（INT-103，INT-121 加 run，INT-123 加任务小状态）：
		// 同步动作立即生效，与 fs_memory 写同类。INT-136 起 create、非 dry_run
		// 的 run、enable/delete 与改动 instruction/needs_report/model 的 update
		// 只能走异步确认路径（runAsyncTool → runScheduleWrite）；这里保留
		// update 的同步分支（只改 name/cron/timezone 仍同步）与 disable。
		return s.execScheduleTool(call)
	case "fs_schedule/run":
		// dry_run=true 的 run 是只读预览，保持同步返回（受理判定对它免确认）；
		// 非 dry_run 的 run 走到这里说明绕过了受理判定，显式拒绝而不是触发。
		var runArgs botScheduleTaskArgs
		if err := json.Unmarshal([]byte(call.ArgumentsJSON), &runArgs); err != nil || !runArgs.DryRunBool() {
			return "", errs.New(errs.ErrInvalidParams.Code, "非 dry_run 的 run 需要用户确认，只能异步执行；预览请显式携带 dry_run=true")
		}
		return s.execScheduleTool(call)
	case "fs_memory/list", "fs_memory/read", "fs_memory/create", "fs_memory/replace",
		"fs_memory/append", "fs_memory/delete", "fs_memory/history", "fs_memory/restore":
		return s.execMemoryTool(call)
	case "fs_memory_search/search":
		return s.execMemorySearch(call)
	case "fs_help/read":
		// 帮助文本包成 JSON：结果会以 json.RawMessage 透出到调用列表与
		// 快照，裸文本会让整个响应无法序列化。
		return marshalJSONString(map[string]string{"text": botHelpText}), nil
	case "fs_result/read":
		return s.execResultRead(call)
	case "fs_stop/stop":
		return s.execStopCurrentRun(call)
	case "fs_cancel/cancel":
		return s.execCancelOwnCall(call)
	default:
		return "", errs.New(errs.ErrInvalidParams.Code, "同步工具不执行该动作: "+call.ToolName+"/"+call.Action)
	}
}
