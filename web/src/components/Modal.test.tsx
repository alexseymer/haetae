import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Modal } from "./Modal";

describe("Modal", () => {
  it("traps focus and closes on Esc", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <Modal title="Test modal" open onClose={onClose}>
        <button type="button">First</button>
        <button type="button">Second</button>
      </Modal>,
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    // Close button is first focusable in header
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "First" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Second" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalled();
  });

  it("marks siblings inert while open and restores after close", () => {
    const { rerender } = render(
      <div>
        <button type="button" data-testid="outside">
          Outside
        </button>
        <Modal title="Test modal" open onClose={() => undefined}>
          <button type="button">Inside</button>
        </Modal>
      </div>,
    );
    const outside = screen.getByTestId("outside");
    expect(outside.inert).toBe(true);

    rerender(
      <div>
        <button type="button" data-testid="outside">
          Outside
        </button>
        <Modal title="Test modal" open={false} onClose={() => undefined}>
          <button type="button">Inside</button>
        </Modal>
      </div>,
    );
    expect(outside.inert).toBe(false);
  });
});
