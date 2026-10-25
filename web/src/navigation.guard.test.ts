import { describe, expect, it } from "vitest";

// react-router 6.x has an open-redirect advisory for Link/navigate with
// attacker-controlled targets. This app must never build a navigation target
// from data other than constants and server-generated host ids.
const modules = import.meta.glob(["./**/*.tsx", "!./**/*.test.tsx"], {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

describe("navigation targets", () => {
  it("are constants or host ids only", () => {
    const allowed = [
      /^"\/[a-z-]*"$/, // "/login", "/", "/setup", "/settings", "/incidents", "/chat"
      /^`\/hosts\/\$\{[a-z]+\.id\}`$/, // `/hosts/${h.id}`
      /^`\/hosts\/\$\{[a-z]+\.hostId\}`$/, // `/hosts/${project.hostId}` / incident.hostId
      /^`\/incidents\/\$\{[a-z]+\.id\}`$/, // `/incidents/${inc.id}`
      /^`\/projects\/\$\{[a-z]+\.id\}`$/, // `/projects/${p.id}`
      /^`\/projects\/\$\{[a-z]+\.projectId\}`$/, // `/projects/${deploy.projectId}`
      /^`\/deploys\/\$\{[a-z]+\.id\}`$/, // `/deploys/${d.id}`
      /^setupRequired \? "\/setup" : user \? "\/" : "\/login"$/,
    ];
    const found: string[] = [];
    for (const text of Object.values(modules)) {
      for (const m of text.matchAll(/\bto=(\{(?:[^{}]|\$\{[^}]*\})*\}|"[^"]*")|navigate\(([^,)]+)/g)) {
        found.push((m[1] ?? m[2] ?? "").replace(/^\{|\}$/g, "").trim());
      }
    }
    expect(found.length).toBeGreaterThan(5); // the scan really finds the calls
    expect(found.filter((t) => !allowed.some((re) => re.test(t)))).toEqual([]);
  });
});
