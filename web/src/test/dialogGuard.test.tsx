import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Modal } from "../components/Modal";
import { dialogOverflows, expectNoDialogScroll } from "./dialogGuard";

function stub(el: HTMLElement, sizes: { sh: number; ch: number; sw?: number; cw?: number }) {
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: sizes.sh });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: sizes.ch });
  Object.defineProperty(el, "scrollWidth", { configurable: true, value: sizes.sw ?? 100 });
  Object.defineProperty(el, "clientWidth", { configurable: true, value: sizes.cw ?? 100 });
}

describe("dialog scroll guard", () => {
  it("passes for a dialog that fits", () => {
    render(
      <Modal title="Fits" open onClose={() => undefined}>
        <p>Short</p>
      </Modal>,
    );
    stub(screen.getByRole("dialog"), { sh: 300, ch: 300 });
    expect(dialogOverflows()).toEqual([]);
    expect(() => expectNoDialogScroll()).not.toThrow();
  });

  it("fails when content is taller than the dialog", () => {
    render(
      <Modal title="Too tall" open onClose={() => undefined}>
        <p>Long</p>
      </Modal>,
    );
    stub(screen.getByRole("dialog"), { sh: 900, ch: 600 });
    expect(() => expectNoDialogScroll()).toThrow(/needs a scrollbar/);
  });

  it("fails on horizontal overflow", () => {
    render(
      <Modal title="Too wide" open onClose={() => undefined}>
        <p>Wide</p>
      </Modal>,
    );
    stub(screen.getByRole("dialog"), { sh: 300, ch: 300, sw: 700, cw: 500 });
    expect(dialogOverflows().length).toBe(1);
  });
});
