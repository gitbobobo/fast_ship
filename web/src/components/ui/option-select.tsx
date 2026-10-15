import type { ReactNode } from "react";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

export interface OptionSelectOption {
  value: string;
  /** 收起态显示的标签，同时是下拉项的默认内容。 */
  label: ReactNode;
  disabled?: boolean;
  /** 下拉项内容与 label 不同时（如带图标、附加说明）单独提供。 */
  itemLabel?: ReactNode;
}

/**
 * 选项选择器：options 既渲染下拉项，也作为 Select 的 items 让收起态按 value
 * 查到标签。Base UI 的 Select 不传 items 且 SelectValue 无子内容时会直接显示
 * 内部 value，业务代码一律用本组件（由 fast-ship/no-raw-select 强制）。
 */
export function OptionSelect({
  options,
  value,
  onValueChange,
  placeholder,
  valueLabel,
  disabled,
  id,
  className,
  size,
  "aria-label": ariaLabel,
}: {
  options: readonly OptionSelectOption[];
  value: string | null | undefined;
  onValueChange: (value: string | null) => void;
  /** 无选中值（null/""）时显示的占位文本。 */
  placeholder?: ReactNode;
  /** 收起态显示内容的整体覆盖；缺省按 options 的 label 查。 */
  valueLabel?: ReactNode;
  disabled?: boolean;
  /** 以下四个属性透传给 SelectTrigger。 */
  id?: string;
  className?: string;
  size?: "sm" | "default";
  "aria-label"?: string;
}) {
  return (
    <Select
      value={value}
      onValueChange={(next) => onValueChange(next ?? null)}
      items={options}
      disabled={disabled}
    >
      <SelectTrigger
        id={id}
        className={className}
        size={size}
        aria-label={ariaLabel}
      >
        <SelectValue placeholder={placeholder}>{valueLabel}</SelectValue>
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem
            key={option.value}
            value={option.value}
            disabled={option.disabled}
          >
            {option.itemLabel ?? option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
