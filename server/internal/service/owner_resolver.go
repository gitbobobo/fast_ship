package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"gorm.io/gorm"
)

const ownerNegCacheTTL = time.Minute

// SelfLogOwnerResolver 按关联列从业务库解析空归属日志的所有者。
// 只处理 user_id 为空、带关联列、未标系统的行；显式 user_id 永不覆盖。
// 同一行多键解析到不同用户、超时、出错、查无 → 该行保持无主（API 不可见）。
// 实现内不打 zap/slog，避免解析失败再入队形成循环。
type SelfLogOwnerResolver struct {
	db  *gorm.DB
	ttl time.Duration

	mu  sync.Mutex
	neg map[string]time.Time // kind+":"+id → 到期时间
}

func NewSelfLogOwnerResolver(db *gorm.DB) *SelfLogOwnerResolver {
	return &SelfLogOwnerResolver{
		db:  db,
		ttl: ownerNegCacheTTL,
		neg: make(map[string]time.Time),
	}
}

func (r *SelfLogOwnerResolver) ResolveOwners(ctx context.Context, rows []selflog.Entry) []string {
	out := make([]string, len(rows))
	if r == nil || r.db == nil || len(rows) == 0 {
		return out
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return out
	}

	var projectIDs, issueIDs, runIDs, threadIDs []string
	eligible := make([]bool, len(rows))
	for i, e := range rows {
		if e.UserID != "" || e.SystemScope || !e.HasAssociation() {
			continue
		}
		eligible[i] = true
		projectIDs = appendID(projectIDs, e.ProjectID)
		issueIDs = appendID(issueIDs, e.IssueID)
		runIDs = appendID(runIDs, e.RunID)
		threadIDs = appendID(threadIDs, e.ThreadID)
	}

	issueProjects := r.lookupIssueProjects(ctx, uniqueIDs(issueIDs))
	for _, pid := range issueProjects {
		projectIDs = appendID(projectIDs, pid)
	}

	projects := r.lookupUsers(ctx, "project", uniqueIDs(projectIDs),
		&model.Project{}, "id")
	// run_id/thread_id 归属查审查运行账（INT-135：原 automation_runs 已删，
	// 审查运行是唯一还产生运行级日志的来源）。
	runs := r.lookupUsers(ctx, "run", uniqueIDs(runIDs),
		&model.ReviewRun{}, "id")
	threads := r.lookupThreadUsers(ctx, uniqueIDs(threadIDs))

	for i, e := range rows {
		if !eligible[i] {
			continue
		}
		owners := map[string]struct{}{}
		unresolved := false
		add := func(id string, byID map[string]string) {
			if id == "" {
				return
			}
			uid, ok := byID[id]
			if !ok || uid == "" {
				unresolved = true
				return
			}
			owners[uid] = struct{}{}
		}
		add(e.RunID, runs)
		add(e.ProjectID, projects)
		add(e.ThreadID, threads)
		if e.IssueID != "" {
			pid, ok := issueProjects[e.IssueID]
			if !ok || pid == "" {
				unresolved = true
			} else {
				add(pid, projects)
			}
		}
		if unresolved || len(owners) != 1 {
			continue
		}
		for uid := range owners {
			out[i] = uid
		}
	}
	return out
}

type idUserRow struct {
	ID     string
	UserID string
}

func (r *SelfLogOwnerResolver) lookupUsers(ctx context.Context, kind string, ids []string, modelPtr interface{}, idCol string) map[string]string {
	found := make(map[string]string)
	need := r.uncached(kind, ids)
	if len(need) == 0 || ctx.Err() != nil {
		return found
	}
	var rows []idUserRow
	err := r.db.WithContext(ctx).Model(modelPtr).
		Select(idCol+" AS id, user_id").
		Where(idCol+" IN ?", need).
		Find(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.rememberMisses(kind, need, found)
		}
		return found
	}
	for _, row := range rows {
		if row.ID != "" && row.UserID != "" {
			found[row.ID] = row.UserID
		}
	}
	r.rememberMisses(kind, need, found)
	return found
}

func (r *SelfLogOwnerResolver) lookupIssueProjects(ctx context.Context, ids []string) map[string]string {
	found := make(map[string]string)
	need := r.uncached("issue", ids)
	if len(need) == 0 || ctx.Err() != nil {
		return found
	}
	var rows []struct {
		ID        string
		ProjectID string
	}
	err := r.db.WithContext(ctx).Model(&model.Issue{}).
		Select("id, project_id").
		Where("id IN ?", need).
		Find(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.rememberMisses("issue", need, found)
		}
		return found
	}
	for _, row := range rows {
		if row.ID != "" && row.ProjectID != "" {
			found[row.ID] = row.ProjectID
		}
	}
	r.rememberMisses("issue", need, found)
	return found
}

func (r *SelfLogOwnerResolver) lookupThreadUsers(ctx context.Context, ids []string) map[string]string {
	found := make(map[string]string)
	need := r.uncached("thread", ids)
	if len(need) == 0 || ctx.Err() != nil {
		return found
	}
	var rows []struct {
		ThreadID string
		UserID   string
	}
	err := r.db.WithContext(ctx).Model(&model.ReviewRun{}).
		Select("thread_id, user_id").
		Where("thread_id IN ?", need).
		Find(&rows).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			r.rememberMisses("thread", need, found)
		}
		return found
	}
	conflict := map[string]struct{}{}
	for _, row := range rows {
		if row.ThreadID == "" || row.UserID == "" {
			continue
		}
		if prev, ok := found[row.ThreadID]; ok && prev != row.UserID {
			delete(found, row.ThreadID)
			conflict[row.ThreadID] = struct{}{}
			continue
		}
		if _, bad := conflict[row.ThreadID]; bad {
			continue
		}
		found[row.ThreadID] = row.UserID
	}
	r.rememberMisses("thread", need, found)
	return found
}

func (r *SelfLogOwnerResolver) uncached(kind string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	var need []string
	for _, id := range ids {
		if id == "" {
			continue
		}
		if exp, ok := r.neg[kind+":"+id]; ok && now.Before(exp) {
			continue
		}
		need = append(need, id)
	}
	return need
}

func (r *SelfLogOwnerResolver) rememberMisses(kind string, queried []string, found map[string]string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	ttl := r.ttl
	if ttl <= 0 {
		ttl = ownerNegCacheTTL
	}
	exp := now.Add(ttl)
	for _, id := range queried {
		if _, ok := found[id]; ok {
			continue
		}
		r.neg[kind+":"+id] = exp
	}
}

func (r *SelfLogOwnerResolver) pruneLocked(now time.Time) {
	for k, exp := range r.neg {
		if !now.Before(exp) {
			delete(r.neg, k)
		}
	}
}

func appendID(dst []string, id string) []string {
	if id == "" {
		return dst
	}
	return append(dst, id)
}

func uniqueIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
