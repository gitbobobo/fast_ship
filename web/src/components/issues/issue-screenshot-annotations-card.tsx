import { Link } from "react-router";
import { ScanSearch } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { screenshotAnnotationApi } from "@/lib/api/screenshot-annotations";
import { cn } from "@/lib/utils";
import { formatDate } from "@/lib/utils/format";

/**
 * Issue 详情里的「截图标注」：展示关联到该 Issue 的标注，点击跳到截图画布
 * 并定位到该标注。没有标注时不渲染整块。
 */
export function IssueScreenshotAnnotationsCard({
  projectId,
  annotations,
}: {
  projectId: string;
  annotations?: ScreenshotAnnotation[];
}) {
  const items = annotations ?? [];
  if (items.length === 0) return null;

  return (
    <Card
      className="shadow-md hover:shadow-lg transition-shadow"
      data-testid="issue-screenshot-annotations"
    >
      <CardHeader className="items-center gap-3 space-y-0 border-b px-5 pb-4 pt-5">
        <div className="flex min-w-0 flex-wrap items-center gap-2.5">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
            <ScanSearch className="h-4 w-4 text-primary" />
          </div>
          <CardTitle className="text-base">截图标注</CardTitle>
          <Badge
            variant="secondary"
            className="text-xs font-semibold bg-primary/10 text-primary border-primary/20"
          >
            {items.length}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 px-5 pb-5 pt-5">
        {items.map((annotation) => {
          const resolved = annotation.status === "resolved";
          return (
            <Link
              key={annotation.id}
              to={`/screenshots?project=${encodeURIComponent(projectId)}&view=canvas&annotation=${encodeURIComponent(annotation.id)}`}
              className="flex items-start gap-3.5 rounded-xl border border-border/60 px-4 py-3 transition-all hover:border-primary/30 hover:shadow-md"
            >
              <img
                src={screenshotAnnotationApi.cropUrl(annotation)}
                alt=""
                loading="lazy"
                className="h-16 w-16 shrink-0 rounded-lg border bg-muted/30 object-cover"
              />
              <div className="min-w-0 flex-1 space-y-1">
                <p
                  className={cn(
                    "line-clamp-2 text-sm font-medium break-words whitespace-pre-wrap",
                    resolved && "text-muted-foreground",
                  )}
                >
                  {annotation.body}
                </p>
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                  <span className="max-w-48 truncate">
                    {annotation.screen_title || annotation.screen_key}
                  </span>
                  <span>{formatDate(annotation.created_at)}</span>
                  <Badge
                    variant={resolved ? "secondary" : "outline"}
                    className={cn(
                      resolved
                        ? "text-emerald-600 dark:text-emerald-400"
                        : "border-amber-500/60 text-amber-600 dark:text-amber-400",
                    )}
                  >
                    {resolved ? "已解决" : "未解决"}
                  </Badge>
                </div>
              </div>
            </Link>
          );
        })}
      </CardContent>
    </Card>
  );
}
