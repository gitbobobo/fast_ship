import { useState } from "react";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GithubTokenCreateDialog } from "@/components/settings/github-token-create-dialog";
import { renderWithRoute } from "@/test/render";
import { useCreateGithubToken } from "@/lib/hooks/use-github-tokens";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/hooks/use-github-tokens", () => ({
  useCreateGithubToken: vi.fn(),
}));

function Harness() {
  const [open, setOpen] = useState(true);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        重新打开
      </button>
      <GithubTokenCreateDialog open={open} onOpenChange={setOpen} />
    </>
  );
}

describe("GithubTokenCreateDialog", () => {
  beforeEach(() => {
    vi.mocked(useCreateGithubToken).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useCreateGithubToken>);
  });

  it("clears the token field when closed via Escape and reopened", async () => {
    const user = userEvent.setup();
    renderWithRoute(<Harness />, { path: "/", initialEntry: "/" });

    const tokenInput = screen.getByLabelText("Token");
    await user.type(tokenInput, "ghp_should_not_stick");
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "重新打开" }));

    expect(screen.getByLabelText("Token")).toHaveValue("");
    expect(screen.getByLabelText("标题")).toHaveValue("");
  });
});
