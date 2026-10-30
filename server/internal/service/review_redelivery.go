package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"go.uber.org/zap"
)

// 投递失败对账（INT-146）：GitHub 发出但没送达本服务的投递（公网隧道超时/502）
// GitHub App 不会自动重试，webhook 侧也没有别的对账入口——不做这一步，一次
// 投递失败就意味着这次推送永远不会被审查。独立低频循环（redeliveryLoop，不
// 占 10s 调度 tick）拉投递记录、按 guid 分组并重投；复杂度收在 pkg/githubapp
// 适配器里，本文件只做分组与决策。
//
// 重投前有一道「PR 实况闸门」：投递列表行没有 repo/PR 信息，只能先拉单次
// 投递详情（GetHookDelivery）拿到 payload 定位 repo/PR，再用安装令牌实时读
// State。payload 是投递时刻的旧快照（PR 当时写着 open），不能据此判断关闭；
// 而本地也可能压根不知道 PR 已关（closed 投递有意不重投，收不到关闭事件），
// 所以只能问 GitHub。已关闭/合并的 PR 重投只会白跑一轮（webhook 会受理并
// 推进账本、取消旧运行）并在连续失败时误报「推送可能没被审查」，闸门把它
// 记成终态后静默跳过。读不到（详情拉取失败/解析不出 PR/实时读失败）时降级
// 为原行为照常重投——下游三道实时关卡（防抖开轮/建线程/写产出）已拦得住
// 误开轮，这道闸门只是省白跑与误报，不能反过来漏掉真投递。
//
// 已重投次数与关闭终态持久化在 github_webhook_redeliveries（重投次数账）：
// 投递列表只保留约 3 天，原始行滑出 24h 回看窗口后从列表推导的次数会变少
// ——可能重复重投或漏发超限通知；账丢了（被清/恢复丢失）顶多退化回列表
// 推导的近期次数。

const (
	// reviewRedeliverySweepEvery 对账频率：远低于 10s 调度 tick——投递失败是
	// 少数事件，且列表端点走 App JWT、没必要每轮都调。
	reviewRedeliverySweepEvery = 5 * time.Minute
	// reviewRedeliveryLookback 回看窗口：GitHub 只保留约 3 天投递记录，24h
	// 足够覆盖「上一轮对账之间漏掉的失败」，也不会翻太多页。
	reviewRedeliveryLookback = 24 * time.Hour
	// reviewRedeliveryMaxAttempts 同一 guid 的重投上限：超出即认定入口持续
	// 不可达（服务停机/隧道下线），继续重投只会空转。
	reviewRedeliveryMaxAttempts = 3
	// reviewRedeliveryTimeout 单轮对账的兜底超时：列表 + 若干次重投的总额。
	// 独立循环不阻塞调度，但仍要有界，避免 GitHub 半挂时无限拖。
	reviewRedeliveryTimeout = 90 * time.Second
	// reviewRedeliveryProbeTimeout PR 实况闸门的独立时间预算：闸门是可选校验
	// （读不到就降级为原行为），不能把整轮时间耗在这里——否则探测超时后重投
	// 请求会拿到已取消的 ctx，后面的真投递既不重投也不通知，与改动前相比是
	// 退化。到点后闸门立即返回未知，剩下的时间留给重投与通知。
	reviewRedeliveryProbeTimeout = 30 * time.Second
	// reviewRedeliveryReserve 整轮总超时里给重投/通知预留的余量：列表页多、
	// 探测又慢时，闸门的截止时间不能晚于「总截止 - 余量」，保证重投还有时间。
	reviewRedeliveryReserve = 30 * time.Second
	// reviewRedeliveryMaxPerSweep 一轮对账内最多发出的重投请求数（按「尝试」
	// 计，失败也占名额）：到顶就停，剩余留到下一轮（5 分钟后）。不按成功计
	// 是因为接口持续 5xx / 404 时请求同样在打 GitHub 与本服务的 webhook 入口，
	// 只按成功计数会让上限形同虚设。
	reviewRedeliveryMaxPerSweep = 20
)

// hookDeliveryState 同一 guid 的投递汇总：原始投递与每次重投各占一行（GitHub
// 追加记录），去重和次数兜底计数都从这里推。
type hookDeliveryState struct {
	GUID      string
	Event     string
	Action    string
	Delivered bool // 任一行 2xx
	Attempts  int  // redelivery=true 的行数（列表可见的近期重投次数）
	TargetID  int64
	TargetAt  time.Time
}

