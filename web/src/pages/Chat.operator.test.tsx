import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { ChatPage } from "./Chat";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function Seed({ user, children }: { user: User; children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession(user, "Acme");
  }, [setSession, user]);
  return children;
}

const admin: User = { id: "1", email: "a@b.c", role: "admin" };
const hostId = "550e8400-e29b-41d4-a716-446655440000";

const proposal = {
  id: "p1",
  kind: "answer",
  direction: "out",
  body: "Haetae proposes: restart api because it is crash-looping [ev_1]",
  actions: [
    { id: "approve_action", label: "Approve" },
    { id: "decline_action", label: "Decline" },
  ],
  evidence: [
    { label: "Container api", path: `/hosts/${hostId}?tab=containers` },
    { label: "bad", path: "javascript:alert(1)" },
  ],
  incidentId: null,
  createdAt: new Date().toISOString(),
};

/** A fresh Response per call: the page also asks for accounts and operator status. */
function always(body: unknown): typeof fetch {
  return vi.fn().mockImplementation(() =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  ) as unknown as typeof fetch;
}

function setup(fetchImpl: typeof fetch, who: User = admin) {
  vi.stubGlobal("fetch", fetchImpl);
  return render(
    <MemoryRouter initialEntries={["/chat"]}>
      <AuthProvider>
        <Seed user={who}>
          <Routes>
            <Route path="/chat" element={<ChatPage />} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("operator messages in chat", () => {
  it("renders evidence as in-app links and drops unsafe ones", async () => {
    setup(
      always({ messages: [proposal], next: "p1" }),
    );
    const link = await screen.findByRole("link", { name: "Container api" });
    expect(link).toHaveAttribute("href", `/hosts/${hostId}?tab=containers`);
    expect(screen.queryByRole("link", { name: "bad" })).not.toBeInTheDocument();
  });

  it("approval buttons post the action and retire both buttons", async () => {
    const calls: Array<{ url: string; body: string }> = [];
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (String(url) === "/api/chat/actions") {
        calls.push({ url, body: String(init?.body) });
        return Promise.resolve(
          new Response(JSON.stringify({ reply: "Approved. The action was sent." }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify({ messages: [proposal], next: "p1" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    });
    setup(fetchMock as unknown as typeof fetch);
    const approve = await screen.findByRole("button", { name: "Approve" });
    await userEvent.click(approve);
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(JSON.parse(calls[0]!.body)).toEqual({ messageId: "p1", action: "approve_action" });
    expect(await screen.findByText("Approved. The action was sent.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Decline" })).toBeDisabled();
  });

  it("members see the proposal without buttons and are told to ask an admin", async () => {
    setup(
      always({ messages: [proposal], next: "p1" }),
      { id: "2", email: "m@b.c", role: "member" },
    );
    expect(await screen.findByText(/Haetae proposes/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Decline" })).not.toBeInTheDocument();
    expect(screen.getByText("Ask an administrator to approve.")).toBeInTheDocument();
  });
});
