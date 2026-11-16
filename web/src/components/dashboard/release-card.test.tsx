import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { ReleaseCard } from "@/components/dashboard/release-card";

describe("ReleaseCard", () => {
  it("renders pending links, timeline and unmerged count", () => {
    render(
      <MemoryRouter>
        <ReleaseCard
          release={{
            pending_count: 2,
            pending_versions: [
              {
                version_id: "v1",
                version_number: "v2.0.0",
                project_id: "p1",
                project_name: "Alpha App",
                created_at: "2026-05-28T00:00:00Z",
                href: "/projects/p1/versions/v1",
              },
            ],
            recent_releases: [
              {
                version_id: "v0",
                version_number: "v1.9.0",
                project_id: "p1",
                project_name: "Alpha App",
                created_at: "2026-05-20T00:00:00Z",
                shipped_at: "2026-05-20T08:00:00Z",
                href: "/projects/p1/versions/v0",
              },
            ],
            unmerged_pr_issue_count: 3,
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("release-card")).toBeInTheDocument();
    expect(screen.getByText("待发版 2")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /v2\.0\.0/ }),
    ).toHaveAttribute("href", "/projects/p1/versions/v1");
    expect(screen.getByText(/距上次发版/)).toBeInTheDocument();
    expect(
      screen.getByText("3 个开启问题还有未合并的 PR"),
    ).toBeInTheDocument();
  });

  it("hides the last-release badge when the block is degraded even with recent releases", () => {
    // recent_releases 在降级载荷里是清零占位，不能据此推「距上次发版」
    //（panel P4）。
    render(
      <MemoryRouter>
        <ReleaseCard
          release={{
            complete: false,
            pending_count: 0,
            pending_versions: [],
            recent_releases: [
              {
                version_id: "v0",
                version_number: "v1.9.0",
                project_id: "p1",
                project_name: "Alpha App",
                created_at: "2026-05-20T00:00:00Z",
                shipped_at: "2026-05-20T08:00:00Z",
                href: "/projects/p1/versions/v0",
              },
            ],
            unmerged_pr_issue_count: 0,
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByText(/距上次发版/)).not.toBeInTheDocument();
    expect(screen.getByTestId("release-card")).toHaveTextContent("暂不可用");
  });

  it("computes the last-release badge by local natural day (PR#215 Note3)", () => {
    // 昨天本地 00:05 发版：自然日差是 1 天，不能因不足 24 小时显示「今天」。
    const yesterday = new Date();
    yesterday.setDate(yesterday.getDate() - 1);
    yesterday.setHours(0, 5, 0, 0);
    render(
      <MemoryRouter>
        <ReleaseCard
          release={{
            pending_count: 0,
            pending_versions: [],
            recent_releases: [
              {
                version_id: "v0",
                version_number: "v1.9.0",
                project_id: "p1",
                project_name: "Alpha App",
                created_at: yesterday.toISOString(),
                shipped_at: yesterday.toISOString(),
                href: "/projects/p1/versions/v0",
              },
            ],
            unmerged_pr_issue_count: 0,
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("距上次发版 1 天")).toBeInTheDocument();
  });

  it("shows 时间未知 for releases whose shipped_at is null (panel round2 F1)", () => {
    render(
      <MemoryRouter>
        <ReleaseCard
          release={{
            pending_count: 0,
            pending_versions: [],
            recent_releases: [
              {
                version_id: "v0",
                version_number: "v1.9.0",
                project_id: "p1",
                project_name: "Alpha App",
                created_at: "2026-05-20T00:00:00Z",
                shipped_at: null,
                href: "/projects/p1/versions/v0",
              },
            ],
            unmerged_pr_issue_count: 0,
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("时间未知")).toBeInTheDocument();
    expect(screen.queryByText(/距上次发版/)).not.toBeInTheDocument();
  });
});
