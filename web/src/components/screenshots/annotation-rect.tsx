import { cn } from "@/lib/utils";
import { ratioRectToPercentStyle } from "@/lib/screenshot-canvas";

interface AnnotationRectProps {
  annotation: ScreenshotAnnotation;
  selected?: boolean;
  hovered?: boolean;
  /** 定位后短暂闪烁，提示用户看这条 */
  flashing?: boolean;
  /** 可点击/悬浮（画布）；只读叠加（预览弹窗）时为 false */
  interactive?: boolean;
  onSelect?: (annotationId: string) => void;
  onHover?: (annotationId: string | null) => void;
}

/**
 * 单个标注矩形，按比例坐标绝对定位在「图片盒」内——父级必须是与图片
 * 显示区域完全重合的 relative 容器。open 琥珀实线，resolved 绿色虚线且降低透明度。
 */
export function AnnotationRect({
  annotation,
  selected,
  hovered,
  flashing,
  interactive,
  onSelect,
  onHover,
}: AnnotationRectProps) {
  const resolved = annotation.status === "resolved";
  const emphasized = selected || hovered || flashing;
  return (
    <div
      data-testid={`annotation-rect-${annotation.id}`}
      data-annotation-status={annotation.status}
      title={annotation.body}
      className={cn(
        "absolute box-border rounded-[2px] border-2 transition-colors",
        resolved
          ? "border-dashed border-emerald-500 bg-emerald-500/10 opacity-60"
          : "border-amber-500 bg-amber-500/15",
        emphasized &&
          (resolved
            ? "bg-emerald-500/25 opacity-100"
            : "bg-amber-500/30"),
        selected && "ring-2 ring-primary ring-offset-1 ring-offset-background",
        flashing && "animate-pulse ring-2 ring-primary",
        interactive ? "pointer-events-auto cursor-pointer" : "pointer-events-none",
      )}
      style={ratioRectToPercentStyle(annotation)}
      onClick={
        interactive
          ? (e) => {
              e.stopPropagation();
              onSelect?.(annotation.id);
            }
          : undefined
      }
      onPointerEnter={interactive ? () => onHover?.(annotation.id) : undefined}
      onPointerLeave={interactive ? () => onHover?.(null) : undefined}
    />
  );
}
