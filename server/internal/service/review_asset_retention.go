package service

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"go.uber.org/zap"
)

// review 截图资产行账保留策略（INT-184）：review-screenshot-cache 面的
// 持久占用此前只增不减，本文件按「以 review_assets 行为账」回收：
//
//   - 行退役：created_at 超过保留期的行删除，但 head 上仍有非终态运行的
//     （queued/dispatched/running）除外——在途运行不能丢自己的材料。关闭
//     PR 的行、旧 head 的行、探测过但没有视觉运行的行（探测落账见
//     review_assets.go）按同一条年龄规则退出，不为触发条件引入 PR 关闭态
//     这样的第二状态源。
//   - 字节回收：存活行的 storage_path 集合交给 githubmedia 的
//     SweepUnreferencedEntries（面内唯一的 sanctioned 删除路径），引用计数
//     + mtime 宽限判定；崩溃窗口（文件已 rename、行未插入）与「行插入前的
//     命中」由宽限覆盖（行插入路径的 mtime 必新：新下载 rename / 命中
//     touch，见 proxy.go touchReviewEntry）。
//
// 每步幂等：任意时点被杀，下一轮从同样的判定重新收敛。删除与行插入之间
// 的残余竞态由 ensureAssets 的文件复核自愈（缺失按失败行重下）。

// reviewAssetRetention 资产行保留期：审查截图作为持久视觉证据保留 30 天，
// 超期行退役、其字节在下一轮扫描回收。
const reviewAssetRetention = 30 * 24 * time.Hour

// reviewAssetSweepGrace 无引用字节的 mtime 宽限：覆盖「文件已落盘、行还没
// 插入」的崩溃窗口与引用快照到删除之间的行插入（行插入路径的 mtime 必新：
// 新下载 rename / 命中 touch，见 proxy.go touchReviewEntry；探测落账即时同
// 步执行后，R12 时代的「探测与视觉运行间隔数小时」场景已不存在，宽限不再
// 需要为它留量）。24h 取保守值，显著大于任何插入路径从落盘到落账的耗时。
const reviewAssetSweepGrace = 24 * time.Hour

// reviewAssetSweepEvery 保留策略扫描周期：与 githubmedia 过期扫描同节奏，
// 孤儿字节最坏一个周期 + 一轮退役延迟内回收。
const reviewAssetSweepEvery = 6 * time.Hour

// reviewAssetRetireBatch 单批退役行数：每批一条独立 DELETE，批间释放
// SQLite 写锁，让调度/webhook 的写请求插得进来。与
// idx_review_assets_created_at / idx_review_runs_asset_head 配合，单批耗
// 时有界。var 是为了让测试注入小值验证多批循环的终止条件。
var reviewAssetRetireBatch = 2000

// reviewAssetSweepCalmBatch 同源自检失败时的单轮回收上限：备份恢复/存储
// 迁移/换挂载这类库与目录不同源的运维动作下，快照推断「目录全是孤儿」
// 是错的——限量回收把误删从一次清空降为缓慢滴漏，运维看到 warn 可介入。
// var 为测试注入。
var reviewAssetSweepCalmBatch = 25

// reviewAssetSweepMismatchFactor / reviewAssetSweepDirFloor 同源判定的比
// 例档与地面：目录候选数超过快照规模十倍且超过地面值才判失配，小部署的
// 少量孤儿不触发限量。
const (
	reviewAssetSweepMismatchFactor = 10
	reviewAssetSweepDirFloor       = 20
)

// reviewAssetSweeper 下载面的行账回收能力（可选）：生产注入的
// *githubmedia.ProxyService 实现；测试桩可不实现，扫描自动跳过文件回收。
type reviewAssetSweeper interface {
	SweepUnreferencedEntries(isReferenced func(binPath string) bool, grace time.Duration, now time.Time) int
	// CacheDir 返回回收目标缓存目录（快照与目录的同源自检用）。
	CacheDir() string
	// CountCacheEntries 返回缓存目录里的 .bin/.json 候选条目数（同源判定
	// 的规模档用）。
	CountCacheEntries() int
}

// assetRetentionLoop 保留策略的低频循环：不与 10s 调度 tick 抢执行权，与
// redeliveryLoop 同款独立 goroutine，共用 cancel。启动即先扫一轮（进程刚
// 起来时最需要收敛停机期间遗留的孤儿字节）。
func (s *ReviewService) assetRetentionLoop(cancel <-chan struct{}) {
	ticker := time.NewTicker(reviewAssetSweepEvery)
	defer ticker.Stop()
	s.sweepAssetRetention(time.Now().UTC())
	for {
		select {
		case <-cancel:
			return
		case now := <-ticker.C:
			s.sweepAssetRetention(now.UTC())
		}
	}
}

