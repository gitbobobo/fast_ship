package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 评论唤醒（INT-179）：PR 顶层评论（issue_comment 事件）正文包含
// @fast-ship-bot 时也开一轮审查——审查结论有误时实现者回帖纠正即可唤醒
// 审查者，不必推空提交。GitHub 与 Forgejo 的 issue_comment payload 同形，
// 与 pull_request 一样共用中性路径；触发语义等同 rerun 的 manual：同 head
// 也恒开新轮（action=comment），不写 seen、不走 handleReviewTriggerTx。
// 编排沿用「事务内零网络调用」铁律，照 revalidateTiedWebhook 两阶段模式：
// 事务外只读预查 + 纯 payload 守卫 + 实拉 PR 实况，最终单事务幂等登记 +
// 复核后开轮。幂等账只随「已确定的受理/丢弃」落定——瞬时错误与事务失败
// 时库中无任何登记，平台重投可完整重走全流程。

// reviewCommentWakeMention 唤醒串的规格字面量（INT-179）：正文包含（大小
// 写不敏感）即视为点名。部署真实机器人句柄（App slug）经
// commentWakeMentions 追加。
const reviewCommentWakeMention = "@fast-ship-bot"

// commentWakeMentions 唤醒串集合：规格字面量 + 部署真实机器人句柄（App
// slug，manifest 流创建的 App slug 形如 fast-ship-<host>，与字面量不同——
// fast-ship-bot 第 20 轮 P2）。读取失败/未配置时回落字面量。
func (s *ReviewService) commentWakeMentions(ctx context.Context) []string {
	mentions := []string{reviewCommentWakeMention}
	if s.appSource == nil {
		return mentions
	}
	cfg, ok, err := s.appSource.Current(ctx)
	if err != nil || !ok || cfg.AppSlug == "" {
		return mentions
	}
	return append(mentions, "@"+strings.ToLower(cfg.AppSlug))
}

// reviewCommentWakeRevalidateMax 观察窗口复核的尝试上限：实拉前观察到的
// 总账 head 与事务内重取不一致时重读重拉；上限内不收敛（总账在同一窗口
// 内被持续并发推进）就放弃并补登幂等账。
const reviewCommentWakeRevalidateMax = 2

// errCommentWakeObservedHeadMoved 观察窗口哨兵：实拉前观察的总账 head 与
// 最终事务内重取的不一致——窗口内有并发写入推进了账本。哨兵让事务整体
// 回滚（幂等登记一并撤销，不烧账），由外层重读重拉后再试。
var errCommentWakeObservedHeadMoved = errors.New("评论唤醒观察窗口内总账被并发推进")

