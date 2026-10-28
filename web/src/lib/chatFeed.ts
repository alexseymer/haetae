import type { ChatMessage } from "../api/m6Types";

/** Human label for the small badge on each message. */
export function kindLabel(m: Pick<ChatMessage, "kind" | "direction">): string {
  switch (m.kind) {
    case "incident":
      return "Incident";
    case "briefing":
      return "Briefing";
    case "action":
      return "Action";
    case "answer":
      return "Haetae";
    case "text":
      return m.direction === "in" ? "You" : "Haetae";
    default:
      return m.kind || "Message";
  }
}

const OLD_ACTION_BODIES: Record<string, string> = {
  ack: "Acknowledged",
  later: "Postponed",
  mute30d: "Muted for 30 days",
};

/** Text of an action echo row; rows stored before the server wrote sentences hold a bare id. */
export function actionNoteText(body: string): string {
  return OLD_ACTION_BODIES[body.trim()] ?? body;
}

export function isActionNote(m: Pick<ChatMessage, "kind" | "direction">): boolean {
  return m.kind === "action" && m.direction === "in";
}

function dayKey(d: Date): string {
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/** "Today", "Yesterday" or a full date, in the browser's time zone. */
export function dayLabel(iso: string, now: Date = new Date()): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "Unknown date";
  if (dayKey(d) === dayKey(now)) return "Today";
  const y = new Date(now);
  y.setDate(y.getDate() - 1);
  if (dayKey(d) === dayKey(y)) return "Yesterday";
  return d.toLocaleDateString(undefined, {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: d.getFullYear() === now.getFullYear() ? undefined : "numeric",
  });
}

export interface DayGroup {
  key: string;
  label: string;
  messages: ChatMessage[];
}

export function groupByDay(messages: ChatMessage[], now: Date = new Date()): DayGroup[] {
  const groups: DayGroup[] = [];
  for (const m of messages) {
    const d = new Date(m.createdAt);
    const key = Number.isNaN(d.getTime()) ? "unknown" : dayKey(d);
    const last = groups[groups.length - 1];
    if (last && last.key === key) {
      last.messages.push(m);
    } else {
      groups.push({ key, label: dayLabel(m.createdAt, now), messages: [m] });
    }
  }
  return groups;
}
