import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CloseAllDoneButton } from "./close-all-done-button";

vi.mock("@/lib/hooks/use-issues", () => ({
  useBatchClosePreviewCount: () => ({
    data: 2,
    isLoading: false,
  }),
  useCloseIssuesBatch: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
}));

describe("CloseAllDoneButton select label", () => {
  it("shows source label in the closed select trigger", async () => {
    const user = userEvent.setup();
    render(<CloseAllDoneButton projectId="proj-1" />);

    await user.click(screen.getByRole("button", { name: /关闭全部/ }));

    const trigger = screen.getByRole("combobox");
    expect(trigger).toHaveTextContent("内部问题");
    expect(trigger).not.toHaveTextContent(/^internal$/);
  });
});
