import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft, LockKeyhole, MessageSquare } from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
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
import { useProject } from "@/lib/hooks/use-projects";
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
  const [formBusy, setFormBusy] = useState(false);

  const { data: issue, isLoading } = useIssue(iid ?? "");
  const { data: project } = useProject(id!);
  const updateIssue = useUpdateIssue(iid ?? "", id);
  const uploadIssueAsset = useUploadIssueAsset(iid ?? "");
  const createIssue = useCreateIssue(id!);
  const uploadDraftIssueAsset = useUploadDraftIssueAsset(id!);

  const issueDetailSearch = searchParams.toString();

  const issueDetailPath = id && iid ? `/projects/${id}/issues/${iid}` : null;
  const headerBackFallback = issueDetailPath ?? (id ? `/issues?project=${id}` : "/issues");
  const formId = isEdit ? "issue-form-edit" : "issue-form-new";

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
        const payload: Parameters<typeof createIssue.mutateAsync>[0] = {
          title: values.title,
          body: values.body,
          source: values.source,
        };
        if (values.source === "internal") {
          payload.workflow_status = values.workflow_status;
        }
        const res = await createIssue.mutateAsync(payload);
        const nextIssueDetailSearch = buildCreatedIssueDetailSearch(res.data);
        toast.success(values.source === "github" ? "GitHub 问题已创建" : "内部问题已创建");
        navigate(
          {
            pathname: `/projects/${id}/issues/${res.data.id}`,
            search: nextIssueDetailSearch ? `?${nextIssueDetailSearch}` : "",
          },
          { replace: true },
        );
      } catch {
        toast.error(values.source === "github" ? "创建 GitHub 问题失败" : "创建内部问题失败");
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
        <Header title="编辑问题" backFallback={headerBackFallback} />
        <div className="w-full px-4 py-4 md:px-6 md:py-6">
          <Skeleton className="h-[calc(100vh-220px)] rounded-2xl" />
        </div>
      </>
    );
  }

  if (isEdit && !issue) {
    return (
      <>
        <Header title="编辑问题" backFallback={headerBackFallback} />
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
        <Header title="编辑问题" backFallback={headerBackFallback} />
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
              <Button
                variant="outline"
                onClick={() => {
                  navigate(
                    {
                      pathname: `/projects/${id}/issues/${iid}`,
                      search: issueDetailSearch ? `?${issueDetailSearch}` : "",
                    },
                    { replace: true },
                  );
                }}
              >
                <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
                返回问题详情
              </Button>
            </CardContent>
          </Card>
        </div>
      </>
    );
  }

  const pageTitle = isEdit ? `编辑 ${issue?.reference}` : "新建问题";
  const submitLabel = isEdit ? "保存修改" : "创建问题";

  return (
    <>
      <Header
        title={pageTitle}
        backFallback={headerBackFallback}
        actions={
          <HeaderActions
            primary={
              <Button
                type="submit"
                form={formId}
                size="sm"
                disabled={formBusy}
                data-testid="issue-form-submit"
              >
                {formBusy ? "保存中..." : submitLabel}
              </Button>
            }
          />
        }
      />
      <div className="w-full px-4 py-4 md:px-6 md:py-6">
        <InternalIssueForm
          formId={formId}
          hideSubmitButton
          defaultValues={
            isEdit && issue
              ? {
                  title: issue.title,
                  body: issue.body,
                  workflow_status: issue.internal_meta?.workflow_status ?? "",
                  source: "internal",
                }
              : {
                  source: "internal",
                }
          }
          isSubmitting={isEdit ? updateIssue.isPending : createIssue.isPending}
          onBusyChange={setFormBusy}
          onPasteImage={handlePasteImage}
          onSubmit={handleSubmit}
          showSourceSelector={!isEdit}
          submitLabel={submitLabel}
          projectName={project?.name}
        />
      </div>
    </>
  );
}
