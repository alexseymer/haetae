import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { OperatorSettings } from "../api/operatorTypes";
import { providerStatus, validateTokenCap } from "../lib/operatorSettings";
import { OperatorSettingsSection } from "./OperatorSettings";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const base: OperatorSettings = {
  aiEnabled: false,
  autonomy: "report",
  dailyTokenCap: 200000,
  provider: { configured: true, name: "anthropic", local: false },
  usageToday: { inputTokens: 1200, outputTokens: 300 },
  available: false,
  reason: "turned off",
};

function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

function testStub(result: unknown, status = 200) {
  const posts: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        posts.push(url);
        return json(result, status);
      }
      return json({ ...base, provider: { ...base.provider, model: "vendor/m" } });
    }),
  );
  return posts;
}

describe("Test connection", () => {
  it("shows success with latency and tool support, and shows the model", async () => {
    const posts = testStub({ ok: true, latencyMs: 412, provider: "openai", model: "vendor/m", local: false, toolCalling: "supported" });
    render(<OperatorSettingsSection isAdmin />);
    expect(await screen.findByTestId("operator-provider")).toHaveTextContent("model vendor/m");
    await userEvent.click(screen.getByRole("button", { name: "Test connection" }));
    const out = await screen.findByText(/Connected to openai \/ vendor\/m in 412 ms, tool calling: supported/);
    expect(out).toHaveAttribute("aria-live", "polite");
    expect(posts).toEqual(["/api/settings/operator/test"]);
  });

  it("warns when the model does not call tools", async () => {
    testStub({ ok: true, latencyMs: 90, provider: "openai", model: "free/m", local: false, toolCalling: "not used by this model" });
    render(<OperatorSettingsSection isAdmin />);
    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/did not call tools; the operator needs a model with tool support/)).toBeInTheDocument();
  });

  it("shows the reason and hint on failure and disables the button while running", async () => {
    let release: (r: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((_u: string, init?: RequestInit) =>
        init?.method === "POST" ? new Promise<Response>((res) => (release = res)) : json(base),
      ),
    );
    render(<OperatorSettingsSection isAdmin />);
    const btn = await screen.findByRole("button", { name: "Test connection" });
    await userEvent.click(btn);
    expect(await screen.findByRole("button", { name: "Testing…" })).toBeDisabled();
    release(
      new Response(
        JSON.stringify({ ok: false, latencyMs: 5, provider: "openai", model: "m", local: false, toolCalling: "unknown",
          reason: "the provider rejected the API key (HTTP 401)", hint: "Check HAETAE_AI_API_KEY." }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    expect(await screen.findByText(/the provider rejected the API key \(HTTP 401\)\. Check HAETAE_AI_API_KEY\./)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Test connection" })).toBeEnabled();
  });

  it("is hidden from members", async () => {
    testStub({});
    render(<OperatorSettingsSection isAdmin={false} />);
    await screen.findByTestId("operator-provider");
    expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
  });
});

describe("OperatorSettingsSection", () => {
  it("shows provider, usage and status, and saves changes", async () => {
    const puts: unknown[] = [];
    const fetchMock = vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body));
        puts.push(body);
        return json({ ...base, ...body, available: true, reason: "" });
      }
      return json(base);
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<OperatorSettingsSection isAdmin />);

    expect(await screen.findByTestId("operator-provider")).toHaveTextContent(
      "anthropic (cloud, redacted before sending)",
    );
    expect(screen.getByTestId("operator-usage")).toHaveTextContent("1,200 input and 300 output");
    expect(screen.getByTestId("operator-status")).toHaveTextContent("unavailable (turned off)");

    await userEvent.click(screen.getByLabelText("Enable the AI operator"));
    await userEvent.selectOptions(screen.getByLabelText("Autonomy"), "act");
    expect(document.getElementById("operator-autonomy-help")).toHaveTextContent(
      "restart crashed containers on its own. It never deletes anything.",
    );
    const cap = screen.getByLabelText("Daily token cap");
    await userEvent.clear(cap);
    await userEvent.type(cap, "5000");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]).toEqual({ aiEnabled: true, autonomy: "act", dailyTokenCap: 5000 });
    expect(await screen.findByText("Saved.")).toBeInTheDocument();
    expect(screen.getByTestId("operator-status")).toHaveTextContent("available");
  });

  it("rejects an invalid cap without calling the API", async () => {
    const fetchMock = vi.fn().mockImplementation(() => json(base));
    vi.stubGlobal("fetch", fetchMock);
    render(<OperatorSettingsSection isAdmin />);
    const cap = await screen.findByLabelText("Daily token cap");
    await userEvent.clear(cap);
    await userEvent.type(cap, "50000001");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("at most 50,000,000");
    expect(fetchMock.mock.calls.filter((c) => c[1]?.method === "PUT")).toHaveLength(0);
  });

  it("is read-only for members", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => json(base)));
    render(<OperatorSettingsSection isAdmin={false} />);
    expect(await screen.findByLabelText("Autonomy")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save" })).not.toBeInTheDocument();
  });
});

describe("operator settings helpers", () => {
  it("describes the provider", () => {
    expect(providerStatus({ configured: false, name: "", local: false })).toMatch(
      /^Not configured: set HAETAE_AI_PROVIDER/,
    );
    expect(providerStatus({ configured: true, name: "ollama", local: true })).toContain("local");
  });
  it("validates the cap", () => {
    expect(validateTokenCap("0")).toBeNull();
    expect(validateTokenCap("50000000")).toBeNull();
    expect(validateTokenCap("-1")).not.toBeNull();
    expect(validateTokenCap("1.5")).not.toBeNull();
    expect(validateTokenCap("")).not.toBeNull();
  });
});
