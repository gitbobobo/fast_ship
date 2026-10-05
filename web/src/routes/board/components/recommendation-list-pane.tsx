import type { Ref } from "react";
import { cn } from "@/lib/utils";

type Priority = IssueRecommendation["priority"];

/** 优先级分组顺序；组内条目保持服务端返回的 updated_at 降序。 */
export const RECOMMENDATION_PRIORITY_GROUPS: {
  priority: Priority;
  label: string;
  dot: string;
}[] = [
  { priority: "high", label: "高优先级", dot: "bg-destructive" },
  { priority: "medium", label: "中优先级", dot: "bg-amber-500" },
  { priority: "low", label: "低优先级", dot: "bg-muted-foreground/50" },
];

interface RecommendationListPaneProps {
  /** 已按分组顺序排列的条目（弹框选中序与展示序一致）。 */
  items: IssueRecommendation[];
  selectedIssueId: string | null;
  onSelect: (issueId: string) => void;
  /** 滚动容器 ref，供键盘选中后 scrollIntoView。 */
  listRef?: Ref<HTMLDivElement>;
  /** 跨项目（全量推荐）时在行尾补项目名。 */
  showProject?: boolean;
}

/**
 * 推荐弹框左栏：优先级分组的单行列表，组标题吸顶。
 * 点击行即选中（不再跳转详情页），选中态由父组件持有。
 */
export function RecommendationListPane({
  items,
  selectedIssueId,
  onSelect,
  listRef,
  showProject = false,
}: RecommendationListPaneProps) {
  return (
    <div
      ref={listRef}
      className="max-h-56 overflow-y-auto border-b pb-1 sm:max-h-none sm:border-r sm:border-b-0"
    >
      {RECOMMENDATION_PRIORITY_GROUPS.map((group) => {
        const groupItems = items.filter(
          (item) => item.priority === group.priority,
        );
        if (groupItems.length === 0) return null;
        return (
          <section key={group.priority} aria-label={group.label}>
            <h3 className="sticky top-0 z-10 flex items-center gap-2 bg-popover px-5 pt-3 pb-1.5 text-xs font-medium text-muted-foreground">
              <span className={cn("h-1.5 w-1.5 rounded-full", group.dot)} />
              {group.label}
              <span className="tabular-nums">{groupItems.length}</span>
            </h3>
            <ul>
              {groupItems.map((item) => {
                const selected = item.issue.id === selectedIssueId;
                return (
                  <li key={item.issue.id}>
                    <button
                      type="button"
                      data-issue-id={item.issue.id}
                      aria-current={selected ? "true" : undefined}
                      title={item.issue.title}
                      onMouseDown={(event) => event.preventDefault()}
                      onClick={() => onSelect(item.issue.id)}
                      className={cn(
                        "flex w-full items-center gap-2 px-5 py-2 text-left text-sm transition-colors",
                        selected
                          ? "bg-accent font-medium text-foreground"
                          : "text-foreground/80 hover:bg-muted/60",
                      )}
                    >
                      <span className="shrink-0 font-mono text-xs text-muted-foreground">
                        {item.issue.reference}
                      </span>
                      <span className="min-w-0 flex-1 truncate">
                        {item.issue.title}
                      </span>
                      {showProject && item.issue.project_name ? (
                        <span className="shrink-0 text-[11px] text-muted-foreground">
                          {item.issue.project_name}
                        </span>
                      ) : null}
                    </button>
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
    </div>
  );
}
