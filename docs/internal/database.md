# 数据库

SQLite（glebarez/modernc 驱动，纯 Go 无 CGO），单文件 `database.path`。连接固定一个（`SetMaxOpenConns(1)`），靠 `busy_timeout` 串行化竞争。

测试侧不是全量单连接，按需要显式声明：`router` 公共测试库（`setupRouterTestEnvMaybeLogs`）与 `service` 的机器人测试、序号/单连接回归测试都调 `SetMaxOpenConns(1)`；`service` 的通用 `setupTestServices` 仍是默认连接池。共享缓存内存库放开多连接时，两个并发短事务各自持读锁再升写锁会直接返回 `SQLITE_DEADLOCK`（extended 6），`busy_timeout` 等不到持锁方提交，机器人主线/worker/决定并发就会随机 500——所以从机器人侧起测试的库必须跟随生产纪律。

## 硬性规则：事务闭包内只能用事务句柄

`SetMaxOpenConns(1)` 意味着全局只有一条连接。`Transaction(func(tx) {...})` 会借走这唯一的连接开事务；闭包内再调用任何走池根的方法（`r.db`/`s.db`/`repo` 的非 Tx 方法），等于向空池借第二条连接——`database/sql.(*DB).conn` 的借连接等待**没有超时**，`busy_timeout` 只覆盖 SQLite 锁等待，管不到池级等待。结果是 goroutine 永久挂起、事务永不提交，此后所有查库请求排队，**服务整体冻结**（INT-97 生产事故）。

因此：

- 事务闭包内只允许：事务句柄本身（`tx.X` / `txRepo.X`）、实参显式传事务句柄的调用（`repo.SaveTx(tx, ...)`、`s.emitIssueEventTx(tx, ...)`）。`XxxTx` 命名不构成放行理由——不收句柄的 `XxxTx`（如 `UploadRunTx`）是自己开事务的方法，闭包内调用同样自锁。
- 禁止在闭包内再经事务发起方句柄调用：`db.Transaction(func(tx){ db.Exec(...) })` 里的 `db.Exec` 就是向池根借第二条连接。
- 事务里需要的数据（如项目归属人）在**进事务前**查好传入，参考 `issue_mirror.go` `upsertGitHubIssue` 的 `ownerID` 解析。
- 禁止把 `x.db` 池根句柄作为实参传进闭包内调用。
- 读-改-写类取值（如 `MAX+1` 取号、`NextSequenceNumberTx`）必须与写入在同一事务内走 tx 句柄：单连接让事务整体串行，取号放事务外会让并发方读到同一快照、撞唯一索引（INT-96）。取号同事务只保证序号唯一，不保证同键实体唯一——并发首见同一实体（如同一 `github_issue_id`）仍需在事务内复核身份再决定走创建还是更新，参考 `issue_mirror.go` `upsertGitHubIssue`。
- 守护：`service` 包的 `TestTransactionClosuresOnlyUseTxHandles` 用 AST 扫描全部事务闭包，随 `go test ./...` 运行；回归用例见 `single_conn_pool_test.go`（单连接池 + goroutine 超时断言）。已知盲区：把 `r.db` 赋给局部变量再调用、`Transaction(fc)` 委托形态的闭包体——写这两类代码时须人工核对本规则。

Fast Ship 自身日志（INT-94）不在本库：独立 SQLite 文件 `logging.self_log_path`，见 `docs/internal/self-logs.md`。

## 外键约定

**所有外键都必须 `ON DELETE CASCADE`。** DSN 全局开启 `_pragma=foreign_keys(1)`，删除父行由数据库级联清理子行，业务代码不手动删子表。

**唯一的例外：projects → user_github_tokens 的两个引用列（`public_github_token_id` / `code_github_token_id`）不建外键。** 原因有二：建 FK 会在删用户时与 users 的 CASCADE 冲突（同一 Token 被多项目引用时数据库无从级联）；且 CASCADE 删 Token 会连带删引用它的项目行。引用一致性由应用层保证——引用必须属于当前用户（40420），被引用禁止删除（40913），删项目不删 Token。

另一类无外键的是**探测缓存表**：`bot_model_windows`（INT-115）按「规范化 API 地址 + 模型名」缓存模型窗口元数据，不指向任何 bot——多个机器人可共享同一上游，地址或模型变化后旧行自然失效（键不同），不随机器人删除级联，过期数据只影响一次多余的探测，不值得为其建引用关系。`context_window=0` 的行是探测失败的负缓存标记（`source="detect_failed"`，TTL 10 分钟，区别于成功行的 24 小时），能力解析只认 >0 的值，标记行只用于期内免白探。

GORM 的坑：同一关联在 has-many/has-one（父侧）与 belongs-to（子侧）双侧声明时，生成的外键只取**父侧**字段的 `constraint` tag，belongs-to 侧写了也不生效。因此：

