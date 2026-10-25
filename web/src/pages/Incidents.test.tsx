import { render, screen } from "@testing-library/react";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { IncidentsPage } from "./Incidents";

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

describe("IncidentsPage", () => {
  it("shows severity as a text label with colour (not colour only)", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          incidents: [
            {
              id: "i1",
              orgId: "o1",
              hostId: "h1",
              hostName: "web-1",
              title: "web: oom on web-1",
              severity: "critical",
              state: "open",
              openedAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
              resolvedAt: null,
              alertCount: 2,
            },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(
      <MemoryRouter initialEntries={["/incidents"]}>
        <AuthProvider>
          <Seed>
            <Routes>
              <Route path="/incidents" element={<IncidentsPage />} />
            </Routes>
          </Seed>
        </AuthProvider>
      </MemoryRouter>,
    );

    expect(await screen.findByText("critical")).toBeInTheDocument();
    expect(screen.getByText("web: oom on web-1")).toBeInTheDocument();
    expect(screen.getByText("web-1")).toBeInTheDocument();
    const badge = screen.getByText("critical");
    expect(badge.className).toMatch(/badge/);
  });
});
