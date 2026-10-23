package repository

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IssueRepository struct {
	db *gorm.DB
}

func NewIssueRepository(db *gorm.DB) *IssueRepository {
	return &IssueRepository{db: db}
}

func (r *IssueRepository) Create(issue *model.Issue) error {
	return r.db.Create(issue).Error
}

func (r *IssueRepository) CreateTx(tx *gorm.DB, issue *model.Issue) error {
	return tx.Create(issue).Error
}

func (r *IssueRepository) Transaction(fc func(tx *gorm.DB) error) error {
	return r.db.Transaction(fc)
}

func (r *IssueRepository) Save(issue *model.Issue) error {
	return r.db.Save(issue).Error
}

func (r *IssueRepository) SaveTx(tx *gorm.DB, issue *model.Issue) error {
	return tx.Save(issue).Error
}

// TouchUpdatedAt 仅刷新某个 issue 的 updated_at，用于在不改动主表业务字段时反映状态变更。
func (r *IssueRepository) TouchUpdatedAt(tx *gorm.DB, id string, at time.Time) error {
	return tx.Model(&model.Issue{}).Where("id = ?", id).Update("updated_at", at).Error
}

func (r *IssueRepository) FindByID(id string) (*model.Issue, error) {
	return r.FindByIDTx(r.db, id)
}

func (r *IssueRepository) FindByIDTx(tx *gorm.DB, id string) (*model.Issue, error) {
	var issue model.Issue
	if err := tx.Preload("GitHubMeta").Where("id = ?", id).First(&issue).Error; err != nil {
		return nil, err
	}
	return &issue, nil
}

func (r *IssueRepository) ListByIDs(ids []string) (map[string]model.Issue, error) {
	return r.ListByIDsTx(r.db, ids)
}

