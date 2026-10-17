package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
// async + 用户确认/已有授权；同步读取与 fs_schedule（INT-103，全部同步）
// 也记调用账本。不做反射调用 service、不提供任意 RPC。参数使用专用结构体，
// action 在 schema 中列 enum，服务端再校验该 action 的必填、禁止字段。

type botActionDef struct {
	Name                 string
	Description          string
	Mode                 model.BotToolExecutionMode
	RequiresConfirmation bool
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
// 结构体；v3 起 fs_schedule 使用独立的 botScheduleTaskArgs，此结构体只为
// 存量会话的前缀稳定保留，不得复用或修改。
type botScheduleArgs struct {
	Action string `json:"action" jsonschema:"enum=status,required"`
}

// botScheduleTaskArgs 是 v3 fs_schedule 的参数超集：schema 描述并集，服务端
// 按 action 校验必填/禁止字段。全部动作同步执行、立即生效。
type botScheduleTaskArgs struct {
	Action string `json:"action" jsonschema:"enum=list,create,update,delete,runs,enable,disable,required"`
	// TaskID 是任务 ID（update/delete/runs/enable/disable 必填；create 禁止）。
	TaskID string `json:"task_id,omitempty" jsonschema:"description=任务 ID（update/delete/runs/enable/disable 必填）"`
	// Name 任务名（create 必填；update 提供即改）。
	Name string `json:"name,omitempty" jsonschema:"description=任务名（create 必填；update 可改）"`
	// Cron 标准五字段表达式 分 时 日 月 周（create 必填；update 提供即改）。
	Cron string `json:"cron,omitempty" jsonschema:"description=cron 表达式，标准五字段（分 时 日 月 周；create 必填；update 提供即改）"`
	// Timezone 是 IANA 名称；空串=UTC。update 传空保持原值，显式换时区传 UTC。
	Timezone string `json:"timezone,omitempty" jsonschema:"description=时区，IANA 名称如 Asia/Shanghai（可选，默认 UTC；update 提供即改）"`
	// Instruction 是每次运行交给机器人的任务指令（create 必填；update 提供即改）。
	Instruction string `json:"instruction,omitempty" jsonschema:"description=任务指令：每次运行交给你的目标说明（create 必填；update 可改）"`
	// Model 可选：省略则保持原值（update）/用机器人当前模型（create）；
	// 显式传空串清除覆盖，回到机器人当前模型。用指针区分「未提供」与
	// 「显式清空」，否则创建时指定的模型永远无法撤掉。
	Model *string `json:"model,omitempty" jsonschema:"description=运行使用的模型名（可选；省略则保持不变或跟随机器人当前模型，传空串清除覆盖）"`
	// NeedsReport 标记成功且非静默的运行是否也追加事件进主线。
	NeedsReport *bool `json:"needs_report,omitempty" jsonschema:"description=运行是否需要汇报（可选；true 时成功且非静默的运行也会通知主线）"`
	// Limit/Offset 是 runs/list 的分页参数。
	Limit  int `json:"limit,omitempty" jsonschema:"description=条数上限（runs 默认 20 上限 100；list 默认且最多 200）"`
	Offset int `json:"offset,omitempty" jsonschema:"description=分页偏移（runs/list，默认 0）"`
}

// NeedsReportBool 是 needs_report 的缺省读取（缺省 false）。
func (a *botScheduleTaskArgs) NeedsReportBool() bool {
	return a.NeedsReport != nil && *a.NeedsReport
}

// botMemoryArgs 是 fs_memory 的参数超集（v2 起可用）。
type botMemoryArgs struct {
	Action  string  `json:"action" jsonschema:"enum=status,list,read,create,replace,append,delete,history,restore,required"`
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
	Action        string  `json:"action" jsonschema:"enum=list,get,create,update,required"`
	ID            string  `json:"id,omitempty" jsonschema:"description=项目 ID（get/update 必填）"`
	Page          int     `json:"page,omitempty" jsonschema:"description=页码（从 1 起，list 用）"`
	PageSize      int     `json:"page_size,omitempty" jsonschema:"description=页长（1-100，list 用）"`
	Name          string  `json:"name,omitempty" jsonschema:"description=项目名（create 必填；update 可改）"`
	Description   *string `json:"description,omitempty" jsonschema:"description=项目描述；update 提供空串即清空"`
	RepositoryURL string  `json:"repository_url,omitempty" jsonschema:"description=仓库 URL（http/ssh）"`
}

// botIssueArgs 是 fs_issue 的参数超集：schema 描述并集，服务端按 action
// 校验必填/禁止字段（botValidateActionFields）。
type botIssueArgs struct {
	Action string `json:"action" jsonschema:"enum=list,get,comments,timeline,create,update,set_workflow,replace_checklist,comment,required"`
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
}

// botThreadArgs 是 fs_thread 的参数超集。
type botThreadArgs struct {
	Action    string `json:"action" jsonschema:"enum=list,get,item,composer_options,workspace_options,launch,send,stop,required"`
	EnvID     string `json:"env_id,omitempty" jsonschema:"description=T3 环境 ID（除 list 外必填）"`
	ThreadID  string `json:"thread_id,omitempty" jsonschema:"description=T3 线程 ID（get/item/send/stop 必填）"`
	ItemID    string `json:"item_id,omitempty" jsonschema:"description=T3 线程条目 ID（item 必填）"`
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=T3 项目 ID（workspace_options/launch 必填）"`
	Query     string `json:"query,omitempty" jsonschema:"description=引用名过滤（workspace_options）"`
	Cursor    *int   `json:"cursor,omitempty" jsonschema:"description=上一页返回的 next_cursor（workspace_options）"`
	Text      string `json:"text,omitempty" jsonschema:"description=消息正文（launch/send 必填）"`
	Title     string `json:"title,omitempty" jsonschema:"description=线程标题（launch 可选，缺省由 T3 生成）"`
}

type botEnvironmentArgs struct {
	Action string `json:"action" jsonschema:"enum=list,projects,required"`
}

// 各版本注册表按固定顺序独立冻结为字面量；顺序即协议 tools 数组顺序。
// v1/v2 是历史版本的冻结副本——v3 变化（含 fs_schedule 及任何共享工具的
// 描述调整）不得影响它们，否则存量会话的前缀/tools_hash 失稳。

// botToolRegistryV3 是当前发布注册表：fs_schedule 自 INT-103 起开放
// list/create/update/delete/runs/enable/disable，全部同步执行。
func botToolRegistryV3() []*botToolDef {
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
			ArgsType:    reflect.TypeOf(botScheduleTaskArgs{}),
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

// botToolRegistryV2 是 INT-102 冻结的 v2 注册表：fs_schedule 仍是 status
// 占位（available:false），fs_memory 全量开放并新增 fs_memory_search。
// 存量会话在压缩边界/重启前的构建必须使用与本版完全一致的字节。
func botToolRegistryV2() []*botToolDef {
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
	default:
		tools = botToolRegistryV3()
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
		tool.Parameters = shared.FunctionParameters(parsed)
		holder.byName[tool.Name] = tool
		for _, action := range tool.Actions {
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
	return prepared, nil
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

// botValidateActionFields 按工具+action 校验必填/禁止字段。
func botValidateActionFields(toolName, action string, raw map[string]json.RawMessage) error {
	has := func(key string) bool {
		v, ok := raw[key]
		return ok && string(v) != "null"
	}
	str := func(key string) string {
		var s string
		_ = json.Unmarshal(raw[key], &s)
		return strings.TrimSpace(s)
	}
	bad := func(msg string) error {
		return errs.New(errs.ErrInvalidParams.Code, msg)
	}
	switch toolName + "/" + action {
	case "fs_project/list":
	case "fs_project/get", "fs_project/update":
		if !has("id") {
			return bad("fs_project " + action + " 需要 id")
		}
	case "fs_project/create":
		if !has("name") || str("name") == "" {
			return bad("fs_project create 需要 name")
		}
		if has("id") {
			return bad("fs_project create 不接受 id")
		}
	case "fs_issue/list":
		if !has("project_id") {
			return bad("fs_issue list 需要 project_id")
		}
	case "fs_issue/get", "fs_issue/comments", "fs_issue/timeline",
		"fs_issue/update", "fs_issue/set_workflow", "fs_issue/replace_checklist", "fs_issue/comment":
		if !has("issue_id") {
			return bad("fs_issue " + action + " 需要 issue_id")
		}
		if action == "comment" && (!has("comment_body") || str("comment_body") == "") {
			return bad("fs_issue comment 需要 comment_body")
		}
		if action == "set_workflow" && !has("workflow_status") {
			return bad("fs_issue set_workflow 需要 workflow_status")
		}
		if action == "replace_checklist" && !has("items") {
			return bad("fs_issue replace_checklist 需要 items（整组替换）")
		}
	case "fs_issue/create":
		if !has("project_id") {
			return bad("fs_issue create 需要 project_id")
		}
		if !has("title") || str("title") == "" {
			return bad("fs_issue create 需要 title")
		}
	case "fs_thread/list":
	case "fs_thread/get", "fs_thread/item":
		if !has("env_id") || !has("thread_id") {
			return bad("fs_thread " + action + " 需要 env_id 与 thread_id")
		}
		if action == "item" && !has("item_id") {
			return bad("fs_thread item 需要 item_id")
		}
	case "fs_thread/composer_options":
		if !has("env_id") {
			return bad("fs_thread composer_options 需要 env_id")
		}
	case "fs_thread/workspace_options":
		if !has("env_id") || !has("project_id") {
			return bad("fs_thread workspace_options 需要 env_id 与 project_id")
		}
	case "fs_thread/launch":
		if !has("env_id") || !has("project_id") || !has("text") {
			return bad("fs_thread launch 需要 env_id、project_id 与 text")
		}
	case "fs_thread/send":
		if !has("env_id") || !has("thread_id") || !has("text") {
			return bad("fs_thread send 需要 env_id、thread_id 与 text")
		}
	case "fs_thread/stop":
		if !has("env_id") || !has("thread_id") {
			return bad("fs_thread stop 需要 env_id 与 thread_id")
		}
	case "fs_environment/list", "fs_environment/projects":
	case "fs_schedule/status", "fs_memory/status", "fs_help/read", "fs_stop/stop":
	case "fs_schedule/list":
		// 无必填；limit/offset 可选。
	case "fs_schedule/create":
		if !has("name") || str("name") == "" {
			return bad("fs_schedule create 需要 name")
		}
		if !has("cron") || str("cron") == "" {
			return bad("fs_schedule create 需要 cron（标准五字段）")
		}
		if !has("instruction") || str("instruction") == "" {
			return bad("fs_schedule create 需要 instruction")
		}
		if has("task_id") {
			return bad("fs_schedule create 不接受 task_id")
		}
	case "fs_schedule/update":
		if !has("task_id") || str("task_id") == "" {
			return bad("fs_schedule update 需要 task_id")
		}
	case "fs_schedule/delete", "fs_schedule/runs", "fs_schedule/enable", "fs_schedule/disable":
		if !has("task_id") || str("task_id") == "" {
			return bad("fs_schedule " + action + " 需要 task_id")
		}
	case "fs_memory/list":
		// 无必填；prefix 可选。
	case "fs_memory/read", "fs_memory/history":
		if !has("path") || str("path") == "" {
			return bad("fs_memory " + action + " 需要 path")
		}
	case "fs_memory/create":
		if !has("path") || str("path") == "" {
			return bad("fs_memory create 需要 path")
		}
		if !has("content") {
			return bad("fs_memory create 需要 content")
		}
	case "fs_memory/replace", "fs_memory/append":
		if !has("path") || str("path") == "" {
			return bad("fs_memory " + action + " 需要 path")
		}
		if !has("content") {
			return bad("fs_memory " + action + " 需要 content")
		}
		if !has("expected_version") {
			return bad("fs_memory " + action + " 需要 expected_version")
		}
	case "fs_memory/delete":
		if !has("path") || str("path") == "" {
			return bad("fs_memory delete 需要 path")
		}
		if !has("expected_version") {
			return bad("fs_memory delete 需要 expected_version")
		}
	case "fs_memory/restore":
		if !has("path") || str("path") == "" {
			return bad("fs_memory restore 需要 path")
		}
		if !has("version") {
			return bad("fs_memory restore 需要 version")
		}
		if !has("expected_version") {
			return bad("fs_memory restore 需要 expected_version")
		}
	case "fs_memory_search/search":
		if !has("query") || str("query") == "" {
			return bad("fs_memory_search search 需要 query")
		}
	case "fs_result/read":
		if !has("call_id") {
			return bad("fs_result read 需要 call_id")
		}
	case "fs_cancel/cancel":
		if !has("call_id") {
			return bad("fs_cancel cancel 需要 call_id")
		}
	default:
		return bad("不支持的工具动作: " + toolName + "/" + action)
	}
	return nil
}

// ---------- 执行 ----------

// botExecuteSyncTool 在主线循环内执行同步工具：读取类、能力查询、帮助、
// 结果续读、停止与取消。返回进入协议回复的内容（JSON 字符串）。
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
	case "fs_schedule/list", "fs_schedule/create", "fs_schedule/update",
		"fs_schedule/delete", "fs_schedule/runs", "fs_schedule/enable", "fs_schedule/disable":
		// 定时任务管理（INT-103）：全部同步、立即生效，与 fs_memory 写同类。
		return s.execScheduleTool(call)
	case "fs_schedule/status", "fs_memory/status":
		return s.execCapabilityStatus(call)
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
