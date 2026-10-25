import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { ChatPage } from "./Chat";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => "visible",
  });
});

function Seed({ user, children }: { user: User; children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession(user, "Acme");
  }, [setSession, user]);
  return children;
}

function renderChat(user: User, fetchImpl: typeof fetch) {
  vi.stubGlobal("fetch", fetchImpl);
  return render(
    <MemoryRouter initialEntries={["/chat"]}>
      <AuthProvider>
        <Seed user={user}>
          <Routes>
            <Route path="/chat" element={<ChatPage />} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

const admin: User = { id: "1", email: "a@b.c", role: "admin" };
const member: User = { id: "2", email: "m@b.c", role: "member" };

function msg(
  id: string,
  overrides: Partial<{
    kind: string;
    body: string;
    actions: Array<{ id: string; label: string }>;
    evidence: Array<{ label: string; path: string }>;
  }> = {},
) {
  return {
    id,
    kind: overrides.kind ?? "text",
    direction: "out",
    body: overrides.body ?? `body-${id}`,
    actions: overrides.actions ?? [],
    evidence: overrides.evidence ?? [],
    incidentId: null,
    createdAt: new Date().toISOString(),
  };
}

describe("ChatPage", () => {
  beforeEach(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
  });

  it("polls with after-cursor, ignores stale responses, pauses when hidden", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let visibility = "visible";
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => visibility,
    });

    const slowResolvers: Array<(r: Response) => void> = [];
    let call = 0;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (!String(url).startsWith("/api/chat/messages")) {
        return Promise.resolve(new Response("{}", { status: 404 }));
      }
      call += 1;
      if (call === 1) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              messages: [msg("m1")],
              next: "m1",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (call === 2) {
        return new Promise<Response>((resolve) => {
          slowResolvers.push(resolve);
        });
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            messages: [msg("m2")],
            next: "m2",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    });

    renderChat(admin, fetchMock as unknown as typeof fetch);
    expect(await screen.findByText("body-m1")).toBeInTheDocument();

    await act(async () => {
      vi.advanceTimersByTime(3000);
    });
    await waitFor(() => expect(call).toBe(2));

    await act(async () => {
      vi.advanceTimersByTime(3000);
    });
    await waitFor(() => expect(call).toBe(3));
    expect(await screen.findByText("body-m2")).toBeInTheDocument();

    // Stale response from call 2 must not duplicate or overwrite.
    await act(async () => {
      slowResolvers[0]?.(
        new Response(
          JSON.stringify({
            messages: [msg("stale", { body: "STALE" })],
            next: "stale",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    });
    expect(screen.queryByText("STALE")).not.toBeInTheDocument();
    expect(screen.getByText("body-m1")).toBeInTheDocument();
    expect(screen.getByText("body-m2")).toBeInTheDocument();

    visibility = "hidden";
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    const callsWhenHidden = call;
    await act(async () => {
      vi.advanceTimersByTime(9000);
    });
    expect(call).toBe(callsWhenHidden);
  });

  it("renders unsafe evidence paths as text and safe ones as links", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          messages: [
            msg("e1", {
              evidence: [
                {
                  label: "ok",
                  path: "/incidents/550e8400-e29b-41d4-a716-446655440000",
                },
                { label: "js", path: "javascript:alert(1)" },
                { label: "proto", path: "//evil" },
                { label: "abs", path: "https://evil.example/" },
                { label: "prefix", path: "/incidents/not-a-uuid" },
              ],
            }),
          ],
          next: "e1",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    renderChat(admin, fetchMock as unknown as typeof fetch);
    const ok = await screen.findByRole("link", { name: "ok" });
    expect(ok).toHaveAttribute(
      "href",
      "/incidents/550e8400-e29b-41d4-a716-446655440000",
    );
    expect(screen.queryByRole("link", { name: "js" })).not.toBeInTheDocument();
    expect(screen.getByText("js: javascript:alert(1)")).toBeInTheDocument();
    expect(screen.getByText("proto: //evil")).toBeInTheDocument();
    expect(screen.getByText("abs: https://evil.example/")).toBeInTheDocument();
    expect(screen.getByText("prefix: /incidents/not-a-uuid")).toBeInTheDocument();
  });

  it("hides action buttons for members", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          messages: [
            msg("a1", {
              kind: "incident",
              actions: [{ id: "ack", label: "Acknowledge" }],
            }),
          ],
          next: "a1",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    renderChat(member, fetchMock as unknown as typeof fetch);
    expect(await screen.findByText("body-a1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Acknowledge" })).not.toBeInTheDocument();
  });

  it("shows action buttons for admins and disables after use", async () => {
    const payload = {
      messages: [
        msg("a1", {
          kind: "incident",
          actions: [{ id: "ack", label: "Acknowledge" }],
        }),
      ],
      next: "a1",
    };
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (String(url) === "/api/chat/actions" && init?.method === "POST") {
        return Promise.resolve(
          new Response(JSON.stringify({ reply: "Acknowledged." }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify(payload), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    });

    renderChat(admin, fetchMock as unknown as typeof fetch);
    const btn = await screen.findByRole("button", { name: "Acknowledge" });
    const user = userEvent.setup();
    await user.click(btn);
    expect(await screen.findByText("Acknowledged.")).toBeInTheDocument();
    expect(btn).toBeDisabled();
  });
});