func (r *IssueRepository) ListByIDsTx(tx *gorm.DB, ids []string) (map[string]model.Issue, error) {
	result := make(map[string]model.Issue, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	var issues []model.Issue
	if err := tx.Preload("GitHubMeta").Where("id IN ?", ids).Find(&issues).Error; err != nil {
		return nil, err
	}
	for _, issue := range issues {
		result[issue.ID] = issue
	}
	return result, nil
}

// IssueListQuery 是 Issue 列表的过滤条件，与 service.IssueListFilters 同义：
// 由 service 层映射后传入（Query 保证已 TrimSpace），SQL 构造与过滤语义集中在 repository。
type IssueListQuery struct {
	ProjectID string
	State     string
	Source    string
	Workflow  string // "", "unset", "todo", "in_progress", "done"
	Query     string // 空串不过滤
	Label     string
	Assignee  string
	Milestone string
}

// IssueFilterOptionSource 是筛选下拉选项的数据源投影：一行 Issue 摘出 GitHub meta
// 与 internal meta 的标签/指派人/里程碑 JSON，meta 缺失的列为空串。
type IssueFilterOptionSource struct {
	LabelsJSON         string
	AssigneesJSON      string
	MilestoneJSON      string
	InternalLabelsJSON string
}

// issueListColumns 是列表分页查询的投影列：排除 body/body_html，列表响应不携带
// 正文（详情接口走 FindByID 读全列）。body 只允许出现在 WHERE 的搜索条件里。
const issueListColumns = "issues.id, issues.project_id, issues.source, issues.sequence_number, issues.state, issues.state_reason, issues.title, issues.author_user_id, issues.author_login, issues.author_avatar_url, issues.closed_at, issues.created_at, issues.updated_at"

// issueGitHubMetaListColumns 是列表预载 GitHub meta 的投影列：排除 raw_json 与
// collections_* 列（列表响应用不到），必须含 issue_id 供 GORM 回填关联。
const issueGitHubMetaListColumns = "issue_id, project_id, github_issue_id, github_node_id, number, html_url, author_association, assignees_json, labels_json, milestone_json, reactions_json, comments_count, locked, active_lock_reason, synced_at"

// issueSortNumberExpr 是排序决胜键：GitHub issue 走 meta.number，否则用项目内序号。
const issueSortNumberExpr = "CASE WHEN issues.source = 'github' AND gh.number IS NOT NULL THEN gh.number ELSE issues.sequence_number END"

// issueCommentCountExpr 是评论数排序键：无 GitHub meta 记 0，与内存实现一致。
const issueCommentCountExpr = "COALESCE(gh.comments_count, 0)"

// escapeLikePattern 转义 LIKE 通配符，配合 ESCAPE '\' 使用，让用户输入里的
// %、_、\ 按字面量匹配。
func escapeLikePattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// issueQueryAsNumber 判断 q 是否为整数字面量（strconv.Atoi 语义，允许 +/- 前缀），
// 是则追加序号与 GitHub number 的精确匹配分支。
func issueQueryAsNumber(query string) (int, bool) {
	num, err := strconv.Atoi(strings.TrimSpace(query))
	return num, err == nil
}

// buildIssueSearchCondition 构造 q 搜索条件：标题/正文/作者/reference 的大小写
// 不敏感子串，纯数字时再 OR 序号与 GitHub number 精确匹配。整组条件带括号，
// 与其他过滤器之间保持 AND 语义。已知边界：SQLite 的 lower/LIKE 只折叠 ASCII，
// 与 Go strings.ToLower 的 Unicode 折叠在非 ASCII 大写字母上不一致（中文无影响）。
func buildIssueSearchCondition(query string) (string, []interface{}) {
	pattern := "%" + escapeLikePattern(strings.ToLower(query)) + "%"
	referenceExpr := "CASE WHEN issues.source = 'github' AND gh.number IS NOT NULL " +
		"THEN 'GH-' || gh.number ELSE 'INT-' || issues.sequence_number END"
	expr := "(lower(issues.title) LIKE ? ESCAPE '\\' OR lower(issues.body) LIKE ? ESCAPE '\\' " +
		"OR lower(issues.author_login) LIKE ? ESCAPE '\\' OR lower(" + referenceExpr + ") LIKE ? ESCAPE '\\')"
	args := []interface{}{pattern, pattern, pattern, pattern}
	if num, ok := issueQueryAsNumber(query); ok {
		expr += " OR issues.sequence_number = ? OR gh.number = ?"
		args = append(args, num, num)
	}
	return expr, args
}

// jsonColumnOr 返回保证合法的 JSON 列表达式：列值为 NULL、空串或坏 JSON 时回落到
// fallback。迁移前的内存过滤用 parseJSON/extractLabelNames 对坏数据一律按空处理，
// 直接把这些列喂给 json_each/json_extract 会在坏数据上抛 malformed JSON，让整条
// 列表查询失败——这里必须保持「坏数据按空处理」的旧契约。
func jsonColumnOr(column, fallback string) string {
	return "CASE WHEN json_valid(" + column + ") THEN " + column + " ELSE '" + fallback + "' END"
}

// jsonArrayOr 返回保证是 JSON 数组的列表达式：NULL、空串、坏 JSON 与其它顶层形态
// （标量、对象）统一回落到 '[]'。迁移前的 extractLabelNames 只接受字符串数组或对象
// 数组，顶层非数组一律按「没有标签」处理；不这样收口的话，`"urgent"`、
// `{"name":"urgent"}` 这类行的元素会被 json_each 取出来意外命中标签筛选。
// json_type 必须在 json_valid 为真后才能调用（坏 JSON 上会抛 malformed JSON），
// 且本驱动不保证 AND 短路，因此用嵌套 CASE 而不是 AND。
func jsonArrayOr(column string) string {
	return "CASE WHEN json_valid(" + column + ") THEN (CASE WHEN json_type(" + column + ") = 'array' THEN " + column + " ELSE '[]' END) ELSE '[]' END"
}

// buildFilteredQuery 构造 Issue 列表过滤查询：issues 为主表左联两张 meta 表
// （两表 issue_id 都是唯一键，不会行扇出）。JSON 列的 NULL、空串与坏 JSON 直接喂给
// json_each/json_extract 会抛 malformed JSON 拖垮整条查询，统一经 jsonColumnOr 的
// json_valid 防护回落到合法默认值，保持内存实现「坏数据按空处理」的契约；meta 不存在
// 的行（JOIN 出 NULL）走同一路径得空集/NULL，与「meta 为空直接不匹配」一致。
func (r *IssueRepository) buildFilteredQuery(q IssueListQuery) *gorm.DB {
	query := r.db.Model(&model.Issue{}).
		Joins("LEFT JOIN issue_github_meta AS gh ON gh.issue_id = issues.id").
		Joins("LEFT JOIN issue_internal_meta AS im ON im.issue_id = issues.id").
		Where("issues.project_id = ?", q.ProjectID)

	if q.State != "" {
		query = query.Where("issues.state = ?", q.State)
	}
	if q.Source != "" {
		query = query.Where("issues.source = ?", q.Source)
	}
	switch q.Workflow {
	case "":
		// 不过滤
	case "unset":
		query = query.Where("(im.issue_id IS NULL OR im.workflow_status = '')")
	default:
		query = query.Where("im.workflow_status = ?", q.Workflow)
	}
	if q.Label != "" {
		// 标签历史数据有两种形态：纯名称数组 ["a","b"] 与对象数组 [{"name":"a"}]
		// （8b274f3 改存储格式前的存量行），两种都要参与匹配。顶层必须是数组、元素
		// 只认字符串与对象两种形态，与迁移前的 extractLabelNames 一致：顶层标量/
		// 对象的行经 jsonArrayOr 收口按「没有标签」处理，数字/布尔/null 元素与无
		// name 的对象元素都得 NULL，不匹配任何筛选值；混排数组仍按元素处理（更
		// 宽容，保持现状）。语义照旧是 EqualFold（lower 等值）。两个 EXISTS 用 OR
		// 连接并作为单个 Where 传入，GORM 会整体加括号，不影响与其他条件的 AND
		// 语义。
		query = query.Where(
			"EXISTS (SELECT 1 FROM json_each("+jsonArrayOr("gh.labels_json")+") AS je "+
				"WHERE lower(CASE je.type WHEN 'object' THEN json_extract(je.value,'$.name') "+
				"WHEN 'text' THEN je.value END) = lower(?)) "+
				"OR EXISTS (SELECT 1 FROM json_each("+jsonArrayOr("im.labels_json")+") AS je "+
				"WHERE lower(CASE je.type WHEN 'object' THEN json_extract(je.value,'$.name') "+
				"WHEN 'text' THEN je.value END) = lower(?))",
			q.Label, q.Label)
	}
	if q.Assignee != "" {
		// 元素应为 {"login":...} 对象；CASE 只对对象元素求值 json_extract，字符串
		// 元素得 NULL 不匹配——旧实现对无法解析成对象数组的行整体不匹配，新实现按
		// 元素处理，更宽容。
		query = query.Where(
			"gh.issue_id IS NOT NULL AND EXISTS (SELECT 1 FROM json_each("+jsonColumnOr("gh.assignees_json", "[]")+") AS je "+
				"WHERE lower(CASE WHEN je.type = 'object' THEN json_extract(je.value,'$.login') END) = lower(?))",
			q.Assignee)
	}
	if q.Milestone != "" {
		// 空串/坏 JSON 回落 '{}'；合法但非对象的 JSON json_extract 得 NULL，
		// 比较为 NULL → 不匹配，与旧行为一致。
		query = query.Where(
			"gh.issue_id IS NOT NULL AND lower(json_extract("+jsonColumnOr("gh.milestone_json", "{}")+",'$.title')) = lower(?)",
			q.Milestone)
	}
	if q.Query != "" {
		expr, args := buildIssueSearchCondition(q.Query)
		query = query.Where(expr, args...)
	}
	return query
}

// issueListOrderClause 把排序键映射为 ORDER BY 表达式；未知键回落到 updated_desc，
// 与迁移前内存排序的默认分支一致。决胜规则同旧 sortIssues：时间同值按 sortNum，
// 评论数同值按 updated_at（无第三级）。每个分支末尾以 sequence_number DESC 收尾
// 作为唯一键兜底：上述排序键完全并列时（如 comments_asc/desc 的 commentCount 与
// updated_at 都相同），SQL 不保证并列行的先后，LIMIT/OFFSET 分页跨请求会不稳定；
// sequence_number 有项目内唯一索引 idx_issues_project_sequence，兜底后得到全序。
func issueListOrderClause(sortKey string) string {
	switch sortKey {
	case "updated_asc":
		return "issues.updated_at ASC, " + issueSortNumberExpr + " ASC, issues.sequence_number DESC"
	case "created_desc":
		return "issues.created_at DESC, " + issueSortNumberExpr + " DESC, issues.sequence_number DESC"
	case "created_asc":
		return "issues.created_at ASC, " + issueSortNumberExpr + " ASC, issues.sequence_number DESC"
	case "comments_desc":
		return issueCommentCountExpr + " DESC, issues.updated_at DESC, issues.sequence_number DESC"
	case "comments_asc":
		return issueCommentCountExpr + " ASC, issues.updated_at ASC, issues.sequence_number DESC"
	default:
		return "issues.updated_at DESC, " + issueSortNumberExpr + " DESC, issues.sequence_number DESC"
	}
}

// CountFiltered 统计过滤后的总数，供列表分页 total；不读正文。
func (r *IssueRepository) CountFiltered(q IssueListQuery) (int64, error) {
	var total int64
	if err := r.buildFilteredQuery(q).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// ListFiltered 分页取过滤后的 Issue：SQL 带 LIMIT/OFFSET，投影不含 body/body_html，
// GitHub meta 预载不含 raw_json。limit<=0 时不加 LIMIT，仅限不需要分页边界的
// 内部调用（如批量上限探测）。
func (r *IssueRepository) ListFiltered(q IssueListQuery, sortKey string, offset, limit int) ([]model.Issue, error) {
	query := r.buildFilteredQuery(q).
		Select(issueListColumns).
		Order(issueListOrderClause(sortKey)).
		Offset(offset)
	if limit > 0 {
		query = query.Limit(limit)
	}
	var issues []model.Issue
	if err := query.
		Preload("GitHubMeta", func(db *gorm.DB) *gorm.DB {
			return db.Select(issueGitHubMetaListColumns)
		}).
		Find(&issues).Error; err != nil {
		return nil, err
	}
	return issues, nil
}

// ListFilterOptionSources 投影项目全部 Issue 的筛选下拉数据源（不含正文与
// raw_json）：每行一个 Issue，摘出两张 meta 表的标签/指派人/里程碑 JSON，
// meta 缺失的列 COALESCE 成空串，交由上层按空处理。
func (r *IssueRepository) ListFilterOptionSources(projectID string) ([]IssueFilterOptionSource, error) {
	var rows []IssueFilterOptionSource
	err := r.db.Model(&model.Issue{}).
		Joins("LEFT JOIN issue_github_meta AS gh ON gh.issue_id = issues.id").
		Joins("LEFT JOIN issue_internal_meta AS im ON im.issue_id = issues.id").
		Where("issues.project_id = ?", projectID).
		Select("COALESCE(gh.labels_json,'') AS labels_json, COALESCE(gh.assignees_json,'') AS assignees_json, " +
			"COALESCE(gh.milestone_json,'') AS milestone_json, COALESCE(im.labels_json,'') AS internal_labels_json").
		Scan(&rows).Error
	return rows, err
}

// NextSequenceNumberTx 在事务句柄上取下一个项目内序号。必须随写入同事务调用：
// MaxOpenConns=1 下事务串行执行，MAX+1 与 INSERT 不会交错；事务外取号会让并发
// 创建读到同一个 MAX，撞 idx_issues_project_sequence（INT-96）。
func (r *IssueRepository) NextSequenceNumberTx(tx *gorm.DB, projectID string) (int, error) {
	var currentMax int
	if err := tx.Model(&model.Issue{}).
		Where("project_id = ?", projectID).
		Select("COALESCE(MAX(sequence_number), 0)").
		Scan(&currentMax).Error; err != nil {
		return 0, err
	}
	return currentMax + 1, nil
}

func (r *IssueRepository) ListStaleProjectIDs(before time.Time, limit int) ([]string, error) {
	var ids []string
	query := r.db.Model(&model.IssueSyncState{}).
		Where("status != ?", model.IssueSyncStatusRunning).
		Where("last_synced_at IS NULL OR last_synced_at < ?", before).
		Order("last_synced_at ASC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Pluck("project_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

type IssueGitHubMetaRepository struct {
	db *gorm.DB
}

func NewIssueGitHubMetaRepository(db *gorm.DB) *IssueGitHubMetaRepository {
	return &IssueGitHubMetaRepository{db: db}
}

func (r *IssueGitHubMetaRepository) Upsert(meta *model.IssueGitHubMeta) error {
	return r.UpsertTx(r.db, meta)
}

func (r *IssueGitHubMetaRepository) UpsertTx(tx *gorm.DB, meta *model.IssueGitHubMeta) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "project_id"}, {Name: "github_issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"issue_id",
			"github_node_id",
			"number",
			"html_url",
			"author_association",
			"assignees_json",
			"labels_json",
			"milestone_json",
			"reactions_json",
			"comments_count",
			"locked",
			"active_lock_reason",
			"synced_at",
			"raw_json",
		}),
	}).Create(meta).Error
}

