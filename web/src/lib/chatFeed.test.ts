import { describe, expect, it } from "vitest";
import { actionNoteText, dayLabel, groupByDay, kindLabel } from "./chatFeed";
import type { ChatMessage } from "../api/m6Types";

const now = new Date(2026, 9, 27, 12, 0, 0);

function at(d: Date): ChatMessage {
  return {
    id: d.toISOString(),
    kind: "text",
    direction: "out",
    body: "x",
    actions: [],
    evidence: [],
    incidentId: null,
    createdAt: d.toISOString(),
  };
}

describe("chatFeed", () => {
  it("labels days", () => {
    expect(dayLabel(new Date(2026, 9, 27, 1).toISOString(), now)).toBe("Today");
    expect(dayLabel(new Date(2026, 9, 26, 23).toISOString(), now)).toBe("Yesterday");
    expect(dayLabel(new Date(2026, 9, 20, 9).toISOString(), now)).not.toMatch(/Today|Yesterday/);
  });

  it("groups by day in order", () => {
    const g = groupByDay(
      [at(new Date(2026, 9, 26, 8)), at(new Date(2026, 9, 26, 9)), at(new Date(2026, 9, 27, 8))],
      now,
    );
    expect(g.map((x) => [x.label, x.messages.length])).toEqual([
      ["Yesterday", 2],
      ["Today", 1],
    ]);
  });

  it("maps old action bodies and keeps new sentences", () => {
    expect(actionNoteText("ack")).toBe("Acknowledged");
    expect(actionNoteText("later")).toBe("Postponed");
    expect(actionNoteText("mute30d")).toBe("Muted for 30 days");
    expect(actionNoteText("You acknowledged Strato offline")).toBe("You acknowledged Strato offline");
  });

  it("labels kinds", () => {
    expect(kindLabel({ kind: "text", direction: "in" })).toBe("You");
    expect(kindLabel({ kind: "answer", direction: "out" })).toBe("Haetae");
    expect(kindLabel({ kind: "briefing", direction: "out" })).toBe("Briefing");
    expect(kindLabel({ kind: "incident", direction: "out" })).toBe("Incident");
    expect(kindLabel({ kind: "action", direction: "out" })).toBe("Action");
  });
});
