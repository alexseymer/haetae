import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { expectNoDialogScroll } from "../test/dialogGuard";
import { AddHostDialog } from "./AddHostDialog";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const COMMAND = "curl -fsS 'https://haetae.example/install.sh' | sh -s -- --token 'tok' --ca-pin 'sha256:abc'";

function jsonResponse(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

function hostRow(connected: boolean) {
  return {
    id: "h9",
    name: "new-host",
    hostname: "new-host",
    os: "linux",
    kernel: "6",
    arch: "amd64",
    dockerVersion: "24",
    agentVersion: "0.1.0",
    connected,
    lastSeen: new Date().toISOString(),
    enrolledAt: new Date().toISOString(),
    containers: { running: 0, total: 0 },
  };
}

function setup(state: { hosts: unknown[]; enrollStatus?: number }) {
  const fetchMock = vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    if (url === "/api/hosts/enrollment" && init?.method === "POST") {
      if (state.enrollStatus === 409) return jsonResponse({ error: "a host with that name already exists" }, 409);
      return jsonResponse(
        { token: "tok", expiresAt: "2030-01-01T00:00:00Z", command: COMMAND, host: "new-host" },
        201,
      );
    }
    if (url === "/api/hosts") return jsonResponse({ hosts: state.hosts });
    return jsonResponse({}, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  const onClose = vi.fn();
  render(
    <MemoryRouter>
      <AddHostDialog open onClose={onClose} />
    </MemoryRouter>,
  );
  return { fetchMock, onClose };
}

describe("AddHostDialog", () => {
  it("step 1 asks for a name, with a step indicator and helper text", async () => {
    setup({ hosts: [] });
    const dialog = screen.getByRole("dialog", { name: "Add host" });
    expect(dialog).toBeInTheDocument();
    const steps = screen.getByRole("list", { name: "Add host progress" });
    expect(steps).toHaveTextContent("Name");
    expect(steps).toHaveTextContent("Install");
    expect(steps).toHaveTextContent("Connected");
    expect(steps.querySelector('[aria-current="step"]')).toHaveTextContent("Name");
    expect(screen.getByLabelText("Host name")).toBeInTheDocument();
    expect(screen.getByText(/How long the one-time command stays valid/)).toBeInTheDocument();
    expectNoDialogScroll();
  });

  it("step 2 shows the command with a waiting state; Back returns to step 1", async () => {
    setup({ hosts: [] });
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Host name"), "new-host");
    await user.click(screen.getByRole("button", { name: "Get install command" }));
    expect(await screen.findByLabelText("Command")).toHaveTextContent("--token");
    expect(screen.getByRole("status")).toHaveTextContent("Waiting for the host to connect");
    expect(
      screen.getByRole("list", { name: "Add host progress" }).querySelector('[aria-current="step"]'),
    ).toHaveTextContent("Install");
    expectNoDialogScroll();

    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByLabelText("Host name")).toHaveValue("new-host");
    expect(screen.queryByLabelText("Command")).not.toBeInTheDocument();
  });

  it("turns into Connected when the host appears in the hosts API", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const state = { hosts: [] as unknown[] };
    setup(state);
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await user.type(screen.getByLabelText("Host name"), "new-host");
    await user.click(screen.getByRole("button", { name: "Get install command" }));
    expect(await screen.findByText(/Waiting for the host to connect/)).toBeInTheDocument();

    state.hosts = [hostRow(false)];
    await act(async () => {
      vi.advanceTimersByTime(3100);
    });
    expect(await screen.findByText(/has enrolled\. Waiting for it to come online/)).toBeInTheDocument();

    state.hosts = [hostRow(true)];
    await act(async () => {
      vi.advanceTimersByTime(3100);
    });
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Connected"));
    expect(screen.getByText(/is online/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open host" })).toBeInTheDocument();
    expectNoDialogScroll();
  });

  it("shows the server error on a duplicate name and stays on step 1", async () => {
    setup({ hosts: [], enrollStatus: 409 });
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Host name"), "new-host");
    await user.click(screen.getByRole("button", { name: "Get install command" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/already exists/);
    expect(screen.getByLabelText("Host name")).toBeInTheDocument();
  });

  it("Done closes the dialog", async () => {
    const { onClose } = setup({ hosts: [] });
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Host name"), "new-host");
    await user.click(screen.getByRole("button", { name: "Get install command" }));
    await user.click(await screen.findByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("never shows a previous command when reopened", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation((url: string, init?: RequestInit) =>
        url === "/api/hosts/enrollment" && init?.method === "POST"
          ? jsonResponse({ token: "t", expiresAt: "2030-01-01T00:00:00Z", command: COMMAND, host: "h" }, 201)
          : jsonResponse({ hosts: [] }),
      ),
    );
    const user = userEvent.setup();
    const ui = (open: boolean) => (
      <MemoryRouter>
        <AddHostDialog open={open} onClose={() => undefined} />
      </MemoryRouter>
    );
    const { rerender } = render(ui(true));
    await user.type(screen.getByLabelText("Host name"), "h");
    await user.click(screen.getByRole("button", { name: "Get install command" }));
    expect(await screen.findByLabelText("Command")).toBeInTheDocument();
    rerender(ui(false));
    rerender(ui(true));
    // Synchronously after reopening: step 1, no command, empty name.
    expect(screen.queryByLabelText("Command")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Host name")).toHaveValue("");
  });
});