func (r *IssueGitHubMetaRepository) FindByProjectAndGitHubID(projectID string, gitHubIssueID int64) (*model.IssueGitHubMeta, error) {
	return r.FindByProjectAndGitHubIDTx(r.db, projectID, gitHubIssueID)
}

func (r *IssueGitHubMetaRepository) FindByProjectAndGitHubIDTx(tx *gorm.DB, projectID string, gitHubIssueID int64) (*model.IssueGitHubMeta, error) {
	var meta model.IssueGitHubMeta
	if err := tx.Where("project_id = ? AND github_issue_id = ?", projectID, gitHubIssueID).First(&meta).Error; err != nil {
		return nil, err
	}
	return &meta, nil
}

// AdvanceCollectionsWatermarkIfVersionTx 在集合镜像事务内复核失效版本后推进该
// Issue 评论/时间线集合的镜像水位。预期版本与当前版本不一致，说明取数期间发生了
// 并发 push（writePartialCollections 递增了 collections_invalidated_version）：
// 本次取到的是旧快照，放弃推进并返回 false，让空水位保留给下一次重拉（INT-105）。
// 版本读取与水位写入在同一事务内，MaxOpenConns=1 下与并发 push 的失效事务串行，
// 不会出现「读到的版本还没变、写入时已经变了」的窗口。必须与集合镜像写入
// （ReplaceSyncedTx）同一事务：事务外单独推进会与并发 push 路径的镜像替换交错，
// 留下「旧镜像 + 新水位」的组合，缺失的集合内容再也不会被判据发现重拉。
func (r *IssueGitHubMetaRepository) AdvanceCollectionsWatermarkIfVersionTx(tx *gorm.DB, issueID string, expectedVersion int64, remoteUpdatedAt time.Time, commentsCount int) (bool, error) {
	var meta model.IssueGitHubMeta
	err := tx.Select("collections_invalidated_version").
		Where("issue_id = ?", issueID).First(&meta).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
		// meta 不在（issue 已删）时没有可推进的水位，交由上层跳过。
		return false, nil
	}
	if meta.CollectionsInvalidatedVersion != expectedVersion {
		return false, nil
	}
	if err := tx.Model(&model.IssueGitHubMeta{}).
		Where("issue_id = ?", issueID).
		Updates(map[string]any{
			"collections_synced_updated_at": remoteUpdatedAt,
			"collections_synced_comments":   commentsCount,
			"collections_heal_queued_at":    nil,
		}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// InvalidateCollectionsTx 让该 Issue 的集合镜像失效，供 push 路径使用：push 只
// 重拉了时间线或一条评论、没有重拉两个集合，不能推进水位，只能置空让下一次后台
// 同步（增量列表或兜底）保守重拉。同时递增 collections_invalidated_version，使
// 并发进行中的完整镜像事务能在提交前发现取数已过期、放弃旧快照。
// 队列位置（collections_heal_queued_at）只在「健康→失效」时分配：本来就已空水位
// （还在排队等待）的条目保持不变，否则等待中的 push 会把条目送到队尾、被新进
// 条目插队（INT-105）。必须与集合镜像写入（ReplaceSyncedTx）同一事务，理由同
// AdvanceCollectionsWatermarkIfVersionTx。
func (r *IssueGitHubMetaRepository) InvalidateCollectionsTx(tx *gorm.DB, issueID string) error {
	now := time.Now().UTC()
	return tx.Model(&model.IssueGitHubMeta{}).
		Where("issue_id = ?", issueID).
		Updates(map[string]any{
			"collections_synced_updated_at":   nil,
			"collections_synced_comments":     0,
			"collections_invalidated_version": gorm.Expr("collections_invalidated_version + 1"),
			"collections_heal_queued_at": gorm.Expr(
				"CASE WHEN collections_synced_updated_at IS NULL THEN collections_heal_queued_at ELSE ? END", now),
		}).Error
}

// MarkCollectionsHealQueued 把条目移到兜底队列尾部：记录一次实际尝试（成功或
// 失败都该调），候选按 collections_heal_queued_at 升序取，因此下次会排在其它
// 等待条目之后，形成“最久未尝试优先”的公平轮转（INT-105）。
func (r *IssueGitHubMetaRepository) MarkCollectionsHealQueued(issueID string, at time.Time) error {
	return r.db.Model(&model.IssueGitHubMeta{}).
		Where("issue_id = ?", issueID).
		Update("collections_heal_queued_at", at).Error
}

// staleCollectionsSafeLimit 是 ListStaleCollectionsByProject 的安全上限：兜底查询
// 必须保持有界，limit 非正或超过它都回落为它。
const staleCollectionsSafeLimit = 100

// ListStaleCollectionsByProject 列出该项目集合水位为空的 GitHub meta 行，是同步
// 兜底重拉（INT-105 healStaleCollections）的数据源：push 部分写入清空水位、或
// 加列前的旧数据没有水位，这些 Issue 的两个集合镜像不完整。按队列位置升序取
// （NULL 最前，仅剩加列前的存量行；其余按首次入队时间先后）——等待期间被重新
// 失效不会改变位置，实际尝试后才移到队尾，所以新进入的条目插不到已在等待的旧
// 条目前面，形成近似先来先服务，每个条目最终都会被尝试（队列位置是墙上时钟，
// 依赖系统时间正常前进；持续的回拨只会推迟重试，不影响正确性）。limit 决定单轮重拉的
// 有界规模（防 GitHub 限流）。
func (r *IssueGitHubMetaRepository) ListStaleCollectionsByProject(projectID string, limit int) ([]model.IssueGitHubMeta, error) {
	if limit <= 0 || limit > staleCollectionsSafeLimit {
		limit = staleCollectionsSafeLimit
	}
	var metas []model.IssueGitHubMeta
	if err := r.db.
		Where("project_id = ? AND collections_synced_updated_at IS NULL", projectID).
		Order("collections_heal_queued_at ASC").
		Limit(limit).
		Find(&metas).Error; err != nil {
		return nil, err
	}
	return metas, nil
}

type IssueCommentRepository struct {
	db *gorm.DB
}

func NewIssueCommentRepository(db *gorm.DB) *IssueCommentRepository {
	return &IssueCommentRepository{db: db}
}

func (r *IssueCommentRepository) Upsert(comment *model.IssueComment) error {
	return r.UpsertTx(r.db, comment)
}

func (r *IssueCommentRepository) UpsertTx(tx *gorm.DB, comment *model.IssueComment) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}, {Name: "github_comment_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source",
			"author_user_id",
			"github_node_id",
			"body",
			"body_html",
			"html_url",
			"author_login",
			"author_avatar_url",
			"author_association",
			"reactions_json",
			"created_at",
			"updated_at",
			"raw_json",
			"idempotency_key",
		}),
	}).Create(comment).Error
}

