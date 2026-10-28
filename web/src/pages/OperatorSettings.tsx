import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { ApiError, api } from "../api/client";
import { EmptyState } from "../components/EmptyState";
import { InfoTip } from "../components/InfoTip";
import type { OperatorAutonomy, OperatorSettings, OperatorTestResult } from "../api/operatorTypes";
import {
  AUTONOMY_OPTIONS,
  describeTest,
  providerStatus,
  validateTokenCap,
} from "../lib/operatorSettings";
import { docUrl } from "../lib/help";

/** Settings section "AI operator": enable, autonomy, daily cap, provider, usage. */
export function OperatorSettingsSection({ isAdmin }: { isAdmin: boolean }) {
  const [data, setData] = useState<OperatorSettings | null>(null);
  const [aiEnabled, setAiEnabled] = useState(false);
  const [autonomy, setAutonomy] = useState<OperatorAutonomy>("report");
  const [cap, setCap] = useState("200000");
  const [capError, setCapError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{ tone: "ok" | "warn" | "fail"; text: string } | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const apply = useCallback((s: OperatorSettings) => {
    setData(s);
    setAiEnabled(s.aiEnabled);
    setAutonomy(s.autonomy);
    setCap(String(s.dailyTokenCap));
  }, []);

  const load = useCallback(async () => {
    try {
      const s = await api.operatorSettings();
      if (!mountedRef.current) return;
      apply(s);
      setError(null);
    } catch (err) {
      if (!mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to load operator settings");
    }
  }, [apply]);

  useEffect(() => {
    void load();
  }, [load]);

  const onSave = async (e: FormEvent) => {
    e.preventDefault();
    setMsg(null);
    const problem = validateTokenCap(cap);
    setCapError(problem);
    if (problem) return;
    setBusy(true);
    try {
      const s = await api.putOperatorSettings({
        aiEnabled,
        autonomy,
        dailyTokenCap: Number(cap),
      });
      if (!mountedRef.current) return;
      apply(s);
      setError(null);
      setMsg("Saved.");
    } catch (err) {
      if (!mountedRef.current) return;
      setError(err instanceof ApiError ? err.message : "Failed to save");
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  };

  const onTest = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const r: OperatorTestResult = await api.testOperatorProvider();
      if (mountedRef.current) setTestResult(describeTest(r));
    } catch (err) {
      if (mountedRef.current) {
        setTestResult({
          tone: "fail",
          text: `Connection test failed: ${err instanceof ApiError ? err.message : "request failed"}.`,
        });
      }
    } finally {
      if (mountedRef.current) setTesting(false);
    }
  };

  const help = AUTONOMY_OPTIONS.find((o) => o.id === autonomy)?.help ?? "";

  return (
    <section className="card">
      <h2>AI operator</h2>
      {error ? (
        <div className="app-error" role="alert" aria-live="assertive">
          {error}
        </div>
      ) : null}
      {!data && !error ? <p className="muted">Loading…</p> : null}
      {data ? (
        <>
          {!data.provider.configured ? (
            <EmptyState title="The AI operator is not set up on this server yet">
              Two switches turn it on. First, an administrator of the server sets
              HAETAE_AI_PROVIDER, HAETAE_AI_MODEL and, for hosted providers,
              HAETAE_AI_API_KEY in the server settings and restarts it. Then enable it below.
              Until then no AI connection is made, and rule-based alerts and the daily briefing
              keep working.{" "}
              <a href={docUrl("ai-operator")} target="_blank" rel="noopener noreferrer">
                Read the setup steps
              </a>
              .
            </EmptyState>
          ) : null}
          <p className="muted" data-testid="operator-provider">
            {providerStatus(data.provider)}
          </p>
          {isAdmin && data.provider.configured ? (
            <div className="field">
              <div className="row label-row">
                <button type="button" className="btn" disabled={testing} onClick={() => void onTest()}>
                  {testing ? "Testing…" : "Test connection"}
                </button>
                <InfoTip term="toolCalling" />
              </div>
              <p
                className={testResult?.tone === "fail" ? "field-error" : "muted"}
                aria-live="polite"
                data-testid="operator-test-result"
              >
                {testResult?.text ?? ""}
              </p>
            </div>
          ) : null}
          <p className="muted" data-testid="operator-status">
            {data.available
              ? "The operator is available."
              : `The operator is unavailable${data.reason ? ` (${data.reason})` : ""}; rule alerts are active.`}
          </p>
          <p className="muted" data-testid="operator-usage">
            Usage today: {data.usageToday.inputTokens.toLocaleString("en-US")} input and{" "}
            {data.usageToday.outputTokens.toLocaleString("en-US")} output tokens.
          </p>
          <form noValidate onSubmit={(e) => void onSave(e)}>
            <fieldset disabled={!isAdmin || busy} className="settings-fieldset">
              <div className="field">
                <label className="checkbox" htmlFor="operator-enabled">
                  <input
                    id="operator-enabled"
                    name="aiEnabled"
                    type="checkbox"
                    checked={aiEnabled}
                    onChange={(e) => setAiEnabled(e.target.checked)}
                  />
                  <span>Enable the AI operator</span>
                </label>
                <p className="field-hint">
                  Rule-based alerts and the daily briefing work with or without it.
                </p>
              </div>
              <div className="field">
                <div className="row label-row">
                  <label htmlFor="operator-autonomy">Autonomy</label>
                  <InfoTip term="autonomy" />
                </div>
                <select
                  id="operator-autonomy"
                  name="autonomy"
                  aria-describedby="operator-autonomy-help"
                  value={autonomy}
                  onChange={(e) => setAutonomy(e.target.value as OperatorAutonomy)}
                >
                  {AUTONOMY_OPTIONS.map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.label}
                    </option>
                  ))}
                </select>
                <p className="field-hint" id="operator-autonomy-help">
                  {help}
                </p>
              </div>
              <div className="field">
                <div className="row label-row">
                  <label htmlFor="operator-cap">Daily token cap</label>
                  <InfoTip term="tokenCap" />
                </div>
                <input
                  id="operator-cap"
                  name="dailyTokenCap"
                  type="number"
                  inputMode="numeric"
                  min={0}
                  max={50000000}
                  step={1}
                  aria-describedby="operator-cap-help"
                  value={cap}
                  onChange={(e) => {
                    setCap(e.target.value);
                    setCapError(null);
                  }}
                  required
                />
                {capError ? (
                  <p className="field-error" role="alert">
                    {capError}
                  </p>
                ) : (
                  <p className="field-hint" id="operator-cap-help">
                    Input plus output tokens per UTC day, 0 to 50,000,000. At the cap the
                    operator stops until tomorrow. 0 means no AI use.
                  </p>
                )}
              </div>
            </fieldset>
            {!isAdmin ? <p className="muted">Only administrators can change these settings.</p> : null}
            {isAdmin ? (
              <button type="submit" className="btn" disabled={busy}>
                {busy ? "Saving…" : "Save"}
              </button>
            ) : null}
            {msg ? (
              <p className="muted" role="status">
                {msg}
              </p>
            ) : null}
          </form>
        </>
      ) : null}
    </section>
  );
}
