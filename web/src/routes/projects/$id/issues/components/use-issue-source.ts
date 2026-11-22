import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import {
  buildBoardSourceSearchParams,
  buildIssueDetailSearchParams,
  type IssueSourceContext,
} from "@/lib/issue-list-context";
import { useInfiniteIssueList } from "@/lib/hooks/use-issues";

/** 来源列表条目；stale 表示当前问题曾出现在列表、后被查询移出，保留位置与身份 */
export interface IssueSourceEntry {
  issue: Issue;
  stale?: boolean;
}

/**
 * 当前问题锚点：渲染列表中的前后邻居身份（beforeId = 后继、afterId = 前驱，边界为
 * null）。不用原始下标：多个保留项的记录先后发生，下标会随移出互相挤压而错位；
 * 双邻居在重建时迭代解析，仍能保持各项的相对顺序。scopeKey（项目+来源筛选）用于
 * 来源/项目切换瞬间的竞态防护。
 */
interface IssueAnchor {
  issue: Issue;
  beforeId: string | null;
  afterId: string | null;
  scopeKey: string;
}

/** 已被查询移出的保留项：集合，跨 iid 切换持续到来源/项目变化或卸载；邻居语义同 IssueAnchor */
interface RetainedIssue {
  issue: Issue;
  beforeId: string | null;
  afterId: string | null;
}

/** 保留集合的空态常量：保证引用稳定，避免 reset 效果在挂载时触发多余重渲染 */
const NO_RETAINED: RetainedIssue[] = [];

/**
 * 详情页来源列表控制器：同一来源下跨 iid 变更持有状态（不随左栏开关卸载）。
 * - 当前问题尚未出现在已加载页时按顺序补拉，直到出现或没有更多页（补拉失败时
 *   停止自动请求，停留在手动重试状态）；
 * - 当前问题曾出现后被查询移出时，按最后已知位置插入保留项（stale）：位置锚是
 *   移出前的后继真实条目 id（beforeId），多个保留项沿链解析插入，相对顺序与移出
 *   前一致；任何保留项重新命中查询即从集合移除（真实条目让位）。保留项是集合，
 *   与当前问题锚点分开维护，跨 iid 切换持续到来源/项目变化或卸载；
 * - J/K 按列表顺序翻页，末项且有下一页时先补拉再自动前进（补拉期间防重复跳转）；
 * - 点击与翻页均为 replace 导航：保留来源参数，清掉上一问题的 comments_page/
 *   timeline_page/hash；首次进入详情的 push 由来源页负责。
 * - 来源或项目变化时清空保留项与补拉状态；卸载详情路由时随组件销毁。
 */
