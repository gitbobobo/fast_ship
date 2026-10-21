import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { ChevronRight, GitPullRequest, Settings2 } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { OptionSelect, withMissingOption } from "@/components/ui/option-select";
import { Skeleton } from "@/components/ui/skeleton";
import { Header } from "@/components/layout/header";
import { PrActivityDrawer } from "@/components/reviews/pr-activity-drawer";
import { ApiRequestError } from "@/lib/api/client";
import {
  reviewActivityStageLabel,
  reviewFindingsText,
  reviewStagesText,
  type ReviewPullRequestActivity,
} from "@/lib/api/reviews";
import {
  REVIEW_POLL_INTERVAL,
  useReviewProjects,
  useReviewPullRequests,
  useRerunReview,
} from "@/lib/hooks/use-reviews";
import {
  debounceRemainingSeconds,
  formatDate,
  formatElapsed,
  shortSha,
} from "@/lib/utils/format";
import { cn } from "@/lib/utils";

// 审查首页（INT-141）：跨项目 PR 动态。分组（进行中/需要处理/最近通过）由
// 服务端算好，前端只渲染。进行中 PR 存在时列表 10s 轮询（见 hook），行内
// 的已用时长/防抖倒计时每 10s 本地重算一次。

const ALL_PROJECTS = "all";
const PAGE_SIZE = 50;