// ReplaceSyncedTx 把该 Issue 的 GitHub 评论镜像替换成本轮拉取的集合：逐条 upsert
// 后清掉不在集合内的镜像行，任一步失败整体回滚。必须在调用方的事务内与时间线替换
// （IssueTimelineRepository.ReplaceSyncedTx）及集合水位变更
// （AdvanceCollectionsWatermarkIfVersionTx/InvalidateCollectionsTx）一起提交，理由见 issue_mirror.go 的
// writeFullCollections；远端分页必须在事务外拉完——事务内只执行数据库操作。
func (r *IssueCommentRepository) ReplaceSyncedTx(tx *gorm.DB, issueID string, comments []*model.IssueComment) error {
	keepIDs := make([]int64, 0, len(comments))
	for _, comment := range comments {
		if err := r.UpsertTx(tx, comment); err != nil {
			return err
		}
		keepIDs = append(keepIDs, comment.GitHubCommentID)
	}
	return r.DeleteMissingTx(tx, issueID, keepIDs)
}

func (r *IssueCommentRepository) Create(comment *model.IssueComment) error {
	return r.db.Create(comment).Error
}

func (r *IssueCommentRepository) FindByIdempotencyKey(issueID, key string) (*model.IssueComment, error) {
	var comment model.IssueComment
	if err := r.db.Where("issue_id = ? AND idempotency_key = ?", issueID, key).First(&comment).Error; err != nil {
		return nil, err
	}
	return &comment, nil
}

