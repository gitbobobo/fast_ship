import { Activity } from "lucide-react";

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

export function SectionUnavailable() {
  return <p className="text-xs text-muted-foreground">暂不可用</p>;
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
