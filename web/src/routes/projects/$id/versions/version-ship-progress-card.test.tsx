import { render, screen } from "@testing-library/react";
import { VersionShipProgressCard } from "./version-ship-progress-card";

function makeVersion(
  overrides: Partial<Version> = {},
): Version {
  return {
    id: "ver-1",
    project_id: "proj-1",
    version_number: "v1.2.0",
    status: "pending",
    release_notes: null,
    target_commitish: "main",
    github_release_url: null,
    error_log: null,
    ship_status: "in_progress",
    ship_stage: "upload_assets",
    ship_message: "正在上传安装包",
    created_at: "2026-04-06T10:00:00Z",
    shipped_at: null,
    ...overrides,
  };
}

describe("VersionShipProgressCard", () => {
  it("renders progress steps when shipping is in progress", () => {
    render(
      <VersionShipProgressCard
        version={makeVersion()}
        isPending
        isShipping
      />,
    );

    expect(screen.getByText("发货进度")).toBeInTheDocument();
    expect(screen.getByText("创建 Git Tag")).toBeInTheDocument();
    expect(screen.getByText("上传安装包")).toBeInTheDocument();
    expect(screen.getByText("进行中")).toBeInTheDocument();
    expect(screen.getByText("正在上传安装包")).toBeInTheDocument();
  });

  it("renders failed state when ship_status is failed", () => {
    render(
      <VersionShipProgressCard
        version={makeVersion({
          ship_status: "failed",
          ship_stage: "create_release",
          ship_message: "创建 Release 失败",
        })}
        isPending
        isShipping={false}
      />,
    );

    expect(screen.getByText("失败")).toBeInTheDocument();
    expect(screen.getByText("创建 Release 失败")).toBeInTheDocument();
  });

  it("does not render when version is not pending", () => {
    const { container } = render(
      <VersionShipProgressCard
        version={makeVersion({
          status: "shipped",
          ship_status: "completed",
        })}
        isPending={false}
        isShipping={false}
      />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  it("does not render when there is no shipping activity", () => {
    const { container } = render(
      <VersionShipProgressCard
        version={makeVersion({
          ship_status: "",
          ship_stage: "",
          ship_message: null,
        })}
        isPending
        isShipping={false}
      />,
    );

    expect(container).toBeEmptyDOMElement();
  });
});
