# 数据库

SQLite（glebarez/modernc 驱动，纯 Go 无 CGO），单文件 `database.path`。连接固定一个（`SetMaxOpenConns(1)`），靠 `busy_timeout` 串行化竞争。

## 外键约定

**所有外键都必须 `ON DELETE CASCADE`。** DSN 全局开启 `_pragma=foreign_keys(1)`，删除父行由数据库级联清理子行，业务代码不手动删子表。

GORM 的坑：同一关联在 has-many/has-one（父侧）与 belongs-to（子侧）双侧声明时，生成的外键只取**父侧**字段的 `constraint` tag，belongs-to 侧写了也不生效。因此：

- 双侧声明：两侧都写 `constraint:OnDelete:CASCADE`（如 `Issue.Comments` + `IssueComment.Issue`）。
- 只声明 belongs-to 一侧：约束即生效，无需在父模型声明 has-many。
- 防回归靠 `TestAutoMigrate_AllForeignKeysCascade`（cmd/server）：新建库遍历 `PRAGMA foreign_key_list`，断言全库外键都是 CASCADE，例外须进白名单并注明理由。

## 启动迁移顺序

main.go 依次：

1. `dropLegacyLogTables`：删旧版日志表。
2. `repairLegacyForeignKeys`：存量库外键纠偏，**必须在 AutoMigrate 之前**。AutoMigrate 发现缺失外键会隐式整表重建（SQLite 不支持 `ALTER TABLE ADD CONSTRAINT`），悬空行还在就直接失败；先清行再重建，AutoMigrate 才会把各表视为已合规而跳过。
3. `db.AutoMigrate(autoMigrateModels...)`。
4. 手工索引（`CREATE INDEX IF NOT EXISTS`）：放在纠偏与 AutoMigrate 之后，保证落在最终表上；表被重建时旧索引随旧表删除，这里的 `IF NOT EXISTS` 会重新创建。
5. 各数据回填函数（`backfillIssueSourceModel` 等）。

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
