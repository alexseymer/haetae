import { describe, expect, it } from "vitest";
import { stripAnsi } from "./ansi";

describe("stripAnsi", () => {
  it("removes colour and style sequences without interpreting them", () => {
    expect(stripAnsi("\u001b[31mred\u001b[0m plain")).toBe("red plain");
    expect(stripAnsi("\u001b[1;32mbold\u001b[m")).toBe("bold");
  });

  it("keeps tab and newline", () => {
    expect(stripAnsi("a\tb\nc")).toBe("a\tb\nc");
  });

  it("replaces other control characters", () => {
    expect(stripAnsi("a\u0000b\u0007c\u001fd")).toBe("abcd");
  });

  it("strips OSC sequences", () => {
    expect(stripAnsi("\u001b]0;title\u0007ok")).toBe("ok");
  });

  it("leaves plain text unchanged", () => {
    expect(stripAnsi("hello world")).toBe("hello world");
  });
});
