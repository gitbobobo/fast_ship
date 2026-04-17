import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft, FilePenLine, LockKeyhole, MessageSquare } from "lucide-react";
import { Header } from "@/components/layout/header";
import { InternalIssueForm, type InternalIssueFormInput } from "@/components/issues/internal-issue-form";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useIssue, useUpdateIssue } from "@/lib/hooks/use-issues";
import { toast } from "sonner";

export default function EditInternalIssuePage() {
  const navigate = useNavigate();
  const { id, iid } = useParams();
  const [searchParams] = useSearchParams();
  const { data: issue, isLoading } = useIssue(iid!);
  const updateIssue = useUpdateIssue(iid!, id);
  const issueDetailSearch = searchParams.toString();

  const backToDetail = () => {
    navigate({
      pathname: `/projects/${id}/issues/${iid}`,
      search: issueDetailSearch ? `?${issueDetailSearch}` : "",
    });
  };

  const handleSubmit = async (values: InternalIssueFormInput) => {
    try {
      await updateIssue.mutateAsync({
        title: values.title,
        body: values.body,
      });
      toast.success("内部问题已更新");
      backToDetail();
    } catch {
      toast.error("更新内部问题失败");
    }
  };

  if (isLoading) {
    return (
      <>
        <Header title="编辑问题" />
        <div className="mx-auto max-w-4xl space-y-5 p-4 md:p-6">
          <Skeleton className="h-8 w-28 rounded-lg" />
          <Skeleton className="h-[560px] rounded-2xl" />
        </div>
      </>
    );
  }

  if (!issue) {
    return (
      <>
        <Header title="编辑问题" />
        <div className="mx-auto max-w-3xl p-4 md:p-6">
          <Card>
            <CardContent className="space-y-4 p-6 text-center">
              <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <MessageSquare className="h-5 w-5 text-muted-foreground" />
              </div>
              <div className="space-y-1">
                <p className="text-base font-semibold">问题不存在</p>
                <p className="text-sm text-muted-foreground">该问题可能已被删除或您没有访问权限。</p>
              </div>
              <div className="flex justify-center">
                <Button
                  variant="outline"
                  render={<Link to={id ? `/projects/${id}/issues/${iid}` : "/issues"} />}
                >
                  返回
                </Button>
              </div>
            </CardContent>
          </Card>
        </div>
      </>
    );
  }

  if (issue.source !== "internal") {
    return (
      <>
        <Header title="编辑问题" />
        <div className="mx-auto max-w-3xl p-4 md:p-6">
          <Card className="border-amber-500/20 bg-amber-500/5">
            <CardHeader className="space-y-3 border-b py-5">
              <div className="flex items-center gap-3">
                <div className="flex h-10 w-10 items-center justify-center rounded-2xl bg-background shadow-sm">
                  <LockKeyhole className="h-4 w-4 text-amber-600" />
                </div>
                <div>
                  <CardTitle>GitHub 问题为只读</CardTitle>
                  <CardDescription>
                    这个页面只用于编辑内部问题，GitHub 同步来的问题不能在 Fast Ship 内直接修改。
                  </CardDescription>
                </div>
              </div>
            </CardHeader>
            <CardContent className="p-5">
              <Button variant="outline" onClick={backToDetail}>
                <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
                返回问题详情
              </Button>
            </CardContent>
          </Card>
        </div>
      </>
    );
  }

  return (
    <>
      <Header title={`编辑 ${issue.reference}`} />
      <div className="mx-auto grid max-w-6xl gap-6 p-4 md:grid-cols-[minmax(0,1fr)_300px] md:p-6">
        <div className="space-y-5">
          <div className="flex flex-wrap items-center gap-3">
            <Button variant="outline" size="sm" onClick={backToDetail}>
              <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
              返回详情
            </Button>
            <span className="inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium text-muted-foreground">
              {issue.reference}
            </span>
          </div>

          <Card className="border-foreground/10 shadow-sm">
            <CardHeader className="space-y-3 border-b py-5">
              <div className="flex items-center gap-3">
                <div className="flex h-11 w-11 items-center justify-center rounded-2xl bg-primary/10 text-primary">
                  <FilePenLine className="h-5 w-5" />
                </div>
                <div>
                  <CardTitle>编辑内部问题</CardTitle>
                  <CardDescription>
                    更新标题或描述。评论、关闭状态和内部状态仍然在详情页处理。
                  </CardDescription>
                </div>
              </div>
            </CardHeader>
            <CardContent className="p-5 md:p-6">
              <InternalIssueForm
                defaultValues={{
                  title: issue.title,
                  body: issue.body,
                  workflow_status: issue.internal_meta?.workflow_status ?? "todo",
                }}
                isSubmitting={updateIssue.isPending}
                onCancel={backToDetail}
                onSubmit={handleSubmit}
                submitLabel="保存修改"
              />
            </CardContent>
          </Card>
        </div>

        <aside>
          <Card className="bg-muted/30">
            <CardHeader className="space-y-1 border-b py-5">
              <CardTitle>这次会更新什么</CardTitle>
              <CardDescription>仅更新内部问题本身，不会影响已有评论和动态。</CardDescription>
            </CardHeader>
            <CardContent className="space-y-3 p-5 text-sm text-muted-foreground">
              <div className="rounded-2xl border bg-background p-4">
                标题适合写成一句可以单独转发的结论，描述里再补过程和背景。
              </div>
              <div className="rounded-2xl border bg-background p-4">
                如果只是切换内部状态或关闭问题，直接在详情页操作会更快。
              </div>
            </CardContent>
          </Card>
        </aside>
      </div>
    </>
  );
}
