interface StepperProps<T extends string> {
  steps: ReadonlyArray<{ id: T; label: string }>;
  current: T;
  label: string;
}

/** Step indicator for multi-step dialogs. Text and a number, never colour alone. */
export function Stepper<T extends string>({ steps, current, label }: StepperProps<T>) {
  const idx = steps.findIndex((s) => s.id === current);
  return (
    <ol className="stepper" aria-label={label}>
      {steps.map((s, i) => {
        const state = i < idx ? "done" : i === idx ? "current" : "todo";
        return (
          <li
            key={s.id}
            className={`stepper-item stepper-${state}`}
            aria-current={state === "current" ? "step" : undefined}
          >
            <span className="stepper-num" aria-hidden="true">
              {state === "done" ? "✓" : i + 1}
            </span>
            <span className="stepper-label">
              {s.label}
              <span className="sr-only">
                {` (step ${i + 1} of ${steps.length}${state === "done" ? ", done" : ""})`}
              </span>
            </span>
          </li>
        );
      })}
    </ol>
  );
}
