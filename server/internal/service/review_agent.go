package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"gorm.io/gorm"
)

// 运行令牌与 Agent 端点（INT-135）：令牌只存 SHA-256；requireLive 端点在
// 运行终态后拒绝；result 端点允许结束后的「完全相同」重放。

// ResolveRunToken 解析 Bearer 运行令牌。
// requireLive=true：令牌未过期、未消费且运行在途（dispatched/running）才通过；
// requireLive=false（result 端点）：终态运行与已消费令牌也放行——是否重放
// 由 SubmitResult 比对 result_json 决定。
// 首次成功鉴权把 dispatched → running（条件 UPDATE）。
func (s *ReviewService) ResolveRunToken(ctx context.Context, raw string, requireLive bool) (*RunTokenIdentity, error) {
	if !strings.HasPrefix(raw, "fsr_") {
		return nil, errs.ErrRunTokenInvalid
	}
	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])
	now := time.Now().UTC()

	var token model.ReviewRunToken
	if err := s.db.WithContext(ctx).Where("token_hash = ?", hash).First(&token).Error; err != nil {
		return nil, errs.ErrRunTokenInvalid
	}
	if token.ExpiresAt.Before(now) {
		return nil, errs.ErrRunTokenInvalid
	}
	var run model.ReviewRun
	if err := s.db.WithContext(ctx).Where("id = ?", token.RunID).First(&run).Error; err != nil {
		return nil, errs.ErrRunTokenInvalid
	}
	if requireLive {
		// 在途端点：消费过或非在途一律拒绝；queued（尚未派发）同样无活令牌。
		if token.ConsumedAt != nil || !run.Status.Active() {
			return nil, errs.ErrRunTokenInvalid
		}
	} else if token.ConsumedAt != nil && !run.Status.Terminal() {
		return nil, errs.ErrRunTokenInvalid
	}
	// 首次成功鉴权：dispatched → running（条件更新，失败即并发状态已变）。
	s.db.WithContext(ctx).Model(&model.ReviewRun{}).
		Where("id = ? AND status = ?", run.ID, model.ReviewRunDispatched).
		Updates(map[string]any{"status": model.ReviewRunRunning, "started_at": now, "updated_at": now})
	return &RunTokenIdentity{UserID: run.UserID, RunID: run.ID}, nil
}

// loadAgentRun 端点共用的运行加载（令牌身份 → 运行行）。
func (s *ReviewService) loadAgentRun(ctx context.Context, runID string) (*model.ReviewRun, error) {
	var run model.ReviewRun
	if err := s.db.WithContext(ctx).Where("id = ?", runID).First(&run).Error; err != nil {
		return nil, errs.ErrRunTokenInvalid
	}
	return &run, nil
}

func (s *ReviewService) ghClient(ctx context.Context) (githubapi.GitHubClient, error) {
	if s.gh == nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "GitHub 访问面未配置")
	}
	return s.gh.ClientFor(ctx)
}

