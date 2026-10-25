import { describe, expect, it } from "vitest";
import { isSafeEvidencePath } from "./evidenceLink";

const uuid = "550e8400-e29b-41d4-a716-446655440000";

describe("isSafeEvidencePath", () => {
  it("allows exact allowlisted paths", () => {
    expect(isSafeEvidencePath("/incidents")).toBe(true);
    expect(isSafeEvidencePath("/hosts")).toBe(true);
    expect(isSafeEvidencePath(`/hosts/${uuid}?tab=logs`)).toBe(true);
    expect(isSafeEvidencePath(`/incidents/${uuid}`)).toBe(true);
  });

  it("rejects prefix tricks and unsafe URLs", () => {
    expect(isSafeEvidencePath(`/hosts/${uuid}/../evil`)).toBe(false);
    expect(isSafeEvidencePath("/hosts/not-a-uuid")).toBe(false);
    expect(isSafeEvidencePath("/settings")).toBe(false);
    expect(isSafeEvidencePath("javascript:alert(1)")).toBe(false);
    expect(isSafeEvidencePath("//evil.example/phish")).toBe(false);
    expect(isSafeEvidencePath("https://evil.example/x")).toBe(false);
    expect(isSafeEvidencePath("")).toBe(false);
    expect(isSafeEvidencePath("hosts/x")).toBe(false);
  });
});