// DeleteMissing 删除该 Issue 下不在 gitHubCommentIDs 内的 GitHub 来源评论；
// 空集表示清掉全部镜像行。source='internal' 的本地评论（负数合成 ID）不受清理影响。
func (r *IssueCommentRepository) DeleteMissing(issueID string, gitHubCommentIDs []int64) error {
	return r.DeleteMissingTx(r.db, issueID, gitHubCommentIDs)
}

func (r *IssueCommentRepository) DeleteMissingTx(tx *gorm.DB, issueID string, gitHubCommentIDs []int64) error {
	query := tx.Where("issue_id = ? AND source = ?", issueID, model.IssueSourceGitHub)
	if len(gitHubCommentIDs) > 0 {
		query = query.Where("github_comment_id NOT IN ?", gitHubCommentIDs)
	}
	return query.Delete(&model.IssueComment{}).Error
}

func (r *IssueCommentRepository) List(issueID string, page, pageSize int) ([]model.IssueComment, int64, error) {
	var comments []model.IssueComment
	var total int64

	query := r.db.Model(&model.IssueComment{}).Where("issue_id = ?", issueID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at ASC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&comments).Error
	return comments, total, err
}

func (r *IssueCommentRepository) ListAllByIssueID(issueID string) ([]model.IssueComment, error) {
	var comments []model.IssueComment
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("created_at ASC, id ASC").
		Find(&comments).Error
	return comments, err
}

