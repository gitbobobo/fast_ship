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
- **BatchCloseDoneIssues**：仍按 open+done 筛选，超 200 条返回 `ErrBatchCloseTooMany`。关闭本身（INT-174）走请求 context + 30s 整批截止（INT-185）：30s 预算在 ListFiltered 成功之后才创建，只管关闭阶段（前置权限检查与查询本就不收 ctx）；内部来源单事务批量改状态，`BatchCloseInternalTx` 带 `state=open` 且 `workflow_status=done` 条件（与待关闭清单完全同条件：两语句之间被看板拖回开发中的行只改 workflow、不改 state，必须一并钉住才不会误关）并按 RowsAffected 区分命中/未命中——被并发方提前改写（已非 open）的行不计成功、不删推荐，逐条记入 failures；GitHub 来源 `UpdateIssue` 并发度 4、本地 upsert 串行，semaphore 绑整批 ctx（取消后不再开始新 PATCH），且每条 PATCH 发出前经 `IsOpenAndDone` 按 id 行级复核本地行仍为 open+done（没有事务可依托，worker 拿到槽位后、发 PATCH 前复核；被拖回的行不发 PATCH、按「已跳过关闭」计入 failures，INT-185 R7；守卫带 batchCtx——单连接池排队也能被批次取消打断——守卫通过后再判一次 `batchCtx.Err()`，已取消/超时按「未开始」计数不发 PATCH；守卫自身因带 ctx 被批次取消/熔断时按取消/限流归因而非「复核失败」，守卫返回非 ctx 错误（SQLITE_BUSY、连接池故障）时先记 warn（issue_id + 底层 err）再返回静态文案，与 PATCH/落地失败日志口径一致：除 ctx err 判定外再回读批次状态——批次已死（ctx done 或已熔断）即使守卫返回非 ctx 包装错误也按取消归因，仅批次存活才报内部复核失败（INT-185 R9/R11）；PATCH 成功后本地落地失败（upsert/markStale）也归入「结果未确认」——GitHub 已关、本地可能仍 open，不能用普通内部错误让用户重复操作，INT-185 R10），已发出的 PATCH 经 `context.WithoutCancel` + 单请求 15s 上限继续走完并落库，缩小「远端已关、本地仍 open」的窗口。失败文案分两类：未开始的取消为「请求已取消或超时，未继续关闭」，在途 PATCH 超时为「结果未确认：GitHub 侧可能已关闭，请刷新查看」；`UpdateIssue` 命中 GitHub 限流（`*RateLimitError`/`*AbuseRateLimitError`/429 权威命中无需 body 文案、403 按 Message/body 含 rate limit 字样兜底，INT-185 R13，口径同 PR 域 `pullRequestAccessHint`）时统一回报限流重试文案。命中整批性失败即熔断（INT-185 限流；INT-191 把 401 认证失败与非限流 403「无权限或访问受限」纳入——首个触发类别 CAS 生效，每个触发 worker 释放槽位前幂等 dispatchCancel，纯触发批次槽位释放后派发循环不再取槽，混合批次中非触发项的槽位释放仍可能落在极小窗口内、至多再发出并发度条请求；404/5xx/传输层错误是单条状态或结果未知，不熔断逐条回报（404 成因两义——仓库不可见 vs 单条转移/删除，状态码无法区分，本期一律按单条；仓库级 404/410 的熔断取舍另立 INT-198））：认证/权限触发项按类别回报固定文案（「请检查凭证」/「无权限或访问受限，请检查授权」——403 成因不止仓库权限，真实原因看 warn 日志），未派发项回报对应「…未尝试关闭」文案，逐字固定以保前端分组计数。`upsertGitHubIssue` 入口对 nil/缺 GitHub ID 的响应返回错误而不是 panic。失败文案收敛为少量固定类别（INT-185 R3 审计）：GitHub 5xx 与传输层错误按「结果未确认」处理（PATCH 已发出、结果未知，宁可多提示刷新）；其余 ErrorResponse 规范化为「更新 GitHub Issue 失败：<状态码> <Message>」——不带 URL/Issue 号，保证同类错误文案逐字相同，前端按文案分组计数才有效；前端对失败原因按分组全部展示（每组取样例引用），明细差额（failed 超 failures 明细上限）显式标注，不做容量截断；明细被截断时分组不展示 ×N 计数（那只是已列明细的下界，展示会低估，INT-185 R6）。前端 `formatBatchCloseError` 先按 `status >= 500` 走刷新确认（服务端自身 500 带 JSON 正文也不能只回具体文案，INT-185 R9），再处理 4xx 的 message。`IssueBatchResult.failures` 语义统一（INT-185 R7）：schema 被批量关闭与 `PUT /api/issues/internal-meta` 共享，两端共用 `appendBatchFailureEntry`——达到 50 条明细上限后每类新文案各保留一条样例，后续类别不再被静默丢弃。

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
  `É`）在 1–2 rune 的 q 搜索（LIKE 分支）与 label/assignee/milestone 精确匹配里
  不再折叠；中文无大小写不受影响。≥3 rune 的 q 搜索自 INT-209 走 FTS5 trigram，
  折叠口径变宽，实测结论见下文「q 搜索走 FTS5 trigram」一节。不为此引入自定义
  SQL 函数。

