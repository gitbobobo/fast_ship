package repository

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// issueFTSTriggerDropDDL 先于建表执行：旧触发器必须在任何旧虚表删除之前摘除，
// 否则两步之间进程退出会留下引用已删表的触发器——SQLite 编译任何 issues 上的
// UPDATE 时即报 no such table，下一次启动连 runBootMigrations 里的常规 UPDATE
// 都过不去，永远到不了 EnsureIssueFTSSchema 的修复入口。先摘触发器后，中断残留
// 只会是「无触发器的旧虚表」（无害，由 legacy 检测收敛）或「缺触发器的新虚表」
// （由建表与回填收敛）。
var issueFTSTriggerDropDDL = []string{
	`DROP TRIGGER IF EXISTS trg_issues_fts_insert`,
	`DROP TRIGGER IF EXISTS trg_issues_fts_update`,
	`DROP TRIGGER IF EXISTS trg_issues_fts_delete`,
}

// issueFTSSchemaDDL 是 Issue 全文检索的派生索引（INT-209）：issues_fts 是
// FTS5 trigram 虚表，只存 title/body 两个索引列，事实来源是 issues 表，检索
// 只按 issues.id 回连主表取行。FTS 行身份用整数 docid：issue_fts_docs 维护
// issue_id TEXT ↔ doc INTEGER 的双向唯一映射，FTS 行以 doc 为显式 rowid 写
// 入，MATCH 子查询只取 rowid 再回映射表换 issue_id——不按 docid 回读 FTS
// %_content 行。第 1 轮把 issue_id 作为 UNINDEXED 列存进 FTS，高命中查询物
// 化 IN 集合时要逐条回读 content 行，万级夹具上 count+list 各付一次，是
// 高命中查询 p50 回退的主因。也不走 issues 自身 rowid 做身份：issues 是
// TEXT 主键、无 INTEGER 主键，VACUUM 会重排其 rowid，当不了 FTS docid。
// doc 用普通 INTEGER PRIMARY KEY（不用 AUTOINCREMENT）：映射行与 FTS 行总在
// 同一触发器里成对删，id 复用无风险。trigram 最小可匹配 3 个 Unicode 字符，
// 更短的查询由 buildIssueSearchCondition 退回 LIKE 分支，口径与
// bot_memory_fts 先例一致。
//
// title/body 都经 COALESCE 防 NULL：issues.body 列可空，NULL 统一索引成
// 空串（空串不含任何 trigram，永不命中），与 LIKE 对 NULL 不匹配的旧语义
// 等价。
var issueFTSSchemaDDL = []string{
	`CREATE TABLE IF NOT EXISTS issue_fts_docs (
    doc      INTEGER PRIMARY KEY,
    issue_id TEXT NOT NULL UNIQUE
)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
    title,
    body,
    tokenize='trigram'
)`,
	// INSERT：先补映射行，再按同一 doc 写 FTS 行（UNIQUE issue_id 保证取回
	// 唯一 doc）。
	`CREATE TRIGGER IF NOT EXISTS trg_issues_fts_doc_insert
AFTER INSERT ON issues
BEGIN
    INSERT INTO issue_fts_docs(issue_id) VALUES (NEW.id);
    INSERT INTO issues_fts(rowid, title, body)
    SELECT doc, COALESCE(NEW.title, ''), COALESCE(NEW.body, '')
    FROM issue_fts_docs WHERE issue_id = NEW.id;
END`,
	// UPDATE：issues.id 不变，doc 复用——先删 FTS 旧行，再按同一 doc 重插。
	`CREATE TRIGGER IF NOT EXISTS trg_issues_fts_doc_update
AFTER UPDATE ON issues
BEGIN
    DELETE FROM issues_fts WHERE rowid IN (SELECT doc FROM issue_fts_docs WHERE issue_id = OLD.id);
    INSERT INTO issues_fts(rowid, title, body)
    SELECT doc, COALESCE(NEW.title, ''), COALESCE(NEW.body, '')
    FROM issue_fts_docs WHERE issue_id = NEW.id;
END`,
	// DELETE：先删 FTS 行（需要映射行还在），再删映射行，两张表成对清理。
	`CREATE TRIGGER IF NOT EXISTS trg_issues_fts_doc_delete
AFTER DELETE ON issues
BEGIN
    DELETE FROM issues_fts WHERE rowid IN (SELECT doc FROM issue_fts_docs WHERE issue_id = OLD.id);
    DELETE FROM issue_fts_docs WHERE issue_id = OLD.id;
END`,
}

