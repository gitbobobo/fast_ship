import {
  BoxSelect,
  Eye,
  EyeOff,
  Maximize,
  PanelRightClose,
  PanelRightOpen,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";

interface CanvasToolbarProps {
  scale: number;
  drawMode: boolean;
  showResolved: boolean;
  panelOpen: boolean;
  onToggleDraw: () => void;
  onToggleResolved: () => void;
  onTogglePanel: () => void;
  onZoomIn: () => void;
  onZoomOut: () => void;
  onFit: () => void;
}

/** 画布左上角工具栏：框选、显示已解决、缩放、适应全部、面板开关 */
export function CanvasToolbar({
  scale,
  drawMode,
  showResolved,
  panelOpen,
  onToggleDraw,
  onToggleResolved,
  onTogglePanel,
  onZoomIn,
  onZoomOut,
  onFit,
}: CanvasToolbarProps) {
  return (
    <div
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
      <Button
        size="sm"
        variant={showResolved ? "secondary" : "ghost"}
        aria-pressed={showResolved}
        title="在画布上显示已解决的标注"
        onClick={onToggleResolved}
      >
        {showResolved ? (
          <Eye className="mr-1 h-3.5 w-3.5" />
        ) : (
          <EyeOff className="mr-1 h-3.5 w-3.5" />
        )}
        已解决
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
