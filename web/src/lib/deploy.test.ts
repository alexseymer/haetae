import { describe, expect, it } from "vitest";
import type { Deploy } from "../api/types";
import {
  canRollback,
  domainHref,
  safeHttpsHref,
  shortSha,
  sourceSummary,
  triggerLabel,
} from "./deploy";

function deploy(partial: Partial<Deploy> & Pick<Deploy, "id" | "status">): Deploy {
  return {
    projectId: "p1",
    projectName: "shop",
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

describe("safeHttpsHref", () => {
  it("allows only https URLs", () => {
    expect(safeHttpsHref("https://app.example.com")).toBe("https://app.example.com/");
    expect(safeHttpsHref("http://app.example.com")).toBeNull();
    expect(safeHttpsHref("javascript:alert(1)")).toBeNull();
    expect(safeHttpsHref("not a url")).toBeNull();
  });
});

describe("domainHref", () => {
  it("links only valid host names", () => {
    expect(domainHref("app.example.com")).toBe("https://app.example.com");
    expect(domainHref("http://evil")).toBeNull();
    expect(domainHref("")).toBeNull();
  });
});

describe("sourceSummary", () => {
  it("summarises git and inline sources", () => {
    expect(
      sourceSummary({
        kind: "git",
        gitUrl: "https://github.com/acme/shop",
        gitRef: "main",
      }),
    ).toBe("github.com/acme/shop@main");
    expect(sourceSummary({ kind: "inline" })).toBe("pasted compose");
  });
});

describe("shortSha / triggerLabel", () => {
  it("shortens commits and labels triggers", () => {
    expect(shortSha("abcdef0123456789")).toBe("abcdef0");
    expect(triggerLabel("manual")).toBe("Manual");
    expect(triggerLabel("push")).toBe("Push");
    expect(triggerLabel("rollback")).toBe("Rollback");
  });
});

describe("canRollback", () => {
  it("allows rollback only for healthy deploys that are not the latest healthy", () => {
    const list = [
      deploy({ id: "d1", status: "healthy", commit: "aaa" }),
      deploy({ id: "d2", status: "healthy", commit: "bbb" }),
      deploy({ id: "d3", status: "failed", commit: "ccc" }),
    ];
    expect(canRollback(list[0]!, list)).toBe(false);
    expect(canRollback(list[1]!, list)).toBe(true);
    expect(canRollback(list[2]!, list)).toBe(false);
  });
});
