import { describe, expect, it } from "vitest";
import { severityLabel, severityVariant } from "./severity";

describe("severityLabel", () => {
  it("returns text labels for known severities", () => {
    expect(severityLabel("critical")).toBe("critical");
    expect(severityLabel("high")).toBe("high");
    expect(severityLabel("medium")).toBe("medium");
    expect(severityLabel("low")).toBe("low");
  });
});

describe("severityVariant", () => {
  it("maps severity to colour variants without dropping the text label", () => {
    expect(severityVariant("critical")).toBe("danger");
    expect(severityVariant("high")).toBe("danger");
    expect(severityVariant("medium")).toBe("warn");
    expect(severityVariant("low")).toBe("info");
    // Label is always present separately from colour.
    expect(severityLabel("critical")).not.toBe("");
    expect(severityLabel("high")).not.toBe("");
  });
});
