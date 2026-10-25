import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { ProjectPage } from "./Project";

afterEach(() => {
  vi.unstubAllGlobals();
});

function Seed({ user, children }: { user: User; children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession(user, "Acme");
  }, [setSession, user]);
  return children;
}

const project = {
  id: "p1",
  hostId: "h1",
  name: "shop",
  source: {
    kind: "git" as const,
    gitUrl: "https://github.com/acme/shop",
    gitRef: "main",
    composePath: "compose.yaml",
  },
  domain: "shop.example.com",
  service: "web",
  port: 8080,
  autoDeploy: true,
  hasToken: false,
  createdAt: new Date().toISOString(),
};

const deploys = [
  {
    id: "d1",
    projectId: "p1",
    projectName: "shop",
    status: "healthy",
    commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    trigger: "manual",
    rollbackOf: null,
    createdAt: new Date().toISOString(),
    startedAt: new Date().toISOString(),
    finishedAt: new Date().toISOString(),
    url: "https://shop.example.com",
    error: "",
    message: "",
  },
  {
    id: "d2",
    projectId: "p1",
    projectName: "shop",
    status: "healthy",
    commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    trigger: "push",
    rollbackOf: null,
    createdAt: new Date().toISOString(),
    startedAt: new Date().toISOString(),
    finishedAt: new Date().toISOString(),
    url: "https://shop.example.com",
    error: "",
    message: "",
  },
];

function mockFetch() {
  return vi.fn().mockImplementation((url: string) => {
    if (url === "/api/projects/p1") {
      return Promise.resolve(
        new Response(JSON.stringify({ project }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url === "/api/projects/p1/deploys") {
      return Promise.resolve(
        new Response(JSON.stringify({ deploys }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url.startsWith("/api/deploys/") && url.includes("/logs")) {
      return Promise.resolve(
        new Response(
          JSON.stringify({ logs: [], done: true, status: "healthy" }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }
    if (url.startsWith("/api/deploys/")) {
      const did = url.split("/")[3];
      const d = deploys.find((x) => x.id === did) ?? deploys[0];
      return Promise.resolve(
        new Response(JSON.stringify({ deploy: d }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
}

function renderProject(user: User) {
  vi.stubGlobal("fetch", mockFetch());
  return render(
    <MemoryRouter initialEntries={["/projects/p1"]}>
      <AuthProvider>
        <Seed user={user}>
          <Routes>
            <Route path="/projects/:id" element={<ProjectPage />} />
            <Route path="/hosts/:id" element={<div>host</div>} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("ProjectPage", () => {
  it("shows write actions for admins and a11y roles", async () => {
    renderProject({ id: "1", email: "a@b.c", role: "admin" });
    expect(await screen.findByRole("heading", { name: "shop" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Deploy now" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rollback" })).toBeInTheDocument();
    expect(screen.getByRole("log")).toBeInTheDocument();
    expect(screen.getByLabelText("Deploy phases")).toBeInTheDocument();
  });

  it("hides write buttons for members", async () => {
    renderProject({ id: "2", email: "m@b.c", role: "member" });
    expect(await screen.findByRole("heading", { name: "shop" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Deploy now" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Rollback" })).not.toBeInTheDocument();
  });

  it("confirms remove with volume-preserving copy", async () => {
    const user = userEvent.setup();
    renderProject({ id: "1", email: "a@b.c", role: "admin" });
    await screen.findByRole("heading", { name: "shop" });
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(
      await screen.findByText(/Stops the containers. Volumes and data are kept./),
    ).toBeInTheDocument();
  });

  it("shows force option after 409 on delete", async () => {
    const user = userEvent.setup();
    const base = mockFetch();
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (
        typeof url === "string" &&
        url.startsWith("/api/projects/p1") &&
        init?.method === "DELETE"
      ) {
        return Promise.resolve(
          new Response(JSON.stringify({ error: "host is offline; pass force=1" }), {
            status: 409,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return base(url, init);
    });
    vi.stubGlobal("fetch", fetchMock);

    render(
      <MemoryRouter initialEntries={["/projects/p1"]}>
        <AuthProvider>
          <Seed user={{ id: "1", email: "a@b.c", role: "admin" }}>
            <Routes>
              <Route path="/projects/:id" element={<ProjectPage />} />
            </Routes>
          </Seed>
        </AuthProvider>
      </MemoryRouter>,
    );
    await screen.findByRole("heading", { name: "shop" });
    await user.click(screen.getByRole("button", { name: "Remove" }));
    const dialogRemove = screen.getAllByRole("button", { name: "Remove" }).pop()!;
    await user.click(dialogRemove);
    await waitFor(() =>
      expect(
        screen.getByRole("checkbox", { name: /Force delete/i }),
      ).toBeInTheDocument(),
    );
  });
});
