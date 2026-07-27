import { ChevronDown, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { copyWithToast } from "@/lib/copy";
import { buildIssuePrompt } from "@/lib/issue-prompt";
import { useIssuePromptList } from "@/lib/hooks/use-issue-prompt";

export function CopyIssuePromptButton({
  projectId,
  issueId,
}: {
  projectId: string;
  issueId: string;
}) {
  const issuePrompts = useIssuePromptList();

  const handleCopyIssuePrompt = async (content: string) => {
    await copyWithToast(
      buildIssuePrompt({ projectId, issueId, content }),
      "已复制提示词",
    );
  };

  if (issuePrompts.length === 1) {
    return (
      <Button
        variant="ghost"
        size="sm"
        className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground"
        onClick={() => void handleCopyIssuePrompt(issuePrompts[0].content)}
      >
        <Copy className="h-3.5 w-3.5" />
        复制提示词
      </Button>
    );
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground"
          >
            <Copy className="h-3.5 w-3.5" />
            复制提示词
            <ChevronDown className="h-3.5 w-3.5" />
          </Button>
        }
      />
      <DropdownMenuContent align="end">
        {issuePrompts.map((p) => (
          <DropdownMenuItem
            key={p.id}
            onClick={() => void handleCopyIssuePrompt(p.content)}
          >
            {p.name}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
