import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PhaseStepper } from "./PhaseStepper";

describe("PhaseStepper", () => {
  it("marks the active phase with aria-current", () => {
    render(<PhaseStepper status="building" />);
    const current = screen.getByText("Building");
    expect(current).toHaveAttribute("aria-current", "step");
    expect(screen.getByText("Fetching")).not.toHaveAttribute("aria-current");
  });

  it("shows failure outcome", () => {
    render(<PhaseStepper status="failed" />);
    expect(screen.getByRole("status")).toHaveTextContent("Failed");
  });

  it("shows rollback states", () => {
    const { rerender } = render(<PhaseStepper status="rolling_back" />);
    expect(screen.getByRole("status")).toHaveTextContent("Rolling back");
    rerender(<PhaseStepper status="rolled_back" />);
    expect(screen.getByRole("status")).toHaveTextContent("Rolled back");
    rerender(<PhaseStepper status="rollback_failed" />);
    expect(screen.getByRole("status")).toHaveTextContent("Rollback failed");
  });

  it("completes all steps when healthy", () => {
    render(<PhaseStepper status="healthy" />);
    expect(screen.getByText("Healthy")).toHaveAttribute("aria-current", "step");
  });
});
