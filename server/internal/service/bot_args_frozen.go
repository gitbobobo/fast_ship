package service

import "encoding/json"

// v1/v2 冻结的工具参数结构体：存量会话在压缩/重启边界前继续用旧字节构建
// tools 数组，`action` 枚举的历史生成结果（只列出第一个取值）必须逐字节保留。
// v3 起使用 bot_tools.go 里的同名结构体（已修正枚举）；v5 起活跃的 fs_thread
// 参数标签又演进了，v4 的旧标签由本文件的 botThreadArgsV4 保留。不要改动
// 本文件里的任何标签，否则已发布版本的 tools_hash 会失稳（见 bot_int103_test.go
// 与 bot_int120_test.go 冻结基线）。

// botProjectArgsFrozen 是 v1/v2 的 fs_project 参数。
type botProjectArgsFrozen struct {
	Action        string  `json:"action" jsonschema:"enum=list,get,create,update,required"`
	ID            string  `json:"id,omitempty" jsonschema:"description=项目 ID（get/update 必填）"`
	Page          int     `json:"page,omitempty" jsonschema:"description=页码（从 1 起，list 用）"`
	PageSize      int     `json:"page_size,omitempty" jsonschema:"description=页长（1-100，list 用）"`
	Name          string  `json:"name,omitempty" jsonschema:"description=项目名（create 必填；update 可改）"`
	Description   *string `json:"description,omitempty" jsonschema:"description=项目描述；update 提供空串即清空"`
	RepositoryURL string  `json:"repository_url,omitempty" jsonschema:"description=仓库 URL（http/ssh）"`
}

