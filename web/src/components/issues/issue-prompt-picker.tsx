import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";

interface IssuePromptPickerProps {
  prompts: readonly IssuePrompt[];
  onSelectContent: (content: string) => void;
  renderSingle: (select: () => void) => React.ReactNode;
  renderDropdownTrigger: () => React.ReactElement;
}

/**
 * 给定一组提示词模板：恰好一条时直接按钮，多条时下拉选择 content。
 * 不负责拉取模板、过滤 supports_batch 或复制逻辑。
 */
export function IssuePromptPicker({
  prompts,
  onSelectContent,
  renderSingle,
  renderDropdownTrigger,
}: IssuePromptPickerProps) {
  if (prompts.length === 0) {
    return null;
  }

  if (prompts.length === 1) {
    return renderSingle(() => void onSelectContent(prompts[0].content));
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={renderDropdownTrigger()} />
      <DropdownMenuContent align="end">
        {prompts.map((prompt) => (
          <DropdownMenuItem
            key={prompt.id}
            onClick={() => void onSelectContent(prompt.content)}
          >
            {prompt.name}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
