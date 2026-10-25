import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { HostsPage } from "./Hosts";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function Seed({ user, children }: { user: User; children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession(user, "Acme");
  }, [setSession, user]);
  return children;
}

function renderHosts(user: User, fetchImpl: typeof fetch) {
  vi.stubGlobal("fetch", fetchImpl);
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <AuthProvider>
        <Seed user={user}>
          <Routes>
            <Route path="/" element={<HostsPage />} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

const admin: User = { id: "1", email: "a@b.c", role: "admin" };

describe("HostsPage", () => {
  it("renders online and offline hosts", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          hosts: [
            {
              id: "h1",
              name: "web-1",
              hostname: "web-1.local",
              os: "linux",
              kernel: "6.1",
              arch: "amd64",
              dockerVersion: "24",
              agentVersion: "0.1.0",
              connected: true,
              lastSeen: new Date().toISOString(),
              enrolledAt: new Date().toISOString(),
              containers: { running: 2, total: 3 },
            },
            {
              id: "h2",
              name: "db-1",
              hostname: "db-1.local",
              os: "linux",
              kernel: "6.1",
              arch: "amd64",
              dockerVersion: "24",
              agentVersion: "0.1.0",
              connected: false,
              lastSeen: null,
              enrolledAt: new Date().toISOString(),
              containers: { running: 0, total: 1 },
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    renderHosts(admin, fetchMock as unknown as typeof fetch);
    expect((await screen.findAllByText("web-1")).length).toBeGreaterThan(0);
    expect(screen.getAllByText("db-1").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Online").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Offline").length).toBeGreaterThan(0);
  });

  it("shows empty state", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ hosts: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    renderHosts(admin, fetchMock as unknown as typeof fetch);
    expect(await screen.findByText(/No hosts yet/i)).toBeInTheDocument();
  });

  it("ignores a stale hosts response that finishes after a newer one", async () => {
    const slowResolvers: Array<(r: Response) => void> = [];
    let calls = 0;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (url === "/api/hosts") {
        calls += 1;
        if (calls === 1) {
          return new Promise<Response>((resolve) => {
            slowResolvers.push(resolve);
          });
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              hosts: [
                {
                  id: "h-new",
                  name: "fresh-host",
                  hostname: "fresh",
                  os: "linux",
                  kernel: "6.1",
                  arch: "amd64",
                  dockerVersion: "24",
                  agentVersion: "0.1.0",
                  connected: true,
                  lastSeen: new Date().toISOString(),
                  enrolledAt: new Date().toISOString(),
                  containers: { running: 1, total: 1 },
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 404 }));
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });
    renderHosts(admin, fetchMock as unknown as typeof fetch);
    await waitFor(() => expect(calls).toBe(1));
    // Advance past the poll interval to start a second load.
    await vi.advanceTimersByTimeAsync(10_000);
    await waitFor(() => expect(calls).toBe(2));
    expect((await screen.findAllByText("fresh-host")).length).toBeGreaterThan(0);

    for (const resolve of slowResolvers) {
      resolve(
        new Response(
          JSON.stringify({
            hosts: [
              {
                id: "h-old",
                name: "stale-host",
                hostname: "stale",
                os: "linux",
                kernel: "6.1",
                arch: "amd64",
                dockerVersion: "24",
                agentVersion: "0.1.0",
                connected: false,
                lastSeen: null,
                enrolledAt: new Date().toISOString(),
                containers: { running: 0, total: 0 },
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }
    await waitFor(() => {
      expect(screen.getAllByText("fresh-host").length).toBeGreaterThan(0);
    });
    expect(screen.queryByText("stale-host")).not.toBeInTheDocument();
  });

  it("Add host dialog shows command after create; Esc closes", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url === "/api/hosts" && (!init || init.method === "GET" || !init.method)) {
        return Promise.resolve(
          new Response(JSON.stringify({ hosts: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (url === "/api/hosts/enrollment") {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              token: "tok",
              expiresAt: "2030-01-01T00:00:00Z",
              command: "curl -sL https://example.invalid/install | bash -s tok",
              host: "new-host",
            }),
            { status: 201, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 404 }));
    });
    const user = userEvent.setup();
    renderHosts(admin, fetchMock as unknown as typeof fetch);
    await screen.findByText(/No hosts yet/i);
    await user.click(screen.getByRole("button", { name: "Add host" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Host name"), "new-host");
    await user.click(screen.getByRole("button", { name: "Create token" }));
    expect(await screen.findByText(/Run this on the server/i)).toBeInTheDocument();
    expect(screen.getByLabelText("Command")).toHaveTextContent("curl -sL");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
