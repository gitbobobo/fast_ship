package repository

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/model"
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

// issueFTSTableDDL 是 Issue 全文检索的派生索引（INT-209）：issues_fts 是
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
var issueFTSTableDDL = []string{
	`CREATE TABLE IF NOT EXISTS issue_fts_docs (
    doc      INTEGER PRIMARY KEY,
    issue_id TEXT NOT NULL UNIQUE
)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
    title,
    body,
    tokenize='trigram'
)`,
}

// issueFTSDocTriggers 是三条同步触发器的期望定义（不含 IF NOT EXISTS——
// 存量库按 sqlite_master 原文与这里的期望串比对，不一致就 DROP 重建，因此
// 日后修改触发器定义不需要换名登记：改完常量，存量库下次启动自动收敛；
// IF NOT EXISTS 对已存在的旧定义是静默空操作，反而会造成行为与代码分叉，
// 与虚表的 legacy 检测是同一类陷阱）。
//
// UPDATE 触发器带两层守卫：UPDATE OF title, body 把触发范围收窄到 SET 里
// 列出这两列的语句（只改 state/updated_at/closed_at 的元数据写入路径——
// TouchUpdatedAt、批量关闭、同步回写——完全不走重索引）；WHEN 再按值判断，
// 全列写入（如 gorm Save）但 title/body 未变的也不重写。无守卫时一次元数据
// UPDATE 会成批重写正文索引（2000 行 × 3KB 正文实测 511ms，带守卫 3.2ms）。
// IS NOT 对 NULL 安全（body 列可空）。issues.id 不可变，doc 复用——先删
// FTS 旧行，再按同一 doc 重插。DELETE 先删 FTS 行（需要映射行还在），再删
// 映射行，两张表成对清理。INSERT 先补映射行，再按同一 doc 写 FTS 行
// （UNIQUE issue_id 保证取回唯一 doc）。
var issueFTSDocTriggers = []struct {
	name   string
	create string
}{
	{"trg_issues_fts_doc_insert", `CREATE TRIGGER trg_issues_fts_doc_insert
AFTER INSERT ON issues
BEGIN
    INSERT INTO issue_fts_docs(issue_id) VALUES (NEW.id);
    INSERT INTO issues_fts(rowid, title, body)
    SELECT doc, COALESCE(NEW.title, ''), COALESCE(NEW.body, '')
    FROM issue_fts_docs WHERE issue_id = NEW.id;
END`},
	{"trg_issues_fts_doc_update", `CREATE TRIGGER trg_issues_fts_doc_update
AFTER UPDATE OF title, body ON issues
WHEN OLD.title IS NOT NEW.title OR OLD.body IS NOT NEW.body
BEGIN
    DELETE FROM issues_fts WHERE rowid IN (SELECT doc FROM issue_fts_docs WHERE issue_id = OLD.id);
    INSERT INTO issues_fts(rowid, title, body)
    SELECT doc, COALESCE(NEW.title, ''), COALESCE(NEW.body, '')
    FROM issue_fts_docs WHERE issue_id = NEW.id;
END`},
	{"trg_issues_fts_doc_delete", `CREATE TRIGGER trg_issues_fts_doc_delete
AFTER DELETE ON issues
BEGIN
    DELETE FROM issues_fts WHERE rowid IN (SELECT doc FROM issue_fts_docs WHERE issue_id = OLD.id);
    DELETE FROM issue_fts_docs WHERE issue_id = OLD.id;
END`},
}

// hasLegacyIssuesFTS 检测库里的 issues_fts 是否不是当前形态。CREATE VIRTUAL
// TABLE IF NOT EXISTS 对已存在的任何旧结构都是静默空操作，收敛不到新形态，
// 必须先 DROP 重建。按 PRAGMA table_info 精确判定：列集合恰为 {title, body}
// 才是当前形态，其余（第 1 轮的 issue_id 列、或未来任何意外形态）一律重建。
// 表不存在返回 false。
func hasLegacyIssuesFTS(db *gorm.DB) (bool, error) {
	type ftsColumn struct {
		Name string
	}
	var cols []ftsColumn
	if err := db.Raw(`PRAGMA table_info(issues_fts)`).Scan(&cols).Error; err != nil {
		return false, err
	}
	if len(cols) == 0 {
		return false, nil
	}
	if len(cols) != 2 {
		return true, nil
	}
	return (cols[0].Name != "title" || cols[1].Name != "body") &&
		(cols[0].Name != "body" || cols[1].Name != "title"), nil
}

// EnsureIssueFTSSchema 在 AutoMigrate 之后补齐 Issue 全文检索：docid 映射表 +
// FTS5 虚表 + 三条同步触发器 + 存量行幂等回填。全部幂等，重复启动无副作用；
// 任何一条失败都返回错误，由启动方决定退出，绝不带错运行。
//
// 旧形态收敛：库里的 issues_fts 若不是当前形态（列集合 ≠ {title, body}），
// DROP 旧虚表后按新形态重建，存量行由回填补齐；旧触发器的摘除永远先于旧
// 虚表的 DROP（见 issueFTSTriggerDropDDL），且整个收敛过程包在一个事务里：
// 任一步中断都不会留下「触发器引用已删表」的死局。触发器定义按
// sqlite_master 原文与期望串比对，漂移即重建——任何起点（新库 / 第 1 轮库 /
// 中断残留 / 旧触发器定义）跑完都收敛到当前结构（有测试锁定）。
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
		for _, ddl := range issueFTSTableDDL {
			if err := tx.Exec(ddl).Error; err != nil {
				return fmt.Errorf("issue fts schema: %w", err)
			}
		}
		// 触发器按原文比对，漂移即重建：新建与定义变更共用同一条收敛路径。
		for _, trg := range issueFTSDocTriggers {
			var current string
			if err := tx.Raw(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg.name).Scan(&current).Error; err != nil {
				return fmt.Errorf("issue fts trigger check %s: %w", trg.name, err)
			}
			if current == trg.create {
				continue
			}
			if err := tx.Exec(`DROP TRIGGER IF EXISTS ` + trg.name).Error; err != nil {
				return fmt.Errorf("issue fts trigger drop %s: %w", trg.name, err)
			}
			if err := tx.Exec(trg.create).Error; err != nil {
				return fmt.Errorf("issue fts trigger create %s: %w", trg.name, err)
			}
		}
		// 回填触发器创建之前就存在的行：有 issues 行、无映射行 → 补映射；有映射
		// 行、无 FTS 行 → 按映射 doc 重插 FTS。旧形态 DROP 重建后 FTS 为空，两条
		// 连跑即完成全量重建。三方计数一致时直接跳过（启动快速路径）：两个
		// NOT IN 反连接都要扫全表，无缺口也付全量成本，十万行量级实测 ~200ms；
		// 计数不一致才对账。
		var issueCount, docCount, ftsCount int64
		if err := tx.Model(&model.Issue{}).Count(&issueCount).Error; err != nil {
			return fmt.Errorf("issue fts count issues: %w", err)
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM issue_fts_docs`).Scan(&docCount).Error; err != nil {
			return fmt.Errorf("issue fts count docs: %w", err)
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM issues_fts`).Scan(&ftsCount).Error; err != nil {
			return fmt.Errorf("issue fts count fts: %w", err)
		}
		if docCount == issueCount && ftsCount == docCount {
			return nil
		}
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
