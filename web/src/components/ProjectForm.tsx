import { useState, type FormEvent } from "react";
import type { CreateProjectRequest, PatchProjectRequest, Project } from "../api/types";
import { projectInlineCompose } from "../lib/deploy";
import {
  MAX_INLINE_COMPOSE,
  validateGitRef,
  validateGitUrl,
  validateIngress,
  validateInlineCompose,
  validateProjectName,
} from "../lib/projectValidation";
import { InfoTip } from "./InfoTip";

export interface ProjectFormValues {
  name: string;
  kind: "git" | "inline";
  gitUrl: string;
  gitRef: string;
  composePath: string;
  gitToken: string;
  removeToken: boolean;
  inlineCompose: string;
  domain: string;
  service: string;
  port: string;
  autoDeploy: boolean;
}

function emptyValues(): ProjectFormValues {
  return {
    name: "",
    kind: "git",
    gitUrl: "",
    gitRef: "main",
    composePath: "compose.yaml",
    gitToken: "",
    removeToken: false,
    inlineCompose: "",
    domain: "",
    service: "",
    port: "",
    autoDeploy: false,
  };
}

function fromProject(project: Project, inlineCompose?: string): ProjectFormValues {
  return {
    name: project.name,
    kind: project.source.kind,
    gitUrl: project.source.gitUrl ?? "",
    gitRef: project.source.gitRef ?? "main",
    composePath: project.source.composePath ?? "compose.yaml",
    gitToken: "",
    removeToken: false,
    inlineCompose: projectInlineCompose(project, inlineCompose),
    domain: project.domain ?? "",
    service: project.service ?? "",
    port: project.port ? String(project.port) : "",
    autoDeploy: project.autoDeploy,
  };
}

interface ProjectFormProps {
  mode: "create" | "edit";
  project?: Project | null;
  inlineCompose?: string;
  serverError?: string | null;
  submitting?: boolean;
  onClose: () => void;
  onSubmit: (payload: {
    create?: CreateProjectRequest;
    patch?: PatchProjectRequest;
  }) => void | Promise<void>;
}

/**
 * Create or edit a project. This is an inline panel on a page, not a dialog:
 * it is too long to fit a dialog without scrolling.
 */
