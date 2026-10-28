import { cleanup, render, screen, within } from "@testing-library/react";
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
const incId = "650e8400-e29b-41d4-a716-446655440000";

function setup(messages: unknown[], opts: { available?: boolean } = {}) {
  const fetchMock = vi.fn().mockImplementation((url: string) => {
    let body: unknown = { messages, next: null };
    if (String(url).startsWith("/api/chat/accounts")) body = { accounts: [{ id: "x" }] };
    if (String(url).includes("operator")) body = { available: opts.available ?? true };
    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  return render(
    <MemoryRouter initialEntries={["/chat"]}>
      <AuthProvider>
        <Seed user={admin}>
          <Routes>
            <Route path="/chat" element={<ChatPage />} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

function incident(id: string, state: string, createdAt: string) {
  return {
    id,
    kind: "incident",
    direction: "out",
    body: "[CRITICAL] Strato offline\nRAW-FALLBACK",
    actions: [
      { id: "later", label: "Later" },
      { id: "mute30d", label: "Mute 30 days" },
      { id: "ack", label: "Acknowledge" },
    ],
    evidence: [
      { label: "Host", path: `/hosts/${hostId}` },
      { label: "Incident", path: `/incidents/${incId}` },
    ],
    meta: { severity: "critical", title: "Strato offline", state, host: "Strato", summary: ["disk full"] },
    incidentId: incId,
    createdAt,
  };
}

describe("incident card", () => {
  it("renders chip, title once, host, links, buttons and time", async () => {
    const created = new Date(Date.now() - 53 * 60_000).toISOString();
    setup([incident("i1", "open", created)]);
    expect(await screen.findByText("Strato offline")).toBeInTheDocument();
    expect(screen.getAllByText("Strato offline")).toHaveLength(1);
    expect(screen.getByText("CRITICAL")).toBeInTheDocument();
    expect(screen.queryByText("Resolved")).not.toBeInTheDocument();
    expect(screen.getByText("Host: Strato")).toBeInTheDocument();
    expect(screen.getByText("disk full")).toBeInTheDocument();
    expect(screen.queryByText(/RAW-FALLBACK/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View host" })).toHaveAttribute("href", `/hosts/${hostId}`);
    expect(screen.getByRole("link", { name: "View incident" })).toHaveAttribute(
      "href",
      `/incidents/${incId}`,
    );
    for (const name of ["Later", "Mute 30 days", "Acknowledge"]) {
      expect(screen.getByRole("button", { name })).toBeEnabled();
    }
    const time = screen.getByText("53 mins ago");
    expect(time.tagName).toBe("TIME");
    expect(time).toHaveAttribute("datetime", created);
    expect(time.getAttribute("title")).toBeTruthy();
  });

  it("shows a Resolved chip", async () => {
    setup([{ ...incident("i2", "resolved", new Date().toISOString()), actions: [] }]);
    expect(await screen.findByText("Resolved")).toBeInTheDocument();
    expect(screen.getByText("CRITICAL")).toBeInTheDocument();
  });
});

describe("feed", () => {
  it("separates days and maps old action bodies to notes", async () => {
    const now = new Date();
    const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12).toISOString();
    const base = { actions: [], evidence: [], incidentId: null };
    setup([
      { ...base, id: "a", kind: "action", direction: "in", body: "ack", createdAt: yesterday },
      { ...base, id: "b", kind: "action", direction: "in", body: "mute30d", createdAt: now.toISOString() },
      { ...base, id: "c", kind: "action", direction: "in", body: "You postponed Strato offline", createdAt: now.toISOString() },
      { ...base, id: "d", kind: "text", direction: "in", body: "hi", createdAt: now.toISOString() },
      { ...base, id: "e", kind: "answer", direction: "out", body: "hello", createdAt: now.toISOString() },
    ]);
    expect(await screen.findByText("Yesterday")).toBeInTheDocument();
    expect(screen.getByText("Today")).toBeInTheDocument();
    expect(screen.getByText(/^Acknowledged/)).toBeInTheDocument();
    expect(screen.getByText(/^Muted for 30 days/)).toBeInTheDocument();
    expect(screen.getByText(/^You postponed Strato offline/)).toBeInTheDocument();
    expect(screen.queryByText("ack")).not.toBeInTheDocument();
    const feed = screen.getByRole("log");
    expect(within(feed).getByText("You")).toBeInTheDocument();
    expect(within(feed).getByText("Haetae")).toBeInTheDocument();
  });

  it("shows the empty state", async () => {
    setup([]);
    expect(await screen.findByText("Nothing here yet")).toBeInTheDocument();
  });
});

describe("composer", () => {
  it("has a helpful placeholder and a helper when the operator is unavailable", async () => {
    setup([], { available: false });
    expect(
      screen.getByPlaceholderText(/why was shop-api slow this morning\?/),
    ).toBeInTheDocument();
    const link = await screen.findByRole("link", { name: /AI operator/ });
    expect(link).toHaveAttribute("href", "/settings?tab=operator");
    expect(screen.getByText(/The operator is off or not configured/)).toBeInTheDocument();
  });
});
