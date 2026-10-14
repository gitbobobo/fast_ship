import { ChevronDown, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { copyWithToast } from "@/lib/copy";
import { buildIssuePrompt } from "@/lib/issue-prompt";
import { useIssuePromptList } from "@/lib/hooks/use-issue-prompt";
import { IssuePromptPicker } from "@/components/issues/issue-prompt-picker";

/** 复制单个问题的提示词；传入 reason 时把推荐理由一并写进提示词。 */
export function CopyIssuePromptButton({
  projectId,
  issueId,
  reason,
}: {
  projectId: string;
  issueId: string;
  reason?: string;
}) {
  const issuePrompts = useIssuePromptList();

  const handleCopyIssuePrompt = async (content: string) => {
    await copyWithToast(
      buildIssuePrompt({ projectId, issueId, content, reason }),
      "已复制提示词",
    );
  };

  return (
    <IssuePromptPicker
      prompts={issuePrompts}
      onSelectContent={(content) => void handleCopyIssuePrompt(content)}
      renderSingle={(select) => (
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground"
          onClick={select}
        >
          <Copy className="h-3.5 w-3.5" />
          复制提示词
        </Button>
      )}
      renderDropdownTrigger={() => (
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground"
        >
          <Copy className="h-3.5 w-3.5" />
          复制提示词
          <ChevronDown className="h-3.5 w-3.5" />
        </Button>
      )}
    />
  );
}
