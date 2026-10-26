import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { User } from "../api/types";
import { AuthProvider, useAuth } from "../auth";
import { HostSecurityTab } from "./HostSecurityTab";

afterEach(() => {
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
const member: User = { id: "2", email: "m@b.c", role: "member" };

function payload(over: {
  scanStatus?: string;
  matchStatus?: string;
  scanError?: string;
  urls?: string[];
  conntrackState?: string;
  conntrackReason?: string;
} = {}) {
  return {
    security: {
      conntrack: {
        state: over.conntrackState ?? "ok",
        reason: over.conntrackReason ?? "",
        notMonitored: ["hostnet1"],
      },
      misconfigs: [
        {
          containerId: "c1",
          containerName: "web",
          kind: "privileged",
          severity: "high",
          detail: "privileged=true",
          firstSeen: "2026-01-01T00:00:00Z",
          lastSeen: "2026-01-01T00:00:00Z",
        },
      ],
      summary: { critical: 0, high: 1, medium: 0, low: 0 },
      scanFailed: over.scanStatus === "failed",
    },
    vulns: {
      containers: [
        {
          containerId: "c1",
          name: "web",
          image: "nginx:1",
          imageId: "sha256:1",
          scan: {
            status: over.scanStatus ?? "ok",
            error: over.scanError ?? "",
            at: "2026-01-01T00:00:00Z",
            tool: "syft",
          },
          match: {
            status: over.matchStatus ?? "ok",
            error: "",
            dbBuiltAt: "2026-01-01T00:00:00Z",
          },
          counts: { critical: 1, high: 0, medium: 0, low: 0, unknown: 0 },
          findings:
            (over.scanStatus ?? "ok") === "ok" && (over.matchStatus ?? "ok") === "ok"
              ? [
                  {
                    vulnId: "CVE-2024-1",
                    package: "openssl",
                    version: "1.0",
                    fixedVersion: "1.1",
                    severity: "critical",
                    description: "bad",
                    urls: over.urls ?? [
                      "https://nvd.nist.gov/vuln/detail/CVE-2024-1",
                      "http://evil.example/x",
                    ],
                  },
                ]
              : [],
        },
        {
          containerId: "c2",
          name: "db",
          image: "postgres:16",
          imageId: "",
          scan: { status: "none", error: "", at: null, tool: "" },
          match: { status: "none", error: "", dbBuiltAt: null },
          counts: { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 },
          findings: [],
        },
      ],
      db: {
        status: "ok",
        builtAt: "2026-01-01T00:00:00Z",
        checkedAt: "2026-01-01T00:00:00Z",
        error: "",
        configured: true,
      },
    },
    outbound: {
      conns: [
        {
          containerId: "c1",
          containerName: "web",
          dstIp: "1.2.3.4",
          dstPort: 443,
          proto: "tcp",
          count: 2,
          firstSeen: "2026-01-01T00:00:00Z",
          lastSeen: "2026-01-01T00:00:00Z",
        },
      ],
    },
    updates: {
      updates: [
        {
          containerId: "c1",
          containerName: "web",
          image: "nginx:1",
          runningDigest: "sha256:a",
          candidateDigest: "sha256:b",
          candidateCreated: "2026-01-01T00:00:00Z",
          decision: "wait",
          reason: "young",
          decidedAt: "2026-01-01T00:00:00Z",
        },
      ],
    },
    settings: { autoPull: false },
  };
}

function stubFetch(data = payload()) {
  return vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    if (url.endsWith("/security")) {
      return Promise.resolve(
        new Response(JSON.stringify(data.security), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url.endsWith("/vulnerabilities")) {
      return Promise.resolve(
        new Response(JSON.stringify(data.vulns), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url.includes("/outbound")) {
      return Promise.resolve(
        new Response(JSON.stringify(data.outbound), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url.endsWith("/image-updates")) {
      return Promise.resolve(
        new Response(JSON.stringify(data.updates), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url === "/api/settings/updates") {
      if (init?.method === "PUT") {
        return Promise.resolve(
          new Response(JSON.stringify({ autoPull: true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(JSON.stringify(data.settings), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    if (url.endsWith("/scan")) {
      return Promise.resolve(
        new Response(JSON.stringify({ accepted: true, scanId: "s1" }), {
          status: 202,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }
    return Promise.resolve(new Response("{}", { status: 404 }));
  });
}

function renderTab(user: User, fetchImpl: typeof fetch) {
  vi.stubGlobal("fetch", fetchImpl);
  return render(
    <MemoryRouter>
      <AuthProvider>
        <Seed user={user}>
          <HostSecurityTab hostId="h1" active />
        </Seed>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe("HostSecurityTab", () => {
  it("renders vulnerability, misconfig, outbound, and image update sections", async () => {
    renderTab(admin, stubFetch() as unknown as typeof fetch);
    expect(await screen.findByRole("heading", { name: "Vulnerabilities" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Misconfigurations" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Outbound connections" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Image updates" })).toBeInTheDocument();
    expect(screen.getByRole("tabpanel")).toHaveAttribute("id", "panel-security");
    expect(await screen.findByText("privileged: privileged=true")).toBeInTheDocument();
    expect(screen.getByText("waiting 48 h")).toBeInTheDocument();
    expect(screen.getByText("1.2.3.4")).toBeInTheDocument();
  });

  it("shows failed-scan wording and never implies clean zero vulns", async () => {
    renderTab(
      admin,
      stubFetch(
        payload({ scanStatus: "failed", scanError: "helper oom" }),
      ) as unknown as typeof fetch,
    );
    expect(await screen.findByText("Scan failed: helper oom")).toBeInTheDocument();
    expect(screen.queryByText(/^critical 0$/)).not.toBeInTheDocument();
    expect(screen.getByText("Not scanned yet")).toBeInTheDocument();
  });

  it("shows matching pending, never clean, when scan ok but match is pending", async () => {
    renderTab(
      admin,
      stubFetch(
        payload({ scanStatus: "ok", matchStatus: "pending" }),
      ) as unknown as typeof fetch,
    );
    expect(await screen.findByText("Matching pending")).toBeInTheDocument();
    expect(screen.queryByText(/^critical 0$/)).not.toBeInTheDocument();
  });

  it("shows degraded conntrack banner with reason", async () => {
    renderTab(
      admin,
      stubFetch(
        payload({
          conntrackState: "degraded",
          conntrackReason: "permission denied",
        }),
      ) as unknown as typeof fetch,
    );
    expect(
      await screen.findByText(/Conntrack degraded: permission denied/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Not monitored \(host network\): hostnet1/)).toBeInTheDocument();
  });

  it("links CVE ids only to https URLs", async () => {
    const user = userEvent.setup();
    renderTab(admin, stubFetch() as unknown as typeof fetch);
    const show = await screen.findByRole("button", { name: /Show findings/ });
    await user.click(show);
    const link = screen.getByRole("link", { name: "CVE-2024-1" });
    expect(link).toHaveAttribute("href", "https://nvd.nist.gov/vuln/detail/CVE-2024-1");
    expect(screen.queryByRole("link", { name: /evil/ })).not.toBeInTheDocument();
  });

  it("shows Scan now for admins and hides it for members", async () => {
    const { unmount } = renderTab(admin, stubFetch() as unknown as typeof fetch);
    expect(await screen.findAllByRole("button", { name: "Scan now" })).toHaveLength(2);
    expect(screen.getByRole("checkbox")).toBeInTheDocument();
    unmount();
    renderTab(member, stubFetch() as unknown as typeof fetch);
    await screen.findByRole("heading", { name: "Vulnerabilities" });
    expect(screen.queryByRole("button", { name: "Scan now" })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.getByText(/Auto-pull is off/)).toBeInTheDocument();
  });

  it("disables Scan now while a request is pending", async () => {
    let resolveScan: (r: Response) => void = () => {};
    const fetchMock = stubFetch();
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (url.endsWith("/scan")) {
        return new Promise<Response>((resolve) => {
          resolveScan = resolve;
        });
      }
      return stubFetch()(url, init);
    });
    const user = userEvent.setup();
    renderTab(admin, fetchMock as unknown as typeof fetch);
    const buttons = await screen.findAllByRole("button", { name: "Scan now" });
    await user.click(buttons[0]!);
    expect(await screen.findByRole("button", { name: "Scanning…" })).toBeDisabled();
    resolveScan(
      new Response(JSON.stringify({ accepted: true, scanId: "s1" }), {
        status: 202,
        headers: { "Content-Type": "application/json" },
      }),
    );
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Scan now" }).length).toBe(2));
  });

  it("exposes severity as text badges", async () => {
    renderTab(admin, stubFetch() as unknown as typeof fetch);
    expect(await screen.findByText("critical 1")).toBeInTheDocument();
    const mis = screen.getByText(/privileged:/).closest("li");
    expect(mis).toBeTruthy();
    expect(within(mis!).getByText("high")).toBeInTheDocument();
  });
});
