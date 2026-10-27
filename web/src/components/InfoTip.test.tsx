import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { HELP, GLOSSARY_ORDER, docUrl } from "../lib/help";
import { InfoTip } from "./InfoTip";
import { Modal } from "./Modal";

describe("InfoTip", () => {
  it("is a button that toggles a popover and reports aria-expanded", async () => {
    const user = userEvent.setup();
    render(<InfoTip term="severity" />);
    const btn = screen.getByRole("button", { name: "What is Severity?" });
    expect(btn).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("group", { name: "Severity" })).not.toBeInTheDocument();

    await user.click(btn);
    expect(btn).toHaveAttribute("aria-expanded", "true");
    const pop = screen.getByRole("group", { name: "Severity" });
    expect(pop).toHaveTextContent(HELP.severity.body);
    expect(btn).toHaveAttribute("aria-controls", pop.id);
    // A popover, not a dialog: it never traps focus.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(btn).toHaveFocus();

    await user.click(btn);
    expect(btn).toHaveAttribute("aria-expanded", "false");
  });

  it("closes on Esc and returns focus to the button", async () => {
    const user = userEvent.setup();
    render(<InfoTip term="autonomy" />);
    const btn = screen.getByRole("button", { name: /What is Autonomy/ });
    await user.click(btn);
    await user.tab(); // into the docs link inside the popover
    expect(screen.getByRole("link", { name: /Read more/ })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("group", { name: "Autonomy" })).not.toBeInTheDocument();
    expect(btn).toHaveFocus();
  });

  it("closes on an outside click and when focus leaves", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <InfoTip term="rollback" />
        <button type="button">Elsewhere</button>
      </div>,
    );
    await user.click(screen.getByRole("button", { name: /What is Rollback/ }));
    expect(screen.getByRole("group", { name: "Rollback" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Elsewhere" }));
    expect(screen.queryByRole("group", { name: "Rollback" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /What is Rollback/ }));
    await user.tab();
    await user.tab(); // past the docs link, onto Elsewhere
    expect(screen.queryByRole("group", { name: "Rollback" })).not.toBeInTheDocument();
  });

  it("Esc closes only the popover when inside a dialog", async () => {
    const user = userEvent.setup();
    let closed = 0;
    render(
      <Modal title="Dlg" open onClose={() => (closed += 1)}>
        <InfoTip term="enrollToken" />
      </Modal>,
    );
    await user.click(screen.getByRole("button", { name: /What is Install command/ }));
    await user.keyboard("{Escape}");
    expect(closed).toBe(0);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(closed).toBe(1);
  });

  it("docs links open safely and every help text is plain and short", async () => {
    const user = userEvent.setup();
    render(<InfoTip term="scanFailed" />);
    await user.click(screen.getByRole("button"));
    const link = screen.getByRole("link", { name: /Read more/ });
    expect(link).toHaveAttribute("href", docUrl("security"));
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    for (const term of GLOSSARY_ORDER) {
      expect(HELP[term].body.length).toBeLessThan(400);
    }
  });
});
