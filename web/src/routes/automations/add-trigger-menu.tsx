import { useMemo, useState } from "react";
import {
  Clock,
  FilePlus2,
  GitPullRequest,
  GitPullRequestArrow,
  Plus,
  RefreshCw,
  Search,
  Tag,
  type LucideIcon,
} from "lucide-react";

import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import type { AutomationTriggerType } from "@/lib/api/automations";
import { automationTriggerLabel } from "@/lib/api/automations";

// 「添加触发器」弹出菜单（INT-118）：搜索框 + 6 种触发器类型列表，
// 类型可重复添加（同类型多实例各自配置）。

const TRIGGER_MENU_ITEMS: { type: AutomationTriggerType; icon: LucideIcon }[] =
  [
    { type: "cron", icon: Clock },
    { type: "issue_created", icon: FilePlus2 },
    { type: "issue_status_changed", icon: RefreshCw },
    { type: "issue_label_added", icon: Tag },
    { type: "github_pr_opened", icon: GitPullRequest },
    { type: "github_pr_synchronize", icon: GitPullRequestArrow },
  ];

export function AddTriggerMenu({
  onAdd,
  disabled,
}: {
  onAdd: (type: AutomationTriggerType) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");

  const items = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return TRIGGER_MENU_ITEMS;
    return TRIGGER_MENU_ITEMS.filter((item) =>
      automationTriggerLabel(item.type).toLowerCase().includes(q),
    );
  }, [query]);

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setQuery("");
      }}
    >
      <PopoverTrigger
        className={cn(
          buttonVariants({ variant: "outline" }),
          "h-10 w-full justify-start border-dashed px-3 text-muted-foreground hover:text-foreground",
        )}
        disabled={disabled}
      >
        <Plus className="h-4 w-4" />
        添加触发器
      </PopoverTrigger>
      <PopoverContent className="w-72 overflow-hidden p-0" align="start">
        <div className="flex items-center gap-2 border-b border-border/70 px-2.5">
          <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
          <input
            type="text"
            aria-label="搜索触发器"
            placeholder="搜索触发器…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-9 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
        </div>
        <div className="p-1">
          {items.map((item) => {
            const label = automationTriggerLabel(item.type);
            const Icon = item.icon;
            return (
              <button
                key={item.type}
                type="button"
                className="flex w-full cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 text-left text-sm select-none hover:bg-accent"
                onClick={() => {
                  onAdd(item.type);
                  setOpen(false);
                  setQuery("");
                }}
              >
                <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                {label}
              </button>
            );
          })}
          {items.length === 0 && (
            <p className="px-2 py-6 text-center text-xs text-muted-foreground">
              无匹配触发器
            </p>
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
