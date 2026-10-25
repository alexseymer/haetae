import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Deploy } from "../api/types";
import { DeployHistory } from "./DeployHistory";

function d(
  partial: Partial<Deploy> & Pick<Deploy, "id" | "status" | "commit" | "trigger">,
): Deploy {
  return {
    projectId: "p1",
    projectName: "shop",
    rollbackOf: null,
    createdAt: new Date().toISOString(),
    startedAt: new Date().toISOString(),
    finishedAt: new Date().toISOString(),
    url: "https://shop.example.com",
    error: "",
    message: "",
    ...partial,
  };
}

const list: Deploy[] = [
  d({ id: "d1", status: "healthy", commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", trigger: "manual" }),
  d({ id: "d2", status: "healthy", commit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", trigger: "push" }),
  d({ id: "d3", status: "failed", commit: "cccccccccccccccccccccccccccccccccccccccc", trigger: "manual" }),
];

describe("DeployHistory", () => {
  it("shows status labels, triggers, and rollback only for older healthy deploys", async () => {
    const user = userEvent.setup();
    const onRollback = vi.fn().mockResolvedValue(undefined);
    render(
      <DeployHistory
        deploys={list}
        selectedId={null}
        onSelect={() => {}}
        isAdmin
        onRollback={onRollback}
      />,
    );
    expect(screen.getAllByText("Healthy")).toHaveLength(2);
    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.getAllByText("Manual").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("Push")).toBeInTheDocument();

    const rollbackBtns = screen.getAllByRole("button", { name: "Rollback" });
    expect(rollbackBtns).toHaveLength(1);
    await user.click(rollbackBtns[0]!);
    const dialog = screen.getByRole("dialog", { name: "Confirm rollback" });
    expect(dialog).toBeInTheDocument();
    expect(dialog).toHaveTextContent(/bbbbbbb/);
    await user.click(screen.getByRole("button", { name: "Roll back" }));
    expect(onRollback).toHaveBeenCalledWith(list[1]);
  });

  it("hides rollback for members", () => {
    render(
      <DeployHistory
        deploys={list}
        selectedId={null}
        onSelect={() => {}}
        isAdmin={false}
        onRollback={async () => {}}
      />,
    );
    expect(screen.queryByRole("button", { name: "Rollback" })).not.toBeInTheDocument();
  });
});
