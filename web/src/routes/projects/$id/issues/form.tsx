import { useMemo } from "react";
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
import {
  useCreateIssue,
  useIssue,
  useUpdateIssue,
  useUploadDraftIssueAsset,
  useUploadIssueAsset,
} from "@/lib/hooks/use-issues";
import { buildIssueDetailSearchParams } from "@/lib/issue-list-context";
import { toast } from "sonner";

function buildCreatedIssueDetailSearch(issue: Issue) {
  return buildIssueDetailSearchParams({
    state: issue.state,
    source: issue.source,
    workflowStatus: issue.internal_meta?.workflow_status ?? "",
    sort: "updated_desc",
    page: 1,
  }).toString();
}

export default function IssueFormPage() {
  const navigate = useNavigate();
  const { id, iid } = useParams();
  const [searchParams] = useSearchParams();
  const isEdit = Boolean(iid);

  const { data: issue, isLoading } = useIssue(iid ?? "");
  const updateIssue = useUpdateIssue(iid ?? "", id);
  const uploadIssueAsset = useUploadIssueAsset(iid ?? "");
  const createIssue = useCreateIssue(id!);
  const uploadDraftIssueAsset = useUploadDraftIssueAsset(id!);

  const issueDetailSearch = searchParams.toString();

  const issuesSearch = useMemo(() => {
    const next = new URLSearchParams(searchParams);
    if (id && !next.has("project")) {
      next.set("project", id);
    }
    return next.toString();
  }, [id, searchParams]);

  const handleCancel = () => {
    if (isEdit) {
      navigate(
        {
          pathname: `/projects/${id}/issues/${iid}`,
          search: issueDetailSearch ? `?${issueDetailSearch}` : "",
        },
        { replace: true },
      );
    } else {
      navigate({
        pathname: "/issues",
        search: issuesSearch ? `?${issuesSearch}` : "",
      });
    }
  };

  const handleSubmit = async (values: InternalIssueFormInput) => {
    if (isEdit) {
      try {
        await updateIssue.mutateAsync({
          title: values.title,
          body: values.body,
        });
        toast.success("内部问题已更新");
        navigate(
          {
            pathname: `/projects/${id}/issues/${iid}`,
            search: issueDetailSearch ? `?${issueDetailSearch}` : "",
          },
          { replace: true },
        );
      } catch {
        toast.error("更新内部问题失败");
      }
    } else {
      try {
        const res = await createIssue.mutateAsync(values);
        const nextIssueDetailSearch = buildCreatedIssueDetailSearch(res.data);
        toast.success("内部问题已创建");
        navigate(
          {
            pathname: `/projects/${id}/issues/${res.data.id}`,
            search: nextIssueDetailSearch ? `?${nextIssueDetailSearch}` : "",
          },
          { replace: true },
        );
      } catch {
        toast.error("创建内部问题失败");
      }
    }
  };

  const handlePasteImage = async (file: File) => {
    const formData = new FormData();
    formData.append("file", file, file.name || "image.png");

    try {
      const res = isEdit
        ? await uploadIssueAsset.mutateAsync(formData)
        : await uploadDraftIssueAsset.mutateAsync(formData);
      return res.data.markdown;
    } catch {
      toast.error("上传图片失败");
      throw new Error("upload failed");
    }
  };

  if (isEdit && isLoading) {
    return (
      <>
        <Header title="编辑问题" />
        <div className="w-full px-4 py-4 md:px-6 md:py-6">
          <Skeleton className="mb-6 h-8 w-28 rounded-lg" />
          <Skeleton className="h-[calc(100vh-220px)] rounded-2xl" />
        </div>
      </>
    );
  }

  if (isEdit && !issue) {
    return (
      <>
        <Header title="编辑问题" />
        <div className="w-full px-4 py-4 md:px-6 md:py-6">
          <Card>
            <CardContent className="space-y-4 p-6 text-center">
              <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <MessageSquare className="h-5 w-5 text-muted-foreground" />
              </div>
              <div className="space-y-1">
                <p className="text-base font-semibold">问题不存在</p>
                <p className="text-sm text-muted-foreground">
                  该问题可能已被删除或您没有访问权限。
                </p>
              </div>
              <div className="flex justify-center">
                <Button
                  variant="outline"
                  render={
                    <Link to={id ? `/projects/${id}/issues/${iid}` : "/issues"} />
                  }
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

  if (isEdit && issue && issue.source !== "internal") {
    return (
      <>
        <Header title="编辑问题" />
        <div className="w-full px-4 py-4 md:px-6 md:py-6">
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
              <Button variant="outline" onClick={handleCancel}>
                <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
                返回问题详情
              </Button>
            </CardContent>
          </Card>
        </div>
      </>
    );
  }

  const pageTitle = isEdit ? `编辑 ${issue?.reference}` : "新建内部问题";
  const submitLabel = isEdit ? "保存修改" : "创建问题";
  const backLabel = isEdit ? "返回详情" : "返回";

  return (
    <>
      <Header title={pageTitle} />
      <div className="w-full px-4 py-4 md:px-6 md:py-6">
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <Button variant="outline" size="sm" onClick={handleCancel}>
            <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
            {backLabel}
          </Button>
          {isEdit && issue && (
            <span className="inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-medium text-muted-foreground">
              {issue.reference}
            </span>
          )}
        </div>

        <div className="mb-6 flex items-center gap-3">
          <div className="flex h-10 w-10 items-center justify-center rounded-2xl bg-primary/10 text-primary">
            <FilePenLine className="h-5 w-5" />
          </div>
          <div>
            <h2 className="text-base font-semibold">
              {isEdit ? "编辑内部问题" : "新建内部问题"}
            </h2>
            <p className="text-sm text-muted-foreground">
              {isEdit
                ? "更新标题或描述。评论、关闭状态和进度任务清单仍然在详情页处理。"
                : "填写标题和描述来创建一个新的内部问题。"}
            </p>
          </div>
        </div>

        <InternalIssueForm
          defaultValues={
            isEdit && issue
              ? {
                  title: issue.title,
                  body: issue.body,
                  workflow_status: issue.internal_meta?.workflow_status || "todo",
                }
              : undefined
          }
          isSubmitting={isEdit ? updateIssue.isPending : createIssue.isPending}
          onPasteImage={handlePasteImage}
          onCancel={handleCancel}
          onSubmit={handleSubmit}
          submitLabel={submitLabel}
        />
      </div>
    </>
  );
}
