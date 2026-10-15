import {
  BoxSelect,
  Copy,
  Maximize,
  PanelRightClose,
  PanelRightOpen,
  Play,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";

interface CanvasToolbarProps {
  scale: number;
  drawMode: boolean;
  panelOpen: boolean;
  /** 当前筛选范围内 open 标注数；为 0 时「启动线程」置灰 */
  openCount: number;
  /** 父级用 ResizeObserver 实测高度，给「适应全部」留顶部净空 */
  ref?: React.Ref<HTMLDivElement>;
  onToggleDraw: () => void;
  onTogglePanel: () => void;
  onZoomIn: () => void;
  onZoomOut: () => void;
  onFit: () => void;
  /** 固定入口：启动线程处理标注意见（带二次确认的弹框） */
  onLaunchThread: () => void;
  /** 固定入口：复制处理提示词（不确认） */
  onCopyPrompt: () => void;
}

/** 画布左上角工具栏：框选、缩放、适应全部、面板开关，以及处理标注意见的固定入口 */
export function CanvasToolbar({
  scale,
  drawMode,
  panelOpen,
  openCount,
  ref,
  onToggleDraw,
  onTogglePanel,
  onZoomIn,
  onZoomOut,
  onFit,
  onLaunchThread,
  onCopyPrompt,
}: CanvasToolbarProps) {
  return (
    <div
      ref={ref}
      data-no-pan
      data-testid="canvas-toolbar"
      className="absolute top-3 left-3 z-20 flex max-w-[calc(100%-1.5rem)] flex-wrap items-center gap-1 rounded-lg border bg-background/90 p-1 shadow-sm backdrop-blur"
    >
      <Button
        size="sm"
        variant={drawMode ? "default" : "ghost"}
        aria-pressed={drawMode}
        title="框选标注（R）"
        onClick={onToggleDraw}
      >
        <BoxSelect className="mr-1 h-3.5 w-3.5" />
        框选
      </Button>
      <Separator orientation="vertical" className="mx-0.5 h-5" />
      <Button
        size="sm"
        variant="ghost"
        title={openCount > 0 ? "启动线程逐条处理当前范围内的标注意见" : "当前范围内没有未解决标注"}
        disabled={openCount === 0}
        onClick={onLaunchThread}
        data-testid="canvas-launch-thread"
      >
        <Play className="mr-1 h-3.5 w-3.5" />
        启动线程
      </Button>
      <Button
        size="sm"
        variant="ghost"
        title={openCount > 0 ? "复制处理标注意见的提示词" : "当前范围内没有未解决标注"}
        disabled={openCount === 0}
        onClick={onCopyPrompt}
        data-testid="canvas-copy-prompt"
      >
        <Copy className="mr-1 h-3.5 w-3.5" />
        复制提示词
      </Button>
      <Separator orientation="vertical" className="mx-0.5 h-5" />
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="缩小"
        title="缩小"
        onClick={onZoomOut}
      >
        <ZoomOut className="h-3.5 w-3.5" />
      </Button>
      <span
        className="w-11 text-center text-xs tabular-nums text-muted-foreground"
        data-testid="canvas-zoom-percent"
      >
        {Math.round(scale * 100)}%
      </span>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="放大"
        title="放大"
        onClick={onZoomIn}
      >
        <ZoomIn className="h-3.5 w-3.5" />
      </Button>
      <Button size="sm" variant="ghost" title="适应全部" onClick={onFit}>
        <Maximize className="mr-1 h-3.5 w-3.5" />
        适应全部
      </Button>
      <Separator orientation="vertical" className="mx-0.5 h-5" />
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label={panelOpen ? "收起标注面板" : "展开标注面板"}
        title={panelOpen ? "收起标注面板" : "展开标注面板"}
        onClick={onTogglePanel}
      >
        {panelOpen ? (
          <PanelRightClose className="h-3.5 w-3.5" />
        ) : (
          <PanelRightOpen className="h-3.5 w-3.5" />
        )}
      </Button>
    </div>
  );
}
