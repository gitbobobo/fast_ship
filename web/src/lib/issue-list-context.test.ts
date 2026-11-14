import { describe, expect, it } from "vitest";
import {
  buildBoardSourceSearchParams,
  buildIssueDetailSearchParams,
  readIssueSourceContext,
} from "./issue-list-context";

function parse(search: string) {
  return readIssueSourceContext(new URLSearchParams(search));
}

describe("readIssueSourceContext", () => {
  it("returns none when no issue params exist", () => {
    expect(parse("")).toEqual({ kind: "none" });
    expect(parse("project=p1&other=x")).toEqual({ kind: "none" });
  });

  it("parses a valid board context", () => {
    expect(
      parse("issue_from=board&issue_workflow_status=todo&issue_label=bug&issue_source=github"),
    ).toEqual({
      kind: "board",
      workflowStatus: "todo",
      label: "bug",
      source: "github",
    });
  });

  it("encodes the unset column and normalizes all filters in a board context", () => {
    expect(
      parse("issue_from=board&issue_workflow_status=unset&issue_label=all&issue_source=all"),
    ).toEqual({
      kind: "board",
      workflowStatus: "unset",
      label: "",
      source: "",
    });
  });

  it("rejects board context with missing or invalid workflow status", () => {
    expect(parse("issue_from=board")).toEqual({ kind: "none" });
    expect(parse("issue_from=board&issue_workflow_status=archived")).toEqual({
      kind: "none",
    });
  });

  it("rejects unknown issue_from values", () => {
    expect(parse("issue_from=list&issue_state=open")).toEqual({ kind: "none" });
  });

  it("ignores q/sort mixed into a board URL", () => {
    const context = parse(
      "issue_from=board&issue_workflow_status=done&issue_q=crash&issue_sort=created_desc",
    );
    expect(context).toEqual({
      kind: "board",
      workflowStatus: "done",
      label: "",
      source: "",
    });
  });

  it("parses legacy list context from issue_* params", () => {
    expect(
      parse(
        "issue_state=open&issue_q=crash&issue_label=bug&issue_source=github&issue_workflow_status=in_progress&issue_sort=updated_asc&issue_page=2",
      ),
    ).toEqual({
      kind: "list",
      state: "open",
      q: "crash",
      label: "bug",
      source: "github",
      workflowStatus: "in_progress",
      sort: "updated_asc",
      page: 2,
    });
  });

  it("normalizes all values and clamps page in list context", () => {
    expect(parse("issue_state=all&issue_label=all&issue_source=all&issue_page=0")).toEqual({
      kind: "list",
      state: "",
      q: "",
      label: "",
      source: "",
      workflowStatus: "",
      sort: "updated_desc",
      page: 1,
    });
  });
});

describe("buildBoardSourceSearchParams", () => {
  it("writes board params and drops all/empty filters", () => {
    expect(
      buildBoardSourceSearchParams({
        workflowStatus: "unset",
        label: "",
        source: "all",
      }).toString(),
    ).toBe("issue_from=board&issue_workflow_status=unset");
  });

  it("keeps concrete filters", () => {
    expect(
      buildBoardSourceSearchParams({
        workflowStatus: "todo",
        label: "bug",
        source: "github",
      }).toString(),
    ).toBe(
      "issue_from=board&issue_workflow_status=todo&issue_label=bug&issue_source=github",
    );
  });
});

describe("buildIssueDetailSearchParams", () => {
  it("keeps the list-page round trip stable", () => {
    const search = buildIssueDetailSearchParams({
      state: "open",
      q: "crash",
      label: "bug",
      source: "github",
      workflowStatus: "in_progress",
      sort: "updated_desc",
      page: 2,
    }).toString();
    expect(parse(search)).toEqual({
      kind: "list",
      state: "open",
      q: "crash",
      label: "bug",
      source: "github",
      workflowStatus: "in_progress",
      sort: "updated_desc",
      page: 2,
    });
  });

  it("omits default page and all filters", () => {
    expect(
      buildIssueDetailSearchParams({
        state: "open",
        label: "all",
        source: "all",
        sort: "updated_desc",
        page: 1,
      }).toString(),
    ).toBe("issue_state=open&issue_sort=updated_desc");
  });
});
