import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Loading } from "./Skeleton";

describe("Loading skeleton", () => {
  it("is a busy status with an accessible label and decorative shapes", () => {
    const { container } = render(<Loading label="Loading hosts…" variant="page" />);
    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText("Loading hosts…")).toHaveClass("sr-only");
    const shapes = container.querySelectorAll(".skeleton");
    expect(shapes.length).toBeGreaterThan(3);
    shapes.forEach((s) => expect(s).toHaveAttribute("aria-hidden", "true"));
  });

  it("renders the requested number of table rows", () => {
    const { container } = render(<Loading variant="table" rows={3} />);
    expect(container.querySelectorAll(".skeleton-row")).toHaveLength(3);
  });
});
