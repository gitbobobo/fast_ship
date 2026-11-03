# Issue 列表查询下推 SQL（INT-106）

## 背景

迁移前 `IssueService.List` / `CountIssues` / `GetFilterOptions` / `BatchCloseDoneIssues`
都走 `IssueRepository.ListByProject` 一次读出项目全部 issue（含 body、body_html 与整行
GitHub meta 含 raw_json），在内存里过滤、排序后才按 page_size 切片。看板 4 列各发一次
列表请求，读盘量与内存随项目 issue 总数线性增长。

## 现状

过滤、排序、分页全部下推 SQL，集中在 `repository` 包：

- `IssueListQuery`：列表过滤条件（与 `service.IssueListFilters` 同义，service 层映射后
  传入，`Query` 保证已 TrimSpace）。
- `buildFilteredQuery`：issues 主表 LEFT JOIN `issue_github_meta AS gh`、
  `issue_internal_meta AS im`（两表 issue_id 都是唯一键，不会行扇出），各筛选维度
  的语义与迁移前的内存过滤 `matchesIssueFilters` 一致。
- `CountFiltered`：`COUNT(*)`，供分页 total。
- `ListFiltered(q, sortKey, offset, limit)`：显式投影（不含 body/body_html）+ ORDER BY +
  OFFSET/LIMIT；GitHub meta 用 `Preload(..., Select(...))` 装载本页所需列（不含
  raw_json 与 collections_*）。
- `ListFilterOptionSources`：GetFilterOptions 的只读投影，只取两张 meta 表的
  标签/指派人/里程碑 JSON。

service 层（`issue_list.go`）只做映射与编排：`List` 先 `CountFiltered` 再 `ListFiltered`
取本页，meta/shipHook/未读/PR 聚合全部只按本页 issue 查询；`BatchCloseDoneIssues` 取
`batchCloseDoneMaxIssues+1` 条探测超上限。内存实现 `matchesIssueFilters`、`sortIssues`
及只被它们使用的 helper 已删除。

## 行为契约

- **列表响应不携带正文**：分页结果的 `IssueResponse.Body` / `BodyHtml` 为空字符串
  （字段保留，API schema 不变）。web 看板卡片、问题列表页、分页导航都不读列表项
  body；详情与编辑走 `GET /api/issues/{iid}`；bot 的 `botSlimIssue` 不用 body。
- **排序语义**（与旧 `sortIssues` 一致）：`updated_desc`（默认，未知键也走它）、
  `updated_asc`、`created_desc`、`created_asc` 同值时按 sortNum（GitHub meta.number，
  否则 sequence_number）同向决胜；`comments_desc/asc` 按 commentCount
  （`COALESCE(gh.comments_count,0)`）排序、同值按 updated_at 同向决胜，无第三级。
  排序键完全并列的行（例如 `comments_asc/desc` 下 commentCount 与 updated_at 都相同）
  在新实现下由 ORDER BY 末尾的 `sequence_number DESC` 兜底出全序（该列有项目内唯一
  索引），顺序可能与迁移前 Go 不稳定排序（`sort.Slice`）给出的顺序不同——两者都满足
  排序键，不构成行为回归；补唯一键是为了 LIMIT/OFFSET 分页在跨请求时稳定不重不漏。
- **page 超过末页**：返回空切片、total 不变。
- **BatchCloseDoneIssues**：仍按 open+done 筛选，超 200 条返回 `ErrBatchCloseTooMany`。关闭本身（INT-174）走请求 context + 30s 整批截止（INT-185）：30s 预算在 ListFiltered 成功之后才创建，只管关闭阶段（前置权限检查与查询本就不收 ctx）；内部来源单事务批量改状态，`BatchCloseInternalTx` 带 `state=open` 且 `workflow_status=done` 条件（与待关闭清单完全同条件：两语句之间被看板拖回开发中的行只改 workflow、不改 state，必须一并钉住才不会误关）并按 RowsAffected 区分命中/未命中——被并发方提前改写（已非 open）的行不计成功、不删推荐，逐条记入 failures；GitHub 来源 `UpdateIssue` 并发度 4、本地 upsert 串行，semaphore 绑整批 ctx（取消后不再开始新 PATCH），且每条 PATCH 发出前经 `IsOpenAndDone` 按 id 行级复核本地行仍为 open+done（没有事务可依托，worker 拿到槽位后、发 PATCH 前复核；被拖回的行不发 PATCH、按「已跳过关闭」计入 failures，INT-185 R7；守卫带 batchCtx——单连接池排队也能被批次取消打断——守卫通过后再判一次 `batchCtx.Err()`，已取消/超时按「未开始」计数不发 PATCH；守卫自身因带 ctx 被批次取消/熔断时按取消/限流归因而非「复核失败」，INT-185 R9），已发出的 PATCH 经 `context.WithoutCancel` + 单请求 15s 上限继续走完并落库，缩小「远端已关、本地仍 open」的窗口。失败文案分两类：未开始的取消为「请求已取消或超时，未继续关闭」，在途 PATCH 超时为「结果未确认：GitHub 侧可能已关闭，请刷新查看」；`UpdateIssue` 命中 GitHub 限流（`*RateLimitError`/`*AbuseRateLimitError`/403/429 且响应体含 rate limit 字样，口径同 PR 域 `pullRequestAccessHint`）时统一回报限流重试文案。`upsertGitHubIssue` 入口对 nil/缺 GitHub ID 的响应返回错误而不是 panic。失败文案收敛为少量固定类别（INT-185 R3 审计）：GitHub 5xx 与传输层错误按「结果未确认」处理（PATCH 已发出、结果未知，宁可多提示刷新）；其余 ErrorResponse 规范化为「更新 GitHub Issue 失败：<状态码> <Message>」——不带 URL/Issue 号，保证同类错误文案逐字相同，前端按文案分组计数才有效；前端对失败原因按分组全部展示（每组取样例引用），明细差额（failed 超 failures 明细上限）显式标注，不做容量截断；明细被截断时分组不展示 ×N 计数（那只是已列明细的下界，展示会低估，INT-185 R6）。前端 `formatBatchCloseError` 先按 `status >= 500` 走刷新确认（服务端自身 500 带 JSON 正文也不能只回具体文案，INT-185 R9），再处理 4xx 的 message。`IssueBatchResult.failures` 语义统一（INT-185 R7）：schema 被批量关闭与 `PUT /api/issues/internal-meta` 共享，两端共用 `appendBatchFailureEntry`——达到 50 条明细上限后每类新文案各保留一条样例，后续类别不再被静默丢弃。

