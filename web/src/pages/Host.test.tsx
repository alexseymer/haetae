import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { HostPage } from "./Host";

afterEach(() => {
  seedRole = "admin";
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

let seedRole: User["role"] = "admin";

function Seed({ children }: { children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession({ id: "1", email: "a@b.c", role: seedRole } satisfies User, "Acme");
  }, [setSession]);
  return children;
}

function hostPayload(name = "web-1") {
  return {
    host: {
      id: "h1",
      name,
      hostname: "web-1.local",
      os: "linux",
      kernel: "6.1",
      arch: "amd64",
      dockerVersion: "24",
      agentVersion: "0.1.0",
      connected: true,
      lastSeen: new Date().toISOString(),
      enrolledAt: new Date().toISOString(),
      containers: { running: 0, total: 0 },
    },
    logStats: null,
  };
}

function defaultFetch(url: string) {
  if (url.startsWith("/api/hosts/h1/containers")) {
    return Promise.resolve(
      new Response(JSON.stringify({ containers: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (url.startsWith("/api/hosts/h1/logs")) {
    return Promise.resolve(
      new Response(JSON.stringify({ events: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (url.startsWith("/api/hosts/h1/metrics")) {
    return Promise.resolve(
      new Response(JSON.stringify({ metric: "cpu_pct", resolution: "1m", points: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (url === "/api/hosts/h1") {
    return Promise.resolve(
      new Response(JSON.stringify(hostPayload()), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (url.startsWith("/api/projects")) {
    return Promise.resolve(
      new Response(JSON.stringify({ projects: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  return Promise.resolve(new Response("{}", { status: 404 }));
}

function renderHost() {
  return render(
    <MemoryRouter initialEntries={["/hosts/h1"]}>
      <AuthProvider>
        <Seed>
          <Routes>
            <Route path="/hosts/:id" element={<HostPage />} />
            <Route path="/" element={<p>Hosts list page</p>} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("HostPage", () => {
  it("switches tabs via the URL", async () => {
    const fetchMock = vi.fn().mockImplementation(defaultFetch);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderHost();

    expect(await screen.findByRole("heading", { name: "web-1" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    await user.click(screen.getByRole("tab", { name: "Containers" }));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Containers" })).toHaveAttribute(
        "aria-selected",
        "true",
      ),
    );
    expect(await screen.findByText("No containers.")).toBeInTheDocument();
    expect(screen.getByRole("tabpanel")).toHaveAttribute("id", "panel-containers");

    await user.click(screen.getByRole("tab", { name: "Logs" }));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Logs" })).toHaveAttribute("aria-selected", "true"),
    );
    expect(await screen.findByText("No events")).toBeInTheDocument();
  });

  it("supports arrow-key roving tabindex on the tablist", async () => {
    const fetchMock = vi.fn().mockImplementation(defaultFetch);
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderHost();
    await screen.findByRole("heading", { name: "web-1" });
    const overview = screen.getByRole("tab", { name: "Overview" });
    overview.focus();
    expect(overview).toHaveAttribute("tabindex", "0");
    await user.keyboard("{ArrowRight}");
    const containers = screen.getByRole("tab", { name: "Containers" });
    expect(containers).toHaveAttribute("aria-selected", "true");
    expect(containers).toHaveAttribute("tabindex", "0");
    expect(overview).toHaveAttribute("tabindex", "-1");
    expect(containers).toHaveAttribute("aria-controls", "panel-containers");
    await user.keyboard("{End}");
    expect(screen.getByRole("tab", { name: "Security" })).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Home}");
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
  });

  it("opens the Security tab from ?tab=security", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (
        url.includes("/security") ||
        url.includes("/vulnerabilities") ||
        url.includes("/outbound") ||
        url.includes("/image-updates") ||
        url.includes("/settings/updates")
      ) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              conntrack: { state: "ok", reason: "", notMonitored: [] },
              misconfigs: [],
              summary: { critical: 0, high: 0, medium: 0, low: 0 },
              containers: [],
              db: { status: "none", builtAt: null, checkedAt: null, error: "", configured: false },
              conns: [],
              updates: [],
              autoPull: false,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return defaultFetch(url);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(
      <MemoryRouter initialEntries={["/hosts/h1?tab=security"]}>
        <AuthProvider>
          <Seed>
            <Routes>
              <Route path="/hosts/:id" element={<HostPage />} />
            </Routes>
          </Seed>
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByRole("tab", { name: "Security" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(await screen.findByRole("heading", { name: "Vulnerabilities" })).toBeInTheDocument();
  });

  it("ignores a stale metrics response after a newer range load", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const slowResolvers: Array<(r: Response) => void> = [];
    let metricsStarted = 0;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url.startsWith("/api/hosts/h1/metrics")) {
        metricsStarted += 1;
        if (metricsStarted <= 8) {
          return new Promise<Response>((resolve) => {
            slowResolvers.push(resolve);
          });
        }
        const metric = new URL(url, "http://local").searchParams.get("metric") || "cpu_pct";
        return Promise.resolve(
          new Response(
            JSON.stringify({
              metric,
              resolution: "1m",
              points: [{ t: 2, v: 99 }],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return defaultFetch(url);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderHost();
    await screen.findByRole("heading", { name: "web-1" });
    await waitFor(() => expect(metricsStarted).toBe(8));

    await user.click(screen.getByRole("button", { name: "6h" }));
    await waitFor(() => expect(metricsStarted).toBe(16));
    await waitFor(() => {
      expect(screen.getByRole("img", { name: /CPU %: 1 points, last value 99/ })).toBeInTheDocument();
    });

    for (const resolve of slowResolvers) {
      resolve(
        new Response(
          JSON.stringify({
            metric: "cpu_pct",
            resolution: "1m",
            points: [{ t: 1, v: 1 }],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }
    await waitFor(() => {
      expect(screen.getByRole("img", { name: /CPU %: 1 points, last value 99/ })).toBeInTheDocument();
    });
    expect(screen.queryByRole("img", { name: /last value 1\.0%/ })).not.toBeInTheDocument();
  });

  it("removes a host after a confirmation that explains the effects", async () => {
    const calls: Array<{ url: string; method?: string }> = [];
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") {
        calls.push({ url, method: init.method });
        return Promise.resolve(new Response(null, { status: 204 }));
      }
      return defaultFetch(url);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderHost();
    await screen.findByRole("heading", { name: "web-1" });

    await user.click(screen.getByRole("button", { name: "Remove host" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(/agent is disconnected/i);
    expect(dialog).toHaveTextContent(/containers on the host keep running/i);
    expect(dialog).toHaveTextContent(/Uninstall the agent/i);
    expect(calls).toHaveLength(0); // nothing happens before the confirmation

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(calls).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Remove host" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove host" }));
    await waitFor(() => expect(calls).toEqual([{ url: "/api/hosts/h1", method: "DELETE" }]));
    expect(await screen.findByText("Hosts list page")).toBeInTheDocument();
  });

  it("shows the error and stays on the page when removal fails", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === "DELETE") {
        return Promise.resolve(
          new Response(JSON.stringify({ error: "try again" }), {
            status: 503,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return defaultFetch(url);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderHost();
    await screen.findByRole("heading", { name: "web-1" });
    await user.click(screen.getByRole("button", { name: "Remove host" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove host" }));
    expect(await within(screen.getByRole("dialog")).findByRole("alert")).toHaveTextContent("try again");
    expect(screen.queryByText("Hosts list page")).not.toBeInTheDocument();
  });

  it("hides Remove host from members", async () => {
    seedRole = "member";
    vi.stubGlobal("fetch", vi.fn().mockImplementation(defaultFetch));
    renderHost();
    await screen.findByRole("heading", { name: "web-1" });
    expect(screen.queryByRole("button", { name: "Remove host" })).not.toBeInTheDocument();
  });
});
