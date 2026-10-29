import { act, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AnimatedNumber } from "./AnimatedNumber";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("AnimatedNumber", () => {
  it("shows the value at once with reduced motion", () => {
    vi.stubGlobal("matchMedia", () => ({ matches: true }));
    const { container } = render(<p>{<AnimatedNumber value={42} />} hosts</p>);
    expect(container.textContent).toBe("42 hosts");
  });

  it("counts up and ends on exactly the final value", () => {
    vi.useFakeTimers({ toFake: ["requestAnimationFrame", "cancelAnimationFrame", "performance"] });
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    const { container, rerender } = render(<p><AnimatedNumber value={10} duration={500} /></p>);
    expect(container.textContent).toBe("0");
    act(() => {
      vi.advanceTimersByTime(250);
    });
    const mid = Number(container.textContent);
    expect(mid).toBeGreaterThan(0);
    expect(mid).toBeLessThan(10);
    act(() => {
      vi.advanceTimersByTime(400);
    });
    expect(container.textContent).toBe("10");

    rerender(<p><AnimatedNumber value={3} duration={500} /></p>);
    act(() => {
      vi.advanceTimersByTime(600);
    });
    expect(container.textContent).toBe("3");
  });
});
