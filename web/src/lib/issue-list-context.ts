export interface IssueListContext {
  state?: string;
  q?: string;
  label?: string;
  assignee?: string;
  milestone?: string;
  workflowStatus?: string;
  sort?: string;
  page?: number;
}

const ISSUE_DETAIL_SEARCH_PARAM_MAP = {
  state: "issue_state",
  q: "issue_q",
  label: "issue_label",
  assignee: "issue_assignee",
  milestone: "issue_milestone",
  workflowStatus: "issue_workflow_status",
  sort: "issue_sort",
  page: "issue_page",
} as const;

export function buildIssueDetailSearchParams(context: IssueListContext) {
  const params = new URLSearchParams();

  if (context.state) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.state, context.state);
  }
  if (context.q?.trim()) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.q, context.q.trim());
  }
  if (context.label && context.label !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.label, context.label);
  }
  if (context.assignee && context.assignee !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.assignee, context.assignee);
  }
  if (context.milestone && context.milestone !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.milestone, context.milestone);
  }
  if (context.workflowStatus && context.workflowStatus !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.workflowStatus, context.workflowStatus);
  }
  if (context.sort) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.sort, context.sort);
  }
  if ((context.page ?? 1) > 1) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.page, String(context.page));
  }

  return params;
}

export function readIssueDetailContext(
  searchParams: URLSearchParams,
): Required<IssueListContext> {
  return {
    state: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.state) ?? "",
    q: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.q) ?? "",
    label: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.label) ?? "",
    assignee: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.assignee) ?? "",
    milestone: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.milestone) ?? "",
    workflowStatus:
      searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.workflowStatus) ?? "",
    sort: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.sort) ?? "updated_desc",
    page: Math.max(
      Number(searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.page) ?? "1") || 1,
      1,
    ),
  };
}
