import { MessagesSquare } from "lucide-react";

/**
 * /threads 未选中线程时右栏的空态：提示从左栏挑选，本身不带标题栏，
 * 「新建线程」入口在左栏顶栏。
 */
export default function ThreadsPage() {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 py-16 text-center">
      <MessagesSquare
        aria-hidden
        className="h-8 w-8 text-muted-foreground"
      />
      <h2 className="text-base font-medium">从左侧选择一个线程查看详情</h2>
      <p className="max-w-xs text-sm text-muted-foreground">
        左栏可按标题搜索，并按环境、项目或状态筛选线程。
      </p>
    </div>
  );
}