export default function ReviewsPage() {
  const {
    data: projects,
    isLoading: projectsLoading,
    isError: projectsError,
    refetch: refetchProjects,
  } = useReviewProjects();
  const [searchParams, setSearchParams] = useSearchParams();
  const filter = searchParams.get("project") ?? ALL_PROJECTS;
  const projectId = filter === ALL_PROJECTS ? undefined : filter;
  // 固定页大小 + offset 分页（服务端一次最多 200 条）。切换项目时回到第一页。
  const [offset, setOffset] = useState(0);
  useEffect(() => setOffset(0), [filter]);
  const {
    data: activity,
    isLoading,
    isError,
    refetch,
  } = useReviewPullRequests(projectId, PAGE_SIZE, offset);
  // 只记选中的标识，列表轮询后用最新的 item 覆盖分组/阶段；条目暂时不在当前页
  // 时退回点击时的快照。
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [selectedSnapshot, setSelectedSnapshot] =
    useState<ReviewPullRequestActivity | null>(null);
  const [passedOpen, setPassedOpen] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  // 已用时长与防抖倒计时每 10s 重算（与轮询同节奏）。
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), REVIEW_POLL_INTERVAL);
    return () => clearInterval(timer);
  }, []);

  // 轮询期间总数可能变小（例如进行中的 PR 被取消后整行不再返回）。当前页
  // 越界时回退到最后一个有效页，避免停在永远空的一页、翻页控件也被隐藏。
  useEffect(() => {
    if (!activity) return;
    const maxOffset =
      activity.total > 0
        ? Math.floor((activity.total - 1) / PAGE_SIZE) * PAGE_SIZE
        : 0;
    if (offset > maxOffset) setOffset(maxOffset);
  }, [activity, offset]);

  const enabledProjects = (projects ?? []).filter((p) => p.enabled);
  const items = activity?.items ?? [];
  const inProgress = items.filter((i) => i.group === "in_progress");
  const needsAction = items.filter((i) => i.group === "needs_action");
  const passed = items.filter((i) => i.group === "recently_passed");
  const selected = selectedKey
    ? (items.find((i) => rowKey(i) === selectedKey) ?? selectedSnapshot)
    : null;
  const openItem = (item: ReviewPullRequestActivity) => {
    setSelectedKey(rowKey(item));
    setSelectedSnapshot(item);
  };
  const closeDrawer = () => {
    setSelectedKey(null);
    setSelectedSnapshot(null);
  };

  const loading = isLoading || projectsLoading;

  return (
    <div className="flex flex-col">
      <Header title="审查" />
      <div className="mx-auto w-full max-w-3xl space-y-4 p-4 md:p-6">
        <div className="flex items-center justify-between gap-3">
          <OptionSelect
            aria-label="按项目筛选"
            className="w-56"
            size="sm"
            value={filter}
            onValueChange={(next) => {
              const value = next ?? ALL_PROJECTS;
              setSearchParams(
                value === ALL_PROJECTS ? {} : { project: value },
              );
            }}
            options={withMissingOption(
              [
                { value: ALL_PROJECTS, label: "全部项目" },
                ...enabledProjects.map((p) => ({
                  value: p.project_id,
                  label: p.project_name,
                })),
              ],
              filter === ALL_PROJECTS ? null : filter,
              "已失效项目",
            )}
          />
          <Button
            variant="outline"
            size="sm"
            render={<Link to="/reviews/config" />}
          >
            <Settings2 className="mr-1.5 h-3.5 w-3.5" />
            配置（{enabledProjects.length} 个项目已开启）
          </Button>
        </div>

        {loading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-16 rounded-lg" />
            ))}
          </div>
        ) : projectsError ? (
          <LoadFailed text="项目列表加载失败" onRetry={() => void refetchProjects()} />
        ) : isError ? (
          <LoadFailed text="审查动态加载失败" onRetry={() => void refetch()} />
        ) : enabledProjects.length === 0 ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <GitPullRequest className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              还没有项目开启审查
            </p>
            <div className="mt-4">
              <Button size="sm" render={<Link to="/reviews/config" />}>
                前往配置
              </Button>
            </div>
          </div>
        ) : items.length === 0 ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <GitPullRequest className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              推送或打开 PR 后会出现在这里
            </p>
          </div>
        ) : (
          <>
            {inProgress.length > 0 && (
              <ActivitySection title="进行中">
                {inProgress.map((item) => (
                  <InProgressRow
                    key={rowKey(item)}
                    item={item}
                    now={now}
                    onOpen={() => openItem(item)}
                  />
                ))}
              </ActivitySection>
            )}

            {needsAction.length > 0 && (
              <ActivitySection title="需要处理">
                {needsAction.map((item) => (
                  <NeedsActionRow
                    key={rowKey(item)}
                    item={item}
                    onOpen={() => openItem(item)}
                  />
                ))}
              </ActivitySection>
            )}

            {passed.length > 0 && (
              <section className="space-y-2">
                <button
                  type="button"
                  aria-expanded={passedOpen}
                  onClick={() => setPassedOpen((open) => !open)}
                  className="flex items-center gap-1 text-sm font-medium text-muted-foreground hover:text-foreground"
                >
                  <ChevronRight
                    className={cn(
                      "h-4 w-4 transition-transform",
                      passedOpen && "rotate-90",
                    )}
                  />
                  最近通过
                </button>
                {passedOpen && (
                  <div className="space-y-2">
                    {passed.map((item) => (
                      <PassedRow
                        key={rowKey(item)}
                        item={item}
                        onOpen={() => openItem(item)}
                      />
                    ))}
                  </div>
                )}
              </section>
            )}

            {activity && activity.total > PAGE_SIZE && (
              <div className="flex items-center justify-between gap-2 pt-2 text-xs text-muted-foreground">
                <span>
                  第 {offset + 1}–
                  {Math.min(offset + items.length, activity.total)} 条 / 共{" "}
                  {activity.total} 条
                </span>
                <div className="flex gap-1.5">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={offset === 0}
                    onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
                  >
                    上一页
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={offset + PAGE_SIZE >= activity.total}
                    onClick={() => setOffset(offset + PAGE_SIZE)}
                  >
                    下一页
                  </Button>
                </div>
              </div>
            )}
          </>
        )}
      </div>

      <PrActivityDrawer item={selected} onClose={closeDrawer} />
    </div>
  );
}

function LoadFailed({ text, onRetry }: { text: string; onRetry: () => void }) {
  return (
    <div className="rounded-lg border bg-muted/40 py-12 text-center">
      <p className="text-sm text-muted-foreground">{text}</p>
      <div className="mt-4">
        <Button variant="outline" size="sm" onClick={onRetry}>
          重试
        </Button>
      </div>
    </div>
  );
}

function rowKey(item: ReviewPullRequestActivity): string {
  return `${item.project_id}/${item.repo_full_name}#${item.pr_number}`;
}