export function ProjectForm({
  mode,
  project = null,
  inlineCompose,
  serverError = null,
  submitting = false,
  onClose,
  onSubmit,
}: ProjectFormProps) {
  const [values, setValues] = useState<ProjectFormValues>(() =>
    mode === "edit" && project ? fromProject(project, inlineCompose) : emptyValues(),
  );
  const [touched, setTouched] = useState(false);

  const set = <K extends keyof ProjectFormValues>(key: K, value: ProjectFormValues[K]) => {
    setValues((v) => ({ ...v, [key]: value }));
  };

  const nameErr = touched ? validateProjectName(values.name) : null;
  const urlErr =
    touched && values.kind === "git" ? validateGitUrl(values.gitUrl) : null;
  const refErr =
    touched && values.kind === "git" ? validateGitRef(values.gitRef) : null;
  const ingressErr = touched
    ? validateIngress(values.domain, values.service, values.port)
    : null;
  const composeErr =
    touched && values.kind === "inline"
      ? validateInlineCompose(values.inlineCompose)
      : null;

  const inlineBytes = new TextEncoder().encode(values.inlineCompose).length;
  const hasStoredToken = mode === "edit" && !!project?.hasToken;

  const onFormSubmit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    const n = validateProjectName(mode === "create" ? values.name : project?.name ?? "");
    const ingress = validateIngress(values.domain, values.service, values.port);
    if (mode === "create" && n) return;
    if (ingress) return;
    if (values.kind === "git") {
      if (validateGitUrl(values.gitUrl) || validateGitRef(values.gitRef)) return;
    } else if (validateInlineCompose(values.inlineCompose)) {
      return;
    }

    const portNum = values.port ? Number(values.port) : 0;
    if (mode === "create") {
      const body: CreateProjectRequest = {
        name: values.name,
        source:
          values.kind === "git"
            ? {
                kind: "git",
                gitUrl: values.gitUrl,
                gitRef: values.gitRef || "main",
                composePath: values.composePath || "compose.yaml",
                gitToken: values.gitToken || undefined,
              }
            : { kind: "inline", inlineCompose: values.inlineCompose },
        domain: values.domain,
        service: values.service,
        port: portNum,
        autoDeploy: values.kind === "git" ? values.autoDeploy : false,
      };
      void onSubmit({ create: body });
      return;
    }

    const patch: PatchProjectRequest = {
      domain: values.domain,
      service: values.service,
      port: portNum,
      autoDeploy: values.kind === "git" ? values.autoDeploy : false,
      source:
        values.kind === "git"
          ? {
              kind: "git",
              gitUrl: values.gitUrl,
              gitRef: values.gitRef || "main",
              composePath: values.composePath || "compose.yaml",
              ...(values.removeToken
                ? { gitToken: "" }
                : values.gitToken
                  ? { gitToken: values.gitToken }
                  : {}),
            }
          : { kind: "inline", inlineCompose: values.inlineCompose },
    };
    void onSubmit({ patch });
  };

  return (
    <section
      className="card project-form-card"
      aria-labelledby="project-form-title"
    >
      <h2 id="project-form-title">{mode === "create" ? "New project" : "Edit project"}</h2>
      <form className="stack project-form" onSubmit={onFormSubmit} noValidate>
        <div className="warning-banner" role="note">
          Deploying runs containers on the host with whatever privileges the compose
          file asks for. This is the same as root access to that server. Only
          administrators can deploy.
        </div>

        {mode === "create" ? (
          <div className="field">
            <label htmlFor="proj-name">Name</label>
            <input
              id="proj-name"
              value={values.name}
              onChange={(e) => set("name", e.target.value)}
              autoComplete="off"
              spellCheck={false}
              required
              pattern="[a-z0-9][a-z0-9-]{0,39}"
            />
            {nameErr ? (
              <p className="field-error" role="alert">
                {nameErr}
              </p>
            ) : (
              <p className="field-hint">Lowercase letters, digits and dashes; max 40.</p>
            )}
          </div>
        ) : (
          <p>
            <strong>{project?.name}</strong>
          </p>
        )}

        <fieldset className="field">
          <legend>Source</legend>
          <div className="row">
            <label className="radio">
              <input
                type="radio"
                name="source-kind"
                checked={values.kind === "git"}
                onChange={() => set("kind", "git")}
              />{" "}
              Git repository
            </label>
            <label className="radio">
              <input
                type="radio"
                name="source-kind"
                checked={values.kind === "inline"}
                onChange={() => set("kind", "inline")}
              />{" "}
              Pasted compose file
            </label>
          </div>
        </fieldset>

        {values.kind === "git" ? (
          <>
            <div className="field">
              <label htmlFor="proj-url">Repository URL</label>
              <input
                id="proj-url"
                value={values.gitUrl}
                onChange={(e) => set("gitUrl", e.target.value)}
                placeholder="https://github.com/acme/shop"
                autoComplete="off"
                spellCheck={false}
              />
              {urlErr ? (
                <p className="field-error" role="alert">
                  {urlErr}
                </p>
              ) : (
                <p className="field-hint">
                  A plain https address with no password in it. For a private repository, use
                  the access token below.
                </p>
              )}
            </div>
            <div className="field">
              <label htmlFor="proj-ref">Ref</label>
              <input
                id="proj-ref"
                value={values.gitRef}
                onChange={(e) => set("gitRef", e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
              {refErr ? (
                <p className="field-error" role="alert">
                  {refErr}
                </p>
              ) : (
                <p className="field-hint">Branch name or 40-character commit id.</p>
              )}
            </div>
            <div className="field">
              <label htmlFor="proj-compose-path">Compose path</label>
              <input
                id="proj-compose-path"
                value={values.composePath}
                onChange={(e) => set("composePath", e.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
              <p className="field-hint">
                Where the compose file is inside the repository, ending in .yml or .yaml.
                Keep compose.yaml if it sits at the top.
              </p>
            </div>
            <div className="field">
              <label htmlFor="proj-token">Access token</label>
              <input
                id="proj-token"
                type="password"
                autoComplete="off"
                value={values.gitToken}
                onChange={(e) => set("gitToken", e.target.value)}
                placeholder={
                  hasStoredToken ? undefined : "stored encrypted, never shown again"
                }
                disabled={values.removeToken}
              />
              {!hasStoredToken ? (
                <p className="field-hint">
                  Only for private repositories. It is stored encrypted on the server and never
                  shown again.
                </p>
              ) : null}
              {hasStoredToken ? (
                <>
                  <p className="field-hint">
                    A token is stored. Leave empty to keep it.
                  </p>
                  <label className="checkbox">
                    <input
                      type="checkbox"
                      checked={values.removeToken}
                      onChange={(e) => {
                        set("removeToken", e.target.checked);
                        if (e.target.checked) set("gitToken", "");
                      }}
                    />{" "}
                    Remove token
                  </label>
                </>
              ) : null}
            </div>
            <div className="field">
              <div className="row checkbox-row">
                <label className="checkbox">
                  <input
                    type="checkbox"
                    checked={values.autoDeploy}
                    onChange={(e) => set("autoDeploy", e.target.checked)}
                  />{" "}
                  Auto-deploy on push
                </label>
                <InfoTip term="autoDeploy" />
              </div>
              <p className="field-hint">
                Deploys again when the branch changes. Use a branch name, not a commit id.
              </p>
            </div>
          </>
        ) : (
          <div className="field">
            <label htmlFor="proj-inline">Compose file</label>
            <textarea
              id="proj-inline"
              className="mono"
              rows={12}
              value={values.inlineCompose}
              onChange={(e) => set("inlineCompose", e.target.value)}
              spellCheck={false}
            />
            <p className="field-hint">
              {(inlineBytes / 1024).toFixed(1)} KiB / {MAX_INLINE_COMPOSE / 1024} KiB
            </p>
            {composeErr ? (
              <p className="field-error" role="alert">
                {composeErr}
              </p>
            ) : null}
          </div>
        )}

        <fieldset className="field">
          <legend>Domain and HTTPS (optional)</legend>
          <div className="field">
            <label htmlFor="proj-domain">Domain</label>
            <input
              id="proj-domain"
              value={values.domain}
              onChange={(e) => set("domain", e.target.value)}
              placeholder="app.example.com"
              autoComplete="off"
              spellCheck={false}
            />
            <p className="field-hint">The address people will open, in lower case.</p>
          </div>
          <div className="field">
            <label htmlFor="proj-service">Service</label>
            <input
              id="proj-service"
              value={values.service}
              onChange={(e) => set("service", e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
            <p className="field-hint">The service name in your compose file that receives the traffic.</p>
          </div>
          <div className="field">
            <label htmlFor="proj-port">Port</label>
            <input
              id="proj-port"
              inputMode="numeric"
              value={values.port}
              onChange={(e) => set("port", e.target.value)}
              autoComplete="off"
            />
            <p className="field-hint">The port that service listens on inside its container.</p>
          </div>
          {ingressErr ? (
            <p className="field-error" role="alert">
              {ingressErr}
            </p>
          ) : (
            <p className="field-hint">
              Fill in all three or none. DNS must point to the host, and ports 80 and 443 must
              be reachable for automatic HTTPS. <InfoTip term="ingress" />
            </p>
          )}
        </fieldset>

        {serverError ? (
          <p className="field-error" role="alert">
            {serverError}
          </p>
        ) : null}

        <div className="row form-actions">
          <button type="submit" className="btn" disabled={submitting}>
            {submitting ? "Saving…" : mode === "create" ? "Create project" : "Save changes"}
          </button>
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}
