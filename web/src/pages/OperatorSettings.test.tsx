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
