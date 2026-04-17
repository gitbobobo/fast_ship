import { useMemo } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ArrowLeft } from "lucide-react";
import { Header } from "@/components/layout/header";
import { InternalIssueForm, type InternalIssueFormInput } from "@/components/issues/internal-issue-form";
import { Button } from "@/components/ui/button";

import { useCreateIssue } from "@/lib/hooks/use-issues";
import { buildIssueDetailSearchParams } from "@/lib/issue-list-context";
import { toast } from "sonner";

function buildIssueDetailSearch(searchParams: URLSearchParams) {
  return buildIssueDetailSearchParams({
    state: searchParams.get("state") ?? "",
    q: searchParams.get("q") ?? "",
    label: searchParams.get("label") ?? "",
    assignee: searchParams.get("assignee") ?? "",
    milestone: searchParams.get("milestone") ?? "",
    workflowStatus: searchParams.get("workflow_status") ?? "",
    sort: searchParams.get("sort") ?? "updated_desc",
    page: Math.max(Number(searchParams.get("page") ?? "1") || 1, 1),
  }).toString();
}

export default function NewInternalIssuePage() {
  const navigate = useNavigate();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const createIssue = useCreateIssue(id!);

  const issuesSearch = useMemo(() => {
    const next = new URLSearchParams(searchParams);
    if (id && !next.has("project")) {
      next.set("project", id);
    }
    return next.toString();
  }, [id, searchParams]);

  const issueDetailSearch = useMemo(
    () => buildIssueDetailSearch(searchParams),
    [searchParams],
  );

  const handleCancel = () => {
    navigate({
      pathname: "/issues",
      search: issuesSearch ? `?${issuesSearch}` : "",
    });
  };

  const handleSubmit = async (values: InternalIssueFormInput) => {
    try {
      const res = await createIssue.mutateAsync(values);
      toast.success("内部问题已创建");
      navigate({
        pathname: `/projects/${id}/issues/${res.data.id}`,
        search: issueDetailSearch ? `?${issueDetailSearch}` : "",
      });
    } catch {
      toast.error("创建内部问题失败");
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
          showWorkflowStatus
          isSubmitting={createIssue.isPending}
          onCancel={handleCancel}
          onSubmit={handleSubmit}
          submitLabel="创建问题"
        />
      </div>
    </>
  );
}
