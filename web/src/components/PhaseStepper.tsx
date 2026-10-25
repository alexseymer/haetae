import type { DeployStatus } from "../api/types";
import { PHASE_STEPS, deployStatusLabel, phaseIndex } from "../lib/deploy";

interface PhaseStepperProps {
  status: DeployStatus | string;
}

export function PhaseStepper({ status }: PhaseStepperProps) {
  const idx = phaseIndex(status);
  const failed = status === "failed";
  const rollback =
    status === "rolling_back" ||
    status === "rolled_back" ||
    status === "rollback_failed";
  const healthy = status === "healthy";

  return (
    <div className="phase-stepper">
      <ol className="phase-list" aria-label="Deploy phases">
        {PHASE_STEPS.map((step, i) => {
          let state: "done" | "current" | "todo" = "todo";
          if (healthy) {
            state = i < PHASE_STEPS.length - 1 ? "done" : "current";
          } else if (!failed && !rollback && idx >= 0) {
            if (i < idx) state = "done";
            else if (i === idx) state = "current";
          }
          return (
            <li
              key={step}
              className={`phase-step phase-${state}`}
              aria-current={state === "current" ? "step" : undefined}
            >
              {deployStatusLabel(step)}
            </li>
          );
        })}
      </ol>
      {status === "queued" ? (
        <p className="phase-outcome" role="status">
          Queued
        </p>
      ) : null}
      {failed ? (
        <p className="phase-outcome phase-outcome-failed" role="status">
          Failed
        </p>
      ) : null}
      {rollback ? (
        <p className="phase-outcome" role="status">
          {deployStatusLabel(status)}
        </p>
      ) : null}
    </div>
  );
}
