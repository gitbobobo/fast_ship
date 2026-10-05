import { useEffect, useMemo, useState, type ComponentType } from "react";
import {
  Check,
  HelpCircle,
  Loader2,
  Sparkles,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { GitHubContent } from "@/components/github-content";
import {
  type CollabDeleteSection,
  useDeleteCollabSection,
  useIssueCollab,
} from "@/lib/hooks/use-issue-collab";
import { cn, getInitials } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";

type TabValue = "consensus" | "summary";

interface TabConfig {
  value: TabValue;
  label: string;
  icon: ComponentType<{ className?: string }>;
  iconClassName: string;
  section: CollabDeleteSection;
  deleteAriaLabel: string;
  deleteTitle: string;
  deleteDescription: string;
}

const COLLAB_TABS: TabConfig[] = [
  {
    value: "consensus",
    label: "共识",
    icon: Sparkles,
    iconClassName: "text-violet-500",
    section: "consensus",
    deleteAriaLabel: "删除共识",
    deleteTitle: "删除共识？",
    deleteDescription: "将删除该问题的共识内容，不可恢复。",
  },
  {
    value: "summary",
    label: "完成总结",
    icon: Check,
    iconClassName: "text-emerald-500",
    section: "summary",
    deleteAriaLabel: "删除完成总结",
    deleteTitle: "删除完成总结？",
    deleteDescription: "将删除该问题的完成总结，不可恢复。",
  },
];

const SECTION_SUCCESS_TOAST: Record<CollabDeleteSection, string> = {
  all: "协作区已清空",
  consensus: "共识已删除",
  summary: "完成总结已删除",
};

interface CollaborationAreaProps {
  issueId: string;
  /** 只读时隐藏全部删除入口（含「清空协作区」与各节删除按钮），用于推荐弹框等不能再弹确认框的场景。 */
  readOnly?: boolean;
}

function CollabActorBadge({ actor }: { actor: IssueCollabActor }) {
  if (actor.kind === "agent") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full border border-violet-500/20 bg-violet-500/10 px-2 py-0.5 text-xs font-medium text-violet-600 dark:text-violet-400">
        <Sparkles className="h-3 w-3" />
        {actor.login}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <Avatar size="sm">
        {actor.avatar_url ? <AvatarImage src={actor.avatar_url} alt={actor.login} /> : null}
        <AvatarFallback>{getInitials(actor.login)}</AvatarFallback>
      </Avatar>
      <span className="font-medium text-foreground">@{actor.login}</span>
    </span>
  );
}

interface DeleteCollabButtonProps {
  issueId: string;
  section: CollabDeleteSection;
  ariaLabel: string;
  title: string;
  description: string;
  variant?: "destructive" | "ghost";
  className?: string;
}

