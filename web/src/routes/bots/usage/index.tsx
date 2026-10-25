import { useSearchParams } from "react-router";
import { Header } from "@/components/layout/header";
import { BotUsageOverviewPanel } from "@/components/bots/bot-usage-overview";

/**
 * 机器人花费总览（INT-162）：/bots/usage，可选 ?task=<taskId> 定位到
 * 按定时任务表中的某一行（详情页「近 7 天」入口带过来）。
 */
export default function BotUsagePage() {
  const [searchParams] = useSearchParams();
  const focusTaskId = searchParams.get("task");

  return (
    <div className="flex flex-col">
      <Header title="机器人花费" backTo="/bots" />
      <div className="mx-auto w-full max-w-6xl p-4 md:p-6">
        <BotUsageOverviewPanel focusTaskId={focusTaskId} />
      </div>
    </div>
  );
}
