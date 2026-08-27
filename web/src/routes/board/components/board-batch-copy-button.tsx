import { ChevronDown, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { copyWithToast } from "@/lib/copy";
import { buildIssuePromptBatch } from "@/lib/issue-prompt";
import { useIssuePromptList } from "@/lib/hooks/use-issue-prompt";
import { IssuePromptPicker } from "@/components/issues/issue-prompt-picker";

interface BoardBatchCopyButtonProps {
  projectId: string;
  selectedCount: number;
  getOrderedSelectedIssues: () => Issue[];
  /** 复制成功后的回调（看板页传 clearSelection：清空勾选但留在多选模式）。 */
  onCopied: () => void;
}

/**
 * 多选模式下的批量复制提示词按钮。
 *
 * 只列出 supports_batch 的模板；恰好一个时直接复制，多个时下拉选择。
 * 复制成功后清空勾选（onCopied），失败保持勾选。
 */
export function BoardBatchCopyButton({
  projectId,
  selectedCount,
  getOrderedSelectedIssues,
  onCopied,
}: BoardBatchCopyButtonProps) {
  const issuePrompts = useIssuePromptList();
  const batchPrompts = issuePrompts.filter((prompt) => prompt.supports_batch);

  // 两种禁用原因分开提示：未选择 / 无可批量模板
  const disabledReason =
    selectedCount === 0
      ? "先选择要复制的问题"
      : batchPrompts.length === 0
        ? "没有可用于批量的提示词"
        : null;

  const handleCopy = async (content: string) => {
    const issueIds = getOrderedSelectedIssues().map((issue) => issue.id);
    const copied = await copyWithToast(
      buildIssuePromptBatch({ projectId, content, issueIds }),
      "已复制提示词",
    );
    if (copied) {
      onCopied();
    }
  };

  // 禁用时 title 放在包裹元素上（disabled button 不响应 hover）
  if (disabledReason !== null) {
    return (
      <span className="inline-flex" title={disabledReason}>
        <Button variant="outline" size="sm" disabled>
          <Copy className="mr-1.5 h-3.5 w-3.5" />
          复制提示词
        </Button>
      </span>
    );
  }

  return (
    <IssuePromptPicker
      prompts={batchPrompts}
      onSelectContent={(content) => void handleCopy(content)}
      renderSingle={(select) => (
        <Button variant="outline" size="sm" onClick={select}>
          <Copy className="mr-1.5 h-3.5 w-3.5" />
          复制提示词
        </Button>
      )}
      renderDropdownTrigger={() => (
        <Button variant="outline" size="sm">
          <Copy className="mr-1.5 h-3.5 w-3.5" />
          复制提示词
          <ChevronDown className="h-3.5 w-3.5" />
        </Button>
      )}
    />
  );
}
