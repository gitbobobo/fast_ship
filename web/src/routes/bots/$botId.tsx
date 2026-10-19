import { useParams } from "react-router";
import { BotWorkspace } from "@/components/bots/bot-workspace";

// 机器人工作台（INT-125）：选中机器人后中栏进入唯一主线，右栏为详情。
export default function BotDetailPage() {
  const { botId = "" } = useParams();
  return <BotWorkspace botId={botId} />;
}