function DeleteCollabButton({
  issueId,
  section,
  ariaLabel,
  title,
  description,
  variant = "ghost",
  className,
}: DeleteCollabButtonProps) {
  const [open, setOpen] = useState(false);
  const deleteSection = useDeleteCollabSection(issueId, section);

  const handleConfirm = async (event: React.MouseEvent) => {
    event.preventDefault();
    try {
      await deleteSection.mutateAsync();
      toast.success(SECTION_SUCCESS_TOAST[section]);
      setOpen(false);
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger
        render={
          <Button
            type="button"
            variant={variant}
            size="icon-sm"
            aria-label={ariaLabel}
            disabled={deleteSection.isPending}
            className={className}
          />
        }
      >
        <Trash2 className="h-4 w-4" />
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleteSection.isPending}>取消</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => void handleConfirm(event)}
            disabled={deleteSection.isPending}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {deleteSection.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                删除中…
              </>
            ) : (
              "确认删除"
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function CollabDocSection({
  issueId,
  tab,
  doc,
  readOnly,
}: {
  issueId: string;
  tab: TabConfig;
  doc: IssueCollabDoc;
  readOnly: boolean;
}) {
  const Icon = tab.icon;
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <Icon className={cn("h-4 w-4", tab.iconClassName)} />
          {tab.label}
        </div>
        <div className="flex items-center gap-2">
          {readOnly ? null : (
            <DeleteCollabButton
              issueId={issueId}
              section={tab.section}
              ariaLabel={tab.deleteAriaLabel}
              title={tab.deleteTitle}
              description={tab.deleteDescription}
            />
          )}
          <CollabActorBadge actor={doc.author} />
        </div>
      </div>
      <div className="rounded-lg border bg-card p-3">
        <div className="markdown-body text-sm">
          <GitHubContent markdown={doc.body} />
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          更新于 {formatRelativeTime(doc.updated_at)}
        </p>
      </div>
    </div>
  );
}

function EmptyTabPanel() {
  return (
    <div className="flex min-h-[88px] items-center justify-center py-8">
      <p className="text-muted-foreground" role="status">
        暂无内容
      </p>
    </div>
  );
}

export function CollaborationArea({ issueId, readOnly = false }: CollaborationAreaProps) {
  const { data, isLoading } = useIssueCollab(issueId);
  const consensus = data?.consensus ?? null;
  const summary = data?.summary ?? null;

  const docsByTab = useMemo<Record<TabValue, IssueCollabDoc | null>>(
    () => ({
      consensus,
      summary,
    }),
    [consensus, summary],
  );

  const hasContent = useMemo<Record<TabValue, boolean>>(
    () => ({
      consensus: consensus !== null,
      summary: summary !== null,
    }),
    [consensus, summary],
  );

  const defaultTab = COLLAB_TABS.find((tab) => hasContent[tab.value])?.value ?? null;
  const hasAnyContent = defaultTab !== null;
  const [activeTab, setActiveTab] = useState<TabValue | null>(null);
  const resolvedTab = activeTab ?? defaultTab;

  useEffect(() => {
    setActiveTab(null);
  }, [issueId]);

  useEffect(() => {
    if (!hasAnyContent) {
      setActiveTab(null);
      return;
    }
    setActiveTab((current) => {
      if (current === null || !hasContent[current]) {
        return defaultTab;
      }
      return current;
    });
  }, [issueId, defaultTab, hasAnyContent, hasContent]);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <HelpCircle className="h-4 w-4 text-primary" />
          <h2 className="text-sm font-semibold">人机协作区</h2>
        </div>
        {hasAnyContent && !readOnly ? (
          <DeleteCollabButton
            issueId={issueId}
            section="all"
            ariaLabel="清空协作区"
            title="清空协作区？"
            description="将删除共识与完成总结，不可恢复。"
            variant="destructive"
          />
        ) : null}
      </div>

      {isLoading ? (
        <div className="space-y-3">
          <p className="sr-only">正在加载人机协作区</p>
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
        </div>
      ) : hasAnyContent && resolvedTab ? (
        <Tabs value={resolvedTab} onValueChange={(value) => setActiveTab(value as TabValue)} className="flex flex-col gap-4">
          <TabsList aria-label="人机协作区内容" className="w-full justify-start overflow-x-auto">
            {COLLAB_TABS.map((tab) => {
              const Icon = tab.icon;
              const filled = hasContent[tab.value];
              return (
                <TabsTrigger
                  key={tab.value}
                  value={tab.value}
                  aria-label={filled ? `${tab.label}，有内容` : `${tab.label}，暂无内容`}
                  className={cn("shrink-0", !filled && "text-muted-foreground")}
                >
                  <Icon className="h-3.5 w-3.5" aria-hidden="true" />
                  {tab.label}
                  {filled ? (
                    <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden="true" />
                  ) : null}
                </TabsTrigger>
              );
            })}
          </TabsList>

          {COLLAB_TABS.map((tab) => {
            const doc = docsByTab[tab.value];
            return (
              <TabsContent key={tab.value} value={tab.value}>
                {doc ? <CollabDocSection issueId={issueId} tab={tab} doc={doc} readOnly={readOnly} /> : <EmptyTabPanel />}
              </TabsContent>
            );
          })}
        </Tabs>
      ) : null}
    </div>
  );
}