// sweepAssetRetention 一轮保留策略扫描：先快照存活行的 storage_path 集合，
// 再退役超龄行，再按**退役前**的快照回收无引用字节——本轮被退役行释放的
// 字节下一轮再收（宽限从退役时刻起算，R5 审查意见）：退役守卫只在 DELETE
// 一刻判定，若快照在删除之后，删除与快照之间新建的运行会在同轮失去文件。
// 行退役失败或引用集合读失败时直接返回，不动文件——账不确定时宁可保守
// （下轮重试）。
func (s *ReviewService) sweepAssetRetention(now time.Time) {
	// 引用集合先于退役快照（含即将退役的行）：本轮释放的字节留给下一轮，
	// 关闭 DELETE 与快照之间的新建运行丢失窗口；代价是退役释放的字节晚一
	// 个扫描周期回收，探测/崩溃残留的真孤儿不受影响（任何快照里都没有它
	// 们，同轮即收）。
	var paths []string
	if err := s.db.Model(&model.ReviewAsset{}).Where("storage_path <> ''").
		Distinct().Pluck("storage_path", &paths).Error; err != nil {
		s.logger.Warn("截图资产引用集合读取失败", zap.Error(err))
		return
	}
	referenced := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		referenced[p] = struct{}{}
	}

	// 行退役：跳过仍有非终态运行在身的 head。重开/手动重跑/评论唤醒可能重新
	// 审查一个超龄 head，而命中行不刷新 created_at（AgentScreenshotImage 直
	// 接读 storage_path，不经缓存面）——无守卫的话，在途运行会在两次扫描之
	// 间失去行与文件（取图 404、截图列表变空），ensureAssets 的复核发生在
	// 删除之前救不回来。运行进终态后，下一轮恢复按年龄退役。
	// 分批执行：每批先 SELECT 待退役行（走 idx_review_assets_created_at 定
	// 位、守卫走 idx_review_runs_asset_head），再按 id DELETE，批间释放
	// SQLite 唯一写锁——长时间停机后待退役行最多，单条大 DELETE 会把调度/
	// webhook 写排队到 busy_timeout 超时（INT-184 R2 审查实测）。批失败即
	// 停：引用集合已是退役前快照，账与快照一致，本轮不做文件回收（下轮重
	// 试）。
	retired := int64(0)
	// 非终态集合从 model 常量拼接：新增非终态状态时不会在 SQL 字面量里静
	// 默漂移（方向审计建议）。常量无注入面。
	activeStatusLiterals := []string{
		"'" + string(model.ReviewRunQueued) + "'",
		"'" + string(model.ReviewRunDispatched) + "'",
		"'" + string(model.ReviewRunRunning) + "'",
	}
	selectSQL := fmt.Sprintf(`SELECT a.id, a.storage_path FROM review_assets a
		WHERE a.created_at < ?
			AND NOT EXISTS (
				SELECT 1 FROM review_runs r
				WHERE r.integration_id = a.integration_id
					AND r.repo_full_name = a.repo_full_name
					AND r.pr_number = a.pr_number
					AND r.head_sha = a.head_sha
					AND r.status IN (%s)
			)
		LIMIT ?`, strings.Join(activeStatusLiterals, ", "))
	for {
		// 先选再删：退役时对释放的字节做一次 mtime 续期（os.Chtimes 到本轮
		// 扫描时刻），让「宽限从退役时刻起算」真正成立——否则旧 mtime 在下
		// 一轮扫描即判超宽限，退役后新建的排队运行若 6h 内没调
		// AgentScreenshots 就会在开始前丢文件（R6 审查意见）；运行在宽限内
		// 启动则 ensureAssets 缓存命中 touch 续保。
		var batch []model.ReviewAsset
		if err := s.db.Raw(selectSQL, now.Add(-reviewAssetRetention), reviewAssetRetireBatch).
			Scan(&batch).Error; err != nil {
			s.logger.Warn("截图资产行退役失败", zap.Error(err))
			return
		}
		if len(batch) == 0 {
			break
		}
		ids := make([]string, len(batch))
		paths := make([]string, 0, len(batch))
		for i := range batch {
			ids[i] = batch[i].ID
			// 续期在 DELETE 之前：多 touch 无害（行若因批失败保留，宽限从本
			// 轮起算依然正确），崩溃方向安全——先删行后 touch 被杀会留下旧
			// mtime，下一轮 6h 即判超宽限（R7 审查意见）。
			if batch[i].StoragePath != "" {
				paths = append(paths, batch[i].StoragePath)
				if cerr := os.Chtimes(batch[i].StoragePath, now, now); cerr != nil {
					s.logger.Warn("退役续期失败，文件将按旧 mtime 进入宽限判定",
						zap.String("path", batch[i].StoragePath), zap.Error(cerr))
				}
			}
		}
		if err := s.db.Where("id IN ?", ids).Delete(&model.ReviewAsset{}).Error; err != nil {
			s.logger.Warn("截图资产行退役失败", zap.Error(err))
			return
		}
		retired += int64(len(batch))
		if len(batch) < reviewAssetRetireBatch {
			break
		}
	}

	// 下载面未配置时没有文件可回收，行退役照常推进（失败行与成功行的账都
	// 在这张表）。
	if s.media == nil {
		if retired > 0 {
			s.logger.Info("截图资产行退役", zap.Int64("retired_rows", retired))
		}
		return
	}
	sweeper, ok := s.media.(reviewAssetSweeper)
	if !ok {
		return
	}
	// 同源自检（R7 审查）：引用快照与缓存目录失配时，「目录里全是孤儿」的
	// 推断会整批删除仍有归属的历史截图——备份恢复（库回到旧时点、目录还
	// 在新状态）、存储迁移（行路径全在旧根）、新实例挂旧卷都会触发。三档
	// 失配改判限量：每轮最多回收 reviewAssetSweepCalmBatch 个文件并 warn
	// 留痕——误删从一次清空降为缓慢滴漏，运维看到 warn 可介入；正常排水
	// 的尾巴（全部行退役后的最后一批文件）也走这条限量路径，收敛变慢但
	// 不永久遗留。
	dirCandidates := sweeper.CountCacheEntries()
	underCacheDir := 0
	for p := range referenced {
		if strings.HasPrefix(p, sweeper.CacheDir()+string(os.PathSeparator)) {
			underCacheDir++
		}
	}
	budget := dirCandidates
	calmReason := ""
	switch {
	case dirCandidates == 0:
		// 目录无候选，无需限量。
	case len(referenced) == 0:
		calmReason = "快照为空而缓存目录有条目"
		budget = reviewAssetSweepCalmBatch
	case underCacheDir == 0:
		calmReason = "快照路径均不在本缓存目录下"
		budget = reviewAssetSweepCalmBatch
	case dirCandidates > reviewAssetSweepDirFloor && dirCandidates > len(referenced)*reviewAssetSweepMismatchFactor:
		calmReason = "目录候选数远大于快照规模"
		budget = reviewAssetSweepCalmBatch
	}
	if calmReason != "" {
		s.logger.Warn("截图资产回收进入限量模式（快照与缓存目录疑似不同源）",
			zap.String("reason", calmReason), zap.Int("dir_candidates", dirCandidates),
			zap.Int("referenced", len(referenced)), zap.Int("under_cache_dir", underCacheDir),
			zap.Int("budget", budget))
	}
	// allowed 记「已允许删除」的候选：sweeper 会对同一候选重问（未引用复
	// 核），避免预算重复扣减与重复 DB 查询。
	allowed := map[string]bool{}
	removed := sweeper.SweepUnreferencedEntries(func(binPath string) bool {
		if _, ok := referenced[binPath]; ok {
			return true
		}
		if allowed[binPath] {
			return false
		}
		if budget <= 0 {
			// 限量预算用尽：剩余候选一律按有引用跳过，下轮再评估。
			return true
		}
		// 快照后插入的行（探测/ensure 落账都发生在快照之后）快照看不见；
		// 命中即 touch 失败（只读盘/NFS）时 mtime 宽限也失效——候选删除前
		// 做一次真实 DB 复核作最终闸（R6 审查意见）。候选稀少，无索引全
		// 表扫描的代价可忽略。
		var live int64
		if err := s.db.Model(&model.ReviewAsset{}).Where("storage_path = ?", binPath).
			Limit(1).Count(&live).Error; err != nil {
			// 查询失败按有引用保守放行（fail-closed，与上方 Pluck 失败直接
			// return 同口径）：不能把 DB 瞬时错误当「无引用」放行给删除
			// （R7 审查意见）。
			s.logger.Warn("截图资产引用复核失败，候选按有引用跳过",
				zap.String("path", binPath), zap.Error(err))
			return true
		}
		if live > 0 {
			return true
		}
		allowed[binPath] = true
		budget--
		return false
	}, reviewAssetSweepGrace, now)
	if retired > 0 || removed > 0 {
		s.logger.Info("截图资产保留策略扫描",
			zap.Int64("retired_rows", retired), zap.Int("removed_files", removed))
	}
}
