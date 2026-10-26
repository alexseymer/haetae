import { describe, expect, it } from "vitest";
import type { ContainerVuln } from "../api/securityTypes";
import {
  containerScanStatusLine,
  hostSecurityBadgeText,
  imageUpdateDecisionLabel,
  isScanFailedStatus,
  misconfigKindLabel,
  vulnIdHref,
} from "./security";

function baseContainer(over: Partial<ContainerVuln> = {}): ContainerVuln {
  return {
    containerId: "c1",
    name: "web",
    image: "nginx:1",
    imageId: "",
    scan: { status: "none", error: "", at: null, tool: "" },
    match: { status: "none", error: "", dbBuiltAt: null },
    counts: { critical: 0, high: 0, medium: 0, low: 0, unknown: 0 },
    findings: [],
    ...over,
  };
}

describe("security helpers", () => {
  it("labels misconfigs and update decisions in plain words", () => {
    expect(misconfigKindLabel("docker_socket")).toBe("Docker socket mounted");
    expect(misconfigKindLabel("privileged")).toBe("privileged");
    expect(imageUpdateDecisionLabel("current")).toBe("up to date");
    expect(imageUpdateDecisionLabel("wait")).toBe("waiting 48 h");
    expect(imageUpdateDecisionLabel("eligible_critical_fix")).toBe(
      "eligible because it fixes a critical CVE",
    );
  });

  it("distinguishes none vs failed scan wording", () => {
    expect(containerScanStatusLine(baseContainer(), () => "x", () => "y")).toBe(
      "Not scanned yet",
    );
    expect(
      containerScanStatusLine(
        baseContainer({
          scan: { status: "failed", error: "oom killed", at: null, tool: "" },
        }),
        () => "x",
        () => "y",
      ),
    ).toBe("Scan failed: oom killed");
    expect(
      containerScanStatusLine(
        baseContainer({
          scan: { status: "timeout", error: "deadline", at: null, tool: "" },
        }),
        () => "x",
        () => "y",
      ),
    ).toBe("Scan failed: deadline");
    expect(
      containerScanStatusLine(
        baseContainer({
          scan: { status: "ok", error: "", at: "2026-01-01T00:00:00Z", tool: "syft" },
          match: { status: "failed", error: "db missing", dbBuiltAt: null },
        }),
        () => "1 hr ago",
        () => "d",
      ),
    ).toBe("Scan failed: db missing");
    expect(
      containerScanStatusLine(
        baseContainer({
          scan: { status: "ok", error: "", at: "2026-01-01T00:00:00Z", tool: "syft" },
          match: {
            status: "ok",
            error: "",
            dbBuiltAt: "2026-01-01T00:00:00Z",
          },
        }),
        () => "1 hr ago",
        () => "1 Jan 2026",
      ),
    ).toBe("Scanned 1 hr ago (database 1 Jan 2026)");
  });

  it("treats timeout/oom/unsupported as failed", () => {
    expect(isScanFailedStatus("timeout")).toBe(true);
    expect(isScanFailedStatus("oom")).toBe(true);
    expect(isScanFailedStatus("unsupported")).toBe(true);
    expect(isScanFailedStatus("ok")).toBe(false);
    expect(isScanFailedStatus("none")).toBe(false);
  });

  it("links CVE ids only via https URLs", () => {
    expect(
      vulnIdHref("CVE-1", ["http://evil.example/x", "https://nvd.nist.gov/vuln/detail/CVE-1"]),
    ).toMatch(/^https:\/\/nvd\.nist\.gov\//);
    expect(vulnIdHref("CVE-1", ["http://evil.example/x"])).toBeNull();
    expect(vulnIdHref("CVE-1", [])).toBeNull();
  });

  it("builds host badge text", () => {
    expect(hostSecurityBadgeText(true, { critical: 0, high: 0 })).toBe("scan failed");
    expect(hostSecurityBadgeText(false, { critical: 2, high: 3 })).toBe("5");
    expect(hostSecurityBadgeText(false, { critical: 0, high: 0 })).toBeNull();
  });
});
