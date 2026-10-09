import { useLayoutEffect, useState, type RefObject } from "react";
import { AnnotationRect } from "./annotation-rect";

interface ImageBox {
  left: number;
  top: number;
  width: number;
  height: number;
}

/**
 * 量出 img 在 offsetParent 内的实际显示盒。img 自身尺寸就是显示尺寸
 * （max-w/max-h 等比缩放），位置随容器居中变化，所以同时观察 img 与其父级。
 */
function useImageBox(imgRef: RefObject<HTMLImageElement | null>) {
  const [box, setBox] = useState<ImageBox | null>(null);

  useLayoutEffect(() => {
    const img = imgRef.current;
    if (!img) return;
    const measure = () => {
      const next: ImageBox = {
        left: img.offsetLeft,
        top: img.offsetTop,
        width: img.offsetWidth,
        height: img.offsetHeight,
      };
      setBox((prev) =>
        prev &&
        prev.left === next.left &&
        prev.top === next.top &&
        prev.width === next.width &&
        prev.height === next.height
          ? prev
          : next,
      );
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(img);
    if (img.parentElement) observer.observe(img.parentElement);
    img.addEventListener("load", measure);
    return () => {
      observer.disconnect();
      img.removeEventListener("load", measure);
    };
  }, [imgRef]);

  return box;
}

/**
 * 预览弹窗里的只读标注叠加层，必须与 img 同为某个 relative 容器的子节点
 * （img.offsetParent 即该容器）。open/resolved 都显示，不可交互。
 */
export function AnnotationOverlay({
  imgRef,
  annotations,
  focusAnnotationId,
}: {
  imgRef: RefObject<HTMLImageElement | null>;
  annotations: ScreenshotAnnotation[];
  focusAnnotationId?: string | null;
}) {
  const box = useImageBox(imgRef);
  if (!box || box.width <= 0 || annotations.length === 0) return null;
  return (
    <div
      className="pointer-events-none absolute"
      style={{
        left: box.left,
        top: box.top,
        width: box.width,
        height: box.height,
      }}
    >
      {annotations.map((a) => (
        <AnnotationRect
          key={a.id}
          annotation={a}
          selected={a.id === focusAnnotationId}
        />
      ))}
    </div>
  );
}