// reconcileWebhookDeliveries 一轮投递失败对账：GitHub 发出但没送达本服务的
// pull_request 与 issue_comment/created 投递（隧道超时/502）会被重投，避免
// 一次投递失败就让这次推送/评论唤醒永远不生效。节流由 redeliveryLoop 的
// ticker 负责，本函数不做节流。
// 超上限后不再重试并发站内通知。循环内的判定顺序：跳过已送达 → 已判关闭
// 静默跳过 → PR 实况闸门 → 超限通知 → 每轮重投预算 → 重投。
func (s *ReviewService) reconcileWebhookDeliveries(now time.Time) {
	if s.gh == nil {
		return
	}
	// 投递记录是 App 维度、映射不到具体项目：通知发给所有启用审查的用户；
	// 没有启用审查的项目时整个对账都没有意义。
	var userIDs []string
	if err := s.db.Model(&model.ProjectReviewSettings{}).
		Where("enabled = ?", true).Distinct().Pluck("user_id", &userIDs).Error; err != nil {
		s.logger.Error("查询启用审查的用户失败", zap.Error(err))
		return
	}
	if len(userIDs) == 0 {
		return
	}
	// 网络面（ClientFor + 列表 + 重投）共用同一个兜底超时，先建 ctx 再取客户端。
	ctx, cancel := context.WithTimeout(context.Background(), reviewRedeliveryTimeout)
	defer cancel()
	client, err := s.gh.ClientFor(ctx)
	if err != nil {
		// App 未配置等前置缺失是常态，不按错误处理。
		s.logger.Info("投递对账跳过：无法取得 GitHub 客户端", zap.Error(err))
		return
	}
	hooks, ok := client.(githubapp.HookDeliveries)
	if !ok {
		return // 静态令牌等实现没有投递记录访问面
	}
	items, err := hooks.ListHookDeliveries(ctx, now.Add(-reviewRedeliveryLookback))
	if err != nil {
		s.logger.Warn("拉取 GitHub 投递记录失败", zap.Error(err))
		return
	}
	states := groupHookDeliveries(items)
	pending := make([]hookDeliveryState, 0, len(states))
	for _, st := range states {
		if st.Delivered {
			continue
		}
		pending = append(pending, st)
	}
	if len(pending) == 0 {
		return
	}
	// 幂等账键带集成前缀（deliveryLedgerKey）：本循环只对账 GitHub 投递；
	// 裸 guid 一并带上——存量行是未加前缀的 GitHub 键，只查前缀会在保留期
	// 内漏掉旧行误报未送达。
	guids := make([]string, 0, len(pending)*2)
	for _, st := range pending {
		guids = append(guids,
			deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, st.GUID), st.GUID)
	}
	// 本地幂等账一次查全（含超限组）：否则「GitHub 侧三次都超时、但某次重投
	// 实际已在本地落库」的 guid 会被误报故障通知。
	existing, err := s.deliveries.ExistingIDs(s.db, guids)
	if err != nil {
		s.logger.Error("查询本地投递幂等账失败", zap.Error(err))
		return
	}
	// 重投账一次查全：有效次数取 max(账, 列表)——列表里 redelivery=true
	// 的行数只是兜底（DB 行被清/恢复丢失时列表还能反映近期尝试），账是跨
	// 24h 窗口的真相（原始行滑出窗口后列表推不出总次数）。Closed 是上一轮
	// 闸门记下的终态：直接跳过，不再发任何查询。
	ledger, err := s.redeliveries.StatesFor(s.db, guids)
	if err != nil {
		s.logger.Error("查询 webhook 重投账失败", zap.Error(err))
		return
	}
	// PR 实况闸门的查询面：payload/PR 查询共享每轮预算（同重投上限，防一条
	// 积压列表每轮打上百个请求），同一 repo#pr 实时读过一次后整轮复用。
	// 闸门另有独立时间预算（probeCtx）：探测不挤占总超时，否则它会先烧完 90s
	// 再让重投请求拿到已取消的 ctx。
	probeBudget := reviewRedeliveryProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remain := time.Until(deadline) - reviewRedeliveryReserve; remain < probeBudget {
			probeBudget = remain // 列表已花掉大半总超时：给重投留余量，闸门缩到负值即停用
		}
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, probeBudget)
	defer probeCancel()
	// 闸门降级语义：唤醒串读取瞬时失败回落字面量继续重投——闸门本就宽
	// 松，宁可多投不漏投（唤醒侧则按瞬时失败 500 重试，两处口径不同）。
	mentions, mentionsErr := s.commentWakeMentions(ctx)
	if mentionsErr != nil {
		mentions = []string{reviewCommentWakeMention}
	}
	probe := &redeliveryPRProbe{
		hooks:    hooks,
		client:   client,
		budget:   reviewRedeliveryMaxPerSweep,
		mentions: mentions,
		// 唤醒串读取降级时，slug-only 评论会被字面量集合误判成非唤醒而记
		// 终态——那恰是因读取故障失败、最需要重试的投递（codex 第 23 轮
		// P2）。标记降级，评论投递一律按未知重试（降级语义：宁多投不漏投）。
		mentionsDegraded: mentionsErr != nil,
		prStates:         map[string]*probePRState{},
	}
	tried := 0
	for _, st := range pending {
		if ctx.Err() != nil {
			break // 总超时到了，别再发请求
		}
		if existing[st.GUID] ||
			existing[deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, st.GUID)] {
			continue // 已送达（GitHub 侧记录滞后于本地登记），不重投
		}
		if ledger[st.GUID].Closed {
			continue // 已确认关闭的 PR：终态，不重投、不通知、不再查询
		}
		if check := s.redeliveryPRReviewable(probeCtx, probe, st.TargetID, st.Event); check.Known && !check.Reviewable {
			// 记终态后静默跳过——重投只会白跑一轮（webhook 会照常受理并在
			// 连续失败时误报故障通知）。记账失败只留日志，不影响其它组（下
			// 一轮重查同一条，顶多重复一次）。
			if err := s.redeliveries.RecordClosed(s.db, st.GUID, time.Now().UTC()); err != nil {
				s.logger.Error("记录投递终态失败",
					zap.String("guid", st.GUID), zap.Error(err))
			}
			if check.NotWake {
				s.logger.Info("非唤醒评论投递，跳过重投",
					zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID))
			} else if check.Fork {
				s.logger.Info("投递指向的 PR 是跨仓 fork，跳过重投",
					zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID),
					zap.String("repo", check.Repo), zap.Int("pr", check.Number))
			} else {
				s.logger.Info("投递指向的 PR 当前不可审（已关闭/草稿），跳过重投",
					zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID),
					zap.String("repo", check.Repo), zap.Int("pr", check.Number))
			}
			continue
		}
		attempts := ledger[st.GUID].Attempts
		if st.Attempts > attempts {
			attempts = st.Attempts
		}
		if attempts >= reviewRedeliveryMaxAttempts {
			s.notifyRedeliveryExhausted(userIDs, st, attempts)
			continue
		}
		if tried >= reviewRedeliveryMaxPerSweep {
			s.logger.Info("本轮重投请求数已达上限，剩余留到下一轮",
				zap.Int("limit", reviewRedeliveryMaxPerSweep))
			break
		}
		// 先占名额再请求：被拒的尝试同样消耗一轮的预算（见常量注释）。
		tried++
		err := hooks.RedeliverHookDelivery(ctx, st.TargetID)
		if err != nil {
			s.logger.Warn("投递重投请求失败",
				zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID), zap.Error(err))
			var statusErr *githubapi.StatusError
			if errors.As(err, &statusErr) &&
				(statusErr.StatusCode == http.StatusForbidden || statusErr.StatusCode == http.StatusTooManyRequests) {
				// 被限流：GitHub 明确要求停止请求并退避；本轮停止，5 分钟后
				// ticker 自然再来。
				s.logger.Warn("投递重投被限流，本轮对账停止",
					zap.String("guid", st.GUID), zap.Int("status", statusErr.StatusCode))
				break
			}
			continue
		}
		// 只记成功的重投：请求被拒时 GitHub 没产生投递记录，下一轮会再试；
		// 记账失败只留日志，不影响后续组（顶多少计一次，靠列表兜底）。
		if err := s.redeliveries.RecordAttempt(s.db, st.GUID, time.Now().UTC()); err != nil {
			s.logger.Error("记录 webhook 重投次数失败",
				zap.String("guid", st.GUID), zap.Error(err))
		}
		s.logger.Info("已请求 GitHub 重投 webhook 投递",
			zap.String("guid", st.GUID), zap.Int64("delivery", st.TargetID))
	}
}

