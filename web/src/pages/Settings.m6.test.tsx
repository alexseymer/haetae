import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { SettingsPage } from "./Settings";

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
  localStorage.clear();
});

function Seed({ user, children }: { user: User; children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession(user, "Acme");
  }, [setSession, user]);
  return children;
}

function renderSettings(user: User, fetchImpl: typeof fetch) {
  vi.stubGlobal("fetch", fetchImpl);
  return render(
    <MemoryRouter initialEntries={["/settings"]}>
      <AuthProvider>
        <Seed user={user}>
          <Routes>
            <Route path="/settings" element={<SettingsPage />} />
          </Routes>
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

const admin: User = { id: "1", email: "a@b.c", role: "admin" };
const member: User = { id: "2", email: "m@b.c", role: "member" };

const settingsBody = {
  timeZone: "UTC",
  briefingHour: 8,
  cpuPct: 90,
  memPct: 90,
  diskPct: 90,
  sustainedS: 300,
  offlineAfterS: 120,
};

function baseFetch(url: string, init?: RequestInit) {
  const u = String(url);
  if (u === "/api/users") {
    return Promise.resolve(
      new Response(JSON.stringify({ users: [admin] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (u === "/api/settings/alerts" && (!init || init.method === "GET" || !init.method)) {
    return Promise.resolve(
      new Response(JSON.stringify(settingsBody), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (u === "/api/mutes") {
    return Promise.resolve(
      new Response(JSON.stringify({ mutes: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (u === "/api/chat/channels") {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          channels: [
            { name: "web", enabled: true },
            { name: "telegram", enabled: false },
          ],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
  }
  if (u === "/api/chat/accounts") {
    return Promise.resolve(
      new Response(JSON.stringify({ accounts: [] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  }
  if (u === "/api/chat/link" && init?.method === "POST") {
    return Promise.resolve(
      new Response(
        JSON.stringify({
          code: "ABCD1234",
          expiresAt: new Date(Date.now() + 600_000).toISOString(),
          instructions: "Send /link ABCD1234 to the bot.",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
  }
  return Promise.resolve(new Response("{}", { status: 404 }));
}

function json(status: number, body: unknown) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
}

describe("Settings M6", () => {
  it("validates alert settings inline for admins", async () => {
    const user = userEvent.setup();
    renderSettings(admin, baseFetch as unknown as typeof fetch);

    await user.click(screen.getByRole("tab", { name: "Alerts" }));
    const tz = await screen.findByLabelText("Time zone");
    await user.clear(tz);
    await user.type(tz, "Not/AZone");
    await user.click(screen.getByRole("button", { name: "Save alert settings" }));
    expect(await screen.findByText(/valid IANA time zone/i)).toBeInTheDocument();
  });

  it("makes alert settings read-only for members", async () => {
    const user = userEvent.setup();
    cleanup();
    renderSettings(member, baseFetch as unknown as typeof fetch);
    await user.click(screen.getByRole("tab", { name: "Alerts" }));
    expect(await screen.findByLabelText("Time zone")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save alert settings" })).not.toBeInTheDocument();
  });

  it("shows link code in memory and clears it without persisting", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn().mockImplementation(baseFetch);
    renderSettings(admin, fetchMock as unknown as typeof fetch);

    await user.click(screen.getByRole("tab", { name: "Chat channels" }));
    await screen.findByText("web");
    expect(screen.getByText(/HAETAE_TELEGRAM_TOKEN/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Link Telegram" }));
    expect(await screen.findByText("ABCD1234")).toBeInTheDocument();
    expect(screen.getByText(/Send \/link ABCD1234/)).toBeInTheDocument();
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);

    await user.click(screen.getByRole("button", { name: "Clear code" }));
    await waitFor(() => {
      expect(screen.queryByText("ABCD1234")).not.toBeInTheDocument();
    });
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
  });

  it("links email with Send code then a typed code", async () => {
    const user = userEvent.setup();
    const calls: Array<{ url: string; body: string }> = [];
    let confirmStatus = 400;
    const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (url === "/api/chat/link" && init?.method === "POST") {
        calls.push({ url, body: String(init.body) });
        return json(200, {
          expiresAt: new Date(Date.now() + 600_000).toISOString(),
          instructions: "We sent a code to m***@b.c; enter it here.",
          confirm: true,
        });
      }
      if (url === "/api/chat/link/confirm" && init?.method === "POST") {
        calls.push({ url, body: String(init.body) });
        return confirmStatus === 200
          ? json(200, { linked: true, channel: "email", externalId: "m***@b.c" })
          : json(400, { error: "that code is not valid or has expired" });
      }
      return baseFetch(url, init);
    });
    renderSettings(member, fetchMock as unknown as typeof fetch);
    await user.click(screen.getByRole("tab", { name: "Chat channels" }));
    await screen.findByText("web");

    await user.click(screen.getByRole("button", { name: "Link email" }));
    // Nothing is sent until the user asks.
    expect(calls).toHaveLength(0);
    expect(screen.queryByText(/\/link/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Send code" }));
    expect(await screen.findByText(/We sent a code to m\*\*\*@b\.c; enter it here\./)).toBeInTheDocument();
    expect(JSON.parse(calls[0]!.body)).toEqual({ channel: "email" });

    const input = screen.getByLabelText("Code from the email");
    await user.type(input, "WRONG123");
    await user.click(screen.getByRole("button", { name: "Confirm code" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/not valid or has expired/);

    confirmStatus = 200;
    await user.clear(input);
    await user.type(input, "abcd2345");
    await user.click(screen.getByRole("button", { name: "Confirm code" }));
    expect(await screen.findByRole("status")).toHaveTextContent(/Email linked/);
    expect(JSON.parse(calls[calls.length - 1]!.body)).toEqual({ channel: "email", code: "abcd2345" });
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
  });
});
