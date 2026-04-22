import { useMemo } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { Header } from "@/components/layout/header";
import { InternalIssueForm, type InternalIssueFormInput } from "@/components/issues/internal-issue-form";
import { Button } from "@/components/ui/button";

import { useCreateIssue, useUploadDraftIssueAsset } from "@/lib/hooks/use-issues";
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

export default function NewInternalIssuePage() {
  const navigate = useNavigate();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const createIssue = useCreateIssue(id!);
  const uploadDraftIssueAsset = useUploadDraftIssueAsset(id!);

  const issuesSearch = useMemo(() => {
    const next = new URLSearchParams(searchParams);
    if (id && !next.has("project")) {
      next.set("project", id);
    }
    return next.toString();
  }, [id, searchParams]);

  const handleCancel = () => {
    navigate({
      pathname: "/issues",
      search: issuesSearch ? `?${issuesSearch}` : "",
    });
  };

  const handleSubmit = async (values: InternalIssueFormInput) => {
    try {
      const res = await createIssue.mutateAsync(values);
      const issueDetailSearch = buildCreatedIssueDetailSearch(res.data);
      toast.success("内部问题已创建");
      navigate(
        {
          pathname: `/projects/${id}/issues/${res.data.id}`,
          search: issueDetailSearch ? `?${issueDetailSearch}` : "",
        },
        { replace: true },
      );
    } catch {
      toast.error("创建内部问题失败");
    }
  };

  const handlePasteImage = async (file: File) => {
    const formData = new FormData();
    formData.append("file", file, file.name || "image.png");

    try {
      const res = await uploadDraftIssueAsset.mutateAsync(formData);
      return res.data.markdown;
    } catch {
      toast.error("上传图片失败");
      throw new Error("upload failed");
    }
  };

  return (
    <>
      <Header title="新建内部问题" />
      <div className="w-full px-4 py-4 md:px-8 md:py-6 lg:px-12 lg:py-8">
        <div className="mb-6">
          <Button
            variant="outline"
            size="sm"
            render={<Link to={{ pathname: "/issues", search: issuesSearch ? `?${issuesSearch}` : "" }} />}
          >
            <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
            返回
          </Button>
        </div>

        <InternalIssueForm
          isSubmitting={createIssue.isPending}
          onCancel={handleCancel}
          onPasteImage={handlePasteImage}
          onSubmit={handleSubmit}
          submitLabel="创建问题"
        />
      </div>
    </>
  );
}