// notifyRedeliveryExhausted 同一 guid 重投超上限仍未送达：给每个启用审查的
// 用户发一条通知。dedup 键含 guid——重复对账只推进 last_seen_at，不刷屏。
func (s *ReviewService) notifyRedeliveryExhausted(userIDs []string, st hookDeliveryState, attempts int) {
	if s.sink == nil {
		return
	}
	title := fmt.Sprintf("PR 审查投递重投失败：%s/%s", st.Event, st.Action)
	// 恢复指引按事件面区分：推送投递丢了重推即可；评论唤醒丢了重投也不
	// 行——幂等账只随确定结果落定，失败投递没进账，用户重新评论一次就能
	// 触发新一轮（codex 第 25 轮 P3：误导性文案把用户往重推空提交上引）。
	var hint string
	if st.Event == "issue_comment" {
		hint = "这次评论唤醒可能没有开始；请在 PR 上重新评论一次即可重新触发"
	} else {
		hint = "这次推送可能没有被审查；重新推送或合并后重试即可触发"
	}
	body := fmt.Sprintf("GitHub 的 %s/%s 投递在 %d 次重投后仍未送达本服务（guid %s），%s；请检查公网入口与隧道。",
		st.Event, st.Action, attempts, st.GUID, hint)
	for _, userID := range userIDs {
		err := s.sink.Publish(NotificationInput{
			UserID:     userID,
			Kind:       model.NotificationReviewRedeliveryFailed,
			ProblemKey: "webhook-delivery:" + st.GUID,
			Title:      title,
			Body:       body,
		})
		if err != nil {
			s.logger.Warn("投递重投失败通知写入失败", zap.String("user", userID), zap.Error(err))
		}
	}
}