## 索引

列表过滤不新增索引：project 过滤复用 AutoMigrate 已建的 `idx_issues_project_id`，
下推后的查询计划与新增索引与否完全一致（`SEARCH issues USING INDEX
idx_issues_project_id` + `USE TEMP B-TREE FOR ORDER BY`）。

ORDER BY 第二键是 `CASE WHEN ... THEN gh.number ELSE issues.sequence_number END`——
跨表表达式，任何 issues 表上的复合索引都无法覆盖，SQLite 必须用临时 B 树排序。
该排序有界：OFFSET/LIMIT 截断之后才输出，1200 条量级实测 200 次目标查询合计
10-40ms，无索引瓶颈。因此 `createBootIndexes` 没有为此登记新索引。

## q 搜索走 FTS5 trigram（INT-209）

### 背景

`buildIssueSearchCondition` 原本对 title/body/author_login/reference 四路都用
`lower(...) LIKE '%q%'`——正文列前导通配无法走索引，每行都要扫全文。夹具 1 万条
issue（临时文件库 + WAL，Apple M3 Pro）实测带关键词列表 p50 78-126ms、p95 ≥100ms
（`server/internal/repository/issue_search_bench_test.go`，常规 `go test` 不跑，
复测命令见文件头注释）。

### 索引结构

`issues_fts` 是派生全文索引（FTS5 trigram），只存 title/body 两个索引列，事实
来源是 issues 表，照搬 `bot_memory_fts` 先例（见 docs/internal/bots.md「长期
记忆」）。FTS 行身份用整数 docid：`issue_fts_docs` 维护 issue_id TEXT ↔ doc
INTEGER 的双向唯一映射（doc 普通 INTEGER PRIMARY KEY，issue_id UNIQUE），FTS
行以 doc 为显式 rowid 写入：

- **为什么不做 issue_id 列**：第 1 轮把 issue_id 作为 UNINDEXED 列存进 FTS，
  `SELECT issue_id FROM issues_fts WHERE MATCH` 物化结果集时要按 docid 逐条
  回读 %_content 行，万级夹具上 count+list 各付一次（实测同一 MATCH 只取
  rowid 13.5ms、取 issue_id 列要 35.8ms；release checklist 30.9 vs 76.4ms），
  是高命中查询 p50 回退的主因。
- **为什么不用 issues 自身 rowid 做身份**：issues 是 TEXT 主键、无 INTEGER
  主键，VACUUM 会重排无 INTEGER 主键表的 rowid，当不了 FTS docid。
- doc 不用 AUTOINCREMENT：映射行与 FTS 行总在同一触发器里成对删，id 复用无
  风险。
- 三条触发器（trg_issues_fts_doc_insert/update/delete）同步 issues 的
  INSERT/UPDATE/DELETE：INSERT 先补映射行、再按同一 doc 写 FTS 行；UPDATE
  的 issues.id 不变、doc 复用，先删 FTS 旧行再按同一 doc 重插；DELETE 先删
  FTS 行再删映射行。列值统一 `COALESCE(..., '')`：body 列可空，NULL 索引成
  空串（空串不含 trigram，永不命中），与 LIKE 对 NULL 不匹配的旧语义等价。
- **UPDATE 触发器带双层守卫**：`AFTER UPDATE OF title, body` 把触发范围收窄
  到 SET 里列出这两列的语句（只改 state/updated_at/closed_at 的元数据写入
  路径——TouchUpdatedAt、批量关闭、同步回写——完全不走重索引），`WHEN
  OLD.title IS NOT NEW.title OR OLD.body IS NOT NEW.body` 再按值判断，全列
  写入（如 gorm Save）但 title/body 未变的也不重写。无守卫时一次元数据
  UPDATE 会成批重写正文索引（2000 行 × 3KB 正文实测 511ms，带守卫 3.2ms）。
  IS NOT 对 NULL 安全。
- 删 project 外键级联删 issues 时 AFTER DELETE 触发器逐行同步，实测 FTS 与
  映射均无孤儿行。唯一的理论成因是未来 DROP/重建 issues 表的迁移（触发器随
  表消失），孤儿行按 issues.id 回连主表不影响结果正确性、只白占空间；此类
  迁移应同步 DROP issues_fts 与 issue_fts_docs 并重跑 ensure（见下）。

