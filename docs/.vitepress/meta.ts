// Clean titles and one-line descriptions for every page.
// Kept here (not in front matter) so the Markdown stays unchanged on GitHub,
// where YAML front matter would render as a table at the top of each file.
export const pageMeta: Record<string, { title: string; description: string }> = {
  'README.md': {
    title: 'Documentation index',
    description: 'Where to start with Haetae: reading order for users and contributors, and a map of every document.'
  },
  'install.md': {
    title: 'Install the server',
    description: 'Install the Haetae control plane with Docker Compose: requirements, installer options, first login, TLS, backups, upgrade and uninstall.'
  },
  'enroll.md': {
    title: 'Enroll a host',
    description: 'Connect a Linux server to Haetae with one command and a single-use token; what the agent installs, its flags and how to remove a host.'
  },
  'deploy.md': {
    title: 'Deploy an app',
    description: 'Deploy from a Git repository or a pasted compose.yaml with HTTPS, push to deploy and one-click rollback.'
  },
  'chat.md': {
    title: 'Chat, alerts and briefing',
    description: 'Connect web, Telegram, Slack, email or Signal; how incident alerts, the daily briefing and mutes work.'
  },
  'ai-operator.md': {
    title: 'The AI operator',
    description: 'How the optional AI operator answers from your own data, cites evidence, redacts before sending and acts only at the autonomy level you allow.'
  },
  'security.md': {
    title: 'Security scanning',
    description: 'Per-container CVE scanning, misconfigurations, outbound connection watch and image update policy, with the known limits.'
  },
  'troubleshooting.md': {
    title: 'Troubleshooting',
    description: 'Symptom to check lists for install, enrollment, deploys, chat, the AI operator and scanning.'
  },
  'architecture.md': {
    title: 'Architecture',
    description: 'Components, packages, data flows and the trust model of Haetae: server, agent, inventory helper, PostgreSQL and Caddy.'
  },
  'spec.md': {
    title: 'Specification',
    description: 'The one-page Haetae spec: who it is for, exact input and output, the Done correctly criteria S1 to S11 and what is out of scope.'
  },
  'roadmap.md': {
    title: 'Roadmap',
    description: 'The build plan from M0 to M9 with the acceptance criteria of each step.'
  },
  'acceptance.md': {
    title: 'Acceptance report',
    description: 'Every spec criterion with the test that checks it, the measured value, the result and what the measurements do not show.'
  },
  'benchmarks-m7.md': {
    title: 'Benchmarks',
    description: 'Scanner memory and a Grype versus Trivy comparison, measured to check the ADR risks and the agent memory budget.'
  },
  'release.md': {
    title: 'Release builds',
    description: 'How release binaries and the server image are built and verified.'
  },
  'security-ui.md': {
    title: 'Security UI notes',
    description: 'The host Security tab and its API: what the interface must always tell the truth about.'
  },
  'self-hosting.md': {
    title: 'Self-hosting',
    description: 'The self-hosting guide moved to the install page; this page keeps old links working.'
  },
  'adr/0001-stack.md': {
    title: 'ADR 0001: Technology stack',
    description: 'The accepted decision on the technology stack: Go agent and control plane, React web UI, PostgreSQL only.'
  },
  'design/m5-deploys.md': {
    title: 'Design: deploys',
    description: 'Design notes for M5: how deploys, health checks, rollback and push to deploy work.'
  },
  'design/m6-alerts-chat.md': {
    title: 'Design: alerts and chat',
    description: 'Design notes for M6: rule alerts, incidents, chat bridges and the daily briefing.'
  },
  'design/m7-security.md': {
    title: 'Design: security scanning',
    description: 'Design notes for M7: inventory, vulnerability matching, misconfigurations and the outbound watch.'
  },
  'design/m8-operator.md': {
    title: 'Design: AI operator',
    description: 'Design notes for M8: operator tools, evidence, answer validation, action gating and the usage cap.'
  }
}

export const site = {
  url: 'https://alexseymer.github.io/haetae/',
  repo: 'https://github.com/alexseymer/haetae',
  name: 'Haetae',
  tagline: 'An AI operator for Docker Compose servers',
  description:
    'Haetae is a self-hosted AI operator for Docker Compose servers: deploy, monitor, scan and get answers in chat, with evidence for every statement.'
}