function ActivitySection({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-2">
      <h2 className="text-sm font-medium text-muted-foreground">{title}</h2>
      <div className="space-y-2">{children}</div>
    </section>
  );
}

function RowMeta({ item }: { item: ReviewPullRequestActivity }) {
  return (
    <>
      <span>
        {item.repo_full_name} #{item.pr_number}
      </span>
      {item.stage !== "debouncing" && <span>第 {item.round_number} 轮</span>}
      <span className="font-mono">{shortSha(item.head_sha)}</span>
    </>
  );
}

function InProgressRow({
  item,
  now,
  onOpen,
}: {
  item: ReviewPullRequestActivity;
  now: number;
  onOpen: () => void;
}) {
  const debouncing = item.stage === "debouncing";
  const remaining =
    debouncing && item.debounce_fire_at
      ? debounceRemainingSeconds(item.debounce_fire_at, now)
      : null;
  return (
    <button
      type="button"
      onClick={onOpen}
      className="block w-full rounded-lg border px-3 py-2.5 text-left transition-colors hover:bg-muted/40"
    >
      <span className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium">
          {item.pr_title}
        </span>
        <Badge variant="outline">{reviewActivityStageLabel(item.stage)}</Badge>
      </span>
      <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
        <RowMeta item={item} />
        <span>
          {remaining != null
            ? remaining > 0
              ? `约 ${remaining} 秒后开始`
              : "即将开始"
            : `已用 ${formatElapsed(item.started_at, now)}`}
        </span>
      </span>
      {item.stages.length > 0 && (
        <span className="mt-1 block text-xs text-muted-foreground">
          {reviewStagesText(item.stages)}
        </span>
      )}
    </button>
  );
}

function NeedsActionRow({
  item,
  onOpen,
}: {
  item: ReviewPullRequestActivity;
  onOpen: () => void;
}) {
  const rerun = useRerunReview(item.project_id);
  const findingsText = reviewFindingsText(item.findings);

  const handleRerun = async () => {
    try {
      const res = await rerun.mutateAsync({
        prNumber: item.pr_number,
        repoFullName: item.repo_full_name,
      });
      toast.success(`已受理，第 ${res.data.round_number} 轮审查已排队`);
    } catch (err) {
      if (err instanceof ApiRequestError && err.status === 404) {
        toast.error("重新审查暂不可用");
        return;
      }
      toast.error(
        err instanceof ApiRequestError && err.message
          ? err.message
          : "重新审查失败",
      );
    }
  };

  return (
    <div className="flex items-start gap-2 rounded-lg border px-3 py-2.5">
      <button
        type="button"
        onClick={onOpen}
        className="min-w-0 flex-1 text-left"
      >
        <span className="block truncate text-sm font-medium">
          {item.pr_title}
        </span>
        <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
          <RowMeta item={item} />
          <span>{formatDate(item.updated_at)}</span>
        </span>
        {findingsText && (
          <span className="mt-1 block text-xs text-destructive">
            {findingsText}
          </span>
        )}
        {item.issues.map((issue, i) => (
          <span key={i} className="mt-1 block text-xs text-destructive">
            {issue.combination ? `${issue.combination} · ` : ""}
            {issue.reason}
            {issue.message && issue.message !== issue.reason
              ? `：${issue.message}`
              : ""}
          </span>
        ))}
      </button>
      <Button
        variant="outline"
        size="sm"
        className="shrink-0"
        disabled={rerun.isPending || !item.can_rerun}
        onClick={() => void handleRerun()}
        title={
          item.can_rerun
            ? undefined
            : "该 PR 不属于项目当前审查仓库，不能重跑"
        }
        aria-label={`重新审查 PR #${item.pr_number}`}
      >
        重新审查
      </Button>
    </div>
  );
}

function PassedRow({
  item,
  onOpen,
}: {
  item: ReviewPullRequestActivity;
  onOpen: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="block w-full rounded-lg border px-3 py-2.5 text-left transition-colors hover:bg-muted/40"
    >
      <span className="block truncate text-sm font-medium">
        {item.pr_title}
      </span>
      <span className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
        <RowMeta item={item} />
        <span>{formatDate(item.updated_at)}</span>
      </span>
    </button>
  );
}