## JSON 列与 json_valid 防护

`labels_json` / `assignees_json` / `milestone_json` 的 Go 零值是空串，空串与被写坏的
JSON（如 `{oops`）都会让 SQLite 的 `json_each` / `json_extract` 抛 `malformed JSON`，
使整条列表查询失败。迁移前的内存过滤用 `parseJSON` / `extractLabelNames` 对坏数据一律
按空处理，这是既有契约，SQL 侧必须保持：所有 JSON 列表达式先过 `json_valid` 防护
（`jsonColumnOr`；标签列用 `jsonArrayOr`，额外要求顶层是数组，见下）——NULL、空串、
坏 JSON 统一回落到合法默认值（`'[]'` / `'{}'`），结果按空集/不匹配处理；meta 行不存在
的 JOIN NULL 走同一路径。合法但非对象的 JSON（如 `'5'`）`json_extract` 得 NULL，
比较不匹配，同旧行为。

标签历史数据有两种形态：commit 8b274f3 把存储从完整对象数组改为纯名称数组之前，存量行
是 `[{"name":"x","color":...}]`。标签列先经 `jsonArrayOr` 收口：`json_valid` 为真且
`json_type(...)='array'` 才取原列，否则回落 `'[]'`——顶层必须是 JSON 数组，顶层
标量（如 `"urgent"`）、对象（如 `{"name":"urgent"}`）、布尔/null 一律按无标签处理，
与迁移前的 `extractLabelNames` 一致。`json_type` 不能依赖 `AND` 短路（本驱动在坏
JSON 上会抛 malformed JSON），必须嵌在 `json_valid` 为真的 CASE 分支里求值。label
筛选在 EXISTS 里用
`CASE je.type WHEN 'object' THEN json_extract(je.value,'$.name') WHEN 'text' THEN je.value END`
同时取两种形态的名称（大小写不敏感），元素只认字符串与对象两种形态：数字/布尔/null
元素与无 name 的对象元素都得 NULL，不匹配任何筛选值；混排数组仍按元素处理（更宽容，
保持现状）。assignee 按元素匹配：
`CASE WHEN je.type = 'object' THEN json_extract(je.value,'$.login') END`，
字符串元素得 NULL 不匹配——旧实现对无法解析成对象数组的行整体不匹配，新实现按元素
处理，更宽容。GetFilterOptions 仍走 Go 侧 `extractLabelNames`，两种形态与坏数据本来就
兼容，未改。

## 已知边界

- **大小写折叠只对 ASCII**：SQLite 的 `lower()` / `LIKE` 只折叠 ASCII，Go 的
  `strings.EqualFold` / `strings.ToLower` 是 Unicode 感知。非 ASCII 大写字母（如
  `É`）在 q 搜索、label/assignee/milestone 精确匹配里不再折叠；中文无大小写不受影响。
  不为此引入自定义 SQL 函数。

## 索引

列表过滤不新增索引：project 过滤复用 AutoMigrate 已建的 `idx_issues_project_id`，
下推后的查询计划与新增索引与否完全一致（`SEARCH issues USING INDEX
idx_issues_project_id` + `USE TEMP B-TREE FOR ORDER BY`）。

ORDER BY 第二键是 `CASE WHEN ... THEN gh.number ELSE issues.sequence_number END`——
跨表表达式，任何 issues 表上的复合索引都无法覆盖，SQLite 必须用临时 B 树排序。
该排序有界：OFFSET/LIMIT 截断之后才输出，1200 条量级实测 200 次目标查询合计
10-40ms，无索引瓶颈。因此 `createBootIndexes` 没有为此登记新索引。
