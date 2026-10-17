import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IssueRecommendationBanner } from "./issue-recommendation-banner";
import { useRestoreRecommendation } from "@/lib/hooks/use-recommendations";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-recommendations", () => ({
  useRestoreRecommendation: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
}));

const activeRecommendation: IssueRecommendation = {
  issue: {
    id: "issue-1",
    project_id: "proj-1",
    project_name: "Alpha App",
    source: "github",
    sequence_number: 42,
    reference: "GH-42",
    title: "Crash on launch",
    state: "open",
    workflow_status: "todo",
  },
  reason: "先做这个能解锁后续工作",
  priority: "high",
  status: "active",
  deferred_at: null,
  defer_note: null,
  created_by: "ci-bot",
  created_at: "2026-08-22T00:00:00Z",
  updated_at: "2026-08-22T00:00:00Z",
  dependencies: [
    {
      issue_id: "dep-1",
      title: "前置任务",
      state: "closed",
      workflow_status: "done",
      project_id: "proj-1",
      sequence_number: 1,
      reference: "GH-41",
    },
  ],
};

const deferredRecommendation: IssueRecommendation = {
  ...activeRecommendation,
  status: "deferred",
  deferred_at: "2026-08-20T00:00:00Z",
  defer_note: "等上游接口定稿",
  dependencies: [],
};

function renderBanner(recommendation: IssueRecommendation) {
  renderWithRoute(<IssueRecommendationBanner recommendation={recommendation} />);
}

describe("IssueRecommendationBanner", () => {
  beforeEach(() => {
    vi.mocked(useRestoreRecommendation).mockClear();
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  it("renders the active recommendation with priority and reason", () => {
    renderBanner(activeRecommendation);

    const banner = screen.getByRole("region", { name: "推荐" });
    expect(banner).toBeInTheDocument();
    expect(screen.getByText("推荐任务")).toBeInTheDocument();
    expect(screen.getByText("高优先级")).toBeInTheDocument();
    expect(screen.getByText("先做这个能解锁后续工作")).toBeInTheDocument();
  });

  it("renders dependencies as links to the dependency issue", () => {
    renderBanner(activeRecommendation);

    expect(screen.getByText("前置")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /GH-41/ })).toHaveAttribute(
      "href",
      "/projects/proj-1/issues/dep-1",
    );
  });

  it("renders the muted deferred banner with note and restore button", () => {
    renderBanner(deferredRecommendation);

    const banner = screen.getByRole("region", { name: "已延后推荐" });
    expect(banner).toBeInTheDocument();
    expect(screen.getByText(/已延后 · /)).toBeInTheDocument();
    expect(screen.getByText("等上游接口定稿")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "推荐" })).not.toBeInTheDocument();
    expect(
      screen.queryByText("先做这个能解锁后续工作"),
    ).not.toBeInTheDocument();
  });

  it("restores a deferred recommendation", async () => {
    const mutateAsync = vi.fn().mockResolvedValue(undefined);
    vi.mocked(useRestoreRecommendation).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useRestoreRecommendation>);

    renderBanner(deferredRecommendation);

    fireEvent.click(screen.getByRole("button", { name: "移回推荐" }));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalledWith("issue-1"));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("已移回推荐"));
  });

  it("shows an error toast when restoring fails", async () => {
    const mutateAsync = vi.fn().mockRejectedValue(new Error("boom"));
    vi.mocked(useRestoreRecommendation).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useRestoreRecommendation>);

    renderBanner(deferredRecommendation);

    fireEvent.click(screen.getByRole("button", { name: "移回推荐" }));

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("移回推荐失败"));
  });

  it("disables the restore button while the mutation is pending", () => {
    vi.mocked(useRestoreRecommendation).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: true,
    } as unknown as ReturnType<typeof useRestoreRecommendation>);

    renderBanner(deferredRecommendation);

    const button = screen.getByRole("button", { name: "移回中..." });
    expect(button).toBeDisabled();
  });
});
