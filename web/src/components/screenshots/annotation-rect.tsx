import type { CSSProperties } from "react";
import { cn } from "@/lib/utils";
import {
  CANVAS_ZOOM_VAR,
  ratioRectToPercentStyle,
  type Rect,
} from "@/lib/screenshot-canvas";

/**
 * 标注「皮肤」像素换算：画布世界容器写了 --canvas-zoom = 当前缩放率
 * （见 screenshot-canvas.tsx），皮肤的目标屏幕像素除以它得到世界坐标
 * 尺寸，随世界 transform 渲染后刚好还原成 Npx。世界缩放本身也通过
 * scale: var(--canvas-zoom) 渲染——渲染缩放与皮肤反算同源，动画期间或
 * 过渡被打断重启都逐帧精确；画布外（预览弹窗等）变量未定义，
 * 回退 1——那里的皮肤本来就是屏幕恒定尺寸。
 */
const ZOOM = `var(${CANVAS_ZOOM_VAR}, 1)`;
const zsPx = (n: number) => `calc(${n}px / ${ZOOM})`;

/**
 * 光圈叠层（box-shadow 先列先画在上层）：选中为「1px 底色间隔 + 3px 红圈」，
 * 闪烁为 4px 半透明光圈，都盖在外侧深色发丝圈之上；发丝圈让框体在深色
 * 截图底色上保持可见。内侧白色发丝改由 SVG 描边内侧绘制（见 FrameStroke）。
 */
function frameShadow(state: {
  selected?: boolean;
  flashing?: boolean;
  resolved?: boolean;
}): string {
  const ring = state.flashing
    ? [
        `0 0 0 ${zsPx(4)} color-mix(in oklab, ${
          state.resolved
            ? "var(--color-emerald-500)"
            : "var(--color-red-500)"
        } 70%, transparent)`,
      ]
    : state.selected
      ? [
          `0 0 0 ${zsPx(1)} var(--background)`,
          `0 0 0 ${zsPx(3)} var(--color-red-500)`,
        ]
      : [];
  return [...ring, `0 0 0 ${zsPx(1)} rgba(15,23,42,0.55)`].join(",");
}

/** 标注框体的比例定位 + 恒定皮肤（圆角、发丝阴影与光圈） */
export function annotationFrameStyle(
  rect: Rect,
  state: { selected?: boolean; flashing?: boolean; resolved?: boolean } = {},
): CSSProperties {
  return {
    ...ratioRectToPercentStyle(rect),
    borderRadius: zsPx(2),
    boxShadow: frameShadow(state),
  };
}

/**
 * 框体描边用 SVG 画而不用 CSS border：border 参与盒模型，深度缩小时
 * 反算的世界边框宽度会超过比例矩形并把框撑大；且浏览器把 border-width
 * 取整到设备像素，非整数宽度下描边粗细漂移。SVG 描边不参与布局、按
 * 亚像素绘制，stroke-width 除以缩放率后屏幕上恒为 2px。主描边 rect 向内
 * 缩进半个描边宽，让描边整段落在比例盒内（外侧深色发丝圈才不被盖住）；
 * 内侧 1px 白色发丝是第二层 rect——原 inset 阴影画在 border 内侧，改 SVG
 * 后父级阴影会被描边盖住，所以发丝也进 SVG。
 */
function FrameStroke({ dashed, resolved }: { dashed?: boolean; resolved?: boolean }) {
  return (
    <svg
      aria-hidden
      className="pointer-events-none absolute inset-0 block h-full w-full overflow-visible"
    >
      <rect
        fill="none"
        style={{
          x: zsPx(1),
          y: zsPx(1),
          width: `calc(100% - 2px / ${ZOOM})`,
          height: `calc(100% - 2px / ${ZOOM})`,
          rx: zsPx(1),
          stroke: resolved
            ? "var(--color-emerald-500)"
            : "var(--color-red-500)",
          strokeWidth: zsPx(2),
          ...(dashed ? { strokeDasharray: `${zsPx(4)} ${zsPx(3)}` } : null),
        }}
      />
      <rect
        fill="none"
        style={{
          x: zsPx(2.5),
          y: zsPx(2.5),
          width: `calc(100% - 5px / ${ZOOM})`,
          height: `calc(100% - 5px / ${ZOOM})`,
          stroke: "rgba(255,255,255,0.85)",
          strokeWidth: zsPx(1),
        }}
      />
    </svg>
  );
}

/** 框选进行中与待提交的虚线预览框：与标注矩形同款皮肤，不可交互 */
export function AnnotationDraftRect({ rect }: { rect: Rect }) {
  return (
    <div className="absolute bg-red-500/10" style={annotationFrameStyle(rect)}>
      <FrameStroke dashed />
    </div>
  );
}

interface AnnotationRectProps {
  annotation: ScreenshotAnnotation;
  /** 面板同序序号角标；不传则不渲染 */
  index?: number;
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
 * 显示区域完全重合的 relative 容器。open 红色实线，resolved 绿色虚线且降低透明度。
 * 边框带内外双层发丝描边（外深内白），在任何截图底色上保持可见。
 */
export function AnnotationRect({
  annotation,
  index,
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
        "absolute transition-colors",
        resolved
          ? "bg-emerald-500/10 opacity-60"
          : "bg-red-500/15",
        emphasized &&
          (resolved ? "bg-emerald-500/25 opacity-100" : "bg-red-500/30"),
        flashing && "animate-pulse",
        interactive ? "pointer-events-auto cursor-pointer" : "pointer-events-none",
      )}
      style={annotationFrameStyle(annotation, { selected, flashing, resolved })}
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
    >
      <FrameStroke dashed={resolved} resolved={resolved} />
      {index !== undefined && (
        <span
          className={cn(
            "absolute top-0 left-0 flex items-center justify-center leading-none font-semibold text-white",
            resolved ? "bg-emerald-500" : "bg-red-500",
          )}
          style={{
            height: zsPx(16),
            minWidth: zsPx(16),
            paddingInline: zsPx(2),
            fontSize: zsPx(10),
            borderBottomRightRadius: zsPx(6),
          }}
        >
          {index}
        </span>
      )}
    </div>
  );
}