// errRedeliveryProbeBudget 一轮内闸门查询预算用完：按设计降级（known=false
// 照常重投），不是故障，调用方不按 Warn 处理。
var errRedeliveryProbeBudget = errors.New("重投闸门查询预算已用完")

// redeliveryPRProbe 一轮对账内 PR 实况闸门的查询面：GetHookDelivery（定位
// 投递指向的 PR）与缓存未命中的 GetPullRequest（实时读 State）共享每轮次数
// 预算（独立于重投请求的 20 次上限），防一条积压列表每轮打上百个请求；同一
// repo#pr 读到一次后整轮复用。
type redeliveryPRProbe struct {
	hooks    githubapp.HookDeliveries
	client   githubapi.GitHubClient
	budget   int
	mentions []string
	prStates map[string]*probePRState // repo#number -> PR 实况快照（可用性 + 作者）
	// mentionsDegraded 唤醒串集合读取降级（瞬时 DB 故障回落字面量）：此刻
	// 字面量判定「非唤醒」不可信——slug-only 的合法唤醒会被记终态永不重试，
	// 评论投递一律按未知重试（codex 第 23 轮 P2）。
	mentionsDegraded bool
}

// redeliveryPRCheck 一次闸门判定的结果。Known=false 表示读不到（payload 拉取
// 失败/解析不出 PR/实时读失败/预算耗尽），调用方降级为原行为照常重投。
// probePRState 闸门缓存的 PR 实况快照：可用性与作者登录名分开存——评论者
// 身份按投递现算，不能掺进缓存。
type probePRState struct {
	usable bool
	author string
	// fork 同 prUsable 一起缓存：命中分支回填 check.Fork，日志不把 fork
	// 误判成关闭/草稿（fast-ship-bot 第 38 轮 Note）。
	fork bool
}

type redeliveryPRCheck struct {
	Known      bool
	Reviewable bool
	Repo       string
	Number     int
	// NotWake 评论唤醒专用：投递是 issue_comment 但 payload 不满足唤醒守卫
	// （编辑/删除、普通 Issue 评论、正文未点名机器人）——webhook 本来就
	// 会静默忽略，对账记终态跳过，不进重试集不吃预算。
	NotWake bool
	// Fork 投递指向跨仓 fork PR（或已删 fork）：唤醒/审查守卫必拒，重投
	// 只会白跑，同样记终态跳过。
	Fork bool
}

// redeliveryPRReviewable 重投前的 PR 实况闸门：确认投递指向的 PR 仍开着。
// 读不到时只记 Warn（含投递 id 与原因，不打 payload 全文——里面是仓库内容）
// 并按未知处理。闸门预算/时间用完是设计内的降级，不当故障记。
func (s *ReviewService) redeliveryPRReviewable(ctx context.Context, probe *redeliveryPRProbe, deliveryID int64, event string) redeliveryPRCheck {
	check, err := probe.lookup(ctx, deliveryID, event)
	if err != nil && !errors.Is(err, errRedeliveryProbeBudget) &&
		!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		s.logger.Warn("重投闸门无法确认投递指向的 PR 实况，降级为照常重投",
			zap.Int64("delivery", deliveryID), zap.Error(err))
	}
	return check
}

