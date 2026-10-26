package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 运行令牌与 Agent 端点（INT-135）：令牌只存 SHA-256；requireLive 端点在
// 运行终态后拒绝；result 端点允许结束后的「完全相同」重放。

// ResolveRunToken 解析 Bearer 运行令牌。
// requireLive=true：令牌未过期、未消费且运行在途（dispatched/running）才通过；
// requireLive=false（result 端点）：终态运行与已消费令牌也放行——是否重放
// 由 SubmitResult 比对 result_json 决定。
// expectedRunID 非空时按 INT-169 做强校验：URL 目标运行与令牌所属运行不
// 一致 → 40307，且在下面的状态推进之前返回——错配请求对双方运行零副作用。
// 首次合法鉴权把 dispatched → running（条件 UPDATE）。
func (s *ReviewService) ResolveRunToken(ctx context.Context, raw string, requireLive bool, expectedRunID string) (*RunTokenIdentity, error) {
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
	// 运行归属强校验（INT-169）：在 dispatched→running 推进之前拒绝错配。
	// 日志只记运行/线程身份与原因，不记令牌内容。
	if expectedRunID != "" && run.ID != expectedRunID {
		s.logger.Warn("审查 Agent 运行归属错配，拒绝请求",
			zap.String("target_run_id", expectedRunID),
			zap.String("token_run_id", run.ID),
			zap.String("thread_id", run.ThreadID),
			zap.String("reason", "run_id_mismatch"),
		)
		return nil, errs.ErrReviewRunMismatch
	}
	// 首次成功鉴权：dispatched → running（条件更新，失败即并发状态已变）。
	// 顺带补 dispatched_at：令牌只经启动提示词交给线程，Agent 能出示即证明
	// 线程已建成——即使启动 RPC 结果没确认。归档阶段以 dispatched_at 非空
	// 为前提，这里不补会漏归档。
	s.db.WithContext(ctx).Model(&model.ReviewRun{}).
		Where("id = ? AND status = ?", run.ID, model.ReviewRunDispatched).
		Updates(map[string]any{
			"status":        model.ReviewRunRunning,
			"started_at":    now,
			"dispatched_at": gorm.Expr("COALESCE(dispatched_at, ?)", now),
			"updated_at":    now,
		})
	return &RunTokenIdentity{UserID: run.UserID, RunID: run.ID, ThreadID: run.ThreadID}, nil
}

// loadAgentRun 端点共用的运行加载（令牌身份 → 运行行）。
func (s *ReviewService) loadAgentRun(ctx context.Context, runID string) (*model.ReviewRun, error) {
	var run model.ReviewRun
	if err := s.db.WithContext(ctx).Where("id = ?", runID).First(&run).Error; err != nil {
		return nil, errs.ErrRunTokenInvalid
	}
	return &run, nil
}

