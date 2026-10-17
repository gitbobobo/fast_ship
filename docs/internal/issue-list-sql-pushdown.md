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
- **BatchCloseDoneIssues**：仍按 open+done 筛选，超 200 条返回 `ErrBatchCloseTooMany`。

## JSON 列与 json_valid 防护

`labels_json` / `assignees_json` / `milestone_json` 的 Go 零值是空串，空串与被写坏的
JSON（如 `{oops`）都会让 SQLite 的 `json_each` / `json_extract` 抛 `malformed JSON`，
使整条列表查询失败。迁移前的内存过滤用 `parseJSON` / `extractLabelNames` 对坏数据一律
按空处理，这是既有契约，SQL 侧必须保持：所有 JSON 列表达式先过 `jsonColumnOr` 的
`json_valid` 防护——NULL、空串、坏 JSON 统一回落到合法默认值（`'[]'` / `'{}'`），
结果按空集/不匹配处理；meta 行不存在的 JOIN NULL 走同一路径。合法但非对象的 JSON
（如 `'5'`）`json_extract` 得 NULL，比较不匹配，同旧行为。

标签历史数据有两种形态：commit 8b274f3 把存储从完整对象数组改为纯名称数组之前，存量行
是 `[{"name":"x","color":...}]`。label 筛选在 EXISTS 里用
`CASE je.type WHEN 'object' THEN json_extract(je.value,'$.name') ELSE je.value END`
同时取两种形态的名称（大小写不敏感）；无 name 的对象元素得 NULL，不匹配任何筛选值。
assignee 按元素匹配：`CASE WHEN je.type = 'object' THEN json_extract(je.value,'$.login') END`，
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
