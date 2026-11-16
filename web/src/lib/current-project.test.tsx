import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  matchRouteProjectId,
  useCurrentProjectId,
  useRouteProject,
  useSyncRouteProjectPreference,
} from "@/lib/current-project";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";

const { projectsMock } = vi.hoisted(() => ({ projectsMock: vi.fn() }));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: projectsMock,
}));

function project(id: string, is_shelved = false): Project {
  return { id, is_shelved } as Project;
}

function CurrentProbe({ projects }: { projects: Project[] }) {
  const id = useCurrentProjectId(projects);
  return <div data-testid="current">{id === null ? "null" : id}</div>;
}

function renderCurrent(entry: string, projects: Project[]) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <CurrentProbe projects={projects} />
    </MemoryRouter>,
  );
}

function RouteProbe() {
  const { projectId, project, isLoading, notFound } = useRouteProject();
  return (
    <div data-testid="route">
      {projectId}|{project?.id ?? "-"}|{isLoading ? "loading" : "ready"}|
      {notFound ? "not-found" : "found-or-loading"}
    </div>
  );
}

describe("matchRouteProjectId", () => {
  it("extracts the project id from /projects/:id/* paths", () => {
    expect(matchRouteProjectId("/projects/p1/issues")).toBe("p1");
    expect(matchRouteProjectId("/projects/p1/logs/run-9")).toBe("p1");
    expect(matchRouteProjectId("/dashboard")).toBeNull();
    expect(matchRouteProjectId("/issues")).toBeNull();
  });
});

describe("useCurrentProjectId 裁决顺序", () => {
  beforeEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  afterEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  it("route id wins even when it is not in the project list", async () => {
    renderCurrent("/projects/ghost/issues", [project("p1")]);

    expect(await screen.findByTestId("current")).toHaveTextContent("ghost");
  });

  it("writes the route id into lastSelectedProjectId", async () => {
    renderCurrent("/projects/p2/board", [project("p1"), project("p2")]);

    await screen.findByTestId("current");
    await waitFor(() => {
      expect(
        useProjectPreferenceStore.getState().lastSelectedProjectId,
      ).toBe("p2");
    });
  });

  it("falls back to lastSelectedProjectId when it is still listed", async () => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: "p2" });

    renderCurrent("/dashboard", [project("p1"), project("p2")]);

    expect(await screen.findByTestId("current")).toHaveTextContent("p2");
  });

  it("falls back to the first non-shelved project otherwise", async () => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: "ghost" });

    renderCurrent("/dashboard", [project("p0", true), project("p1")]);

    expect(await screen.findByTestId("current")).toHaveTextContent("p1");
  });

  it("falls back to the first project when everything is shelved", async () => {
    renderCurrent("/dashboard", [project("p0", true), project("p1", true)]);

    expect(await screen.findByTestId("current")).toHaveTextContent("p0");
  });

  it("returns null without any project", async () => {
    renderCurrent("/dashboard", []);

    expect(await screen.findByTestId("current")).toHaveTextContent("null");
  });
});

function renderRouteProbe(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/projects/:id/*" element={<RouteProbe />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("useRouteProject", () => {
  it("resolves project by the route param once the list is ready", async () => {
    projectsMock.mockReturnValue({
      data: { items: [project("p1"), project("p2")] },
      isLoading: false,
    });

    renderRouteProbe("/projects/p2/issues");

    expect(await screen.findByTestId("route")).toHaveTextContent(
      "p2|p2|ready|found-or-loading",
    );
  });

  it("reports notFound when the list is ready without the route project", async () => {
    projectsMock.mockReturnValue({
      data: { items: [project("p1")] },
      isLoading: false,
    });

    renderRouteProbe("/projects/ghost/documents");

    expect(await screen.findByTestId("route")).toHaveTextContent(
      "ghost|-|ready|not-found",
    );
  });

  it("stays loading while the project list has no data", async () => {
    projectsMock.mockReturnValue({ data: undefined, isLoading: true });

    renderRouteProbe("/projects/p1/issues");

    expect(await screen.findByTestId("route")).toHaveTextContent(
      "p1|-|loading|found-or-loading",
    );
  });
});

describe("useSyncRouteProjectPreference", () => {
  beforeEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  afterEach(() => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  function PreferenceProbe() {
    useSyncRouteProjectPreference();
    const id = useProjectPreferenceStore(
      (state) => state.lastSelectedProjectId,
    );
    return <div data-testid="pref">{id ?? "null"}</div>;
  }

  function renderPreferenceProbe(entry: string) {
    return render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="*" element={<PreferenceProbe />} />
        </Routes>
      </MemoryRouter>,
    );
  }

  it("writes the route project into the preference on project routes", async () => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: "p1" });

    renderPreferenceProbe("/projects/p2/logs/run-9");

    await waitFor(() => {
      expect(
        useProjectPreferenceStore.getState().lastSelectedProjectId,
      ).toBe("p2");
    });
  });

  it("keeps the preference untouched outside project routes", async () => {
    useProjectPreferenceStore.setState({ lastSelectedProjectId: "p1" });

    renderPreferenceProbe("/dashboard");

    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(useProjectPreferenceStore.getState().lastSelectedProjectId).toBe(
      "p1",
    );
  });
});