### 接线与幂等回填

`EnsureIssueFTSSchema`（repository/issue_fts.go）在 AutoMigrate 之后建映射表 +
虚表 + 触发器，然后按两个方向幂等回填（各自 NOT IN 去重，重复启动无副作用）：
「有 issues 行、无映射行」补 issue_fts_docs，「有映射行、无 FTS 行」按 doc 重插
FTS。回填前先比三方计数（issues / issue_fts_docs / issues_fts），一致则直接
跳过——两个 NOT IN 反连接都要扫全表，无缺口也付全量成本（十万行量级实测
约 200ms），计数不一致才对账。取舍：计数相等只是必要条件，「缺一行 FTS 行 +
多一行孤儿」这类等量缺口不会被自愈（正常写路径不可能产生，只可能来自人工
修库或未来重建迁移，应手工清理后重跑 ensure；精确验证与对账同阶成本，不为
它放弃快速路径）。main.go 在 `EnsureBotSchema` 同位置之后调用，
失败 `fatal`；`--dry-run` 路径在此之前已 return，不受影响。

**触发器定义按原文比对收敛**：三条触发器不用 IF NOT EXISTS，ensure 按
sqlite_master 里的存量定义与代码里的期望串逐字比对，漂移即 DROP 重建——日后
修改触发器定义不需要换名登记，改完常量存量库下次启动自动收敛（IF NOT
EXISTS 对旧定义是静默空操作，会造成行为与代码分叉，与虚表的旧形态收敛是
同一类陷阱，有测试锁定存量定义与期望串逐字一致）。

**旧形态收敛**：`CREATE VIRTUAL TABLE IF NOT EXISTS` 对已存在的第 1 轮结构（带
UNINDEXED issue_id 列）是静默空操作，靠 IF NOT EXISTS 收敛不到新形态。ensure
先摘旧触发器（trg_issues_fts_*——新触发器换了名字 trg_issues_fts_doc_*，旧定
义必须显式 DROP，IF NOT EXISTS 拦不住，残留会在重建后的结构上引用不存在的
列），再按 `PRAGMA table_info` 精确判定虚表形态：列集合恰为 {title, body} 才
是当前形态，其余（第 1 轮的 issue_id 列、或未来任何意外形态）一律 DROP 重建，
不依赖 DDL 原文子串。命中即 DROP 旧虚表，然后按新形态重建并回填。**顺序不可反**：
若先删虚表、后摘触发器，两步之间进程退出会留下引用已删表的触发器，SQLite
编译任何 issues 上的 UPDATE 时即报 no such table，下一次启动连常规迁移里的
UPDATE 都过不去，永远到不了修复入口；先摘触发器后，中断残留只会是「无触发器
的旧虚表」或「缺触发器的新虚表」，都由后续启动的 legacy 检测与回填收敛。整个
收敛过程包在一个事务里。任何起点（新库 / 第 1 轮库 / 中断残留 / 旧触发器定义）跑完
都收敛到同一结构（有测试锁定：TestIssueFTS_LegacyShapeConvergence 与
TestIssueFTS_InterruptedMigrationSelfHeals）。

### 查询口径

- **≥3 rune**（Unicode 字符计，同 `SearchFTS`）：title/body 走
  `issues.id IN (SELECT issue_fts_docs.issue_id FROM issues_fts JOIN
  issue_fts_docs ON issue_fts_docs.doc = issues_fts.rowid WHERE issues_fts
  MATCH ?)`，MATCH 参数经 `ftsMatchLiteral` 引号编码（同包复用，不开放任意
  FTS 表达式）。MATCH 只取 FTS rowid，映射表按 INTEGER PRIMARY KEY 点查换回
  issue_id，不回读 FTS content 行；外层仍用 IN 子查询，`buildFilteredQuery`
  其余组合语义不动。子查询不写表别名：别名会让 EXPLAIN QUERY PLAN 里映射表
  只显示别名，计划不可读。
- **1–2 rune 或含 NUL**：整组退回四路 LIKE（trigram 最小可匹配 3 字符，短查询在 FTS 上
  不可表达；FTS5 字面量无法表达 NUL，查询解析器会提前终止报 unterminated
  string，含 NUL 的查询退回 LIKE 保持旧行为——驱动会把 NUL 截断、模式退化，
  改造前就存在，原样保留）。
- author_login 与 reference（`INT-`/`GH-` 编号）两列小、保持 LIKE，不进 FTS；
  纯数字精确匹配分支（`issueQueryAsNumber`）不动。