// lookup 取投递详情、定位其 PR 并实时读 State。每次 GetHookDelivery、每次
// 缓存未命中的 GetPullRequest 各占一个预算名额（被拒的请求同样打到了
// GitHub，与重投预算同口径）；只缓存读到的状态，失败不缓存——预算兜住
// 总量，后续投递仍可重试。payload 按 event 选解析面：pull_request 与
// issue_comment（INT-179 评论唤醒）都带 repository/installation，但 PR 号
// 字段名不同（前者顶层 number、后者 issue.number）。
func (p *redeliveryPRProbe) lookup(ctx context.Context, deliveryID int64, event string) (redeliveryPRCheck, error) {
	// 闸门自己的时间预算已经用完：不再发请求，直接按未知降级（调用方还有
	// 正常的总超时 context 可用）。
	if err := ctx.Err(); err != nil {
		return redeliveryPRCheck{}, err
	}
	if p.budget <= 0 {
		return redeliveryPRCheck{}, errRedeliveryProbeBudget
	}
	p.budget--
	detail, err := p.hooks.GetHookDelivery(ctx, deliveryID)
	if err != nil {
		return redeliveryPRCheck{}, err
	}
	var repo string
	var number int
	var installation int64
	// issue_comment 投递的守卫结果（nil = 非 issue_comment）：用于实拉后的
	// 评论者身份判定（pull_request 投递不需要——推送作者天然有 push 权限）。
	var wakePayload *commentWakePayload
	switch event {
	case "issue_comment":
		// 与评论唤醒共用同一份最小字段面：repository.full_name + issue.number。
		// 解析失败 ≠ 守卫拒绝：空/损坏 payload（或形状变化）按未知降级重投，
		// 与 pull_request 分支同口径——只有解析成功后的守卫拒绝才是非唤醒
		// 终态（fast-ship-bot 第 29 轮 P2：真实唤醒不得被记终态不再重试）。
		var probe commentWakePayload
		if err := json.Unmarshal(detail.Payload, &probe); err != nil {
			return redeliveryPRCheck{}, fmt.Errorf("解析投递 payload: %w", err)
		}
		// 守卫拒绝（非唤醒投递：普通 Issue 评论、正文未点名机器人）webhook
		// 本来就会静默忽略——记终态跳过，不进重试集白吃预算与通知。
		payload := commentWakePayloadGuards(detail.Payload, p.mentions)
		if payload == nil {
			if p.mentionsDegraded {
				// 字面量集合不完整：判「非唤醒」不可信，按未知处理——调用方
				// 对未知投递降级照常重投（宁多投不漏投，codex 第 23 轮 P2）。
				return redeliveryPRCheck{}, errors.New("唤醒串集合读取降级，评论投递按未知重试")
			}
			return redeliveryPRCheck{Known: true, Reviewable: false, NotWake: true}, nil
		}
		wakePayload = payload
		repo, number, installation = payload.Repository.FullName, payload.Issue.Number, payload.Installation.ID
	default:
		var payload prWebhookPayload
		if err := json.Unmarshal(detail.Payload, &payload); err != nil {
			return redeliveryPRCheck{}, fmt.Errorf("解析投递 payload: %w", err)
		}
		repo, number, installation = payload.Repository.FullName, payload.Number, payload.Installation.ID
	}
	check := redeliveryPRCheck{
		Repo:   strings.ToLower(repo),
		Number: number,
	}
	if number <= 0 || repo == "" || installation == 0 {
		return check, errors.New("投递 payload 缺少 repo/PR/installation 字段")
	}
	key := check.Repo + "#" + strconv.Itoa(check.Number)
	if st, ok := p.prStates[key]; ok {
		// 缓存只放 PR 实况快照；评论者身份按投递现算（同 sweep 内同 PR 的
		// 无资格评论不得连累有资格投递，codex 第 13 轮 P2）。
		authorized := wakePayload == nil || commentWakeAuthorized(wakePayload,
			&forge.PullRequest{AuthorLogin: st.author})
		check.Known, check.Reviewable = true, st.usable && authorized
		check.Fork = st.fork
		return check, nil
	}
	if p.budget <= 0 {
		return check, errRedeliveryProbeBudget
	}
	p.budget--
	pr, err := p.client.GetPullRequest(ctx, installation, repo, number)
	if err != nil {
		return check, err
	}
	// 重投是 GitHub 专属路径（deliveries API 不在 forge.ReviewClient 方法面），
	// 实况判定与审查链路同一口径：经共享转换后复用 prReviewable。fork PR
	// （head 仓库缺失或与 base 不同名）的投递无论重投多少次都会被 fork
	// 守卫丢弃；无资格评论者的投递同样必拒——都记终态跳过，不白吃重投
	// 预算（codex 第 5/12 轮 P2）。
	forgePR := forge.PullRequestFromGitHub(pr)
	open := prReviewable(forgePR)
	// fork 判定与评论唤醒守卫同口径：head 与**实拉 base** 仓库名比对——
	// payload 里的仓库名是投递时刻的旧快照，仓库改名/转移后会过期，拿它
	// 当同仓基准会把改名窗口内的同仓 PR 误判成跨仓 fork 记终态、永不重投
	// （fast-ship-bot 第 24 轮 P2）。
	fork := open && (forgePR.HeadRepoFullName == "" || !strings.EqualFold(forgePR.HeadRepoFullName, forgePR.RepoFullName))
	// 评论者身份仅约束 issue_comment 投递（pull_request 投递的推送作者天然
	// 有 push 权限，不需要也不能用评论身份判定），且**按投递现算、不入
	// prStates 缓存**——缓存被同一 sweep 内同 PR 的后续投递复用，掺入某条
	// 无资格评论的判定会把有资格投递也错误跳过（codex 第 13 轮 P2）。
	authorized := wakePayload == nil || commentWakeAuthorized(wakePayload, forgePR)
	// 草稿 PR 与触发面同口径排除（handleReviewTriggerTx 的 t.draft、评论
	// 唤醒事务的 pr.Draft 守卫都会丢弃）：草稿期间的投递重投只会白吃预算
	// 甚至误发故障通知（codex 第 34 轮 P2）；转正式后新投递自然开审。
	prUsable := open && !fork && !forgePR.Draft
	p.prStates[key] = &probePRState{usable: prUsable, author: forgePR.AuthorLogin, fork: fork}
	reviewable := prUsable && authorized
	check.Known, check.Reviewable = true, reviewable
	check.Fork = !reviewable && fork
	return check, nil
}

