import { useState } from "react";
import { api } from "../api/client";
import type { DigestWindow } from "../api/digestTypes";
import { DigestPanel } from "./DigestPanel";

const WINDOWS: { id: DigestWindow; label: string }[] = [
  { id: "1h", label: "1 h" },
  { id: "6h", label: "6 h" },
  { id: "24h", label: "24 h" },
];

export function LogSummary({ hostId }: { hostId: string }) {
  const [win, setWin] = useState<DigestWindow>("24h");
  return (
    <DigestPanel
      heading="Log summary"
      buttonLabel="Summarize logs"
      run={() => api.summarizeLogs(hostId, win)}
      controls={
        <div className="range-select" role="group" aria-label="Time window">
          {WINDOWS.map((w) => (
            <button
              key={w.id}
              type="button"
              className="btn btn-secondary"
              aria-pressed={win === w.id}
              onClick={() => setWin(w.id)}
            >
              {w.label}
            </button>
          ))}
        </div>
      }
    />
  );
}