- 双侧声明：两侧都写 `constraint:OnDelete:CASCADE`（如 `Issue.Comments` + `IssueComment.Issue`）。
- 只声明 belongs-to 一侧：约束即生效，无需在父模型声明 has-many。
- 防回归靠 `TestAutoMigrate_AllForeignKeysCascade`（cmd/server）：新建库遍历 `PRAGMA foreign_key_list`，断言全库外键都是 CASCADE，例外须进白名单并注明理由。
- GORM 默认命名策略会把 `PublicGitHubTokenID` 蛇形化成 `public_git_hub_token_id`；需要特定列名时在模型上显式写 `gorm:"column:..."`（projects 的两个引用列即如此）。

## 项目搁置列（INT-165）

`projects` 新增 `shelved_at` / `shelved_until` 两列（TIMESTAMP，nullable，存量行 NULL），由 AutoMigrate 补齐、无回填。「搁置中」= `shelved_at` 非 NULL 且（`shelved_until` 为 NULL 即无限期，或截止未到）；**到期自动恢复没有定时任务**——读取侧按查询时刻判定，截止一过即视为未搁置。

同一口径的两处投影，改动判定必须同步修改：`model.Project.IsShelved(now)` 与 repository 层的等价 SQL 条件 `projectNotShelvedSQL`（`shelved_at IS NULL OR (shelved_until IS NOT NULL AND shelved_until <= now)`，dashboard 三条聚合查询与 fs_project list 的默认过滤用它）。时间列按 UTC 写入，驱动（glebarez）的存储格式与绑定参数格式一致，TEXT 比较即时间序。搁置不拦截任何读写，只影响聚合与挑选面（dashboard、推荐汇总、fs_project list）；写入走 `ProjectRepository.Shelve/Unshelve`（只动搁置三列），不走带审查联动副作用的 `Update`。

## 启动迁移顺序

main.go 依次：

1. `dropLegacyLogTables`：删旧版日志表。
2. `repairLegacyForeignKeys`：存量库外键纠偏，**必须在 AutoMigrate 之前**。AutoMigrate 发现缺失外键会隐式整表重建（SQLite 不支持 `ALTER TABLE ADD CONSTRAINT`），悬空行还在就直接失败；先清行再重建，AutoMigrate 才会把各表视为已合规而跳过。
3. `db.AutoMigrate(autoMigrateModels...)`。
4. 手工索引（`CREATE INDEX IF NOT EXISTS`）：放在纠偏与 AutoMigrate 之后，保证落在最终表上；表被重建时旧索引随旧表删除，这里的 `IF NOT EXISTS` 会重新创建。
5. 各数据回填/收尾迁移（`migrateUserRoles`、`migrateProjectGitHubTokens`、`backfillIssueSourceModel` 等）。

INT-129 新增两处：

- `migrateUserRoles`（AutoMigrate 与手工索引之后）：`users.role` 列由 AutoMigrate 补齐（存量行落默认 `member`），
  存量部署因此可能一个 admin 都没有；把 `created_at` 最早的用户提为 admin，幂等。
- `migrateDropIntegrations`（B 轮）：INT-119 的用户级 `integrations` 表整表 `DROP`（本仓库部署从未有人配置过）。
  表里还有行时**拒绝启动**，要求设 `FAST_SHIP_DROP_USER_INTEGRATIONS=1` 才删：一套部署只能有一个
  App 身份，多个用户各配一个时该选谁属于产品决策，升级不替用户挑；确认后重新配置的量级就是
  「在 GitHub 上创建 App」点一下。模型与手工索引同步删除，不交给 AutoMigrate。

纠偏失败回滚事务后 `log.Fatalf` 退出——宁可起不来也不带错跑，与其他迁移一致。

## repairLegacyForeignKeys（cmd/server/foreign_keys.go）

每次启动用 `PRAGMA foreign_key_list` 比对各子表的实际外键与期望集（列、父表、父列、CASCADE），不符合才重建，修过即永不再触发，不引入版本表。

需要重建时流程：

1. `VACUUM INTO '<db>.bak-<UTC时间>'` 备份一次（不是每表一份），不自动删除。
2. 单事务内、外键检查保持开启，逐表：
   - `DELETE` 掉指向不存在父行的悬空行（`NOT EXISTS` 逐外键 OR），删除数量写日志；
   - `ALTER TABLE` 改名旧表，删掉其占用名字的手写索引（`sqlite_autoindex` 随表走）；
   - `Migrator().CreateTable(模型)` 建带正确外键与模型索引的新表；
   - `INSERT INTO 新表 (共有列) SELECT 共有列 FROM 旧表`，再 `DROP` 旧表。

事务回滚则数据不变直接退出；恢复手段是回退上一版镜像 + 备份文件。

新增外键到存量子表时一律走这套机制：给模型补 belongs-to 字段、把期望外键加进 `legacyFKTables`，不要指望 AutoMigrate 自己处理悬空行。
