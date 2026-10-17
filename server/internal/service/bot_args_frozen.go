package service

// v1/v2 冻结的工具参数结构体：存量会话在压缩/重启边界前继续用旧字节构建
// tools 数组，`action` 枚举的历史生成结果（只列出第一个取值）必须逐字节保留。
// v3 起使用 bot_tools.go 里的同名结构体（已修正枚举）；不要改动本文件里的
// 任何标签，否则 v1/v2 的 tools_hash 会失稳（见 bot_int103_test.go 冻结基线）。

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