export function useIssueSourceList(options: {
  projectId: string;
  context: IssueSourceContext;
  currentIssueId: string;
  /** 当前问题所属项目；未加载时为 undefined，视为可能属于来源项目 */
  currentIssueProjectId?: string;
}) {
  const { projectId, context, currentIssueId, currentIssueProjectId } = options;
  const navigate = useNavigate();

  const enabled =
    context.kind !== "none" &&
    (currentIssueProjectId === undefined || currentIssueProjectId === projectId);

  const filters = useMemo(() => {
    if (context.kind === "board") {
      // 看板列固定 state=open，只用列/标签/来源；忽略混入的 q/sort
      return {
        state: "open",
        label: context.label || undefined,
        source: context.source || undefined,
        workflow_status: context.workflowStatus,
      };
    }
    if (context.kind === "list") {
      return {
        state: context.state || undefined,
        q: context.q || undefined,
        label: context.label || undefined,
        source: context.source || undefined,
        workflow_status: context.workflowStatus || undefined,
        sort: context.sort || "updated_desc",
      };
    }
    return null;
  }, [context]);

  // 隔离键：项目与来源筛选共同构成作用域；项目切换后旧锚点/旧保留集合一律失效
  const scopeKey = useMemo(
    () => JSON.stringify([projectId, filters]),
    [projectId, filters],
  );

  const query = useInfiniteIssueList(projectId, filters ?? {}, { enabled });
  const {
    data,
    isLoading,
    isError,
    refetch,
    fetchNextPage,
    hasNextPage,
    isFetching,
    isFetchingNextPage,
  } = query;

  // 跨页按 id 去重：数据在翻页间隙发生变化时，同一条目可能出现在相邻两页；
  // 重复条目会破坏保留项插入（同锚点组被插多次）与 J/K 定位
  const rawItems = useMemo(() => {
    if (!data) {
      return [];
    }
    const seen = new Set<string>();
    const items: Issue[] = [];
    for (const page of data.pages) {
      for (const item of page.items) {
        if (seen.has(item.id)) {
          continue;
        }
        seen.add(item.id);
        items.push(item);
      }
    }
    return items;
  }, [data]);

  const [retained, setRetained] = useState<RetainedIssue[]>(NO_RETAINED);
  const [anchor, setAnchor] = useState<IssueAnchor | null>(null);
  const [pendingAdvance, setPendingAdvance] = useState(false);
  /** 末项 J 触发补拉时的 items 长度；补拉成功但列表没增长（新增页全被去重/空页）时据此释放 J/K */
  const [advanceFromLength, setAdvanceFromLength] = useState<number | null>(null);
  /** 本次 pending 期间是否观察到过 isFetchingNextPage=true：区分「拉取已完成无增长」与「尚未开始/无法观测」 */
  const sawAdvanceFetchingRef = useRef(false);

  // 来源或项目变化时清空保留项与补拉状态。声明在锚点 effect 之前，同一提交内先
  // 清空状态；配合 scopeKey 校验挡住旧闭包对新作用域保留集合的写入
  useEffect(() => {
    setRetained(NO_RETAINED);
    setAnchor(null);
    setPendingAdvance(false);
    setAdvanceFromLength(null);
  }, [projectId, scopeKey]);

  // 任何保留项重新命中查询即从集合移除——真实条目让保留项让位，不只处理当前问题；
  // 集合为空时保持 NO_RETAINED 引用稳定，避免多余重渲染
  useEffect(() => {
    if (!enabled || retained.length === 0) {
      return;
    }
    const rawIds = new Set(rawItems.map((item) => item.id));
    const next = retained.filter((entry) => !rawIds.has(entry.issue.id));
    if (next.length !== retained.length) {
      setRetained(next);
    }
  }, [enabled, rawItems, retained]);

  // 保留项渲染列表：真实条目 + stale 保留项（按移出时的前后邻居迭代放置）。供锚点
  // 取渲染邻居（可含 stale 项）与展示使用
  const items = useMemo<IssueSourceEntry[]>(() => {
    const entries: IssueSourceEntry[] = rawItems.map((issue) => ({ issue }));
    const rawIds = new Set(entries.map((entry) => entry.issue.id));
    // 保留项按「双邻居身份」重建位置：优先插在后继（beforeId）之前，后继不可解析
    // （它也是保留项且尚未放置，或已不在列表）时改插在前驱（afterId）之后；两个
    // 邻居都不可解析时延迟到下一轮，多轮无进展则落到列表末尾。迭代放置使相邻保留
    // 项互为锚点时也能还原移出前的相对顺序（顺序/逆序移出均一致），且后加载的真实
    // 条目不会插到保留项之前（末条移出后的位置也稳定）。真实条目在场的保留项让位，
    // 天然去重且无重复渲染键。
    const result: IssueSourceEntry[] = [...entries];
    let pending: RetainedIssue[] = retained.filter(
      (entry) => !rawIds.has(entry.issue.id),
    );
    let progress = true;
    while (pending.length > 0 && progress) {
      progress = false;
      const remaining: RetainedIssue[] = [];
      for (const entry of pending) {
        const indexOf = (id: string | null) =>
          id === null ? -1 : result.findIndex((e) => e.issue.id === id);
        const beforeIndex = indexOf(entry.beforeId);
        if (beforeIndex >= 0) {
          result.splice(beforeIndex, 0, { issue: entry.issue, stale: true });
          progress = true;
          continue;
        }
        if (entry.afterId !== null) {
          const afterIndex = indexOf(entry.afterId);
          if (afterIndex >= 0) {
            result.splice(afterIndex + 1, 0, { issue: entry.issue, stale: true });
            progress = true;
            continue;
          }
        }
        remaining.push(entry);
      }
      pending = remaining;
    }
    // 两个邻居都无法解析（如邻居已被删除）：追加到列表末尾，保持可见与可翻页
    for (const entry of pending) {
      result.push({ issue: entry.issue, stale: true });
    }
    return result;
  }, [rawItems, retained]);

  // 当前问题命中查询时更新锚点（记录渲染列表中的前后邻居身份），脱离查询后按锚点
  // 移入保留集合。邻居取自渲染列表而非原始查询：两者之间可能夹着 stale 保留项，
  // 双邻居才能在逆序移出、末条移出后补页等时序下保住各项的相对顺序与位置。
  // scopeKey 校验防止来源/项目切换同一提交内的旧闭包锚点被误移入新作用域的保留集合。
  useEffect(() => {
    if (!enabled || !currentIssueId) {
      return;
    }
    const rawIndex = rawItems.findIndex((item) => item.id === currentIssueId);
    if (rawIndex >= 0) {
      const issue = rawItems[rawIndex];
      const renderedIndex = items.findIndex(
        (entry) => entry.issue.id === currentIssueId,
      );
      const beforeId =
        renderedIndex >= 0 && renderedIndex + 1 < items.length
          ? items[renderedIndex + 1].issue.id
          : null;
      const afterId =
        renderedIndex > 0 ? items[renderedIndex - 1].issue.id : null;
      setAnchor((prev) =>
        prev &&
        prev.issue === issue &&
        prev.beforeId === beforeId &&
        prev.afterId === afterId
          ? prev
          : { issue, beforeId, afterId, scopeKey },
      );
      return;
    }
    if (
      anchor &&
      anchor.issue.id === currentIssueId &&
      anchor.scopeKey === scopeKey
    ) {
      setRetained((prev) =>
        prev.some((entry) => entry.issue.id === currentIssueId)
          ? prev
          : [
              ...prev,
              {
                issue: anchor.issue,
                beforeId: anchor.beforeId,
                afterId: anchor.afterId,
              },
            ],
      );
      setAnchor(null);
    }
  }, [enabled, currentIssueId, rawItems, items, anchor, scopeKey]);

  // 当前问题尚未出现在已加载页时按顺序补拉（不能把「还没加载到」当「已移出筛选」）。
  // 已移出的保留项与锚定后移出的问题不再追拉剩余页——它们已被查询明确过滤。
  // 补拉失败（isError）时停止自动请求，避免失败后 hasNextPage 仍真导致请求循环；
  // refetch 成功后 isError 复位，自动补拉恢复。
  useEffect(() => {
    if (!enabled || isLoading || isFetching || isError) {
      return;
    }
    if (
      (currentIssueId && rawItems.some((item) => item.id === currentIssueId)) ||
      retained.some((entry) => entry.issue.id === currentIssueId) ||
      anchor?.issue.id === currentIssueId
    ) {
      return;
    }
    if (hasNextPage) {
      void fetchNextPage();
    }
  }, [
    enabled,
    isLoading,
    isFetching,
    isError,
    currentIssueId,
    rawItems,
    retained,
    anchor,
    hasNextPage,
    fetchNextPage,
  ]);

  const currentIndex = useMemo(
    () => items.findIndex((entry) => entry.issue.id === currentIssueId),
    [items, currentIssueId],
  );

  const navigateToIssue = useCallback(
    (target: Issue) => {
      let search = "";
      if (context.kind === "board") {
        search = buildBoardSourceSearchParams(context).toString();
      } else if (context.kind === "list") {
        // issue_page 跟随目标所在页，保证刷新后来源列表仍定位到同一页
        const pageIndex =
          data?.pages.findIndex((page) =>
            page.items.some((item) => item.id === target.id),
          ) ?? -1;
        search = buildIssueDetailSearchParams({
          state: context.state,
          q: context.q,
          label: context.label,
          source: context.source,
          workflowStatus: context.workflowStatus,
          sort: context.sort,
          page: pageIndex >= 0 ? pageIndex + 1 : context.page,
        }).toString();
      }
      navigate(
        {
          pathname: `/projects/${projectId}/issues/${target.id}`,
          search: search ? `?${search}` : "",
        },
        { replace: true },
      );
    },
    [context, data, navigate, projectId],
  );

  // 翻页（点击列表项）后放弃进行中的自动前进，避免与用户选择打架
  useEffect(() => {
    setPendingAdvance(false);
    setAdvanceFromLength(null);
  }, [currentIssueId]);

  const goToNext = useCallback(() => {
    if (!enabled || pendingAdvance || currentIndex < 0) {
      return;
    }
    if (currentIndex < items.length - 1) {
      navigateToIssue(items[currentIndex + 1].issue);
      return;
    }
    if (hasNextPage && !isFetchingNextPage) {
      setAdvanceFromLength(items.length);
      setPendingAdvance(true);
      void fetchNextPage();
    }
  }, [
    enabled,
    pendingAdvance,
    currentIndex,
    items,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
    navigateToIssue,
  ]);

  const goToPrevious = useCallback(() => {
    if (!enabled || pendingAdvance || currentIndex <= 0) {
      return;
    }
    navigateToIssue(items[currentIndex - 1].issue);
  }, [enabled, pendingAdvance, currentIndex, items, navigateToIssue]);

  // 末项补拉完成后自动前进；补拉失败（isError）、没有更多页、或补拉完成后列表没
  // 增长（新增页全部被跨页去重/空页）时放弃，恢复 J/K 供用户重试或反向移动。
  // 无增长释放只在观察到过 isFetchingNextPage=true（拉取确已完成）后生效，
  // 避免拉取进行中就被误判为无增长而提前释放
  useEffect(() => {
    if (!pendingAdvance) {
      return;
    }
    if (isFetchingNextPage) {
      sawAdvanceFetchingRef.current = true;
      return;
    }
    if (currentIndex >= 0 && currentIndex < items.length - 1) {
      setPendingAdvance(false);
      setAdvanceFromLength(null);
      sawAdvanceFetchingRef.current = false;
      navigateToIssue(items[currentIndex + 1].issue);
    } else if (
      !hasNextPage ||
      isError ||
      (sawAdvanceFetchingRef.current &&
        advanceFromLength !== null &&
        items.length <= advanceFromLength)
    ) {
      setPendingAdvance(false);
      setAdvanceFromLength(null);
      sawAdvanceFetchingRef.current = false;
    }
  }, [
    pendingAdvance,
    isFetchingNextPage,
    currentIndex,
    items,
    hasNextPage,
    isError,
    advanceFromLength,
    navigateToIssue,
  ]);

  // 来源总数取接口首页的 total（服务端过滤后的全量计数），供头部「已加载 / 总数」展示；
  // 未加载到数据（加载中/失败）时为 null，由调用方用「—」占位
  const total = data?.pages[0]?.total ?? null;

  return {
    items,
    currentIndex,
    total,
    isLoading,
    isError,
    refetch,
    hasNextPage,
    fetchNextPage,
    isFetching,
    isFetchingNextPage,
    isAdvancing: pendingAdvance,
    goToNext,
    goToPrevious,
    goToIssue: navigateToIssue,
  };
}