// CountSynced 返回该 Issue 下 source='github' 的镜像评论行数。
func (r *IssueCommentRepository) CountSynced(issueID string) (int, error) {
	var count int64
	if err := r.db.Model(&model.IssueComment{}).
		Where("issue_id = ? AND source = ?", issueID, model.IssueSourceGitHub).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

// MaxCreatedAt 返回该 Issue 本地评论的最新 created_at，没有评论时返回 nil。
// 已读水位对齐它，避免用服务器当前时间漏掉时钟偏差内刚同步的评论。
func (r *IssueCommentRepository) MaxCreatedAt(issueID string) (*time.Time, error) {
	var row struct {
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	err := r.db.Model(&model.IssueComment{}).
		Select("created_at").
		Where("issue_id = ?", issueID).
		Order("created_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row.CreatedAt, nil
}

func (r *IssueCommentRepository) NextSyntheticCommentID(issueID string) (int64, error) {
	var currentMin *int64
	if err := r.db.Model(&model.IssueComment{}).
		Where("issue_id = ? AND github_comment_id < 0", issueID).
		Select("MIN(github_comment_id)").
		Scan(&currentMin).Error; err != nil {
		return 0, err
	}
	if currentMin == nil {
		return -1, nil
	}
	return *currentMin - 1, nil
}

type IssueTimelineRepository struct {
	db *gorm.DB
}

func NewIssueTimelineRepository(db *gorm.DB) *IssueTimelineRepository {
	return &IssueTimelineRepository{db: db}
}

func (r *IssueTimelineRepository) Upsert(event *model.IssueTimelineEvent) error {
	return r.UpsertTx(r.db, event)
}

func (r *IssueTimelineRepository) UpsertTx(tx *gorm.DB, event *model.IssueTimelineEvent) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}, {Name: "event_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"github_event_id",
			"event_type",
			"actor_login",
			"actor_avatar_url",
			"body",
			"summary",
			"payload_json",
			"created_at",
		}),
	}).Create(event).Error
}

