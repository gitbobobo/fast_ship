import { Link } from "react-router";
import { ChartColumn, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useBots } from "@/lib/hooks/use-bots";
import { cn } from "@/lib/utils";
import { BotAvatar } from "./bot-avatar";

/**
 * 工作台左栏：机器人列表。选中项高亮并标 aria-current；每项显示
 * 确定性颜色头像、名称与最近一条消息预览（last_message 的唯一事实
 * 来源是列表端点，消息提交后由列表重取更新）。
 * 未选机器人时移动端铺满宽度，选中后移动端只留中栏（回列表走页头入口）。
 */
export function BotListPanel({
  selectedId,
  onCreate,
}: {
  selectedId?: string;
  onCreate: () => void;
}) {
  const { data: bots, isLoading } = useBots();

  return (
    <div
      className={cn(
        "h-full min-h-0 w-full flex-col lg:w-72 lg:shrink-0 lg:border-r",
        selectedId ? "hidden lg:flex" : "flex",
      )}
    >
      <div className="flex h-12 shrink-0 items-center justify-between border-b px-4">
        <div className="flex items-baseline gap-1.5">
          <h2 className="text-sm font-medium">机器人</h2>
          {bots && (
            <span className="text-xs text-muted-foreground">{bots.length}</span>
          )}
        </div>
        <TooltipProvider delay={0}>
          <div className="flex items-center">
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="花费总览"
                    render={<Link to="/bots/usage" />}
                  />
                }
              >
                <ChartColumn className="h-4 w-4" />
              </TooltipTrigger>
              <TooltipContent>花费总览</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label="新建机器人"
                    onClick={onCreate}
                  />
                }
              >
                <Plus className="h-4 w-4" />
              </TooltipTrigger>
              <TooltipContent>新建机器人</TooltipContent>
            </Tooltip>
          </div>
        </TooltipProvider>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="flex items-center gap-3 px-1 py-1">
                <Skeleton className="size-9 rounded-full" />
                <div className="flex-1 space-y-1.5">
                  <Skeleton className="h-3.5 w-2/3" />
                  <Skeleton className="h-3 w-1/2" />
                </div>
              </div>
            ))}
          </div>
        ) : !bots?.length ? (
          <div className="px-4 py-10 text-center">
            <p className="text-sm text-muted-foreground">还没有机器人</p>
            <p className="mt-1 text-xs text-muted-foreground">
              点击右上角「+」新建
            </p>
          </div>
        ) : (
          <ul className="space-y-0.5 p-2">
            {bots.map((bot) => {
              const selected = bot.id === selectedId;
              return (
                <li key={bot.id}>
                  <Link
                    to={`/bots/${bot.id}`}
                    aria-label={bot.name}
                    aria-current={selected ? "page" : undefined}
                    className={cn(
                      "flex items-center gap-3 rounded-lg px-2.5 py-2.5 hover:bg-muted",
                      selected && "bg-accent text-accent-foreground",
                    )}
                  >
                    <BotAvatar name={bot.name} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-medium">
                        {bot.name}
                      </span>
                      {bot.last_message?.text ? (
                        <span className="block truncate text-xs text-muted-foreground">
                          {bot.last_message.kind === "user" ? "你：" : null}
                          {bot.last_message.text}
                        </span>
                      ) : null}
                    </span>
                  </Link>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
