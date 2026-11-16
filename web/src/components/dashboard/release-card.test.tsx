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
});
