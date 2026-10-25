import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuthProvider } from "../auth";
import { SetupPage } from "./Setup";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SetupPage", () => {
  it("submits all fields", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          user: { id: "1", email: "admin@ex.com", role: "admin" },
          orgName: "Widgets",
        }),
        { status: 201, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/setup"]}>
        <AuthProvider>
          <Routes>
            <Route path="/setup" element={<SetupPage />} />
            <Route path="/" element={<div>Home</div>} />
          </Routes>
        </AuthProvider>
      </MemoryRouter>,
    );

    await user.type(screen.getByLabelText("Setup code"), "CODE123");
    await user.type(screen.getByLabelText("Organisation name"), "Widgets");
    await user.type(screen.getByLabelText("Email"), "admin@ex.com");
    await user.type(screen.getByLabelText("Password"), "hunter2");
    const toggle = screen.getByRole("button", { name: "Show password" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Hide password" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await user.click(screen.getByRole("button", { name: "Create organisation" }));

    await waitFor(() => expect(screen.getByText("Home")).toBeInTheDocument());
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({
      setupCode: "CODE123",
      orgName: "Widgets",
      email: "admin@ex.com",
      password: "hunter2",
    });
  });
});
