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
  /**
   * 下拉项自身的 class。分组标题这类「不可选但需要正常对比度」的项用它抵消
   * SelectItem 的 data-disabled:opacity-50。
   */
  className?: string;
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
  contentClassName,
  size,
  invalid,
  alignItemWithTrigger,
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
  /** 以下属性透传给 SelectTrigger/SelectContent。 */
  id?: string;
  className?: string;
  /** 下拉面板自身的 class（触发器较窄、选项较长时用它放宽面板）。 */
  contentClassName?: string;
  size?: "sm" | "default";
  /** 校验失败态：trigger 描边/光环转 destructive。 */
  invalid?: boolean;
  /** 浮层对齐方式：缺省走 SelectContent 的条目对齐；false 与 trigger 同宽贴齐。 */
  alignItemWithTrigger?: boolean;
  "aria-label"?: string;
}) {
  // value 传 undefined 会让 Base UI 落入非受控模式，统一归一为 null；
  // "" 被视为未选中，此时 valueLabel 不应盖掉 placeholder。
  const hasValue = value != null && value !== "";
  const current = value ?? null;
  return (
    <Select
      value={current}
      // Base UI 对重选当前值也会触发 onValueChange：提前挡掉，避免
      // 环境/项目这类联动选择器被同值点击清空下游字段。
      onValueChange={(next) => {
        const normalized = next ?? null;
        if (normalized !== current) onValueChange(normalized);
      }}
      items={options}
      disabled={disabled}
    >
      <SelectTrigger
        id={id}
        className={className}
        size={size}
        aria-label={ariaLabel}
        aria-invalid={invalid || undefined}
      >
        <SelectValue placeholder={placeholder}>
          {hasValue ? valueLabel : undefined}
        </SelectValue>
      </SelectTrigger>
      <SelectContent
        className={contentClassName}
        alignItemWithTrigger={alignItemWithTrigger ?? true}
      >
        {options.map((option) => (
          <SelectItem
            key={option.value}
            value={option.value}
            disabled={option.disabled}
            className={option.className}
          >
            {option.itemLabel ?? option.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/**
 * options 异步加载（或引用的数据被删）时，当前 value 可能不在列表里——
 * Base UI 查不到会回退显示原始 value。用本函数把当前值补成占位项：
 * 加载中传「加载中…」，加载完仍缺失传 `xx（已失效）`。占位项标记 disabled，
 * 只用于收起态文案，点开下拉时不能选中它去覆盖联动字段。
 */
export function withMissingOption(
  options: readonly OptionSelectOption[],
  value: string | null | undefined,
  label: ReactNode,
): OptionSelectOption[] {
  if (!value || options.some((o) => o.value === value)) {
    return [...options];
  }
  return [{ value, label, disabled: true }, ...options];
}
