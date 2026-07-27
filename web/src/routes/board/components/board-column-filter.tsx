import { Filter } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  type IssueSourceFilter,
  ISSUE_SOURCE_FILTER_OPTIONS,
} from "@/lib/issue-source";
import { cn } from "@/lib/utils";

export interface BoardColumnFilterValue {
  label: string;
  source: IssueSourceFilter;
}

export interface BoardColumnFilterProps {
  labels: string[];
  value: BoardColumnFilterValue;
  onChange: (next: BoardColumnFilterValue) => void;
  disabled?: boolean;
}

export const DEFAULT_BOARD_COLUMN_FILTER: BoardColumnFilterValue = {
  label: "",
  source: "all",
};

export function BoardColumnFilter({
  labels,
  value,
  onChange,
  disabled,
}: BoardColumnFilterProps) {
  const isActive = value.label !== "" || value.source !== "all";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        disabled={disabled}
        aria-label="筛选"
        className={cn(
          "relative inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-hidden focus-visible:ring-1 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
          isActive
            ? "bg-primary/10 text-primary"
            : "text-muted-foreground",
        )}
      >
        <Filter className="h-3.5 w-3.5" />
        {isActive && (
          <span
            aria-hidden
            className="absolute right-1 top-1 h-1.5 w-1.5 rounded-full bg-primary"
          />
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        <DropdownMenuGroup>
          <DropdownMenuLabel>来源</DropdownMenuLabel>
          <DropdownMenuRadioGroup
            value={value.source}
            onValueChange={(next) =>
              onChange({ ...value, source: next as IssueSourceFilter })
            }
          >
            {ISSUE_SOURCE_FILTER_OPTIONS.map((opt) => (
              <DropdownMenuRadioItem key={opt.value} value={opt.value}>
                {opt.label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuLabel>标签</DropdownMenuLabel>
          <DropdownMenuRadioGroup
            value={value.label}
            onValueChange={(next) => onChange({ ...value, label: next })}
          >
            <DropdownMenuRadioItem value="">全部标签</DropdownMenuRadioItem>
            {labels.map((label) => (
              <DropdownMenuRadioItem key={label} value={label}>
                {label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() => onChange(DEFAULT_BOARD_COLUMN_FILTER)}
        >
          清除筛选
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