// AgentPullRequest GET /api/review-agent/runs/:runId/pull-request：运行绑定身份 + 实时
// PR 元数据（body 要拿最新——视觉审查的截图说明来自这里）。
func (s *ReviewService) AgentPullRequest(ctx context.Context, runID string) (*api.ReviewAgentPullRequest, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	client, err := s.reviewClient(ctx, run.IntegrationID, run.RepoFullName, run.InstallationID)
	if err != nil {
		return nil, err
	}
	pr, err := client.GetPullRequest(ctx, run.RepoFullName, run.PRNumber)
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

// AgentDiff GET /api/review-agent/runs/:runId/pull-request/diff：>1MB 按字节截断。
func (s *ReviewService) AgentDiff(ctx context.Context, runID string) (*api.ReviewAgentDiff, error) {
	run, err := s.loadAgentRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	client, err := s.reviewClient(ctx, run.IntegrationID, run.RepoFullName, run.InstallationID)
	if err != nil {
		return nil, err
	}
	diff, err := client.GetPullRequestDiff(ctx, run.RepoFullName, run.PRNumber)
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

// AgentHistory GET /api/review-agent/runs/:runId/history：按运行类型拼历史上下文 Markdown。
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
			// 历史上下文是纯文本：选项拼在标题行，不用 <sub>（那是渲染层的小字）。
			label := runCombinationLabel(&r)
			if opts := runCombinationOptions(&r); opts != "" {
				label += " · " + opts
			}
			fmt.Fprintf(&b, "### 组合 %s\n", label)
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
	client, err := s.reviewClient(ctx, run.IntegrationID, run.RepoFullName, run.InstallationID)
	if err != nil {
		return err
	}
	reviewComments, err := client.ListReviewComments(ctx, run.RepoFullName, run.PRNumber)
	if err != nil {
		return errs.New(errs.ErrGitHubAPI.Code, "读取行内评论失败: "+err.Error())
	}
	issueComments, err := client.ListIssueCommentsForPR(ctx, run.RepoFullName, run.PRNumber)
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
		// c.Self 覆盖 Forgejo：那边的机器人是普通账号，不以 [bot] 结尾，
		// 行内评论又没有隐藏标记可拦。
		if c.Self || strings.HasSuffix(c.User, "[bot]") || (!c.CreatedAt.After(after) && !all) {
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
		if c.Self || strings.HasSuffix(c.User, "[bot]") || strings.Contains(c.Body, "<!-- fast-ship-") ||
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

// visualHistory 视觉审查历史：上一轮视觉运行的 findings + unmeasurable，再
// 补上该轮 result 里 prior 仍未解决的条目——模型可能只把遗留写进 prior(open)
// 而没重写进 findings，不补这段的话未解决项会从下一轮历史里消失，复核断链。
func (s *ReviewService) visualHistory(ctx context.Context, run *model.ReviewRun) (string, error) {
	// 最近一轮「确有视觉运行」的轮次：中间轮可能只跑了代码审查，
	// round-1 会拿到空历史。历史只在本 (项目, 集成) 账本内取。
	var prevRound *int
	if err := s.db.WithContext(ctx).Model(&model.ReviewRun{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND kind = ? AND round_number < ?",
			run.ProjectID, run.RepoFullName, run.PRNumber, run.IntegrationID, model.ReviewKindVisual, run.RoundNumber).
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
	var openPrior []api.ReviewPriorDecision
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
		openPrior = append(openPrior, openPriorDecisions(result.Prior)...)
	}
	if len(unmeasurable) > 0 {
		b.WriteString("### 无法判断\n")
		for _, u := range unmeasurable {
			fmt.Fprintf(&b, "- %s：%s\n", u.Screenshot, u.Reason)
		}
	}
	if len(openPrior) > 0 {
		// 独立小节：这些条目「更早一轮」就提出了，上一轮也没能了结——比
		// 上一轮 findings 更值得审查员优先复核。
		b.WriteString("### 仍未解决（更早一轮）\n")
		for _, p := range openPrior {
			fmt.Fprintf(&b, "- %s：%s\n", priorLevelText(p.Level), p.Body)
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// runsAtRound 取本 (项目, 集成) 同 PR 指定轮次、指定 kind 的运行（按创建序）。
func (s *ReviewService) runsAtRound(ctx context.Context, run *model.ReviewRun, round int, kind model.ReviewKind) ([]model.ReviewRun, error) {
	if round < 1 {
		return nil, nil
	}
	var runs []model.ReviewRun
	if err := s.db.WithContext(ctx).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND round_number = ? AND kind = ?",
			run.ProjectID, run.RepoFullName, run.PRNumber, run.IntegrationID, round, kind).
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

// reviewOptionsMaxRunes 组合选项串上限（字符）：选项取自项目配置，长度不可控
// ——只截选项串自身；实例名是分节标题里最有用的部分，不参与截断。
const reviewOptionsMaxRunes = 80

// singleLine 把可能来自配置/上游的显示文本压成一行：换行会拆开标题、表格和
// 评论结构，统一换成空格后由各消费点再做自己的转义（表格转义竖线等）。
func singleLine(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}

// runCombinationLabel 组合显示名：模型 · 实例。不拼选项也不截断——选项另有
// runCombinationOptions 单独展示（评论 <sub> 小字、历史标题纯文本），避免
// 整体截断后把信息量最低的模型名前缀留下、最有用的实例名被截掉。模型/实例
// 来自项目配置，可能含换行，压成单行后再拼。
func runCombinationLabel(run *model.ReviewRun) string {
	return singleLine(run.Model) + " · " + singleLine(run.InstanceID)
}

// runCombinationOptions 组合选项串：k=v · k=v。值取 JSON 解码后的原生形态：
// 字符串原样（不加引号）、null 输出 "null"（%v 会输出 <nil>）、其余类型走
// json.Marshal（bool/number 稳定可读），整体截断到 reviewOptionsMaxRunes；
// 无选项或选项 JSON 损坏返回空串。HTML 转义在渲染层做（见 buildCodeComment）。
func runCombinationOptions(run *model.ReviewRun) string {
	if run.ModelOptionsJSON == "" || run.ModelOptionsJSON == "null" {
		return ""
	}
	var opts []api.ReviewModelOption
	if json.Unmarshal([]byte(run.ModelOptionsJSON), &opts) != nil || len(opts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(opts))
	for _, o := range opts {
		switch v := o.Value.(type) {
		case nil:
			parts = append(parts, o.Id+"=null")
		case string:
			parts = append(parts, o.Id+"="+v)
		default:
			encoded, err := json.Marshal(v)
			if err != nil {
				encoded = []byte(fmt.Sprintf("%v", v))
			}
			parts = append(parts, o.Id+"="+string(encoded))
		}
	}
	return truncateRunes(strings.Join(parts, " · "), reviewOptionsMaxRunes)
}

// AgentSubmitResult POST /api/review-agent/runs/:runId/result：结构化结论的校验与落地。
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
	// codeFindings 仅代码 kind 有值：供事务前的文件归属校验（INT-169）。
	var codeFindings []api.ReviewCodeFinding
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
		codeFindings = result.Findings
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

	// completedReplay 复核运行是否已被并发完全相同提交完成：本次加载时
	// 运行在途，校验/复核期间另一提交落库——与函数头终态重放判定同口径，
	// 命中按幂等成功返回，避免合法重放被 502/40004/40920 顶掉。
	completedReplay := func() bool {
		var cur model.ReviewRun
		return s.db.WithContext(ctx).Where("id = ?", run.ID).First(&cur).Error == nil &&
			cur.Status == model.ReviewRunCompleted && cur.ResultJSON != "" && cur.ResultJSON == string(canonical)
	}

	// 提交前核对 PR 实时状态：已关闭或 head 已前进 → 本轮作废（取消 + 消费令牌 + sync）。
	client, err := s.reviewClient(ctx, run.IntegrationID, run.RepoFullName, run.InstallationID)
	if err != nil {
		return err
	}
	pr, err := client.GetPullRequest(ctx, run.RepoFullName, run.PRNumber)
	if err != nil {
		if completedReplay() {
			return nil
		}
		return errs.New(errs.ErrGitHubAPI.Code, "读取 PR head 失败: "+err.Error())
	}
	now := time.Now().UTC()
	// 已关闭/合并或 head 已前进 → 本轮作废（取消 + 消费令牌 + sync）。
	// 关闭后 head 不再前进，仅比对 SHA 会漏掉「PR 已关」。supersede 封成
	// 闭包：文件归属校验后还要以同一口径复核一次（校验期间 head 可能前进）。
	supersede := func() error {
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
			return s.markPRSyncPendingByRepoTx(tx, run.IntegrationID, run.ProjectID, run.RepoFullName, run.PRNumber, now)
		})
		return errs.ErrReviewHeadChanged
	}
	if !prReviewable(pr) || (pr.HeadSHA != "" && !strings.EqualFold(pr.HeadSHA, run.HeadSHA)) {
		if completedReplay() {
			return nil
		}
		return supersede()
	}

	// 新发现文件归属校验（INT-169）：代码审查 findings 的文件必须存在于
	// 本轮 head_sha 的仓库树中；不存在的路径 40004 拒收并要求核对审查
	// 对象（不改运行状态，可修正后重交）。prior 不查——历史意见里已删除/
	// 重命名的路径合法。位置在终态重放判定之后（合法重放不受新检查影响）
	// 与事务之前（无任何状态副作用）。
	if err := s.checkFindingFilesInTree(ctx, client, run, codeFindings); err != nil {
		if completedReplay() {
			return nil
		}
		return err
	}
	// 文件归属校验是多次 GitHub 调用，可能耗时数秒——进入写入事务前重取
	// 时间并复核 PR head（校验期间 head 可能前进，缩小提交陈旧结果窗口）。
	now = time.Now().UTC()
	pr2, err := client.GetPullRequest(ctx, run.RepoFullName, run.PRNumber)
	if err != nil {
		if completedReplay() {
			return nil
		}
		return errs.New(errs.ErrGitHubAPI.Code, "复核 PR head 失败: "+err.Error())
	}
	if !prReviewable(pr2) || (pr2.HeadSHA != "" && !strings.EqualFold(pr2.HeadSHA, run.HeadSHA)) {
		if completedReplay() {
			return nil
		}
		return supersede()
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
			// 并发完全相同重放：另一提交已先完成且正文一致 → 按幂等成功返回，
			// 与函数头的终态重放判定口径一致（那处只覆盖请求到达时已终态）。
			if cur.Status == model.ReviewRunCompleted && cur.ResultJSON != "" && cur.ResultJSON == string(canonical) {
				return nil
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
					if err := s.markPRSyncPendingByRepoTx(tx, run.IntegrationID, run.ProjectID, run.RepoFullName, run.PRNumber, now); err != nil {
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
		return s.markPRSyncPendingByRepoTx(tx, run.IntegrationID, run.ProjectID, run.RepoFullName, run.PRNumber, now)
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

// checkFindingFilesInTree INT-169 文件归属：代码审查新发现的仓库相对路径
// 逐个在运行 head_sha 的树中查存在性（同一路径去重，去重后上限 64 条——
// 每个唯一路径一次平台调用，需要整体预算）。不存在 → 40004 要求核对
// 审查对象；平台调用失败 → 50200（调用方可重交）。树未命中的路径再用
// 本轮 PR diff 文件列表兜底一次：PR 删除/重命名走的文件仍是合法审查
// 对象（「不该删这个文件」指向的正是已删文件本身），只在 head 树与
// diff 里都查不到才算路径错误。diff 超本端读入上限被截断时路径集不
// 完整——缺席不再是有效证据，树未命中路径放行并 Warn 记录。
const findingFileCheckLimit = 64

func (s *ReviewService) checkFindingFilesInTree(ctx context.Context, client forge.ReviewClient, run *model.ReviewRun, findings []api.ReviewCodeFinding) error {
	seen := map[string]bool{}
	var diffSet map[string]bool
	diffTried, diffComplete := false, true
	for i, f := range findings {
		if seen[f.File] {
			continue
		}
		seen[f.File] = true
		if len(seen) > findingFileCheckLimit {
			return reviewResultErr("findings 涉及的不同文件路径超过 %d 个，超出归属校验预算", findingFileCheckLimit)
		}
		exists, err := client.PathExistsAtRef(ctx, run.RepoFullName, f.File, run.HeadSHA)
		if err != nil {
			if errors.Is(err, forge.ErrTreeTruncated) {
				// 树条目截断：「不在截断清单内」不等于不存在——与 diff
				// 兜底不可证同口径放行并 Warn，不按调用失败把合法提交
				// 卡成永久 50200（重试只拿到同一份截断树）。
				s.logger.Warn("审查文件归属树响应截断，路径存在性不可判定，放行",
					zap.String("run_id", run.ID),
					zap.String("repo", run.RepoFullName),
					zap.String("file", f.File),
				)
				continue
			}
			return errs.New(errs.ErrGitHubAPI.Code, "校验 findings 文件归属失败: "+err.Error())
		}
		if exists {
			continue
		}
		if !diffTried {
			diffTried = true
			diffSet, diffComplete, err = s.diffFilePaths(ctx, client, run)
			if err != nil {
				return errs.New(errs.ErrGitHubAPI.Code, "校验 findings 文件归属失败: "+err.Error())
			}
			if !diffComplete {
				s.logger.Warn("审查文件归属兜底证据不完整，树未命中路径按可采信放行",
					zap.String("run_id", run.ID),
					zap.String("repo", run.RepoFullName),
					zap.Int("pr", run.PRNumber),
				)
			}
		}
		if diffComplete && !diffSet[f.File] {
			return reviewResultErr("findings[%d].file %q 在 head %s 的仓库树中不存在，请核对审查对象后重交", i, f.File, reviewShortSHA(run.HeadSHA))
		}
	}
	return nil
}

// diffFilePaths 本轮 PR diff 中涉及的文件路径集（旧名与新名都算）：
// `--- a/`、`+++ b/`、`rename from/to` 行解析；/dev/null 与引号路径做
// 最小清理。给树未命中的 finding 做存在性兜底。complete=false 表示 diff
// 超出本端读入上限被截断——路径集不完整，缺席不能当不存在的证据。
func (s *ReviewService) diffFilePaths(ctx context.Context, client forge.ReviewClient, run *model.ReviewRun) (map[string]bool, bool, error) {
	diff, err := client.GetPullRequestDiff(ctx, run.RepoFullName, run.PRNumber)
	if err != nil {
		if forge.IsDiffTooLarge(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	// 只在文件头区块解析 ---/+++/rename 行（diff --git 之后、首个 @@ hunk
	// 之前）：被删除行原文若以 `-- a/` 开头会渲染成 `--- a/foo`，在 hunk
	// 体内——不能当路径收（虚增存在性证据会削弱校验本意）。
	paths := map[string]bool{}
	inHeader := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			inHeader = true
			// diff --git 头本身覆盖无 hunk 文件（二进制、纯 mode 变更、
			// 无文本块的删除），两侧路径都收。
			a, b := diffGitHeaderPaths(line[len("diff --git "):])
			if a != "" {
				paths[a] = true
			}
			if b != "" {
				paths[b] = true
			}
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHeader = false
			continue
		}
		if !inHeader {
			continue
		}
		switch {
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			if p := diffSidePath(line[4:]); p != "" {
				paths[p] = true
			}
		case strings.HasPrefix(line, "rename from "):
			if p := unquoteDiffPath(strings.TrimSpace(line[len("rename from "):])); p != "" {
				paths[p] = true
			}
		case strings.HasPrefix(line, "rename to "):
			if p := unquoteDiffPath(strings.TrimSpace(line[len("rename to "):])); p != "" {
				paths[p] = true
			}
		}
	}
	return paths, true, nil
}

// diffGitHeaderPaths 解析 `diff --git` 行两侧路径：含特殊字符时 git 把
// a//b/ 前缀一并包进整段引号 `"a/x y" "b/x y"`——先去引号再剥前缀。
// 未引号形态 git 不对空格引号，路径本身可含 ` b/`（如 images b/x.png），
// 首个 ` b/` 不一定是分界——非重命名两侧同路径，枚举 ` b/` 分割点取
// 两侧等值解；重命名两侧不同路径本就由 rename from/to 行覆盖，不解析。
func diffGitHeaderPaths(rest string) (string, string) {
	if strings.HasPrefix(rest, `"`) {
		qa, rem, ok := quotedToken(rest)
		if !ok {
			return "", ""
		}
		qb, _, ok := quotedToken(strings.TrimSpace(rem))
		if !ok {
			return "", ""
		}
		return stripDiffAB(unquoteDiffPath(qa)), stripDiffAB(unquoteDiffPath(qb))
	}
	if !strings.HasPrefix(rest, "a/") {
		return "", ""
	}
	body := rest[2:] // PATH b/PATH：非重命名两侧同路径
	for i := strings.Index(body, " b/"); i > 0; {
		if body[:i] == body[i+3:] {
			return body[:i], body[:i]
		}
		n := strings.Index(body[i+1:], " b/")
		if n < 0 {
			break
		}
		i += 1 + n
	}
	return "", ""
}

// diffSidePath 解析 ---/+++ 行尾路径：token 是 a/x、b/x、/dev/null 或整段
// 引号 "a/x y"（引号内才含前缀）。返回仓库相对路径；/dev/null 与无法识别
// 返回空串。
func diffSidePath(tok string) string {
	p := stripDiffAB(unquoteDiffPath(strings.TrimSpace(tok)))
	if p == "" || p == "/dev/null" {
		return ""
	}
	return p
}

// stripDiffAB 剥路径前的 a//b/ 段标记（diff --git 与 ---/+++ 行用）。
func stripDiffAB(p string) string {
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// quotedToken 取以 " 开头的引号串（内层 \" 跳过），返回含引号的 token、
// 剩余串与是否闭合；s 不以 " 开头或不闭合返回 ok=false。
func quotedToken(s string) (tok, rest string, ok bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return s[:i+1], s[i+1:], true
		}
	}
	return "", s, false
}

// unquoteDiffPath git core.quotePath 的 C 风格引号还原：整段 "..." 去引号，
// 还原 \\、\"、\NNN 三位八进制字节（非 ASCII 路径形态）与 \a \b \f \n \r
// \t \v 具名转义（git 对控制字符用具名而非八进制）。非引号形态原样返回；
// 不闭合或不可识别的转义按字面保留——匹配不上按缺席处理，不会误放。
func unquoteDiffPath(p string) string {
	if len(p) < 2 || p[0] != '"' || p[len(p)-1] != '"' {
		return p
	}
	body := p[1 : len(p)-1]
	var sb strings.Builder
	sb.Grow(len(body))
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			sb.WriteByte(c)
			continue
		}
		i++
		n := body[i]
		if n == '\\' || n == '"' {
			sb.WriteByte(n)
			continue
		}
		// git 对控制字符用具名转义而非八进制（quote.c 的 sq_lookup 表）。
		if c := diffNamedEscape(n); c != 0 {
			sb.WriteByte(c)
			continue
		}
		if n >= '0' && n <= '7' && i+2 < len(body) &&
			body[i+1] >= '0' && body[i+1] <= '7' && body[i+2] >= '0' && body[i+2] <= '7' {
			sb.WriteByte((n-'0')<<6 | (body[i+1]-'0')<<3 | (body[i+2] - '0'))
			i += 2
			continue
		}
		sb.WriteByte('\\')
		sb.WriteByte(n)
	}
	return sb.String()
}

// diffNamedEscape git 具名转义表（quote.c 对控制字符用具名而非八进制）；
// 非具名转义字符返回 0。
func diffNamedEscape(n byte) byte {
	switch n {
	case 'a':
		return '\a'
	case 'b':
		return '\b'
	case 'f':
		return '\f'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'v':
		return '\v'
	}
	return 0
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
	if err := validatePrior(r.Prior); err != nil {
		return err
	}
	// 视觉专属：open 的 prior 必须保留原严重度。红绿判定按 prior(open) 的级别
	// 计数（priorOpenBlocks），缺级别或误写代码审查的分级（P0/P1 等）会让遗
	// 留项漏出计数、结论错误变绿——在新提交的入口拦下，不把防御摊给各读取方。
	for i, p := range r.Prior {
		if p.Status != api.ReviewPriorDecisionStatusOpen {
			continue
		}
		if p.Level == nil {
			return reviewResultErr("prior[%d].level 必须是 blocker/worth_fixing/minor（open 需保留原严重度）", i)
		}
		switch *p.Level {
		case string(api.Blocker), string(api.WorthFixing), string(api.Minor):
		default:
			return reviewResultErr("prior[%d].level 必须是 blocker/worth_fixing/minor（open 需保留原严重度）", i)
		}
	}
	return nil
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
func (s *ReviewService) RerunReview(ctx context.Context, userID, projectID string, prNumber int, repoFullName string) (*RerunReviewResult, error) {
	project, err := s.ownedProject(userID, projectID)
	if err != nil {
		return nil, err
	}
	if prNumber < 1 {
		return nil, errs.ErrInvalidParams
	}
	// 审查仓库取设置行的 repo_full_name 快照（小写），与 webhook 反查、同步
	// 过滤、防抖复核同口径；不回读项目行 live 字段。这里先不按 enabled 过滤：
	// 「记录属于历史仓库」的拒绝要先于「未开启」返回，否则用户拿到的是 40923
	// 而不是真正的原因。
	var settings model.ProjectReviewSettings
	rowErr := s.db.WithContext(ctx).Where("project_id = ?", projectID).First(&settings).Error
	if rowErr != nil && !errors.Is(rowErr, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}
	// 记录所属仓库是历史仓库时不重跑：同号的无关 PR 不能被当成本项目的 PR。
	if rowErr == nil && repoFullName != "" && settings.RepoFullName != "" &&
		!strings.EqualFold(repoFullName, settings.RepoFullName) {
		return nil, errs.New(errs.ErrInvalidParams.Code,
			"该审查记录属于历史仓库，项目仓库已变更，不能重跑")
	}
	if rowErr != nil || !settings.Enabled {
		return nil, errs.ErrReviewNotEnabled
	}
	// 写库键因此天然是小写（GitHub API 本身大小写不敏感）。
	repo := settings.RepoFullName
	if repo == "" || repo == "/" {
		return nil, errs.ErrProjectGitHubNotConfigured
	}
	// 账本按 (项目, 集成, 仓库, pr) 分区：项目改绑集成后同仓库同号 PR 的
	// 旧平台记录仓库名相同但归属不同。当前集成下没有账本、其他集成下却
	// 有同号记录时，请求只可能来自旧记录（can_rerun 对这类记录已置否）——
	// 拒绝，否则会把旧集成的同号 PR 当成本平台的 PR 误审。从未审过的 PR
	// （任何集成下都没账本）不受影响，仍可手动重跑开轮。
	var ledgerCount, otherLedgerCount int64
	if err := s.db.WithContext(ctx).Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, prNumber, settings.IntegrationID).
		Count(&ledgerCount).Error; err != nil {
		return nil, errs.ErrInternal
	}
	if err := s.db.WithContext(ctx).Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id <> ?",
			project.ID, repo, prNumber, settings.IntegrationID).
		Count(&otherLedgerCount).Error; err != nil {
		return nil, errs.ErrInternal
	}
	if ledgerCount == 0 && otherLedgerCount > 0 {
		return nil, errs.New(errs.ErrInvalidParams.Code,
			"该审查记录属于其他集成的历史记录，不能重跑")
	}
	// rerun 没有 webhook installation 快照：GitHub 先解析真实安装 id 再取
	// PR，解析失败不得拿 0 当安装 id 继续写库；非 GitHub 集成没有安装语义，
	// installationID=0 经 resolver 走集成凭证取 PR——ResolveInstallation 是
	// GitHub App 专有概念，不进 forge.ReviewClient 方法面。
	var installationID int64
	if reviewIntegrationID(settings.IntegrationID) == model.ForgeIntegrationBuiltinGitHubID {
		resolved, err := s.resolveGitHubInstallation(ctx, repo)
		if err != nil {
			return nil, err
		}
		installationID = resolved
	}
	client, err := s.reviewClient(ctx, settings.IntegrationID, repo, installationID)
	if err != nil {
		return nil, err
	}
	pr, err := client.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "读取 PR head 失败: "+err.Error())
	}
	if !prReviewable(pr) {
		return nil, errs.New(errs.ErrInvalidParams.Code, "PR 已关闭，不能重跑审查")
	}
	if pr.HeadSHA == "" {
		return nil, errs.New(errs.ErrGitHubAPI.Code, "PR head 为空")
	}

	now := time.Now().UTC()
	var round int
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 取 PR 与开轮之间可能发生停用/换仓库/改绑集成/新 webhook：事务内复核
		// 设置行的 (集成, 仓库) 快照与启用状态，否则会用旧快照开一轮（甚至
		// 取消新 head 的运行）。
		var fresh model.ProjectReviewSettings
		if err := tx.Select("repo_full_name", "integration_id", "enabled").
			Where("project_id = ?", project.ID).First(&fresh).Error; err != nil {
			return errs.ErrReviewNotEnabled
		}
		if !fresh.Enabled {
			return errs.ErrReviewNotEnabled
		}
		if !strings.EqualFold(fresh.RepoFullName, repo) || fresh.IntegrationID != settings.IntegrationID {
			return errs.New(errs.ErrInvalidParams.Code, "项目仓库已变更，请刷新后重试")
		}
		latest, err := s.currentAcceptedHeadTx(tx, settings.IntegrationID, project.ID, repo, prNumber)
		if err != nil {
			return err
		}
		if latest != "" && !strings.EqualFold(latest, pr.HeadSHA) {
			return errs.ErrReviewHeadMoved
		}
		if err := s.supersedePRRunsTx(tx, settings.IntegrationID, project.ID, repo, prNumber, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, prNumber, settings.IntegrationID).
			Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		var openErr error
		round, openErr = s.openRoundTx(tx, project, &settings, repo, prNumber,
			pr.HeadSHA, "manual", pr.Title, pr.HTMLURL, pr.BaseRef, pr.HeadRef, installationID, now)
		return openErr
	})
	if err != nil {
		if errors.Is(err, errs.ErrReviewNotEnabled) || errors.Is(err, errs.ErrReviewHeadMoved) ||
			errors.Is(err, errs.ErrProjectNotFound) {
			return nil, err
		}
		if appErr := (*errs.AppError)(nil); errors.As(err, &appErr) {
			return nil, err
		}
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
