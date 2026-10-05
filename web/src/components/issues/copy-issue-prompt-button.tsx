import { useCallback, useImperativeHandle, useRef, type Ref } from "react";
import { ChevronDown, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { copyWithToast } from "@/lib/copy";
import { buildIssuePrompt } from "@/lib/issue-prompt";
import { useIssuePromptList } from "@/lib/hooks/use-issue-prompt";
import { IssuePromptPicker } from "@/components/issues/issue-prompt-picker";

export interface CopyIssuePromptButtonHandle {
  /** 键盘入口语义：只有一个模板时直接复制，多个模板时展开选择器。 */
  activate: () => void;
}

/** 复制单个问题的提示词；传入 reason 时把推荐理由一并写进提示词。 */
export function CopyIssuePromptButton({
  projectId,
  issueId,
  reason,
  handleRef,
}: {
  projectId: string;
  issueId: string;
  reason?: string;
  /** 供外层键盘快捷键（如推荐弹框的 Enter）触发与本按钮一致的复制/选择器行为。 */
  handleRef?: Ref<CopyIssuePromptButtonHandle>;
}) {
  const issuePrompts = useIssuePromptList();
  const dropdownTriggerRef = useRef<HTMLButtonElement | null>(null);

  const handleCopyIssuePrompt = useCallback(
    async (content: string) => {
      await copyWithToast(
        buildIssuePrompt({ projectId, issueId, content, reason }),
        "已复制提示词",
      );
    },
    [projectId, issueId, reason],
  );

  useImperativeHandle(
    handleRef,
    (): CopyIssuePromptButtonHandle => ({
      activate() {
        if (issuePrompts.length === 1) {
          void handleCopyIssuePrompt(issuePrompts[0].content);
        } else if (issuePrompts.length > 1) {
          dropdownTriggerRef.current?.click();
        }
      },
    }),
    [issuePrompts, handleCopyIssuePrompt],
  );

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
          ref={dropdownTriggerRef}
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
