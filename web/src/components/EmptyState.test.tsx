import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { EmptyState } from "./EmptyState";

describe("EmptyState", () => {
  it("shows what is missing, a primary action and a safe docs link", () => {
    render(
      <EmptyState
        title="No hosts yet"
        action={<button type="button">Add your first host</button>}
        doc={{ name: "enroll", label: "How enrolling works" }}
      >
        Haetae needs a small agent on each server.
      </EmptyState>,
    );
    expect(screen.getByText("No hosts yet")).toBeInTheDocument();
    expect(screen.queryByRole("heading")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add your first host" })).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "How enrolling works" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/alexseymer/haetae/blob/main/docs/enroll.md",
    );
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });
});
