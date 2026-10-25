import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Chart } from "./Chart";

describe("Chart", () => {
  it("renders accessible fallback table", () => {
    const points = [
      { t: 1_700_000_000_000, v: 10 },
      { t: 1_700_000_060_000, v: 20 },
    ];
    render(
      <Chart
        title="CPU"
        unit="%"
        yMax={100}
        series={[{ label: "CPU", points, color: "var(--color-chart-1)" }]}
      />,
    );
    const table = screen.getByRole("table", { hidden: true });
    expect(table).toBeInTheDocument();
    expect(screen.getByText("CPU", { selector: "caption" })).toBeInTheDocument();
  });

  it("handles empty data", () => {
    render(
      <Chart
        title="CPU"
        unit="%"
        series={[{ label: "CPU", points: [], color: "var(--color-chart-1)" }]}
      />,
    );
    expect(screen.getAllByText("No data yet").length).toBeGreaterThan(0);
  });
});