// ReplaceSyncedTx 把该 Issue 的时间线镜像替换成本轮拉取的集合：逐条 upsert 后
// 清掉 event_key 不在集合内的行，任一步失败整体回滚。必须在调用方的事务内与
// 集合水位变更（AdvanceCollectionsWatermarkIfVersionTx/InvalidateCollectionsTx）一起提交；远端分页必须
// 在事务外拉完——事务内只执行数据库操作。
func (r *IssueTimelineRepository) ReplaceSyncedTx(tx *gorm.DB, issueID string, events []*model.IssueTimelineEvent) error {
	keepKeys := make([]string, 0, len(events))
	for _, event := range events {
		if err := r.UpsertTx(tx, event); err != nil {
			return err
		}
		keepKeys = append(keepKeys, event.EventKey)
	}
	return r.DeleteMissingTx(tx, issueID, keepKeys)
}

func (r *IssueTimelineRepository) DeleteMissing(issueID string, eventKeys []string) error {
	return r.DeleteMissingTx(r.db, issueID, eventKeys)
}

func (r *IssueTimelineRepository) DeleteMissingTx(tx *gorm.DB, issueID string, eventKeys []string) error {
	query := tx.Where("issue_id = ?", issueID)
	if len(eventKeys) > 0 {
		query = query.Where("event_key NOT IN ?", eventKeys)
	}
	return query.Delete(&model.IssueTimelineEvent{}).Error
}

func (r *IssueTimelineRepository) List(issueID string, includeCommented bool, page, pageSize int) ([]model.IssueTimelineEvent, int64, error) {
	var events []model.IssueTimelineEvent
	var total int64

	query := r.db.Model(&model.IssueTimelineEvent{}).Where("issue_id = ?", issueID)
	if !includeCommented {
		query = query.Where("event_type != ?", "commented")
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("created_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&events).Error
	return events, total, err
}

// Count 返回该 Issue 的时间线镜像行数；时间线无来源概念，全部行都属镜像。
func (r *IssueTimelineRepository) Count(issueID string) (int, error) {
	var count int64
	if err := r.db.Model(&model.IssueTimelineEvent{}).
		Where("issue_id = ?", issueID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

type IssueSyncStateRepository struct {
	db *gorm.DB
}

func NewIssueSyncStateRepository(db *gorm.DB) *IssueSyncStateRepository {
	return &IssueSyncStateRepository{db: db}
}

func (r *IssueSyncStateRepository) Get(projectID string) (*model.IssueSyncState, error) {
	var state model.IssueSyncState
	if err := r.db.Where("project_id = ?", projectID).First(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *IssueSyncStateRepository) GetOrCreate(projectID string) (*model.IssueSyncState, error) {
	state := &model.IssueSyncState{
		ProjectID: projectID,
		Status:    model.IssueSyncStatusIdle,
	}
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoNothing: true,
	}).Create(state).Error; err != nil {
		return nil, err
	}
	return r.Get(projectID)
}

// GetOrCreateByProjectIDs 是 GetOrCreate 的批量形态：一条
// INSERT ... ON CONFLICT DO NOTHING 补齐缺失行，再一次 SELECT 拉回全部，
// 供项目列表按页填充 issue_sync；无行的项目会在返回前被创建，因此全部出现
// 在结果中。
func (r *IssueSyncStateRepository) GetOrCreateByProjectIDs(projectIDs []string) (map[string]*model.IssueSyncState, error) {
	result := make(map[string]*model.IssueSyncState, len(projectIDs))
	if len(projectIDs) == 0 {
		return result, nil
	}
	states := make([]model.IssueSyncState, len(projectIDs))
	for i, id := range projectIDs {
		states[i] = model.IssueSyncState{ProjectID: id, Status: model.IssueSyncStatusIdle}
	}
	if err := r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "project_id"}},
		DoNothing: true,
	}).Create(&states).Error; err != nil {
		return nil, err
	}
	var rows []model.IssueSyncState
	if err := r.db.Where("project_id IN ?", projectIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		result[rows[i].ProjectID] = &rows[i]
	}
	return result, nil
}

