// Clean titles and one-line descriptions for every page.
// Kept here (not in front matter) so the Markdown stays unchanged on GitHub,
// where YAML front matter would render as a table at the top of each file.
export const pageMeta: Record<string, { title: string; description: string }> = {
  'getting-started/index.md': {
    title: 'Getting started',
    description: 'From nothing to a working Haetae in six steps: install, sign in, enroll a host, deploy, chat and the optional AI operator.'
  },
  'getting-started/install-the-server.md': {
    title: 'Install the server',
    description: 'Install the Haetae server with Docker Compose: what you need, the installer, settings, certificates, backups, upgrades and removal.'
  },
  'getting-started/first-login.md': {
    title: 'First login',
    description: 'Sign in for the first time with the setup code, create the first administrator and add more people.'
  },
  'getting-started/enroll-a-host.md': {
    title: 'Enroll a host',
    description: 'Connect a Linux server to Haetae with one command; what the installer does and how to remove a host.'
  },
  'getting-started/deploy-an-app.md': {
    title: 'Deploy an app',
    description: 'Deploy a Docker Compose app from a Git repository or a pasted compose file, starting with a tiny example.'
  },
  'getting-started/connect-chat.md': {
    title: 'Connect a chat channel',
    description: 'Get alerts and the daily briefing in Telegram, Slack, email or Signal by linking your account.'
  },
  'getting-started/turn-on-the-operator.md': {
    title: 'Turn on the AI operator',
    description: 'Choose an AI provider, switch the operator on, pick an autonomy level and test the connection.'
  },
  'guides/index.md': {
    title: 'Guides',
    description: 'Task-by-task guides: deploys, domains, rollbacks, alerts, questions, log summaries, vulnerabilities and chat commands.'
  },
  'guides/deploy-from-git.md': {
    title: 'Deploy from Git',
    description: 'What happens during a deploy and how to redeploy automatically on every push.'
  },
  'guides/domain-and-https.md': {
    title: 'A domain with HTTPS',
    description: 'Serve a deployed app on your own domain with a certificate.'
  },
  'guides/rolling-back.md': {
    title: 'Rolling back',
    description: 'What happens when a deploy fails, and how to go back to an earlier version by hand.'
  },
  'guides/alerts-and-incidents.md': {
    title: 'Understanding alerts and incidents',
    description: 'What alerts, incidents and severities mean, what the daily briefing says and how to mute noise.'
  },
  'guides/asking-the-operator.md': {
    title: 'Asking the operator questions',
    description: 'How to ask questions in plain language and how to read an answer and its evidence.'
  },
  'guides/summarising-logs.md': {
    title: 'Summarising logs',
    description: 'A short summary of new error patterns and spikes, on request or once a day.'
  },
  'guides/watching-vulnerabilities.md': {
    title: 'Watching vulnerabilities',
    description: 'How container scans work, how to read the results and how image updates are handled.'
  },
  'guides/chat-commands.md': {
    title: 'Chat commands',
    description: 'What you can type to the Haetae bot: linking, log and security summaries, replies and questions.'
  },
  'concepts/index.md': {
    title: 'Concepts in plain words',
    description: 'Host, agent, container, incident, autonomy levels, evidence, redaction and untrusted log text explained simply.'
  },
  'problems/index.md': {
    title: 'Problems and what to do',
    description: 'What to check when a host is offline, a deploy or scan fails, the operator is silent, chat is quiet or memory is high.'
  },
  'tested.md': {
    title: 'What is tested and what is not',
    description: 'An honest account of what has been verified, on what, and what has not been tried yet.'
  },
  'reference/environment-variables.md': {
    title: 'Environment variables',
    description: 'The settings of the server, the installer and the agent, one sentence each.'
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