// AgentPullRequest GET /api/review-agent/pull-request：运行绑定身份 + 实时
// PR 元数据（body 要拿最新——视觉审查的截图说明来自这里）。
func (s *ReviewService) AgentPullRequest(ctx context.Context, runID string) (*api.ReviewAgentPullRequest, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	client, err := s.ghClient(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := client.GetPullRequest(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 PR 失败: "+err.Error())
	}
	return &api.ReviewAgentPullRequest{
		BaseRef:      pr.BaseRef,
		Body:         pr.Body,
		HeadRef:      pr.HeadRef,
		HeadSha:      run.HeadSHA, // 绑定 SHA 是审查对象（不是实时 head）
		HtmlUrl:      pr.HTMLURL,
		PrNumber:     pr.Number,
		RepoFullName: run.RepoFullName,
		Title:        pr.Title,
	}, nil
}

// reviewDiffMaxBytes diff 端点截断阈值。
const reviewDiffMaxBytes = 1 << 20

// AgentDiff GET /api/review-agent/pull-request/diff：>1MB 按字节截断。
func (s *ReviewService) AgentDiff(ctx context.Context, runID string) (*api.ReviewAgentDiff, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	client, err := s.ghClient(ctx)
	if err != nil {
		return nil, err
	}
	diff, err := client.GetPullRequestDiff(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 diff 失败: "+err.Error())
	}
	truncated := false
	if len(diff) > reviewDiffMaxBytes {
		diff = diff[:reviewDiffMaxBytes]
		truncated = true
	}
	return &api.ReviewAgentDiff{Content: diff, Truncated: truncated}, nil
}

// AgentHistory GET /api/review-agent/history：按运行类型拼历史上下文 Markdown。
func (s *ReviewService) AgentHistory(ctx context.Context, runID string) (string, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.Kind == model.ReviewKindVisual {
		return s.visualHistory(ctx, run)
	}
	return s.codeHistory(ctx, run)
}

// codeHistory 代码审查历史：上一轮各组合 findings + 上轮之后的新讨论；
// 首次审查（无上一轮）给全部讨论（≤50 条）。
func (s *ReviewService) codeHistory(ctx context.Context, run *model.ReviewRun) (string, error) {
	prevRuns, err := s.runsAtRound(ctx, run, run.RoundNumber-1, model.ReviewKindCode)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if len(prevRuns) > 0 {
		var lastFinished time.Time
		for _, r := range prevRuns {
			if r.FinishedAt != nil && r.FinishedAt.After(lastFinished) {
				lastFinished = *r.FinishedAt
			}
		}
		fmt.Fprintf(&b, "## 上一轮（轮次 %d，head %s）\n", run.RoundNumber-1, reviewShortSHA(prevRuns[0].HeadSHA))
		for _, r := range prevRuns {
			fmt.Fprintf(&b, "### 组合 %s\n", runCombinationLabel(&r))
			if r.Status == model.ReviewRunCompleted && r.ResultJSON != "" {
				var result api.ReviewCodeResult
				if json.Unmarshal([]byte(r.ResultJSON), &result) == nil {
					for _, f := range result.Findings {
						loc := f.File
						if f.Line != nil && *f.Line > 0 {
							loc = fmt.Sprintf("%s:%d", f.File, *f.Line)
						}
						fmt.Fprintf(&b, "- %s %s %s\n", f.Level, loc, f.Body)
					}
				}
			} else {
				fmt.Fprintf(&b, "- （本组合未完成：%s）\n", r.Status)
			}
		}
		b.WriteString("\n## 上一轮结论之后的新讨论\n")
		if err := s.appendNewDiscussion(ctx, run, &b, lastFinished, prevRuns == nil); err != nil {
			return "", err
		}
		return strings.TrimRight(b.String(), "\n"), nil
	}

	// 首次审查：全部讨论（≤50 条）。
	b.WriteString("## 全部讨论\n")
	if err := s.appendNewDiscussion(ctx, run, &b, time.Time{}, true); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// appendNewDiscussion 把 after 之后的行内评论与会话评论按时间正序写进 b。
// 本系统自己发的结论（机器人标记/自身 review）过滤掉。limit 最近 50 条。
func (s *ReviewService) appendNewDiscussion(ctx context.Context, run *model.ReviewRun, b *strings.Builder, after time.Time, all bool) error {
	client, err := s.ghClient(ctx)
	if err != nil {
		return err
	}
	reviewComments, err := client.ListReviewComments(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取行内评论失败: "+err.Error())
	}
	issueComments, err := client.ListIssueCommentsForPR(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取会话评论失败: "+err.Error())
	}
	type entry struct {
		at   time.Time
		head string
		body string
	}
	var entries []entry
	for _, c := range reviewComments {
		if strings.HasSuffix(c.User, "[bot]") || (!c.CreatedAt.After(after) && !all) {
			continue
		}
		head := "行内评论"
		if c.Path != "" {
			head += " · " + c.Path
		}
		entries = append(entries, entry{at: c.CreatedAt, head: head + " · @" + c.User, body: c.Body})
	}
	for _, c := range issueComments {
		// 系统自己的顶层结论评论（隐藏标记）与机器人发言不进历史。
		if strings.HasSuffix(c.User, "[bot]") || strings.Contains(c.Body, "<!-- fast-ship-") ||
			(!c.CreatedAt.After(after) && !all) {
			continue
		}
		entries = append(entries, entry{at: c.CreatedAt, head: "会话评论 · @" + c.User, body: c.Body})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	if len(entries) > 50 {
		entries = entries[len(entries)-50:]
	}
	for _, e := range entries {
		fmt.Fprintf(b, "### %s\n%s\n", e.head, e.body)
	}
	return nil
}

// visualHistory 视觉审查历史：上一轮视觉运行的 findings + unmeasurable。
func (s *ReviewService) visualHistory(ctx context.Context, run *model.ReviewRun) (string, error) {
	// 最近一轮「确有视觉运行」的轮次：中间轮可能只跑了代码审查，
	// round-1 会拿到空历史。
	var prevRound *int
	if err := s.db.WithContext(ctx).Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND pr_number = ? AND kind = ? AND round_number < ?",
			run.RepoFullName, run.PRNumber, model.ReviewKindVisual, run.RoundNumber).
		Select("MAX(round_number)").Scan(&prevRound).Error; err != nil {
		return "", errs.ErrInternal
	}
	if prevRound == nil {
		return "", nil
	}
	prevRuns, err := s.runsAtRound(ctx, run, *prevRound, model.ReviewKindVisual)
	if err != nil {
		return "", err
	}
	if len(prevRuns) == 0 {
		return "", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## 上一轮视觉审查（轮次 %d）\n", *prevRound)
	var unmeasurable []api.ReviewUnmeasurable
	for _, r := range prevRuns {
		if r.Status != model.ReviewRunCompleted || r.ResultJSON == "" {
			continue
		}
		var result api.ReviewVisualResult
		if json.Unmarshal([]byte(r.ResultJSON), &result) != nil {
			continue
		}
		for _, f := range result.Findings {
			fmt.Fprintf(&b, "- %s · %s · %s：%s\n  %s\n", f.Severity, f.Screenshot, f.Dimension, f.Body, f.Fix)
		}
		unmeasurable = append(unmeasurable, result.Unmeasurable...)
	}
	if len(unmeasurable) > 0 {
		b.WriteString("### 无法判断\n")
		for _, u := range unmeasurable {
			fmt.Fprintf(&b, "- %s：%s\n", u.Screenshot, u.Reason)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// runsAtRound 取同 PR 指定轮次、指定 kind 的运行（按创建序）。
func (s *ReviewService) runsAtRound(ctx context.Context, run *model.ReviewRun, round int, kind model.ReviewKind) ([]model.ReviewRun, error) {
	if round < 1 {
		return nil, nil
	}
	var runs []model.ReviewRun
	if err := s.db.WithContext(ctx).
		Where("repo_full_name = ? AND pr_number = ? AND round_number = ? AND kind = ?",
			run.RepoFullName, run.PRNumber, round, kind).
		Order("created_at ASC").Find(&runs).Error; err != nil {
		return nil, errs.ErrInternal
	}
	return runs, nil
}

func reviewShortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// reviewLabelMaxRunes 组合显示名上限（字符）：model/options/instance 取自
// 项目配置，长度不可控——标签会进 commit status 描述与评论分节标题，超
// 长输入截断防止灌爆。
const reviewLabelMaxRunes = 60

// runCombinationLabel 组合显示名：model[opt=val,...] / instance（沿用历史
// 上下文与评论分节的同一标签），截断到 reviewLabelMaxRunes。
func runCombinationLabel(run *model.ReviewRun) string {
	label := run.Model
	if run.ModelOptionsJSON != "" && run.ModelOptionsJSON != "null" {
		var opts []api.ReviewModelOption
		if json.Unmarshal([]byte(run.ModelOptionsJSON), &opts) == nil && len(opts) > 0 {
			parts := make([]string, 0, len(opts))
			for _, o := range opts {
				raw, _ := json.Marshal(o.Value)
				parts = append(parts, fmt.Sprintf("%s=%s", o.Id, raw))
			}
			label += "[" + strings.Join(parts, ",") + "]"
		}
	}
	return truncateRunes(label+" / "+run.InstanceID, reviewLabelMaxRunes)
}

// AgentSubmitResult POST /api/review-agent/result：结构化结论的校验与落地。
// 终态运行只允许完全相同 body 的重放（result_json 比对）；HEAD 已前进 →
// 40920 且运行作废。
func (s *ReviewService) AgentSubmitResult(ctx context.Context, runID string, body []byte) error {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return err
	}
	// 必填键存在性先于结构解码：缺键或显式 null 时，Unmarshal 出的零值
	// 会让「没填」与「填了零值」无法区分。
	required, ok := reviewResultRequiredKeys[run.Kind]
	if !ok {
		return errs.ErrRunTokenInvalid
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return errs.New(errs.ErrReviewResultInvalid.Code, "请求体不是合法的审查结论: "+err.Error())
	}
	for _, k := range required {
		if v, ok := raw[k]; !ok || string(v) == "null" {
			return reviewResultErr("缺少必填字段 %s", k)
		}
	}
	// 严格校验：按 kind 反序列化 + 字段校验（不允许未知字段）。
	var canonical []byte
	summary := ""
	switch run.Kind {
	case model.ReviewKindCode:
		var result api.ReviewCodeResult
		if err := decodeStrict(body, &result); err != nil {
			return err
		}
		if err := validateCodeResult(&result); err != nil {
			return err
		}
		canonical, _ = json.Marshal(&result)
		summary = result.Summary
	case model.ReviewKindVisual:
		var result api.ReviewVisualResult
		if err := decodeStrict(body, &result); err != nil {
			return err
		}
		if err := validateVisualResult(&result); err != nil {
			return err
		}
		canonical, _ = json.Marshal(&result)
		summary = result.Summary
	default:
		return errs.ErrRunTokenInvalid
	}

	// 终态重放判定：completed 且正文相同 → 幂等成功；其余一律 40106。
	if run.Status.Terminal() {
		if run.Status == model.ReviewRunCompleted && run.ResultJSON != "" && run.ResultJSON == string(canonical) {
			return nil
		}
		return errs.ErrRunTokenInvalid
	}
	if !run.Status.Active() {
		return errs.ErrRunTokenInvalid
	}

	// 提交前核对 PR 实时 head：已前进 → 本轮作废（取消 + 消费令牌 + sync）。
	client, err := s.ghClient(ctx)
	if err != nil {
		return err
	}
	pr, err := client.GetPullRequest(ctx, run.InstallationID, run.RepoFullName, run.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取 PR head 失败: "+err.Error())
	}
	now := time.Now().UTC()
	if pr.HeadSHA != "" && !strings.EqualFold(pr.HeadSHA, run.HeadSHA) {
		_ = s.db.Transaction(func(tx *gorm.DB) error {
			tx.Model(&model.ReviewRun{}).
				Where("id = ? AND status IN ?", run.ID,
					[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}).
				Updates(map[string]any{
					"status": model.ReviewRunCancelled, "error_code": "superseded",
					"finished_at": now, "updated_at": now,
				})
			tx.Model(&model.ReviewRunToken{}).
				Where("run_id = ? AND consumed_at IS NULL", run.ID).
				Update("consumed_at", now)
			return s.markPRSyncPendingByRepoTx(tx, run.RepoFullName, run.PRNumber, now)
		})
		return errs.ErrReviewHeadChanged
	}

	// submitErr 承载「超时作废」的返回错误：超时收尾（timed_out + 消费令牌 +
	// sync）必须先随事务提交，错误在事务外返回——事务内返回 error 会回滚。
	var submitErr error
	notifyTimeout := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 完成写入带截止时间护栏：deadline 已过的运行不记 completed。
		res := tx.Model(&model.ReviewRun{}).
			Where("id = ? AND status IN ? AND deadline_at > ?", run.ID,
				[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}, now).
			Updates(map[string]any{
				"status":      model.ReviewRunCompleted,
				"result_json": string(canonical),
				"summary":     summary,
				"finished_at": now,
				"posted_at":   nil,
				"updated_at":  now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 仍处活动态但 deadline 已过 → 按超时口径收尾（timed_out + 消费
			// 令牌 + sync）；其余（已终态等）维持 40106。
			var cur model.ReviewRun
			if err := tx.Where("id = ?", run.ID).First(&cur).Error; err != nil {
				return err
			}
			if cur.Status.Active() && !cur.DeadlineAt.After(now) {
				cas := tx.Model(&model.ReviewRun{}).
					Where("id = ? AND status IN ?", run.ID,
						[]model.ReviewRunStatus{model.ReviewRunDispatched, model.ReviewRunRunning}).
					Updates(map[string]any{
						"status": model.ReviewRunTimedOut, "error_code": "timeout",
						"finished_at": now, "updated_at": now,
					})
				if cas.Error != nil {
					return cas.Error
				}
				if cas.RowsAffected > 0 {
					if err := tx.Model(&model.ReviewRunToken{}).
						Where("run_id = ? AND consumed_at IS NULL", run.ID).
						Update("consumed_at", now).Error; err != nil {
						return err
					}
					if err := s.markPRSyncPendingByRepoTx(tx, run.RepoFullName, run.PRNumber, now); err != nil {
						return err
					}
					notifyTimeout = true
				}
				submitErr = errs.New(errs.ErrReviewHeadChanged.Code, "结果提交时已超过运行截止时间，按超时作废")
				return nil
			}
			return errs.ErrRunTokenInvalid
		}
		if err := tx.Model(&model.ReviewRunToken{}).
			Where("run_id = ? AND consumed_at IS NULL", run.ID).
			Update("consumed_at", now).Error; err != nil {
			return err
		}
		return s.markPRSyncPendingByRepoTx(tx, run.RepoFullName, run.PRNumber, now)
	})
	if err != nil {
		return err
	}
	if submitErr != nil {
		if notifyTimeout {
			s.publishReviewNotification(run, model.NotificationReviewRunTimeout, "结果提交时已超过截止时间，运行按超时结束", now)
		}
		return submitErr
	}
	s.notifyReviewLoop()
	return nil
}

// decodeStrict 严格反序列化：未知字段与类型错误一律 40004。
func decodeStrict(body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errs.New(errs.ErrReviewResultInvalid.Code, "请求体不是合法的审查结论: "+err.Error())
	}
	return nil
}

func reviewResultErr(format string, args ...any) error {
	return errs.New(errs.ErrReviewResultInvalid.Code, fmt.Sprintf(format, args...))
}

// validateCodeResult 代码结论字段校验（enum/必填/行号下限/rejected 必带 reason）。
func validateCodeResult(r *api.ReviewCodeResult) error {
	if strings.TrimSpace(r.Summary) == "" {
		return reviewResultErr("summary 不能为空")
	}
	for i, f := range r.Findings {
		switch f.Level {
		case api.P0, api.P1, api.P2, api.Note:
		default:
			return reviewResultErr("findings[%d].level 非法：%q", i, f.Level)
		}
		if strings.TrimSpace(f.File) == "" {
			return reviewResultErr("findings[%d].file 不能为空", i)
		}
		if f.Line != nil && *f.Line < 1 {
			return reviewResultErr("findings[%d].line 必须 >= 1", i)
		}
		if strings.TrimSpace(f.Body) == "" {
			return reviewResultErr("findings[%d].body 不能为空", i)
		}
	}
	return validatePrior(r.Prior)
}

// validateVisualResult 视觉结论字段校验。
func validateVisualResult(r *api.ReviewVisualResult) error {
	if strings.TrimSpace(r.Summary) == "" {
		return reviewResultErr("summary 不能为空")
	}
	for i, f := range r.Findings {
		switch f.Severity {
		case api.Blocker, api.WorthFixing, api.Minor:
		default:
			return reviewResultErr("findings[%d].severity 非法：%q", i, f.Severity)
		}
		switch f.Dimension {
		case api.Hierarchy, api.Spacing, api.Typography, api.Color, api.Interaction, api.Copy, api.Accessibility:
		default:
			return reviewResultErr("findings[%d].dimension 非法：%q", i, f.Dimension)
		}
		if strings.TrimSpace(f.Screenshot) == "" {
			return reviewResultErr("findings[%d].screenshot 不能为空", i)
		}
		if strings.TrimSpace(f.Body) == "" || strings.TrimSpace(f.Fix) == "" {
			return reviewResultErr("findings[%d].body/fix 不能为空", i)
		}
	}
	for i, u := range r.Unmeasurable {
		if strings.TrimSpace(u.Screenshot) == "" || strings.TrimSpace(u.Reason) == "" {
			return reviewResultErr("unmeasurable[%d] 需要 screenshot 与 reason", i)
		}
	}
	return validatePrior(r.Prior)
}

// reviewResultRequiredKeys 结果端点按 kind 的必填顶层键：缺键或显式 null
// 都拒收——仅 Unmarshal 到 struct 会让零值悄悄通过。
var reviewResultRequiredKeys = map[model.ReviewKind][]string{
	model.ReviewKindCode:   {"summary", "findings", "prior", "ui_changed", "ui_changed_since_last_visual"},
	model.ReviewKindVisual: {"summary", "findings", "prior", "unmeasurable"},
}

// validatePrior prior 数组共用校验：body/status 必填 + rejected 必带 reason。
func validatePrior(prior []api.ReviewPriorDecision) error {
	for i, p := range prior {
		if strings.TrimSpace(p.Body) == "" {
			return reviewResultErr("prior[%d].body 不能为空", i)
		}
		switch p.Status {
		case api.ReviewPriorDecisionStatusFixed, api.ReviewPriorDecisionStatusOpen, api.ReviewPriorDecisionStatusRejected:
		default:
			return reviewResultErr("prior[%d].status 非法：%q", i, p.Status)
		}
		if p.Status == api.ReviewPriorDecisionStatusRejected && (p.Reason == nil || strings.TrimSpace(*p.Reason) == "") {
			return reviewResultErr("prior[%d] rejected 必须写 reason", i)
		}
	}
	return nil
}

// RerunReview POST /api/projects/:id/reviews/rerun：用 App 令牌取实时 head，
// 旧 SHA 未终态运行全部取消，恒开新轮（manual）。
func (s *ReviewService) RerunReview(ctx context.Context, userID, projectID string, prNumber int) (*RerunReviewResult, error) {
	project, err := s.ownedProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	if prNumber < 1 {
		return nil, errs.ErrInvalidParams
	}
	var settings model.ProjectReviewSettings
	if err := s.db.WithContext(ctx).Where("project_id = ? AND enabled", projectID).First(&settings).Error; err != nil {
		return nil, errs.ErrReviewNotEnabled
	}
	if project.GithubOwner == "" || project.GithubRepo == "" {
		return nil, errs.ErrProjectGitHubNotConfigured
	}
	client, err := s.ghClient(ctx)
	if err != nil {
		return nil, err
	}
	// 写库键与 webhook 侧同口径小写（GitHub API 本身大小写不敏感）。
	repo := strings.ToLower(project.GithubOwner + "/" + project.GithubRepo)
	// rerun 没有 webhook installation 快照：先解析真实安装 id，再取 PR；
	// 解析不支持/失败时不得拿 0 当安装 id 继续写库。
	installationID, err := client.ResolveInstallation(ctx, repo)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "解析仓库安装失败: "+err.Error())
	}
	pr, err := client.GetPullRequest(ctx, installationID, repo, prNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 PR head 失败: "+err.Error())
	}
	if pr.HeadSHA == "" {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "PR head 为空")
	}

	now := time.Now().UTC()
	var round int
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.supersedePRRunsTx(tx, repo, prNumber, now); err != nil {
			return err
		}
		if err := tx.Where("repo_full_name = ? AND pr_number = ?", repo, prNumber).
			Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		var err error
		round, err = s.openRoundTx(tx, project, &settings, repo, prNumber,
			pr.HeadSHA, "manual", pr.Title, pr.HTMLURL, pr.BaseRef, pr.HeadRef, installationID, now)
		return err
	})
	if err != nil {
		return nil, errs.ErrInternal
	}
	s.notifyReviewLoop()
	return &RerunReviewResult{Accepted: true, HeadSHA: pr.HeadSHA, RoundNumber: round}, nil
}

// RerunReviewResult 手动重跑受理结果。
type RerunReviewResult struct {
	Accepted    bool
	HeadSHA     string
	RoundNumber int
}
