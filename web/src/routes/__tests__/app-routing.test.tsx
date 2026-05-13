import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import { vi } from "vitest";

const { browserEntries } = vi.hoisted(() => ({
  browserEntries: ["/"] as string[],
}));

vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();

  return {
    ...actual,
    BrowserRouter: ({ children }: { children: ReactNode }) => (
      <actual.MemoryRouter initialEntries={browserEntries}>
        {children}
      </actual.MemoryRouter>
    ),
    Navigate: ({ to }: { to: string }) => (
      <div data-testid="navigate-target">{String(to)}</div>
    ),
  };
});

import App from "@/App";

describe("App routing defaults", () => {
  afterEach(() => {
    browserEntries.splice(0, browserEntries.length, "/");
  });

  it("redirects the app root to dashboard", async () => {
    browserEntries.splice(0, browserEntries.length, "/");

    render(<App />);

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/dashboard",
    );
  });

  it("redirects unmatched routes to dashboard", async () => {
    browserEntries.splice(0, browserEntries.length, "/does-not-exist");

    render(<App />);

    expect(await screen.findByTestId("navigate-target")).toHaveTextContent(
      "/dashboard",
    );
  });
});
