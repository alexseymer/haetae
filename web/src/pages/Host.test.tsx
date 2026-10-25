import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { HostPage } from "./Host";

afterEach(() => {
  vi.unstubAllGlobals();
});

function Seed({ children }: { children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession({ id: "1", email: "a@b.c", role: "admin" } satisfies User, "Acme");
  }, [setSession]);
  return children;
}

describe("HostPage", () => {
  it("switches tabs via the URL", async () => {
    const fetchMock = vi.fn().mockImplementation((url: string) => {
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
          new Response(
            JSON.stringify({
              host: {
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
                containers: { running: 0, total: 0 },
              },
              logStats: null,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(new Response("{}", { status: 404 }));
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/hosts/h1"]}>
        <AuthProvider>
          <Seed>
            <Routes>
              <Route path="/hosts/:id" element={<HostPage />} />
            </Routes>
          </Seed>
        </AuthProvider>
      </MemoryRouter>,
    );

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

    await user.click(screen.getByRole("tab", { name: "Logs" }));
    await waitFor(() =>
      expect(screen.getByRole("tab", { name: "Logs" })).toHaveAttribute("aria-selected", "true"),
    );
    expect(await screen.findByText("No events")).toBeInTheDocument();
  });
});
