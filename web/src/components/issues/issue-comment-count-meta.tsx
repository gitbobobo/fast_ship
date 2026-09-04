import { MessageSquare } from "lucide-react";

interface IssueCommentCountMetaProps {
  commentsCount: number;
  unreadCount: number;
}

/** 列表行：「N 条评论 · M 条新」，未读数用 GitHub 强调蓝 */
export function IssueCommentCountMeta({
  commentsCount,
  unreadCount,
}: IssueCommentCountMetaProps) {
  return (
    <span className="inline-flex items-center gap-1">
      <MessageSquare className="h-3 w-3" />
      {commentsCount} 条评论
      {unreadCount > 0 && (
        <>
          <span aria-hidden="true">·</span>
          <span
            data-testid="issue-unread-comments"
            className="font-medium text-github-accent"
          >
            {unreadCount} 条新
          </span>
        </>
      )}
    </span>
  );
}
