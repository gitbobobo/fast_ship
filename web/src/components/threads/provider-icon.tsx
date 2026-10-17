import type { CSSProperties } from "react";
import { Box } from "lucide-react";
import { resolveProviderIcon } from "@/lib/provider-icons";
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
  const resolved = resolveProviderIcon(providerInstanceId);
  if (!resolved) {
    return <Box aria-hidden className={cn("size-3.5 shrink-0", className)} />;
  }
  const { key, icon } = resolved;
  const style = {
    "--icon-light": icon.fill.light,
    "--icon-dark": icon.fill.dark,
  } as CSSProperties;
  // 非路径原语（circle/line/text/polygon…）的图标用原始 inner markup 渲染；
  // 内容是 ACP registry 生成的静态 SVG，不是用户输入。currentColor 由
  // 主题色变量通过 color 驱动。
  if (icon.raw) {
    return (
      <svg
        aria-hidden
        viewBox={icon.viewBox}
        fill="currentColor"
        data-provider-icon={key}
        className={cn(
          "size-3.5 shrink-0 text-(--icon-light) dark:text-(--icon-dark)",
          className,
        )}
        style={style}
        dangerouslySetInnerHTML={{ __html: icon.raw }}
      />
    );
  }
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
      style={style}
    >
      {(icon.paths ?? []).map((path) => {
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