// groupHookDeliveries 把投递行按 guid 汇总：处理会开轮的事件行——
// pull_request 里会开轮的 action（opened/synchronize/ready_for_review/
// reopened）与 issue_comment 的 created（INT-179 评论唤醒也靠 webhook 投递
// 入场，丢了同样永久漏审）。丢失的 closed
// 投递不会导致「推送没被审查」（本功能唯一目标），但把旧的 closed 事件重投
// 回去会无条件取消该 PR 当前有效的审查运行（webhook 的 closed 分支在任何
// 时间戳判定之前执行），破坏面比收益大，有意不重投。同一 guid 只对应一条
// 投递，不存在跨事件混组的歧义。重投目标取最早一条
// 失败行的 delivery id；输出按 TargetAt 升序（GUID 兜同刻排序），保证对账
// 行为确定。Attempts 只数列表可见的重投行——原始行滑出回看窗口后它不再等
// 于总次数，调用方须以重投次数账为准。
func groupHookDeliveries(items []githubapp.HookDelivery) []hookDeliveryState {
	byGUID := map[string]*hookDeliveryState{}
	for _, it := range items {
		switch {
		case it.Event == "pull_request" && reviewTriggerableAction(it.Action):
		case it.Event == "issue_comment" && it.Action == "created":
		default:
			continue
		}
		st, ok := byGUID[it.GUID]
		if !ok {
			st = &hookDeliveryState{GUID: it.GUID, Event: it.Event, Action: it.Action}
			byGUID[it.GUID] = st
		}
		if it.Delivered() {
			st.Delivered = true
		}
		if it.Redelivery {
			st.Attempts++
		}
		if !it.Delivered() && (st.TargetID == 0 || it.DeliveredAt.Before(st.TargetAt)) {
			st.TargetID = it.ID
			st.TargetAt = it.DeliveredAt
		}
	}
	out := make([]hookDeliveryState, 0, len(byGUID))
	for _, st := range byGUID {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].TargetAt.Equal(out[j].TargetAt) {
			return out[i].TargetAt.Before(out[j].TargetAt)
		}
		return out[i].GUID < out[j].GUID
	})
	return out
}
