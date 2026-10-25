import { render, screen } from "@testing-library/react";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { IncidentDetailPage } from "./IncidentDetail";

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

describe("IncidentDetailPage", () => {
  it("caps mute days at 90 to match the API", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          incident: {
            id: "i1",
            orgId: "o1",
            hostId: "h1",
            hostName: "web-1",
            title: "web: oom",
            severity: "high",
            state: "open",
            openedAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
            resolvedAt: null,
            alertCount: 1,
          },
          alerts: [],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(
      <MemoryRouter initialEntries={["/incidents/i1"]}>
        <AuthProvider>
          <Seed>
            <Routes>
              <Route path="/incidents/:id" element={<IncidentDetailPage />} />
            </Routes>
          </Seed>
        </AuthProvider>
      </MemoryRouter>,
    );

    const days = await screen.findByLabelText("Days");
    expect(days).toHaveAttribute("max", "90");
  });
});
