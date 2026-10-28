import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { DOC_LINKS, GLOSSARY_ORDER, HELP, INTROS, docUrl } from "../lib/help";

const OVERVIEW: Array<{ title: string; text: string; link?: ReactNode }> = [
  {
    title: "Hosts and the agent",
    text: "A host is a Linux server with Docker. You enroll it with one command, which installs a small agent. The agent connects out to Haetae, so the server needs no inbound port, and it reconnects by itself.",
    link: <Link to="/">Go to Hosts</Link>,
  },
  {
    title: "Containers and metrics",
    text: "Haetae lists the containers on each host with their state, restarts and ports, and charts CPU, memory, disk and network. Warnings and errors from the logs are collected too, with secrets removed on the host first.",
  },
  {
    title: "Deploys",
    text: "Deploy a Docker Compose app to a host from a Git repository or a pasted compose file, optionally on your own domain with HTTPS. A failed new version leaves the previous one running, and you can roll back later.",
  },
  {
    title: "Incidents and alerts",
    text: "Rules watch for crashes, high CPU, memory or disk, offline hosts, failed deploys and new vulnerabilities. Related alerts are grouped into one incident per host. They work even when the AI is off.",
    link: <Link to="/incidents">Go to Incidents</Link>,
  },
  {
    title: "Chat",
    text: "Haetae reports in chat: incident messages, a short daily briefing, and answers to your questions. Web chat is always on; Telegram, Slack, email and Signal can be linked in Settings.",
    link: <Link to="/chat">Go to Chat</Link>,
  },
  {
    title: "AI operator (optional)",
    text: "The operator answers questions in plain language and explains incidents, always pointing to evidence. It acts only at the autonomy level you set, never deletes anything, and treats log text as untrusted.",
    link: <Link to="/settings?tab=operator">AI operator settings</Link>,
  },
  {
    title: "Security scanning",
    text: "Haetae lists the software inside each running container, checks it against known vulnerabilities, flags risky settings such as a mounted Docker socket, and watches outbound connections. A scan that fails says so; it is never reported as clean.",
  },
];

export function HelpPage() {
  return (
    <div className="page">
      <PageHeader title="Help" intro={INTROS.help} />

      <section className="stack help-section" aria-labelledby="help-how">
        <h2 id="help-how">How Haetae works</h2>
        <div className="help-grid">
          {OVERVIEW.map((o) => (
            <article key={o.title} className="card help-card">
              <h3>{o.title}</h3>
              <p>{o.text}</p>
              {o.link ?? null}
            </article>
          ))}
        </div>
      </section>

      <section className="stack help-section" aria-labelledby="help-glossary">
        <h2 id="help-glossary">Glossary</h2>
        <dl className="glossary card">
          {GLOSSARY_ORDER.map((term) => (
            <div key={term} className="glossary-item">
              <dt>{HELP[term].title}</dt>
              <dd>{HELP[term].body}</dd>
            </div>
          ))}
        </dl>
      </section>

      <section className="stack help-section" aria-labelledby="help-docs">
        <h2 id="help-docs">Documentation</h2>
        <p className="muted">The full guides are on GitHub and open in a new tab.</p>
        <ul className="doc-list card">
          {DOC_LINKS.map((d) => (
            <li key={d.doc}>
              <a href={docUrl(d.doc)} target="_blank" rel="noopener noreferrer">
                {d.label}
              </a>
              <span className="muted"> — {d.blurb}</span>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}
