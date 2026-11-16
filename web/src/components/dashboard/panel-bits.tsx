import { Activity } from "lucide-react";
import { Link } from "react-router";

/**
 * AI 面板与发布节奏卡共用的小件：区块标头、降级占位与时间格式化。
 * 只放纯展示逻辑，不带数据获取。
 */

export function SectionHeader({
  icon: Icon,
  title,
  trailing,
}: {
  icon: typeof Activity;
  title: string;
  trailing?: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-2">
      <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
      <h4 className="text-xs font-medium">{title}</h4>
      {trailing != null && <div className="ml-auto">{trailing}</div>}
    </div>
  );
}

/** 图标 16px + gap 8px = 标题文字起点；值/空态行统一 pl-6 与它对齐（W2）。 */
const SECTION_BODY_OFFSET = "pl-6";

export function SectionUnavailable() {
  return (
    <p className={`text-xs text-muted-foreground ${SECTION_BODY_OFFSET}`}>
      暂不可用
    </p>
  );
}

/** 行内空态入口的标准写法（W6）：主色 + 尾部箭头 + hover 变色，
 *  不要用灰字伪装成不可点的文字。 */
export function SectionEmptyLink({
  to,
  children,
}: {
  to: string;
  children: React.ReactNode;
}) {
  return (
    <Link
      to={to}
      className="shrink-0 text-primary underline-offset-2 transition-colors hover:text-primary/75"
    >
      {children} →
    </Link>
  );
}

export function formatScheduleTime(iso: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString("zh-CN", {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
