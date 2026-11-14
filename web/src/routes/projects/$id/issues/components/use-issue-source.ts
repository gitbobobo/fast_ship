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
 * 当前问题锚点：查询命中时的「后继邻居」身份（beforeId = 后继真实条目 id，列表末尾为
 * null）。不用原始下标：多个保留项的记录先后发生，下标会随移出互相挤压而错位；
 * 邻居身份在重建时按链解析，仍能保持各保留项彼此的相对顺序。scopeKey（项目+来源
 * 筛选）用于来源/项目切换瞬间的竞态防护。
 */
interface IssueAnchor {
  issue: Issue;
  beforeId: string | null;
  scopeKey: string;
}

/** 已被查询移出的保留项：集合，跨 iid 切换持续到来源/项目变化或卸载；beforeId 语义同 IssueAnchor */
interface RetainedIssue {
  issue: Issue;
  beforeId: string | null;
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

  // 保留项渲染列表：真实条目 + stale 保留项（按 beforeId 链解析插入）。供锚点取
  // 渲染后继（可含 stale 项）与展示使用
  const items = useMemo<IssueSourceEntry[]>(() => {
    const entries: IssueSourceEntry[] = rawItems.map((issue) => ({ issue }));
    const rawIds = new Set(entries.map((entry) => entry.issue.id));
    // 保留项按「后继邻居身份」重建位置：先沿 beforeId 链解析到第一个真实条目锚点
    // （后继本身也被保留时继续向后），链尾为空、锚点已不在列表也未入保留集合或异常
    // 成环时落到列表末尾；再按锚点分组插入。组内按链深降序稳定排序：先移出的项
    // 链上经过的保留项更多、离锚更远、应排在前——逆序移出时仍保持移出前的相对顺序
    // （顺序移出与逆序移出两种时序都还原为原列表顺序）。真实条目在场的保留项
    // 让位，天然去重且无重复渲染键。
    const retainedById = new Map(
      retained.map((entry) => [entry.issue.id, entry]),
    );
    const resolved = new Map<
      string,
      { key: string | null; depth: number }
    >();
    for (const entry of retained) {
      if (rawIds.has(entry.issue.id)) {
        continue;
      }
      let key = entry.beforeId;
      let depth = 0;
      const visited = new Set<string>([entry.issue.id]);
      while (key !== null && !rawIds.has(key)) {
        const next = retainedById.get(key);
        if (!next || visited.has(key)) {
          key = null;
          break;
        }
        visited.add(key);
        key = next.beforeId;
        depth += 1;
      }
      resolved.set(entry.issue.id, { key, depth });
    }
    const groups = new Map<string | null, RetainedIssue[]>();
    for (const entry of retained) {
      const info = resolved.get(entry.issue.id);
      if (!info) {
        continue;
      }
      const group = groups.get(info.key);
      if (group) {
        group.push(entry);
      } else {
        groups.set(info.key, [entry]);
      }
    }
    for (const group of groups.values()) {
      // 稳定排序：同链深时保持移出顺序
      group.sort((a, b) => {
        const depthA = resolved.get(a.issue.id)?.depth ?? 0;
        const depthB = resolved.get(b.issue.id)?.depth ?? 0;
        return depthB - depthA;
      });
    }
    const result: IssueSourceEntry[] = [];
    for (const entry of entries) {
      const group = groups.get(entry.issue.id);
      if (group) {
        for (const item of group) {
          result.push({ issue: item.issue, stale: true });
        }
      }
      result.push(entry);
    }
    const tail = groups.get(null);
    if (tail) {
      for (const item of tail) {
        result.push({ issue: item.issue, stale: true });
      }
    }
    return result;
  }, [rawItems, retained]);

  // 当前问题命中查询时更新锚点（记录后继邻居身份），脱离查询后按锚点移入保留集合。
  // 后继取自渲染列表而非原始查询：两者之间可能夹着 stale 保留项，逆序移出时
  // 只有渲染后继能保住各项的相对顺序。scopeKey 校验防止来源/项目切换同一提交内
  // 的旧闭包锚点被误移入新作用域的保留集合。
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
      setAnchor((prev) =>
        prev && prev.issue === issue && prev.beforeId === beforeId
          ? prev
          : { issue, beforeId, scopeKey },
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
          : [...prev, { issue: anchor.issue, beforeId: anchor.beforeId }],
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

  return {
    items,
    currentIndex,
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