EXPLAIN QUERY PLAN（万级夹具，`webhook`）：外层 `SEARCH issues USING INDEX
idx_issues_project_id (project_id=?)`，子查询 `LIST SUBQUERY 1` + `SCAN issues_fts
VIRTUAL TABLE INDEX 0:M2` + `SEARCH issue_fts_docs USING INTEGER PRIMARY KEY
(rowid=?)`——title/body 匹配走 FTS 扫描、映射换 id 走主键点查，无 content
回读（有测试锁定）。

### 大小写折叠差异（实测，modernc 驱动，不为此做处理）

- **≥3 rune MATCH 分支**：trigram 折叠非 ASCII 大小写——`ÉCOLE FRANÇAISE` 命中
  正文 `École française`、`ПРИВЕТ` 命中 `привет мир`；比 LIKE 更宽（接近 Go
  `strings.ToLower` 方向），命中集合相对旧行为是扩张。不去音调：`ecole` 不命中
  `école`。
- **1–2 rune LIKE 分支**：沿用 ASCII-only 折叠的旧边界——查询 `ÉC` 不命中
  `École`；而 3 rune 的 `éco` 经 FTS 折叠命中。同一对非 ASCII 大小写，两个分支
  命中方向相反。
- 命中等价测试（issue_fts_test.go）以 Go 参考实现
  `strings.Contains(strings.ToLower(...))` 为期望，固定查询集（混合大小写英文、
  中文、带空格短语、含引号、含 %/_、数字、作者名）逐条一致；非 ASCII 大小写
  差异单独锁行为（TestIssueFTS_CaseFoldingBoundary）。

### 性能实测边界

FTS 命中率决定收益方向（万级夹具单项目实测，一次 Count+List）。第 1 轮
（issue_id 列形态）选择性查询显著变快（`INT-2` 111→74ms、`shipbobo` 126→47ms），
但高命中查询一致回退（`webhook` 77.7→126.7ms、`release checklist` 80.1→163.9ms、
`404` 83.0→99.6ms），根因是 MATCH 子查询物化结果集时按 issue_id 列逐条回读
%_content 行。

第 2 轮（docid 映射形态）消掉 content 回读后（2026-11 实测，Apple M3 Pro，
临时文件库 + WAL；两轮独立复测一致；注意：当天机器负载 4.4-6.6，未等到空闲
窗口，绝对值偏保守，空闲机复测由后续验收负责）：

| 查询 | 命中 | LIKE 基线 | 第 1 轮 FTS | 第 2 轮（映射） |
|---|---|---|---|---|
| webhook | 9587 | 77.7ms | 126.7ms | 81.8-83.2ms |
| release checklist | 9540 | 80.1ms | 163.9ms | 114.1-115.1ms |
| 404 | 8275 | 83.0ms | 99.6ms | 56.8-58.9ms |
| INT-2 | 2300 | 111ms | 74ms | 54.1-55.1ms |
| shipbobo | 5715 | 126ms | 46.5ms | 42.7-47.5ms |
| retry | 9914 | — | — | 75.5-76.7ms |
| 数据库连接 | 9560 | — | — | 73.3-75.6ms |
| 超时（1–2 rune，LIKE 分支） | 9594 | — | — | 85.7-88.2ms |
| ab（1–2 rune，LIKE 分支） | 2048 | — | — | 114.8-118.6ms |
| 无 q 对照 | — | — | — | 35.6-40.5ms |

- 全部查询相对第 1 轮一致变快，降幅与消除的 content 回读成本吻合
  （约 22-45ms/次执行 × count+list 两次）。
- 加和验证：实测 p50 ≈ 无 q floor + 2 × 第 1 轮诊断的 rowid-only 子查询成本
  （`webhook` 2×13.5+51 ≈ 78 vs 实测 81.8-83.2；`release checklist`
  2×30.9+51 ≈ 113 vs 实测 114.1-115.1）——MATCH 子查询已按 rowid-only 成本
  运行，EXPLAIN 证实无 content 回读（见上「查询口径」）。
- `404` 在负载下已低于 LIKE 基线；`webhook` 与基线差 5% 以内（负载噪声量
  级）；`release checklist` 剩余差距是 doclist 短语验证成本（两词短语在
  9540 命中上交并 + 位置验证，约 31ms/次执行），属 trigram 索引形态在该最坏
  夹具上的固有成本，非本形态可再优化项；空闲机上的严格基线对比由后续验收
  复测裁定。`ab`/`超时` 走未改动的 1–2 rune LIKE 分支，其绝对值就是旧 LIKE
  形态在短查询上的原成本（2048 命中的 2 字符前导通配扫描）。
- EXISTS 关联子查询逐行重跑 MATCH 的替代方案维持第 1 轮结论：实测 174s/条，
  不可用。若后续还要压 `release checklist` 一类最坏情况，方向是服务层
  count/list 合并或索引形态，另立条目评估。