// hasLegacyIssuesFTS 检测库里是否残留第 1 轮形态的 issues_fts（虚表 DDL 带
// UNINDEXED issue_id 列）。CREATE VIRTUAL TABLE IF NOT EXISTS 对已存在的旧
// 结构是静默空操作，靠 IF NOT EXISTS 收敛不到新形态，必须先 DROP 重建。
// 检测依据 sqlite_master 里虚表的 DDL 原文（新形态不含 issue_id 字样），
// 表不存在时返回 false。
func hasLegacyIssuesFTS(db *gorm.DB) (bool, error) {
	var ddl string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'issues_fts'`).Scan(&ddl).Error; err != nil {
		return false, err
	}
	return ddl != "" && strings.Contains(ddl, "issue_id"), nil
}

// EnsureIssueFTSSchema 在 AutoMigrate 之后补齐 Issue 全文检索：docid 映射表 +
// FTS5 虚表 + 三条同步触发器 + 存量行幂等回填。全部幂等（IF NOT EXISTS /
// 回填按 NOT IN 去重），重复启动无副作用；任何一条失败都返回错误，由启动方
// 决定退出，绝不带错运行。
//
// 旧形态收敛：库里的 issues_fts 若是第 1 轮形态（带 issue_id 列），DROP 旧
// 虚表后按新形态重建，存量行由回填补齐——任何起点（新库 / 第 1 轮库 / 新形态
// 库）跑完都收敛到同一结构。旧触发器的摘除永远先于旧虚表的 DROP（见
// issueFTSTriggerDropDDL），且整个收敛过程包在一个事务里：任一步中断都不会
// 留下「触发器引用已删表」的死局。
//
// 孤儿行边界：触发器挂在 issues 表上，正常删除路径（含项目外键 CASCADE）都
// 会同步清掉 FTS 行与映射行，实测不残留。唯一的理论成因是未来某个迁移
// DROP/重建 issues 表——触发器随表一起消失，两张表里会留下孤儿行。孤儿行按
// issues.id 回连主表，永远不会进入检索结果，只白占空间；重建 issues 表的
// 迁移应同步 DROP issues_fts 与 issue_fts_docs 并重跑本函数（回填按 NOT IN
// 天然幂等）。
func EnsureIssueFTSSchema(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		for _, stmt := range issueFTSTriggerDropDDL {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("issue fts legacy trigger drop: %w", err)
			}
		}
		legacy, err := hasLegacyIssuesFTS(tx)
		if err != nil {
			return fmt.Errorf("issue fts legacy check: %w", err)
		}
		if legacy {
			if err := tx.Exec(`DROP TABLE issues_fts`).Error; err != nil {
				return fmt.Errorf("issue fts legacy rebuild: %w", err)
			}
		}
		for _, ddl := range issueFTSSchemaDDL {
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("issue fts schema: %w", err)
			}
		}
		// 回填触发器创建之前就存在的行，覆盖两个方向的缺口（各自 NOT IN 去重，
		// 重复执行无副作用）：有 issues 行、无映射行 → 补映射；有映射行、无 FTS
		// 行 → 按映射 doc 重插 FTS。旧形态 DROP 重建后 FTS 为空，两条连跑即完成
		// 全量重建。
		backfillDocs := `INSERT INTO issue_fts_docs(issue_id)
SELECT id FROM issues
WHERE id NOT IN (SELECT issue_id FROM issue_fts_docs)`
		if err := tx.Exec(backfillDocs).Error; err != nil {
			return fmt.Errorf("issue fts docs backfill: %w", err)
		}
		backfillFTS := `INSERT INTO issues_fts(rowid, title, body)
SELECT d.doc, COALESCE(i.title, ''), COALESCE(i.body, '')
FROM issues i JOIN issue_fts_docs d ON d.issue_id = i.id
WHERE d.doc NOT IN (SELECT rowid FROM issues_fts)`
		if err := tx.Exec(backfillFTS).Error; err != nil {
			return fmt.Errorf("issue fts backfill: %w", err)
		}
		return nil
	})
}
