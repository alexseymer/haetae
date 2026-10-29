import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, type ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthProvider, useAuth } from "../auth";
import { ThemeProvider } from "../theme";
import { Layout } from "./Layout";

function Seed({ children }: { children: ReactNode }) {
  const { setSession } = useAuth();
  useEffect(() => {
    setSession({ id: "1", email: "ada@example.com", role: "admin" }, "Acme Ops");
  }, [setSession]);
  return children;
}

function Where() {
  const { pathname, state } = useLocation();
  return <p data-testid="where">{pathname + (state ? ":state" : "")}</p>;
}

function renderLayout(path = "/") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ThemeProvider>
        <AuthProvider>
          <Seed>
            <Routes>
              <Route element={<Layout />}>
                <Route path="*" element={<Where />} />
              </Route>
            </Routes>
          </Seed>
        </AuthProvider>
      </ThemeProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((url: string) => {
      if (String(url).includes("/api/incidents")) {
        return Promise.resolve(
          new Response(JSON.stringify({ incidents: [{ id: "i1" }, { id: "i2" }] }), { status: 200 }),
        );
      }
      return Promise.resolve(new Response(JSON.stringify({ version: "1.2.3" }), { status: 200 }));
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Layout", () => {
  it("lists the main sections and marks the current page", async () => {
    renderLayout("/incidents");
    const nav = screen.getByRole("navigation", { name: "Main" });
    for (const name of ["Hosts", "Incidents", "Chat", "Help", "Settings"]) {
      expect(nav.querySelector(`a[href]`)).not.toBeNull();
      expect(screen.getAllByRole("link", { name: new RegExp(name) }).length).toBeGreaterThan(0);
    }
    const current = nav.querySelectorAll('[aria-current="page"]');
    expect(current).toHaveLength(1);
    expect(current[0]).toHaveTextContent("Incidents");
    expect(await screen.findByText("2 open")).toBeInTheDocument();
  });

  it("treats host pages as part of Hosts", () => {
    renderLayout("/hosts/abc");
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(nav.querySelector('[aria-current="page"]')).toHaveTextContent("Hosts");
  });

  it("collapses into a drawer on narrow screens", async () => {
    const user = userEvent.setup();
    renderLayout("/");
    const toggle = screen.getByRole("button", { name: "Menu" });
    const sidebar = document.getElementById("app-sidebar")!;
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(sidebar).not.toHaveClass("open");
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(sidebar).toHaveClass("open");
    await user.keyboard("{Escape}");
    expect(sidebar).not.toHaveClass("open");
    expect(toggle).toHaveFocus();
  });

  it("closes the drawer after navigating", async () => {
    const user = userEvent.setup();
    renderLayout("/");
    await user.click(screen.getByRole("button", { name: "Menu" }));
    await user.click(within_nav("Chat"));
    expect(screen.getByTestId("where")).toHaveTextContent("/chat");
    expect(document.getElementById("app-sidebar")).not.toHaveClass("open");
  });

  it("toggles the theme and remembers it", async () => {
    const user = userEvent.setup();
    renderLayout("/");
    await user.click(screen.getByRole("button", { name: "Switch to dark theme" }));
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem("haetae.theme")).toBe("dark");
    expect(screen.getByRole("button", { name: "Switch to light theme" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("opens the chat from the Ask the operator button", async () => {
    const user = userEvent.setup();
    renderLayout("/");
    await user.click(screen.getByRole("button", { name: "Ask the operator" }));
    await waitFor(() => expect(screen.getByTestId("where")).toHaveTextContent("/chat:state"));
  });

  it("shows the organisation and an account menu", async () => {
    const user = userEvent.setup();
    renderLayout("/");
    expect(await screen.findByText("Acme Ops")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /ada@example.com/ }));
    expect(screen.getByRole("menuitem", { name: "Sign out" })).toBeInTheDocument();
  });
});

function within_nav(name: string): HTMLElement {
  const nav = screen.getByRole("navigation", { name: "Main" });
  const link = Array.from(nav.querySelectorAll("a")).find((a) => a.textContent?.includes(name));
  if (!link) throw new Error(`no nav link ${name}`);
  return link;
}
