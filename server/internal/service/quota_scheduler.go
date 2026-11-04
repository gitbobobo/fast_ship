package service

import (
	"context"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

// QuotaScheduler 是额度抓取的后台循环（INT-109）：每轮抓 next_retry_at 到期
// （或为空）的账号，并发受 max_concurrency 限制，单账号失败不影响其它账号。
// 下一次唤醒取「now + interval」与最早的 next_retry_at 的较小值——失败退避
// 到期与窗口重置预抓都按自己的时刻唤醒；到期行把等待压到保底 1 秒，
// kick 已消费但重算落在到期之后的调度抖动会在下一轮自愈（INT-194）。
type QuotaScheduler struct {
	svc    *QuotaService
	repo   *repository.QuotaAccountRepository
	cfg    *config.Config
	logger *zap.Logger
	now    func() time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	// kickCh 缓冲 1 的立即重算：HTTP 侧新建/换凭据/手动刷新会在调度器睡眠期间
	// 写入更早的 next_retry_at，kick 让循环重算等待而不必等满原来的定时器。
	kickCh chan struct{}
}

func NewQuotaScheduler(svc *QuotaService, repo *repository.QuotaAccountRepository, cfg *config.Config, logger *zap.Logger) *QuotaScheduler {
	return &QuotaScheduler{
		svc:    svc,
		repo:   repo,
		cfg:    cfg,
		logger: logger,
		now:    time.Now,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		kickCh: make(chan struct{}, 1),
	}
}

// Kick 非阻塞请求重新计算下一次等待（缓冲 1，已挂起时合并）。
func (s *QuotaScheduler) Kick() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

// Start 启动调度循环；run_on_startup 为真时先同步跑一轮（在后台 goroutine 里）。
func (s *QuotaScheduler) Start() {
	go s.loop()
}

// Stop 停止调度并等循环退出；进行中的单次抓取不打断（进程退出时由调用方兜底）。
func (s *QuotaScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	<-s.doneCh
}

func (s *QuotaScheduler) loop() {
	defer close(s.doneCh)
	// stalled：上一轮未能消化到期账号（ListDue 出错或写回落库失败，见
	// runRound）。该状态下唤醒保底抬到 stalledWakeFloor；任何一轮恢复消化
	// 即回落 1 秒抖动自愈节奏。
	var stalled bool
	if s.cfg.Quota.RunOnStartup {
		stalled = s.runRound(s.now().UTC())
	}
	for {
		timer := time.NewTimer(s.waitDuration(s.now().UTC(), stalled))
		select {
		case <-s.stopCh:
			timer.Stop()
			return
		case <-s.kickCh:
			// 有账号的 next_retry_at 变了（新建/换凭据/手动刷新），重算等待。
			timer.Stop()
			continue
		case t := <-timer.C:
			stalled = s.runRound(t.UTC())
		}
	}
}

// nextWait 算距下一轮的基础等待：常规 interval 与最早的 next_retry_at 取小——
// 未来值提前唤醒，NULL 行与到期行压成非正数后取保底 1 秒。保底本身就是
// 防忙循环的直接保障；「重算时账号已到期」的调度抖动同样在下一轮被
// ListDue 拾起。持续失败（写回落库失败、查询失败）由 waitDuration 的
// stalled 底座放慢节奏，不靠本保底。
func (s *QuotaScheduler) nextWait(now time.Time) time.Duration {
	wait := s.cfg.Quota.Interval()
	if earliest, ok, err := s.repo.EarliestNextRetryAt(); err == nil && ok {
		if d := earliest.Sub(now); d < wait {
			wait = d
		}
	} else if err != nil {
		s.logger.Warn("查询最早 next_retry_at 失败，按常规间隔等待", zap.Error(err))
	}
	if wait <= 0 {
		wait = time.Second
	}
	return wait
}

// stalledWakeFloor 是「上一轮未能消化到期账号」状态下的唤醒保底：ListDue
// 出错，或抓取写回落库失败让行仍处到期时，真实供应商抓取不能被压到
// 1 秒节奏——否则磁盘满、文件锁等持久化故障会让调度器以约 1 Hz 外呼
// 供应商（较常规间隔快数百倍），有触发限流的风险。上限定为常规 interval。
const stalledWakeFloor = 30 * time.Second

// waitDuration 在 nextWait 基础上应用 stalled 底座：上一轮未能把到期账号
// 清零（ListDue 出错或写回落库失败）时，等待抬到 min(stalledWakeFloor,
// interval)；任何一轮恢复消化（写回成功、账号离开到期集合）即回落 1 秒
// 抖动自愈节奏。
func (s *QuotaScheduler) waitDuration(now time.Time, stalled bool) time.Duration {
	wait := s.nextWait(now)
	if stalled {
		if floor := min(stalledWakeFloor, s.cfg.Quota.Interval()); wait < floor {
			wait = floor
		}
	}
	return wait
}

// runRound 抓一轮到期账号：并发上限内各账号独立抓取，单账号失败互不影响。
// 返回本轮是否未能消化到期账号：ListDue 出错，或轮末复查发现本轮该抓的
// 账号仍到期且 next_retry_at 与轮初快照相同（值未变才是本轮写回失败留下
// 的未消化；轮内被外部接管的行——换凭据重置回 NULL 等——值已变化，
// 不算），驱动 loop 在持续故障状态抬高唤醒保底（见 stalledWakeFloor）。
func (s *QuotaScheduler) runRound(now time.Time) bool {
	due, err := s.repo.ListDue(now)
	if err != nil {
		s.logger.Error("查询到期额度账号失败", zap.Error(err))
		return true
	}
	if len(due) == 0 {
		return false
	}
	dueIDs := make([]string, len(due))
	retrySnapshot := make(map[string]*time.Time, len(due))
	for i := range due {
		dueIDs[i] = due[i].ID
		retrySnapshot[due[i].ID] = due[i].NextRetryAt
	}
	sem := make(chan struct{}, s.cfg.Quota.MaxConcurrencyV())
	var wg sync.WaitGroup
	for i := range due {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.svc.FetchAccountIfDue(context.Background(), accountID, now)
		}(due[i].ID)
	}
	wg.Wait()
	// 轮末复查：本轮该抓的账号正常都被写回推到未来。仍到期、且快照值
	// 未变的行才是本轮写回失败留下的「未消化」；被外部接管的行值已变化。
	// 不用 updated_at 比较：gorm 自动时间戳按本地时区偏移写文本，与 UTC
	// 参数跨偏移做文本比较不可靠。
	remaining, err := s.repo.ListDueAmong(dueIDs, now)
	if err != nil {
		s.logger.Warn("轮末复查失败，按未消化处理", zap.Error(err))
		return true
	}
	for i := range remaining {
		snap := retrySnapshot[remaining[i].ID]
		cur := remaining[i].NextRetryAt
		if (snap == nil && cur == nil) || (snap != nil && cur != nil && snap.Equal(*cur)) {
			return true
		}
	}
	return false
}
