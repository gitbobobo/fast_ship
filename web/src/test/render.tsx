import type { ReactElement, ReactNode } from "react";
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";

export function renderWithRoute(
  ui: ReactElement,
  {
    path = "/",
    initialEntry = "/",
  }: {
    path?: string;
    initialEntry?: string;
  } = {},
) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path={path} element={ui} />
      </Routes>
    </MemoryRouter>,
  );
}

export function wrapWithRouter(children: ReactNode) {
  return <MemoryRouter>{children}</MemoryRouter>;
}
