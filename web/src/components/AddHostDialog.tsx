import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, api } from "../api/client";
import type { EnrollmentResponse, HostSummary } from "../api/types";
import { usePolling } from "../hooks/usePolling";
import { formatDateTime } from "../lib/format";
import { CopyBlock } from "./CopyBlock";
import { InfoTip } from "./InfoTip";
import { Modal } from "./Modal";
import { Stepper } from "./Stepper";

type Step = "name" | "install" | "connected";

const STEPS = [
  { id: "name", label: "Name" },
  { id: "install", label: "Install" },
  { id: "connected", label: "Connected" },
] as const;

interface AddHostDialogProps {
  open: boolean;
  onClose: () => void;
}

/**
 * Three short steps instead of one long dialog: name the host, run the
 * command (with a live waiting state), done.
 *
 * The flow is a child of the Modal, which renders nothing while closed, so
 * every opening starts fresh and a token is never shown again after closing.
 */
export function AddHostDialog({ open, onClose }: AddHostDialogProps) {
  return (
    <Modal title="Add host" open={open} onClose={onClose}>
      <AddHostFlow onClose={onClose} />
    </Modal>
  );
}

function AddHostFlow({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const [step, setStep] = useState<Step>("name");
  const [name, setName] = useState("");
  const [ttlHours, setTtlHours] = useState(24);
  const [created, setCreated] = useState<EnrollmentResponse | null>(null);
  const [enrolledName, setEnrolledName] = useState("");
  const [formError, setFormError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [seen, setSeen] = useState<HostSummary | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const checkConnected = useCallback(async () => {
    try {
      const res = await api.hosts();
      if (!mountedRef.current) return;
      const match = res.hosts.find((h) => h.name === enrolledName) ?? null;
      setSeen(match);
      if (match?.connected) setStep("connected");
    } catch {
      // keep waiting; the next poll tries again
    }
  }, [enrolledName]);

  usePolling(checkConnected, 3000, step === "install" && enrolledName !== "", enrolledName);

  const onCreate = async (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);
    setSubmitting(true);
    try {
      const trimmed = name.trim();
      const res = await api.enroll({ name: trimmed, ttlHours });
      setCreated(res);
      setEnrolledName(trimmed);
      setSeen(null);
      setStep("install");
    } catch (err) {
      setFormError(
        err instanceof ApiError
          ? err.message
          : "Could not create the install command. Check your connection and try again.",
      );
    } finally {
      setSubmitting(false);
    }
  };

  const goBack = () => {
    setCreated(null);
    setEnrolledName("");
    setSeen(null);
    setFormError(null);
    setStep("name");
  };

  return (
    <>
      <div className="stack dialog-stack">
        <Stepper steps={STEPS} current={step} label="Add host progress" />

        {step === "name" ? (
          <form className="dialog-form" onSubmit={(e) => void onCreate(e)}>
            <p className="dialog-intro">
              Haetae needs a small agent on each server. Name the server, then you get one
              command to run on it.
            </p>
            <div className="field">
              <label htmlFor="host-name">Host name</label>
              <input
                id="host-name"
                name="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                required
              />
              <p className="field-hint">A label you will recognise, for example hel1-prod.</p>
            </div>
            <div className="field">
              <label htmlFor="ttl-hours">Validity (hours)</label>
              <input
                id="ttl-hours"
                name="ttlHours"
                type="number"
                min={1}
                max={168}
                value={ttlHours}
                onChange={(e) => setTtlHours(Number(e.target.value))}
                required
              />
              <p className="field-hint">
                How long the one-time command stays valid, 1 to 168 hours.
              </p>
            </div>
            {formError ? (
              <div className="app-error" role="alert" aria-live="assertive">
                {formError}
              </div>
            ) : null}
            <div className="row dialog-actions">
              <button type="submit" className="btn" disabled={submitting}>
                {submitting ? "Creating…" : "Get install command"}
              </button>
              <button type="button" className="btn btn-secondary" onClick={onClose}>
                Cancel
              </button>
            </div>
          </form>
        ) : null}

        {step === "install" && created ? (
          <div className="dialog-form">
            <p className="dialog-intro">
              Run this as root on the server (Linux with Docker). The token works once.{" "}
              <InfoTip term="enrollToken" />
            </p>
            <CopyBlock text={created.command} />
            <p className="muted dialog-meta">Expires {formatDateTime(created.expiresAt)}</p>
            <p className="connect-status" role="status" aria-live="polite">
              <span className="connect-dot" aria-hidden="true" />
              {seen
                ? `${enrolledName} has enrolled. Waiting for it to come online…`
                : "Waiting for the host to connect…"}
            </p>
            <div className="row dialog-actions">
              <button type="button" className="btn btn-secondary" onClick={goBack}>
                Back
              </button>
              <button type="button" className="btn" onClick={onClose}>
                Done
              </button>
            </div>
          </div>
        ) : null}

        {step === "connected" ? (
          <div className="dialog-form">
            <p className="connect-status connect-ok" role="status" aria-live="polite">
              Connected
            </p>
            <p className="dialog-intro">
              <strong>{enrolledName}</strong> is online. Its metrics appear within a couple of
              minutes.
            </p>
            <div className="row dialog-actions">
              {seen ? (
                <button
                  type="button"
                  className="btn btn-secondary"
                  onClick={() => navigate(`/hosts/${seen.id}`)}
                >
                  Open host
                </button>
              ) : null}
              <button type="button" className="btn" onClick={onClose}>
                Done
              </button>
            </div>
          </div>
        ) : null}
      </div>
    </>
  );
}
