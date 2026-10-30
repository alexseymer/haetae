import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LogSummary } from "./LogSummary";
import { DigestPanel } from "./DigestPanel";
import { api } from "../api/client";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const hostId = "550e8400-e29b-41d4-a716-446655440000";

function digest(over: Record<string, unknown> = {}) {
  return {
    title: "Log summary, last 24 h",
    generatedAt: "2026-10-30T10:00:00Z",
    windowFrom: "2026-10-29T10:00:00Z",
    windowTo: "2026-10-30T10:00:00Z",
    usedAI: true,
    statements: [{ text: "api logged 12 errors", evidence: ["ev_1", "ev_2"] }],
    evidence: [
      { id: "ev_1", title: "Errors on web-1", link: `/hosts/${hostId}?tab=logs`, kind: "logs" },
      { id: "ev_2", title: "Bad", link: "https://evil.example/x", kind: "logs" },
    ],
    ...over,
  };
}

function mockFetch(status: number, body: unknown) {
  const f = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", f);
  return f;
}

describe("LogSummary", () => {
  it("defaults to 24 h, posts the chosen window, renders statements and safe links", async () => {
    const f = mockFetch(200, digest());
    render(<LogSummary hostId={hostId} />);
    expect(screen.getByRole("button", { name: "24 h" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "6 h" }));
    expect(screen.getByRole("button", { name: "6 h" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(screen.getByRole("button", { name: "Summarize logs" }));
    expect(await screen.findByText("api logged 12 errors")).toBeInTheDocument();
    const [url, init] = f.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`/api/hosts/${hostId}/logs/summary`);
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body as string)).toEqual({ window: "6h" });
    expect(screen.getByRole("link", { name: "Errors on web-1" })).toHaveAttribute(
      "href",
      `/hosts/${hostId}?tab=logs`,
    );
    expect(screen.queryByRole("link", { name: "Bad" })).not.toBeInTheDocument();
    expect(screen.getByText("Bad")).toBeInTheDocument();
  });

  it("shows the reason in a note when the AI was not used", async () => {
    mockFetch(200, digest({ usedAI: false, reason: "Nothing notable, so the AI was not used" }));
    render(<LogSummary hostId={hostId} />);
    await userEvent.click(screen.getByRole("button", { name: "Summarize logs" }));
    expect(await screen.findByRole("note")).toHaveTextContent(
      "Nothing notable, so the AI was not used",
    );
  });

  it("shows the error message", async () => {
    mockFetch(503, { error: "try again" });
    render(<LogSummary hostId={hostId} />);
    await userEvent.click(screen.getByRole("button", { name: "Summarize logs" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("try again");
  });

  it("shows a loading state while the request runs", async () => {
    let resolve!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(new Promise<Response>((r) => (resolve = r))));
    render(<LogSummary hostId={hostId} />);
    await userEvent.click(screen.getByRole("button", { name: "Summarize logs" }));
    expect(screen.getByRole("button", { name: "Summarize logs" })).toBeDisabled();
    expect(screen.getByRole("status")).toBeInTheDocument();
    resolve(new Response(JSON.stringify(digest()), { status: 200, headers: { "Content-Type": "application/json" } }));
    await waitFor(() => expect(screen.getByText("api logged 12 errors")).toBeInTheDocument());
  });
});

describe("security digest card", () => {
  const sec = (over: Record<string, unknown> = {}) =>
    digest({
      title: "Security digest",
      needsAttention: true,
      statements: [{ text: "Scan failed for web: no data, not a clean result", evidence: ["ev_1"] }],
      ...over,
    });

  it("shows the attention badge and renders backend statements as given", async () => {
    const f = mockFetch(200, sec());
    render(
      <DigestPanel
        heading="Security digest"
        buttonLabel="Create security digest"
        showAttention
        run={() => api.securityDigest(hostId)}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Create security digest" }));
    expect(await screen.findByText("Needs attention")).toBeInTheDocument();
    expect(screen.getByText(/Scan failed for web/)).toBeInTheDocument();
    expect(screen.queryByText(/no vulnerabilities/i)).not.toBeInTheDocument();
    expect((f.mock.calls[0] as [string, RequestInit])[0]).toBe("/api/security/digest");
    expect(JSON.parse((f.mock.calls[0] as [string, RequestInit])[1].body as string)).toEqual({ hostId });
  });

  it("has no badge when nothing needs attention", async () => {
    mockFetch(200, sec({ needsAttention: false }));
    render(
      <DigestPanel heading="Security digest" buttonLabel="Go" showAttention run={() => api.securityDigest()} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Go" }));
    await screen.findByText(/Scan failed/);
    expect(screen.queryByText("Needs attention")).not.toBeInTheDocument();
  });
});
