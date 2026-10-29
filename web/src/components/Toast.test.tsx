import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToastProvider, useToast } from "./Toast";

function Trigger({ text }: { text: string }) {
  const { notify } = useToast();
  return (
    <button type="button" onClick={() => notify(text)}>
      go
    </button>
  );
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Toast", () => {
  it("announces in a polite live region and goes away on its own", async () => {
    vi.useFakeTimers();
    render(
      <ToastProvider>
        <Trigger text="Saved" />
      </ToastProvider>,
    );
    const region = screen.getByRole("status");
    expect(region).toHaveAttribute("aria-live", "polite");
    act(() => screen.getByRole("button").click());
    expect(region).toHaveTextContent("Saved");
    // reduced motion (no matchMedia in jsdom): removed straight after the delay
    act(() => {
      vi.advanceTimersByTime(3300);
    });
    expect(region).not.toHaveTextContent("Saved");
  });

  it("plays a leaving animation first when motion is allowed", () => {
    vi.useFakeTimers();
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    render(
      <ToastProvider>
        <Trigger text="Copied" />
      </ToastProvider>,
    );
    act(() => screen.getByRole("button").click());
    act(() => {
      vi.advanceTimersByTime(3300);
    });
    expect(screen.getByText("Copied").closest(".toast")).toHaveClass("leaving");
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(screen.queryByText("Copied")).toBeNull();
  });

  it("is a no-op without a provider", () => {
    render(<Trigger text="x" />);
    act(() => screen.getByRole("button").click());
    expect(screen.queryByText("x")).toBeNull();
  });
});
