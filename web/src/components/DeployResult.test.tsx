import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Deploy } from "../api/types";
import { DeployResult } from "./DeployResult";

function base(partial: Partial<Deploy>): Deploy {
  return {
    id: "d1",
    projectId: "p1",
    projectName: "shop",
    status: "healthy",
    commit: "abc",
    trigger: "manual",
    rollbackOf: null,
    createdAt: "",
    startedAt: null,
    finishedAt: null,
    url: "",
    error: "",
    message: "",
    ...partial,
  };
}

describe("DeployResult", () => {
  it("renders an anchor only for valid https URLs", () => {
    const { rerender } = render(
      <DeployResult deploy={base({ status: "healthy", url: "https://app.example.com" })} />,
    );
    const link = screen.getByRole("link");
    expect(link).toHaveAttribute("href", "https://app.example.com/");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute("target", "_blank");

    rerender(
      <DeployResult deploy={base({ status: "healthy", url: "javascript:alert(1)" })} />,
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByText("javascript:alert(1)")).toBeInTheDocument();

    rerender(
      <DeployResult deploy={base({ status: "healthy", url: "http://app.example.com" })} />,
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByText("http://app.example.com")).toBeInTheDocument();
  });

  it("shows failure error in a pre with redaction note", () => {
    render(
      <DeployResult
        deploy={base({ status: "failed", error: "compose up failed" })}
      />,
    );
    expect(
      screen.getByText(/Output is redacted on the server/),
    ).toBeInTheDocument();
    expect(screen.getByText("compose up failed")).toBeInTheDocument();
  });

  it("explains rolled_back and shows both errors", () => {
    render(
      <DeployResult
        deploy={base({
          status: "rolled_back",
          error: "crash loop",
          message: "restored abc1234",
        })}
      />,
    );
    expect(
      screen.getByText(/The new version failed to start/),
    ).toBeInTheDocument();
    expect(screen.getByText("crash loop")).toBeInTheDocument();
    expect(screen.getByText("restored abc1234")).toBeInTheDocument();
  });
});
