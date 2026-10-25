import { describe, expect, it } from "vitest";
import { isSafeEvidencePath } from "./evidenceLink";

describe("isSafeEvidencePath", () => {
  it("allows app-relative paths", () => {
    expect(isSafeEvidencePath("/incidents")).toBe(true);
    expect(isSafeEvidencePath("/hosts/abc?tab=logs")).toBe(true);
    expect(isSafeEvidencePath("/incidents/550e8400-e29b-41d4-a716-446655440000")).toBe(
      true,
    );
  });

  it("rejects schemes, protocol-relative, and absolute URLs", () => {
    expect(isSafeEvidencePath("javascript:alert(1)")).toBe(false);
    expect(isSafeEvidencePath("//evil.example/phish")).toBe(false);
    expect(isSafeEvidencePath("https://evil.example/x")).toBe(false);
    expect(isSafeEvidencePath("http://evil.example/x")).toBe(false);
    expect(isSafeEvidencePath("")).toBe(false);
    expect(isSafeEvidencePath("hosts/x")).toBe(false);
  });
});