// commentWakePayload issue_comment 事件的最小字段面（其余字段原样丢弃）。
// GitHub 与 Forgejo 的 payload 同形：issue.pull_request 存在即 PR（普通
// Issue 的评论没有该字段）；Forgejo 无安装语义，installation 缺省为 0。
type commentWakePayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
		// PullRequest 存在即 PR：GitHub/Forgejo 都在 PR 的 issue payload 里
		// 带该对象，普通 Issue 评论缺省——指针判存在性即可，内容不消费。
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		// User.Type GitHub 把 App/机器人账号标为 "Bot"：机器自评论不唤醒
		// （见 commentWakePayloadGuards 的机器评论排除）。
		User struct {
			Type  string `json:"type"`
			Login string `json:"login"`
		} `json:"user"`
		// AuthorAssociation GitHub 评论者与仓库/Issue 的关系（OWNER/MEMBER/
		// COLLABORATOR/CONTRIBUTOR/AUTHOR/NONE）：GitHub 入口按它过滤无缘
		// 陌生人（见 commentWakePayloadGuards）。
		Association string `json:"author_association"`
	} `json:"comment"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// commentWakePayloadGuards 纯 payload 守卫（不碰库、不发网络）：依次判
// action（只受理 created）、mention（大小写不敏感包含）、PR 归属
// （issue.pull_request 存在 + 仓库/编号可用）。任一不过返回 nil——忽略判定
// 完全由 payload 决定，不登记幂等账：忽略类投递重投再忽略一遍，零副作用。
// 启用项目反查在最终事务内做（见 handleCommentWake 阶段二），这里不预查。
// 评论者身份过滤在实拉 PR 后做（commentWakeAuthorized）：需要与实拉 PR
// 作者登录名比对，且 Forgejo payload 没有关系字段，无法纯 payload 判定。
func commentWakePayloadGuards(body []byte, mentions []string) *commentWakePayload {
	var p commentWakePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	// 只受理新建评论；编辑/删除不唤醒。
	if p.Action != "created" {
		return nil
	}
	// 正文未点名机器人（规格字面量或部署机器人句柄）：不是给审查者的指令。
	lower := strings.ToLower(p.Comment.Body)
	mentioned := false
	for _, m := range mentions {
		if strings.Contains(lower, m) {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return nil
	}
	// 机器自评论不唤醒：审查结论与复审总结的正文由模型生成，可能引用
	// 用户评论里的 @fast-ship-bot——不排除会形成「发布结论 → 再唤醒」
	// 的无限审查循环（每轮 supersede 上一轮，持续烧额度）。两类识别：
	// 平台标记的 Bot 账号；机器评论自带的隐藏 HTML 标记。标记判定与
	// upsertPRComment 的 own 口径同源（TrimSpace 后前缀匹配）：机器结论
	// 的 marker 固定在正文开头，人类引用/摘录机器人评论片段（marker
	// 落在正文中间）不该被误杀（fast-ship-bot 第 4 轮 P2）。
	trimmedBody := strings.TrimSpace(p.Comment.Body)
	if p.Comment.User.Type == "Bot" ||
		strings.HasPrefix(trimmedBody, reviewCodeCommentMarker) ||
		strings.HasPrefix(trimmedBody, reviewVisualCommentMarker) ||
		strings.HasPrefix(trimmedBody, "<!-- codex-") {
		return nil
	}
	// 普通 Issue 评论（issue.pull_request 缺失）与缺仓库/编号的畸形投递忽略。
	if p.Issue.PullRequest == nil || p.Issue.Number <= 0 || p.Repository.FullName == "" {
		return nil
	}
	return &p
}

// commentAssociationAllowed GitHub association 白名单（可用纯 payload
// 判定的部分）：仓库属主。COLLABORATOR 邀请可授 Read/Triage，与
// MEMBER/CONTRIBUTOR/NONE 一样不留 payload 层判定空间——是否 PR 作者
// 须实拉后按登录名比对（见 commentWakeAuthorized）。
func commentAssociationAllowed(association string) bool {
	return association == "OWNER"
}

// commentWakeAuthorized 评论者身份（实拉 PR 后判定，两平台同口径）：
// 仓库属主（OWNER，恒有管理员权限）或 PR 作者本人（评论者登录名与实拉
// PR 作者一致）。其余一律不收：association 枚举除 OWNER 外没有任何值能
// 编码当前写权限——COLLABORATOR 邀请可授 Read/Triage、MEMBER 只是组织
// 成员身份（组织仓库基础权限可设 Read）、CONTRIBUTOR 是「历史提交过」、
// 没有 AUTHOR 值（首评作者标记为 NONE）——逐个信任它们的补丁已被逐轮
// 反例击穿（codex 第 9/10/12/14/15 轮 P2），根治口径：与 PR 强绑定的
// 作者和属主可点名，其余（无 push 权限的陌生人、组织成员、历史贡献者、
// 只读外部协作者）一律忽略——公开仓库上他们原本触发不了任何审查（fork
// PR 被拒、开不了同仓分支），放行等于开放无限烧额度通道。需要更宽的
// 维护者点名能力须做实时权限校验 API，属产品决策另议。Forgejo payload
// 没有关系字段，只能走登录名比对：仅 PR 作者可点名。
func commentWakeAuthorized(p *commentWakePayload, pr *forge.PullRequest) bool {
	if commentAssociationAllowed(p.Comment.Association) {
		return true
	}
	return p.Comment.User.Login != "" && pr.AuthorLogin != "" &&
		strings.EqualFold(p.Comment.User.Login, pr.AuthorLogin)
}

// deterministicCommentWakeSetupErr 评论唤醒前置解析的确定性失败：访问面未
// 配置（两个哨兵：resolveGitHubInstallation 的 gh==nil 与 reviewClient 链
// 路的 githubapp.ErrAppNotConfigured——webhook-secret-only 部署的永久配置
// 态，重投只会得到同一结果）与平台 401/403/404 且非限流（仓库无安装、
// 凭证吊销/权限不足）。限流 403 与网络/5xx/设置读取失败属瞬时，透出 500
// 让平台重投。其余 AppError（部署设置读取失败等 ErrInternal）也是瞬时
// DB 故障——只有两个哨兵按确定性处理，不能一提子以 errs.AppError 判定
// （fast-ship-bot 第 1 轮 P2、codex 第 11 轮 P2）。
func deterministicCommentWakeSetupErr(err error) bool {
	if errors.Is(err, errReviewGitHubAccessUnconfigured) ||
		errors.Is(err, githubapp.ErrAppNotConfigured) {
		return true
	}
	var statusErr *githubapi.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return !forge.IsRateLimit(err)
		}
		return false
	}
	return false
}

// observeAcceptedHead 评论唤醒实拉前的只读观察值：与 currentAcceptedHeadTx
// 同源（未开火的防抖行优先，否则总账 head_sha），项目反查同样按设置行
// (集成, 仓库) 快照。找不到启用项目时返回空串——观察值不参与判定，最终
// 事务的反查会照常拒开轮。实拉窗口内账本可能被并发推进：事务内以「重取值
// == 观察值」复核，不一致即哨兵重试（见 openCommentWakeRoundTx）。
func (s *ReviewService) observeAcceptedHead(ctx context.Context, integrationID, repo string, prNumber int) (string, error) {
	db := s.db.WithContext(ctx)
	project, _, err := s.findEnabledReviewProjectTx(db, integrationID, repo)
	if err != nil || project == nil {
		return "", err
	}
	return s.currentAcceptedHeadTx(db, integrationID, project.ID, repo, prNumber)
}

// handleCommentWake 评论唤醒的两阶段编排（照 revalidateTiedWebhook 模式）：
//
//	阶段一（事务外）：幂等预查 + 纯 payload 守卫 + installation 兜底解析 +
//	只读观察总账 head + 实拉 PR 实况（实拉独享超时 ctx）。「该不该唤醒」
//	以实时 State/head 为准，不信事件体（重投/迟到的 payload 是旧快照）；
//	构造客户端/实拉/安装解析的确定性失败重投结果相同，判丢弃并补登幂等
//	账；瞬时错误返回 error 让 handler 回 500 由平台重投——此时库里没有
//	任何登记，重投会完整重走全流程（这正是幂等账不能在此前单独提交的原因）。
//	阶段二（单事务）：幂等登记 → 反查启用项目 → 复核设置行 → 观察窗口复核
//	（重取总账 head，与观察值不一致即哨兵回滚、外层重读重拉，上限
//	reviewCommentWakeRevalidateMax 次不收敛判丢弃）→ fork/实况关卡 → 取消
//	在途、删防抖行、按 action=comment 恒开新轮。登记与开轮同一事务：任何
//	一步失败整体回滚（幂等账随之撤销），平台重投可重走；复核不过是已确定
//	的丢弃，事务提交后幂等账落定，重投按 202 去重。
//
//	head 采信语义：账本可能因丢投递落后于平台——观察窗口内总账未被并发推进
//	时，即便实拉 head 与总账不同也采信实拉 head 开轮（openRoundTx 把总账
//	推进到 live head），不再按「账本不同」把唤醒永久锁死。
func (s *ReviewService) handleCommentWake(ctx context.Context, integrationID, event, deliveryID string, body []byte) error {
	// 幂等预查（只读）：已登记的投递直接按重复去重，省一次实拉；预查与
	// 最终事务之间的并发窗口由事务内 insert 判重兜底。存量裸键兼容与
	// runWebhookDeliveryTx 同口径：集成前缀账键引入前的行是裸 GUID，只可
	// 能属于 github-default——升级前就订阅过 issue_comment 的安装，重投
	// 旧投递时仅查前缀键会漏掉裸键行而重复受理（codex 第 8 轮 P2）。
	key := deliveryLedgerKey(integrationID, deliveryID)
	countQuery := s.db.WithContext(ctx).Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", key)
	if integrationID == model.ForgeIntegrationBuiltinGitHubID {
		countQuery = countQuery.Or("delivery_id = ?", deliveryID)
	}
	var registered int64
	if err := countQuery.Count(&registered).Error; err != nil {
		return err
	}
	if registered > 0 {
		return ErrDeliveryDuplicated
	}

	// 纯 payload 守卫：非 created/无唤醒串/非 PR/畸形投递静默忽略（不登记）。
	payload := commentWakePayloadGuards(body, s.commentWakeMentions(ctx))
	if payload == nil {
		return nil
	}

	repo := strings.ToLower(payload.Repository.FullName)
	prNumber := payload.Issue.Number
	// 启用项目预查（只读，模式同 observeAcceptedHead）：未绑定/未启用的
	// 仓库零副作用返回——不写幂等账、不做任何平台 API 调用。否则任何人的
	// 点名评论（哪怕仓库根本没开启审查）都会放大成 installation 解析与
	// 实拉两次 App 级 API 调用（fast-ship-bot 第 16 轮 P2）。最终事务内
	// 会用同一判定复核，防预查与开轮之间停用/改绑。
	preProject, _, err := s.findEnabledReviewProjectTx(s.db.WithContext(ctx), integrationID, repo)
	if err != nil || preProject == nil {
		return err
	}
	// 显然无资格的点名（association 不在允许集且 payload 无登录名可比对
	// 作者）不必实拉——零 API 成本拒绝（codex 第 17 轮 P2：省安装 API
	// 预算）；带登录名的留待实拉后与 PR 作者比对（PR 作者例外）。
	if integrationID == model.ForgeIntegrationBuiltinGitHubID &&
		!commentAssociationAllowed(payload.Comment.Association) &&
		payload.Comment.User.Login == "" {
		return nil
	}
	installationID := payload.Installation.ID
	// GitHub 投递缺 installation 快照（代理剥字段等）时按仓库解析兜底；
	// Forgejo 恒 0（无安装语义，resolver 按集成凭证取 PR）。解析失败按
	// 确定性归类：访问面未配置/App 无该仓库安装（404）/凭证被拒（401、
	// 非限流 403）重投只会得到同一结果——判丢弃并补登幂等账；瞬时错误
	// （限流/网络）返回 error 让 handler 回 500。
	if integrationID == model.ForgeIntegrationBuiltinGitHubID && installationID == 0 {
		resolved, err := s.resolveGitHubInstallation(ctx, repo)
		if err != nil {
			if deterministicCommentWakeSetupErr(err) {
				s.logger.Warn("评论唤醒安装解析确定性失败，丢弃投递",
					zap.String("integration", integrationID),
					zap.String("delivery", deliveryID), zap.Error(err))
				s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
				return nil
			}
			s.logger.Warn("评论唤醒安装解析失败",
				zap.String("integration", integrationID),
				zap.String("delivery", deliveryID), zap.Error(err))
			return err
		}
		installationID = resolved
	}

	// 阶段一（事务外）：构造审查客户端。实拉独享超时预算（照
	// consumeDebounce：从 Background 派生并显式取消），开轮事务与补登账用
	// 请求 ctx——实拉的倒计时不再渗入后续库操作。
	pullCtx, pullCancel := context.WithTimeout(context.Background(), reviewForgejoPollTimeout)
	defer pullCancel()
	client, err := s.reviewClient(pullCtx, integrationID, repo, installationID)
	if err != nil {
		// 集成行已删/密文损坏/部署未配置 App 身份（ErrAppNotConfigured）等
		// 确定性失败重投只会得到同一错误——判丢弃并补登幂等账，不让平台
		// 无限重投；瞬时错误才透出 500。
		if deterministicReviewAccessErr(err) || errors.Is(err, githubapp.ErrAppNotConfigured) {
			s.logger.Warn("评论唤醒构造客户端确定性失败，丢弃投递",
				zap.String("integration", integrationID),
				zap.String("delivery", deliveryID), zap.Error(err))
			s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
			return nil
		}
		s.logger.Warn("评论唤醒构造客户端失败",
			zap.String("integration", integrationID),
			zap.String("delivery", deliveryID), zap.Error(err))
		return err
	}

	// 观察 → 实拉 → 事务复核：窗口内总账被并发推进就重读重拉，上限
	// reviewCommentWakeRevalidateMax 次仍不收敛判丢弃并补登幂等账（窗口
	// 内总有第三方在持续受理，此刻的实拉快照一进事务就过期，再试只是
	// 空转；登记后同 delivery 重投按 202 去重）。
	for attempt := 0; attempt < reviewCommentWakeRevalidateMax; attempt++ {
		observedHead, err := s.observeAcceptedHead(ctx, integrationID, repo, prNumber)
		if err != nil {
			return err
		}
		pr, err := client.GetPullRequest(pullCtx, repo, prNumber)
		if err != nil {
			// PR 已删/凭证吊销——确定性失败重投结果相同：判丢弃并补登幂等
			// 账；瞬时错误才透出 500 让平台重投。
			if deterministicReviewAccessErr(err) {
				s.logger.Warn("评论唤醒实拉确定性失败，丢弃投递",
					zap.String("integration", integrationID),
					zap.String("delivery", deliveryID), zap.Error(err))
				s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
				return nil
			}
			s.logger.Warn("评论唤醒实拉失败",
				zap.String("integration", integrationID),
				zap.String("delivery", deliveryID), zap.Error(err))
			return err
		}
		// 评论者身份：仓库当前授权人员或 PR 作者本人（与实拉 PR 作者登录
		// 名比对）方可点名；其余静默忽略（不登记，重投再忽略零副作用）。
		if !commentWakeAuthorized(payload, pr) {
			return nil
		}
		// 实拉快照时序判定（观察基准 observedHead 的 seen 行存有
		// pr_updated_at 时可比序）：live.UpdatedAt 更早说明实拉读到的是滞后
		// 快照（单 PR 读偶发滞后、自托管实例前置缓存/主从）——采信会把总
		// 账回退到旧 head，新 head 的投递反因「非当前 head」被永久吞掉。
		// 滞后按瞬时失败处理：不落任何账，返回 error 让 handler 回 500——
		// GitHub 侧投递未送达会进对账循环重投，拿到新鲜快照后自然恢复
		// （重投上限 3 次仍滞后则发故障通知）；与触发面的过期判定同口径：
		// 任一侧缺时间戳或无 seen 行（comment 轮自身不写 seen）维持采信
		// （fast-ship-bot 第 21 轮 P2：滞后不得烧幂等账静默丢唤醒）。
		var accepted model.ReviewSeenSHA
		snErr := s.db.WithContext(ctx).Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND head_sha = ? AND integration_id = ?",
			preProject.ID, repo, prNumber, observedHead, integrationID).First(&accepted).Error
		if snErr == nil && accepted.PRUpdatedAt != nil && pr.UpdatedAt != nil &&
			pr.UpdatedAt.Before(*accepted.PRUpdatedAt) {
			s.logger.Warn("评论唤醒实拉快照滞后于已受理 head，按瞬时失败重投",
				zap.String("integration", integrationID),
				zap.String("repo", repo), zap.Int("pr", prNumber),
				zap.String("live", pr.HeadSHA), zap.String("accepted", observedHead))
			return fmt.Errorf("评论唤醒实拉快照滞后于已受理 head")
		}
		err = s.openCommentWakeRoundTx(ctx, integrationID, event, deliveryID, repo, prNumber,
			installationID, observedHead, pr, time.Now().UTC())
		if errors.Is(err, errCommentWakeObservedHeadMoved) {
			s.logger.Info("评论唤醒观察窗口内总账被并发推进，重读重拉",
				zap.String("integration", integrationID),
				zap.String("repo", repo), zap.Int("pr", prNumber),
				zap.Int("attempt", attempt+1))
			continue
		}
		return err
	}
	s.logger.Warn("评论唤醒观察窗口未收敛，放弃并丢弃投递",
		zap.String("integration", integrationID),
		zap.String("repo", repo), zap.Int("pr", prNumber),
		zap.String("delivery", deliveryID))
	s.recordDroppedDelivery(ctx, integrationID, deliveryID, event)
	return nil
}

// openCommentWakeRoundTx 阶段二：最终单事务，幂等登记与复核开轮同生共死。
// 守卫不过静默返回——事务提交后幂等账落定，平台重投按 202 去重；任何一步
// 出错整体回滚（幂等账随之撤销），瞬时失败由平台重投完整重走。观察窗口
// 复核不过时回 errCommentWakeObservedHeadMoved（事务回滚不烧账，由外层
// 重读重拉）。
func (s *ReviewService) openCommentWakeRoundTx(ctx context.Context, integrationID, event, deliveryID, repo string,
	prNumber int, installationID int64, observedHead string, pr *forge.PullRequest, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 幂等登记先行：预查后的并发重投在事务内判重兜底。
		inserted, err := s.deliveries.InsertTx(tx, &model.GitHubWebhookDelivery{
			DeliveryID: deliveryLedgerKey(integrationID, deliveryID),
			Event:      event,
			ReceivedAt: now,
		})
		if err != nil {
			return err
		}
		if !inserted {
			return ErrDeliveryDuplicated
		}
		// 库侧守卫：反查启用项目（与 pull_request 触发同款判定：按设置行
		// (集成, 仓库) 快照）。未命中静默忽略。
		project, settings, err := s.findEnabledReviewProjectTx(tx, integrationID, repo)
		if err != nil || project == nil {
			return err
		}
		// 复核设置行（rerun 同款）：取 PR 与开轮之间可能发生停用/换仓库/
		// 改绑集成——设置行已删或停用、快照 (集成, 仓库) 不再匹配时不开轮。
		var fresh model.ProjectReviewSettings
		if err := tx.Select("repo_full_name", "integration_id", "enabled").
			Where("project_id = ?", project.ID).First(&fresh).Error; err != nil {
			// 只有「设置行已删（审查被移除）」才是已确定的丢弃——静默忽略并
			// 随事务落幂等账；其余库错误必须透出让事务整体回滚（不烧幂等
			// 账），否则一次 DB 抖动就把投递记成已处理，平台重投被 202 永吞。
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			return nil
		}
		if !fresh.Enabled || !strings.EqualFold(fresh.RepoFullName, repo) ||
			fresh.IntegrationID != settings.IntegrationID {
			return nil
		}
		// 观察窗口复核：实拉前观察到的总账 head 与事务内重取不一致——窗口
		// 内有并发写入推进了账本，此刻的实拉快照已经过期。哨兵让整体回滚
		// （幂等登记一并撤销），外层重读重拉后再试；上限内不收敛由调用方
		// 判丢弃并补登幂等账。一致时采信实拉 head：即便它与总账不同（丢
		// 投递造成的落后账本），也照常开轮并把总账推进到 live head。
		current, err := s.currentAcceptedHeadTx(tx, integrationID, project.ID, repo, prNumber)
		if err != nil {
			return err
		}
		if current != observedHead {
			return errCommentWakeObservedHeadMoved
		}
		// PR 实况关卡：跨仓 fork 不审（既有不变量，与 pull_request 触发面
		// 一致）——实拉 head 仓库缺失（已删 fork/异常 payload）或与 base
		// 仓库不同名即 fork，不开轮；closed/merged/draft 没有可审的实现，
		// 空 head 不开轮。
		if pr.HeadRepoFullName == "" || !strings.EqualFold(pr.HeadRepoFullName, pr.RepoFullName) {
			return nil
		}
		if !prReviewable(pr) || pr.Draft || pr.HeadSHA == "" {
			return nil
		}
		// 同 head 已有在途审查（queued/dispatched/running）：该 head 正在
		// 审，再点名是重复请求——不重开（否则两次点名互相作废在途轮，
		// 公开仓库可被连续点名饿死审查）。结论已出的轮不受影响：终态运行
		// 不计入在途，照常开新轮重审；在途运行停留在旧 head 上时也照常
		// supersede 重开。只计代码运行（kind=code）：视觉运行在途说明代码
		// 结论已发布——这正是「结论有误请重审」的主场景，点名必须开新轮
		// 并 supersede 在途视觉段（fast-ship-bot 第 7 轮 P2）。
		var active int64
		if err := tx.Model(&model.ReviewRun{}).
			Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ? AND head_sha = ? AND kind = ? AND status IN ?",
				project.ID, repo, prNumber, integrationID, pr.HeadSHA, model.ReviewKindCode,
				[]model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}).
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			s.logger.Info("同 head 已有在途审查，评论唤醒不重开",
				zap.String("integration", integrationID),
				zap.String("repo", repo), zap.Int("pr", prNumber),
				zap.String("head", pr.HeadSHA))
			return nil
		}
		// 恒开新轮：取消在途（含排队）、删防抖行（旧轮不得再开火），按
		// comment 开轮——comment 与 manual 一样同 head 也分配新轮次。
		if err := s.supersedePRRunsTx(tx, integrationID, project.ID, repo, prNumber, now); err != nil {
			return err
		}
		if err := tx.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
			project.ID, repo, prNumber, integrationID).Delete(&model.ReviewDebounce{}).Error; err != nil {
			return err
		}
		_, err = s.openRoundTx(tx, project, settings, repo, prNumber,
			pr.HeadSHA, "comment", pr.Title, pr.HTMLURL, pr.BaseRef, pr.HeadRef, installationID, now)
		return err
	})
}
