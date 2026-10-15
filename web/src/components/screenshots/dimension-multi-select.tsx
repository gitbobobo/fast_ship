import { Filter } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";
import {
  dimensionLabel,
  toggleDimensionValue,
  type DimensionOption,
} from "@/lib/screenshots";

interface DimensionMultiSelectProps {
  /** 维度显示名（平台 / 语言 / 亮度） */
  label: string;
  /** 数据推导出的选项（含未标注桶与计数） */
  options: DimensionOption[];
  /** 当前选中值（含 __none__ 桶）；空数组 = 不过滤 */
  value: string[];
  onChange: (next: string[]) => void;
}

/** 筛选条上的维度多选：勾选即过滤（OR 语义），全部取消 = 不过滤 */
export function DimensionMultiSelect({
  label,
  options,
  value,
  onChange,
}: DimensionMultiSelectProps) {
  const active = value.length > 0;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            size="sm"
            variant={active ? "secondary" : "outline"}
            className="h-8 max-w-40"
            data-testid={`filter-${label}`}
            aria-pressed={active}
          >
            <Filter className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">
              {label}
              {active && `：${value.map(dimensionLabel).join("、")}`}
            </span>
          </Button>
        }
      />
      <DropdownMenuContent align="start" className="max-h-72 overflow-y-auto">
        <DropdownMenuGroup>
          <DropdownMenuLabel className="text-xs text-muted-foreground">
            {label}（多选，{value.length > 0 ? "已筛选" : "未筛选"}）
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          {options.length === 0 ? (
            <p className="px-2 py-3 text-center text-xs text-muted-foreground">
              暂无可选项
            </p>
          ) : (
            options.map((option) => (
              <DropdownMenuCheckboxItem
                key={option.value}
                checked={value.includes(option.value)}
                closeOnClick={false}
                onCheckedChange={() =>
                  onChange(toggleDimensionValue(value, option.value))
                }
              >
                <span
                  className={cn(
                    "min-w-0 flex-1 truncate",
                    option.value === "__none__" && "text-muted-foreground",
                  )}
                >
                  {dimensionLabel(option.value)}
                </span>
                <span className="ml-auto text-xs text-muted-foreground tabular-nums">
                  {option.count}
                </span>
              </DropdownMenuCheckboxItem>
            ))
          )}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
