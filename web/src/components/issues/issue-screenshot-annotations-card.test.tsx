import { render, screen } from "@testing-library/react";
import { wrapWithRouter } from "@/test/render";
import { IssueScreenshotAnnotationsCard } from "./issue-screenshot-annotations-card";

function makeAnnotation(
  overrides: Partial<ScreenshotAnnotation>,
): ScreenshotAnnotation {
  return {
    id: "a-1",
    project_id: "p-1",
    screen_id: "s1",
    version_id: "v1",
    issue_id: "i-1",
    issue_reference: "INT-1",
    issue_title: "修样式",
    screen_key: "home",
    screen_title: "",
    screen_group: "",
    image_url: "",
    image_width: 0,
    image_height: 0,
    is_latest_version: true,
    x: 0.1,
    y: 0.1,
    width: 0.2,
    height: 0.2,
    pixel_rect: null,
    body: "按钮错位",
    status: "open",
    crop_url: "/api/screenshot-annotations/a-1/crop",
    created_by: "bobo",
    created_at: "2026-10-05T06:30:00Z",
    updated_at: "2026-10-05T06:30:00Z",
    resolved_at: null,
    ...overrides,
  };
}

describe("IssueScreenshotAnnotationsCard", () => {
  it("renders nothing without annotations", () => {
    const { container } = render(
      wrapWithRouter(
        <IssueScreenshotAnnotationsCard projectId="p-1" annotations={[]} />,
      ),
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("links each annotation to the canvas locate URL", () => {
    render(
      wrapWithRouter(
        <IssueScreenshotAnnotationsCard
          projectId="p-1"
          annotations={[
            makeAnnotation({}),
            makeAnnotation({ id: "a-2", body: "已处理", status: "resolved" }),
          ]}
        />,
      ),
    );

    expect(screen.getByText("截图标注")).toBeInTheDocument();
    // 无标题时回退展示 screen_key
    expect(screen.getAllByText("home")).toHaveLength(2);
    expect(screen.getByText("未解决")).toBeInTheDocument();
    expect(screen.getByText("已解决")).toBeInTheDocument();
    const link = screen.getByText("按钮错位").closest("a");
    expect(link).toHaveAttribute(
      "href",
      "/screenshots?project=p-1&view=canvas&annotation=a-1",
    );
  });
});
