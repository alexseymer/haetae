import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DeployLogViewer } from "./DeployLogViewer";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function json(data: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(data), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

describe("DeployLogViewer", () => {
  it("polls by seq and stops when done", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let calls = 0;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (!url.includes("/logs")) return json({});
      calls += 1;
      const after = Number(new URL(url, "http://local").searchParams.get("after") || "0");
      if (calls === 1) {
        expect(after).toBe(0);
        return json({
          logs: [{ seq: 1, stream: "stdout", text: "hello", withheld: false }],
          done: false,
          status: "building",
        });
      }
      if (calls === 2) {
        expect(after).toBe(1);
        return json({
          logs: [{ seq: 2, stream: "stderr", text: "warn", withheld: false }],
          done: true,
          status: "healthy",
        });
      }
      return json({ logs: [], done: true, status: "healthy" });
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<DeployLogViewer deployId="d1" />);

    await waitFor(() => expect(screen.getByRole("log")).toHaveTextContent("hello"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1600);
    });
    await waitFor(() =>
      expect(screen.getByRole("log")).toHaveTextContent("stderr: warn"),
    );
    const afterDone = calls;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(calls).toBe(afterDone);
  });

  it("ignores a stale response after deployId changes", async () => {
    let resolveFirst: ((r: Response) => void) | null = null;
    const fetchMock = vi.fn().mockImplementation((url: string) => {
      if (!url.includes("/logs")) return json({});
      if (url.includes("d1") && !resolveFirst) {
        return new Promise<Response>((resolve) => {
          resolveFirst = resolve;
        });
      }
      return json({
        logs: [{ seq: 1, stream: "stdout", text: "from-d2", withheld: false }],
        done: true,
        status: "healthy",
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const { rerender } = render(<DeployLogViewer deployId="d1" />);
    await waitFor(() => expect(resolveFirst).not.toBeNull());
    rerender(<DeployLogViewer deployId="d2" />);
    await waitFor(() =>
      expect(screen.getByRole("log")).toHaveTextContent("from-d2"),
    );
    resolveFirst!(
      new Response(
        JSON.stringify({
          logs: [{ seq: 1, stream: "stdout", text: "stale-d1", withheld: false }],
          done: true,
          status: "healthy",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    await waitFor(() =>
      expect(screen.getByRole("log")).toHaveTextContent("from-d2"),
    );
    expect(screen.queryByText(/stale-d1/)).not.toBeInTheDocument();
  });

  it("shows withheld marker and strips ANSI", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            logs: [
              {
                seq: 1,
                stream: "stdout",
                text: "\u001b[31mred\u001b[0m",
                withheld: false,
              },
              { seq: 2, stream: "stdout", text: "", withheld: true },
            ],
            done: true,
            status: "failed",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );
    render(<DeployLogViewer deployId="d1" />);
    const log = await screen.findByRole("log");
    expect(log).toHaveTextContent("red");
    expect(log).toHaveTextContent("[output withheld: redaction failed]");
    expect(log.textContent).not.toContain("\u001b");
  });

  it("caps at 5000 lines with a show-all control", async () => {
    const logs = Array.from({ length: 5005 }, (_, i) => ({
      seq: i + 1,
      stream: "stdout",
      text: `line-${i + 1}`,
      withheld: false,
    }));
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ logs, done: true, status: "healthy" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    const user = userEvent.setup();
    render(<DeployLogViewer deployId="d1" />);
    expect(await screen.findByText(/Show all \(5005 lines\)/)).toBeInTheDocument();
    const log = screen.getByRole("log");
    expect(log).not.toHaveTextContent("line-1\n");
    expect(log).toHaveTextContent("line-5005");
    await user.click(screen.getByRole("button", { name: /Show all/ }));
    expect(screen.getByRole("log")).toHaveTextContent("line-1");
  });
});
