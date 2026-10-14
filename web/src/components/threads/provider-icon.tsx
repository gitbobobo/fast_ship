import type { CSSProperties } from "react";
import { Box } from "lucide-react";
import { PROVIDER_ICONS, providerIconKey } from "@/lib/provider-icons";
import { cn } from "@/lib/utils";

/**
 * provider 实例的 glyph。主题色以 CSS 变量传入，`.dark` 切换时无需重渲染；
 * 未知 provider 用通用立方体兜底。
 */
export function ProviderIcon({
  providerInstanceId,
  className,
}: {
  providerInstanceId: string;
  className?: string;
}) {
  const key = providerIconKey(providerInstanceId);
  if (!key) {
    return <Box aria-hidden className={cn("size-3.5 shrink-0", className)} />;
  }
  const icon = PROVIDER_ICONS[key];
  return (
    <svg
      aria-hidden
      viewBox={icon.viewBox}
      fill="none"
      data-provider-icon={key}
      className={cn(
        "size-3.5 shrink-0 fill-(--icon-light) dark:fill-(--icon-dark)",
        className,
      )}
      style={
        {
          "--icon-light": icon.fill.light,
          "--icon-dark": icon.fill.dark,
        } as CSSProperties
      }
    >
      {icon.paths.map((path) => {
        const detail = "fill" in path ? path.fill : undefined;
        return (
          <path
            key={path.d}
            d={path.d}
            fillRule={"fillRule" in path ? path.fillRule : undefined}
            className={
              detail
                ? "fill-(--path-light) dark:fill-(--path-dark)"
                : undefined
            }
            style={
              detail
                ? ({
                    "--path-light": detail.light,
                    "--path-dark": detail.dark,
                  } as CSSProperties)
                : undefined
            }
          />
        );
      })}
    </svg>
  );
}