// botIssueArgsFrozen 是 v1/v2 的 fs_issue 参数。
type botIssueArgsFrozen struct {
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

// botThreadArgsFrozen 是 v1/v2 的 fs_thread 参数。
type botThreadArgsFrozen struct {
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

// botEnvironmentArgsFrozen 是 v1/v2 的 fs_environment 参数。
type botEnvironmentArgsFrozen struct {
	Action string `json:"action" jsonschema:"enum=list,projects,required"`
}

// botMemoryArgsFrozen 是 v1/v2 的 fs_memory 参数。
type botMemoryArgsFrozen struct {
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

// botScheduleTaskArgsFrozen 是 v3 的 fs_schedule 参数（v3 冻结；v4 起用
// bot_tools.go 里修正枚举后的同名结构体）。
type botScheduleTaskArgsFrozen struct {
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
	// 显式传空串清除覆盖，回到机器人当前模型。
	Model *string `json:"model,omitempty" jsonschema:"description=运行使用的模型名（可选；省略则保持不变或跟随机器人当前模型，传空串清除覆盖）"`
	// NeedsReport 标记成功且非静默的运行是否也追加事件进主线。
	NeedsReport *bool `json:"needs_report,omitempty" jsonschema:"description=运行是否需要汇报（可选；true 时成功且非静默的运行也会通知主线）"`
	// Limit/Offset 是 runs/list 的分页参数。
	Limit  int `json:"limit,omitempty" jsonschema:"description=条数上限（runs 默认 20 上限 100；list 默认且最多 200）"`
	Offset int `json:"offset,omitempty" jsonschema:"description=分页偏移（runs/list，默认 0）"`
}

// botThreadArgsV4 是 v4 的 fs_thread 参数（INT-110 修正枚举后的形态）。v5 起
// fs_thread list 才真正支持 env_id/query 过滤并修正两个参数的 schema 描述；
// v4 的 wire 字节由本冻结副本逐字节保留（见 bot_int120_test.go 冻结基线），
// 不要改动本结构体的任何字段或标签。
type botThreadArgsV4 struct {
	Action    string `json:"action" jsonschema:"enum=list,enum=get,enum=item,enum=composer_options,enum=workspace_options,enum=launch,enum=send,enum=stop,required"`
	EnvID     string `json:"env_id,omitempty" jsonschema:"description=T3 环境 ID（get/item/composer_options/workspace_options/launch/send/stop 必填）"`
	ThreadID  string `json:"thread_id,omitempty" jsonschema:"description=T3 线程 ID（get/item/send/stop 必填）"`
	ItemID    string `json:"item_id,omitempty" jsonschema:"description=T3 线程条目 ID（item 必填；条目 ID 由 get 返回的 items[].id 提供）"`
	ProjectID string `json:"project_id,omitempty" jsonschema:"description=T3 项目 ID（workspace_options/launch 必填；list 可选，只保留该项目的线程）"`
	Query     string `json:"query,omitempty" jsonschema:"description=引用名过滤（workspace_options）"`
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

// botScheduleTaskArgsV5 是 v5（INT-120）的 fs_schedule 参数：7 个管理动作、
// 无 dry_run。INT-121 的 run/dry_run 只进 v6，本副本与 v5 发布时逐标签一致，
// 保证 v3/v4/v5 冻结会话的 tools_hash 不回退（基线见 bot_int103_test.go 与
// bot_int120_test.go）。不要改动本结构体的任何标签。
type botScheduleTaskArgsV5 struct {
	Action string `json:"action" jsonschema:"enum=list,enum=create,enum=update,enum=delete,enum=runs,enum=enable,enum=disable,required"`
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
	// 显式传空串清除覆盖，回到机器人当前模型。
	Model *string `json:"model,omitempty" jsonschema:"description=运行使用的模型名（可选；省略则保持不变或跟随机器人当前模型，传空串清除覆盖）"`
	// NeedsReport 标记成功且非静默的运行是否也追加事件进主线。
	NeedsReport *bool `json:"needs_report,omitempty" jsonschema:"description=运行是否需要汇报（可选；true 时成功且非静默的运行也会通知主线）"`
	// Limit/Offset 是 runs/list 的分页参数。
	Limit  int `json:"limit,omitempty" jsonschema:"description=条数上限（runs 默认 20 上限 100；list 默认且最多 200）"`
	Offset int `json:"offset,omitempty" jsonschema:"description=分页偏移（runs/list，默认 0）"`
}

// botScheduleTaskArgsV7 是 v7（INT-123）的 fs_schedule 参数：10 个动作含
// state_get/state_set 与 state 参数。INT-138 的 timezone 描述变更（「默认
// UTC」→「省略即用户时区」）只进 v8；v10（INT-136）只改工具与动作描述、
// 参数标签不动，本副本与 v7 发布时逐标签一致，保证 v7 冻结会话的
// tools_hash 不回退（基线见 bot_int103_test.go）。不要改动
// 本结构体的任何标签。
type botScheduleTaskArgsV7 struct {
	Action string `json:"action" jsonschema:"enum=list,enum=create,enum=update,enum=delete,enum=runs,enum=run,enum=enable,enum=disable,enum=state_get,enum=state_set,required"`
	// TaskID 是任务 ID（update/delete/runs/run/enable/disable 必填；create 禁止）。
	TaskID string `json:"task_id,omitempty" jsonschema:"description=任务 ID（update/delete/runs/run/enable/disable 必填）"`
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

// botThreadArgsV5Frozen 是 v5–v8 共用的 fs_thread 参数冻结副本（v5 起标签
// 一直未变）。INT-148 给 fs_thread 新增 settle/unsettle/resume_queue 动作只进
// v9，v5–v8 的 wire 字节由本副本逐字节保留（基线见 bot_int103_test.go 与
// bot_int138_test.go）。不要改动本结构体的任何字段或标签。
type botThreadArgsV5Frozen struct {
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

// botScheduleTaskArgsV6 是 v6（INT-121）的 fs_schedule 参数：8 个管理动作、
// 含 run/dry_run。INT-123 的 state_get/state_set 与 state 参数只进 v7，本副本
// 与 v6 发布时逐标签一致，保证 v6 冻结会话的 tools_hash 不回退（基线见
// bot_int103_test.go）。不要改动本结构体的任何标签。
type botScheduleTaskArgsV6 struct {
	Action string `json:"action" jsonschema:"enum=list,enum=create,enum=update,enum=delete,enum=runs,enum=run,enum=enable,enum=disable,required"`
	// TaskID 是任务 ID（update/delete/runs/run/enable/disable 必填；create 禁止）。
	TaskID string `json:"task_id,omitempty" jsonschema:"description=任务 ID（update/delete/runs/run/enable/disable 必填）"`
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
	// DryRun 只配合 run 使用：true 时只预览任务与下次计划、不建运行记录不执行；
	// 任务忙碌时回执标 busy=true。其它动作不接受该字段（显式 false 也拒绝），
	// 避免「带预览标记仍写入」的误用。
	DryRun *bool `json:"dry_run,omitempty" jsonschema:"description=只配合 run：true 时只预览（任务信息、指令与下次计划），不建运行记录、不执行；任务已有未结束运行时 busy=true；其它动作携带 dry_run 即被拒绝（显式 false / null 亦然）"`
	// Limit/Offset 是 runs/list 的分页参数。
	Limit  int `json:"limit,omitempty" jsonschema:"description=条数上限（runs 默认 20 上限 100；list 默认且最多 200）"`
	Offset int `json:"offset,omitempty" jsonschema:"description=分页偏移（runs/list，默认 0）"`
}
