import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Chart, CHART_TABLE_MAX_ROWS } from "./Chart";

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

  it("renders a single point without throwing", () => {
    render(
      <Chart
        title="CPU"
        unit="%"
        series={[{ label: "CPU", points: [{ t: 1, v: -3 }], color: "var(--color-chart-1)" }]}
      />,
    );
    expect(screen.getByRole("img", { name: /CPU/ })).toBeInTheDocument();
    const path = document.querySelector("svg path");
    expect(path?.getAttribute("d")).toMatch(/^M/);
    expect(path?.getAttribute("d")).not.toMatch(/NaN/);
  });

  it("skips non-finite points in the path", () => {
    const points = [
      { t: 1, v: 10 },
      { t: 2, v: NaN },
      { t: 3, v: Infinity },
      { t: 4, v: 20 },
    ];
    render(
      <Chart
        title="CPU"
        unit="%"
        series={[{ label: "CPU", points, color: "var(--color-chart-1)" }]}
      />,
    );
    const d = document.querySelector("svg path")?.getAttribute("d") ?? "";
    expect(d).not.toMatch(/NaN/);
    expect(d).toMatch(/L/);
  });

  it("bounds the accessible table for a 10k-point series", () => {
    const points = Array.from({ length: 10_000 }, (_, i) => ({
      t: 1_700_000_000_000 + i * 1000,
      v: i % 100,
    }));
    const started = performance.now();
    render(
      <Chart
        title="CPU"
        unit="%"
        series={[{ label: "CPU", points, color: "var(--color-chart-1)" }]}
      />,
    );
    const elapsed = performance.now() - started;
    expect(elapsed).toBeLessThan(5_000);
    const rows = document.querySelectorAll("table.sr-only tbody tr");
    expect(rows.length).toBe(CHART_TABLE_MAX_ROWS);
    expect(
      screen.getByText(`Showing first ${CHART_TABLE_MAX_ROWS} of ${points.length} points.`),
    ).toBeInTheDocument();
  });

  it("announces the active point in a polite live region", async () => {
    const points = [
      { t: 1_700_000_000_000, v: 10 },
      { t: 1_700_000_060_000, v: 20 },
    ];
    const user = userEvent.setup();
    render(
      <Chart
        title="CPU"
        unit="%"
        series={[{ label: "CPU", points, color: "var(--color-chart-1)" }]}
      />,
    );
    const svg = screen.getByRole("img", { name: /CPU/ });
    svg.focus();
    await user.keyboard("{ArrowRight}");
    const live = document.querySelector("[aria-live='polite']");
    expect(live?.textContent).toMatch(/CPU/);
  });
});
