import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { HostSummary } from "../api/types";
import { GettingStarted } from "./GettingStarted";

afterEach(() => {
  vi.unstubAllGlobals();
  try {
    window.localStorage.clear();
  } catch {
    // ignore
  }
});

const host: HostSummary = {
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
  containers: { running: 1, total: 1 },
};

function stubApi(data: { projects: number; accounts: number; aiEnabled: boolean }) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((url: string) => {
      let body: unknown = {};
      if (url.startsWith("/api/projects")) {
        body = { projects: Array.from({ length: data.projects }, (_, i) => ({ id: `p${i}` })) };
      } else if (url === "/api/chat/accounts") {
        body = { accounts: Array.from({ length: data.accounts }, (_, i) => ({ id: `a${i}` })) };
      } else if (url === "/api/settings/operator") {
        body = { aiEnabled: data.aiEnabled };
      }
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }),
  );
}

function renderIt(hosts: HostSummary[], onAddHost = () => undefined) {
  return render(
    <MemoryRouter>
      <GettingStarted hosts={hosts} onAddHost={onAddHost} />
    </MemoryRouter>,
  );
}

function stepState(title: string): string {
  const item = screen.getAllByText(title)[0]!.closest("li")!;
  return item.textContent ?? "";
}

describe("GettingStarted", () => {
  it("computes each step from real data and links to where it is done", async () => {
    stubApi({ projects: 0, accounts: 0, aiEnabled: false });
    renderIt([host]);
    await screen.findByText("1 of 3 required steps done");
    expect(stepState("Add a host")).toContain("done");
    expect(stepState("Deploy an app")).toContain("not done");
    expect(stepState("Connect a chat channel")).toContain("not done");
    expect(stepState("Turn on the AI operator")).toContain("(optional)");
    expect(screen.getByText("1 of 3 required steps done")).toBeInTheDocument();

    expect(screen.getByRole("link", { name: "Deploy an app" })).toHaveAttribute(
      "href",
      "/hosts/h1?tab=deploy",
    );
    expect(screen.getByRole("link", { name: "Connect a channel" })).toHaveAttribute(
      "href",
      "/settings?tab=chat",
    );
    expect(screen.getByRole("link", { name: "Set up the operator" })).toHaveAttribute(
      "href",
      "/settings?tab=operator",
    );
  });

  it("starts with Add a host when there are no hosts", async () => {
    stubApi({ projects: 0, accounts: 0, aiEnabled: false });
    const onAdd = vi.fn();
    renderIt([], onAdd);
    const btn = await screen.findByRole("button", { name: "Add a host" });
    expect(stepState("Add a host")).toContain("not done");
    expect(screen.getByText("Add a host first")).toBeInTheDocument();
    await userEvent.click(btn);
    expect(onAdd).toHaveBeenCalled();
  });

  it("disappears once the three required steps are done", async () => {
    stubApi({ projects: 1, accounts: 1, aiEnabled: false });
    renderIt([host]);
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText("Getting started")).not.toBeInTheDocument();
  });

  it("collapses, remembers it in localStorage and survives blocked storage", async () => {
    stubApi({ projects: 0, accounts: 0, aiEnabled: true });
    const user = userEvent.setup();
    const { unmount } = renderIt([host]);
    const toggle = await screen.findByRole("button", { name: "Hide checklist" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(stepState("Turn on the AI operator")).toContain("done");
    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Show checklist" })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(screen.queryByText("Deploy an app")).not.toBeInTheDocument();
    unmount();

    const second = renderIt([host]);
    expect(await screen.findByRole("button", { name: "Show checklist" })).toBeInTheDocument();

    second.unmount();
    const getItem = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    renderIt([host]);
    const t2 = await screen.findByRole("button", { name: "Hide checklist" });
    await user.click(t2);
    expect(screen.getByRole("button", { name: "Show checklist" })).toBeInTheDocument();
    getItem.mockRestore();
    setItem.mockRestore();
  });
});
