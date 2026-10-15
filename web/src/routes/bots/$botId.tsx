import { useState } from "react";
import { useParams } from "react-router";
import { KeyRound, Pencil } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Skeleton } from "@/components/ui/skeleton";
import { BotChat } from "@/components/bots/bot-chat";
import { BotFormDialog } from "@/components/bots/bot-form-dialog";
import { BotAuthorizationsDialog } from "@/components/bots/bot-authorizations-dialog";
import { useBot } from "@/lib/hooks/use-bots";

/**
 * 机器人详情：直接进入唯一主线（无会话列表、无新建会话）。
 * 配置编辑与授权管理从页头进入，对话区保持专注。
 */
export default function BotDetailPage() {
  const { botId = "" } = useParams();
  const { data: bot, isLoading } = useBot(botId);
  const [formOpen, setFormOpen] = useState(false);
  const [authOpen, setAuthOpen] = useState(false);

  return (
    <div className="flex flex-col">
      <Header
        title={bot?.name || "机器人"}
        actions={
          bot && (
            <HeaderActions
              primary={
                <div className="flex items-center gap-1.5">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setAuthOpen(true)}
                  >
                    <KeyRound className="mr-1.5 h-3.5 w-3.5" />
                    授权管理
                  </Button>
                  <Button size="sm" onClick={() => setFormOpen(true)}>
                    <Pencil className="mr-1.5 h-3.5 w-3.5" />
                    编辑
                  </Button>
                </div>
              }
            />
          )
        }
      />
      {isLoading ? (
        <div className="mx-auto w-full max-w-3xl space-y-3 px-4 py-6 md:px-6">
          <Skeleton className="h-6 w-1/3" />
          <Skeleton className="h-24 rounded-xl" />
        </div>
      ) : (
        <>
          <BotChat key={botId} botId={botId} />
          {bot && (
            <>
              <BotFormDialog
                open={formOpen}
                editing={bot}
                onClose={() => setFormOpen(false)}
              />
              <BotAuthorizationsDialog
                botId={botId}
                open={authOpen}
                onClose={() => setAuthOpen(false)}
              />
            </>
          )}
        </>
      )}
    </div>
  );
}