func (r *IssueSyncStateRepository) Save(state *model.IssueSyncState) error {
	return r.db.Save(state).Error
}

type IssueInternalMetaRepository struct {
	db *gorm.DB
}

func NewIssueInternalMetaRepository(db *gorm.DB) *IssueInternalMetaRepository {
	return &IssueInternalMetaRepository{db: db}
}

func (r *IssueInternalMetaRepository) Get(issueID string) (*model.IssueInternalMeta, error) {
	return r.GetTx(r.db, issueID)
}

func (r *IssueInternalMetaRepository) GetTx(tx *gorm.DB, issueID string) (*model.IssueInternalMeta, error) {
	var meta model.IssueInternalMeta
	if err := tx.Where("issue_id = ?", issueID).First(&meta).Error; err != nil {
		return nil, err
	}
	return &meta, nil
}

func (r *IssueInternalMetaRepository) ListByIssueIDs(issueIDs []string) (map[string]model.IssueInternalMeta, error) {
	return r.ListByIssueIDsTx(r.db, issueIDs)
}

func (r *IssueInternalMetaRepository) ListByIssueIDsTx(tx *gorm.DB, issueIDs []string) (map[string]model.IssueInternalMeta, error) {
	result := make(map[string]model.IssueInternalMeta, len(issueIDs))
	if len(issueIDs) == 0 {
		return result, nil
	}

	var metas []model.IssueInternalMeta
	if err := tx.Where("issue_id IN ?", issueIDs).Find(&metas).Error; err != nil {
		return nil, err
	}

	for _, meta := range metas {
		result[meta.IssueID] = meta
	}
	return result, nil
}

func (r *IssueInternalMetaRepository) Upsert(meta *model.IssueInternalMeta) error {
	return r.UpsertTx(r.db, meta)
}

func (r *IssueInternalMetaRepository) UpsertTx(tx *gorm.DB, meta *model.IssueInternalMeta) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "issue_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"workflow_status",
			"progress_percent",
			"checklist_total",
			"checklist_done",
			"started_at",
			"completed_at",
			"checklist_updated_at",
			"labels_json",
			"updated_by_user_id",
			"updated_at",
		}),
	}).Create(meta).Error
}

type IssueChecklistRepository struct {
	db *gorm.DB
}

func NewIssueChecklistRepository(db *gorm.DB) *IssueChecklistRepository {
	return &IssueChecklistRepository{db: db}
}

func (r *IssueChecklistRepository) ListByIssueID(issueID string) ([]model.IssueChecklistItem, error) {
	var items []model.IssueChecklistItem
	err := r.db.
		Where("issue_id = ?", issueID).
		Order("sort_order ASC, created_at ASC, id ASC").
		Find(&items).Error
	return items, err
}

func (r *IssueChecklistRepository) Transaction(fc func(tx *gorm.DB) error) error {
	return r.db.Transaction(fc)
}

func (r *IssueChecklistRepository) ReplaceForIssueTx(tx *gorm.DB, issueID string, items []model.IssueChecklistItem) error {
	if err := tx.Where("issue_id = ?", issueID).Delete(&model.IssueChecklistItem{}).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	return tx.Create(&items).Error
}

type GitHubRepoLabelRepository struct {
	db *gorm.DB
}

func NewGitHubRepoLabelRepository(db *gorm.DB) *GitHubRepoLabelRepository {
	return &GitHubRepoLabelRepository{db: db}
}

func (r *GitHubRepoLabelRepository) DeleteByProject(projectID string) error {
	return r.db.Where("project_id = ?", projectID).Delete(&model.GitHubRepoLabel{}).Error
}

func (r *GitHubRepoLabelRepository) Save(label *model.GitHubRepoLabel) error {
	return r.db.Save(label).Error
}

func (r *GitHubRepoLabelRepository) Find(projectID, name string) (*model.GitHubRepoLabel, error) {
	var label model.GitHubRepoLabel
	if err := r.db.Where("project_id = ? AND name = ?", projectID, name).First(&label).Error; err != nil {
		return nil, err
	}
	return &label, nil
}

func (r *GitHubRepoLabelRepository) ListByProject(projectID string) ([]model.GitHubRepoLabel, error) {
	var labels []model.GitHubRepoLabel
	err := r.db.Where("project_id = ?", projectID).Order("name ASC").Find(&labels).Error
	return labels, err
}

func (r *GitHubRepoLabelRepository) ReplaceAllForProject(projectID string, labels []model.GitHubRepoLabel) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project_id = ?", projectID).Delete(&model.GitHubRepoLabel{}).Error; err != nil {
			return err
		}
		if len(labels) == 0 {
			return nil
		}
		return tx.Create(&labels).Error
	})
}
