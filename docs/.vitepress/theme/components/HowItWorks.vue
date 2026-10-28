<script setup lang="ts">
import { withBase } from 'vitepress'

const steps = [
  { t: 'Run the server', d: 'One installer command starts the control plane and PostgreSQL, with its own certificate authority.' },
  { t: 'Enroll a host', d: 'A single-use token. The agent connects outward with mutual TLS and opens no port.' },
  { t: 'Filter on the host', d: 'The agent reads Docker state, metrics and logs and redacts them before anything leaves.' },
  { t: 'Ask in chat', d: 'Rule alerts need no AI. The optional operator answers from your data and cites evidence.' }
]
</script>

<template>
  <section class="hx-section hx-section--alt" aria-labelledby="hx-how-title">
    <div class="hx-wrap">
      <p class="hx-kicker">How it works</p>
      <h2 id="hx-how-title" class="hx-h2">Hosts call out. Nothing calls in.</h2>

      <figure class="hx-arch">
        <div class="hx-arch__grid" role="group" aria-label="Architecture overview">
          <div class="hx-node hx-node--you">
            <h3>You</h3>
            <p>Browser or chat</p>
            <p class="hx-node__sub">web, Telegram, Slack, email, Signal</p>
          </div>

          <div class="hx-link hx-link--solid" aria-hidden="true"><span>chat and web</span></div>

          <div class="hx-node hx-node--cp">
            <h3>Control plane</h3>
            <p class="hx-node__sub">Docker Compose on one machine</p>
            <ul>
              <li>haetae-server: UI, API, agent gateway</li>
              <li>Alert rules, no AI needed</li>
              <li>AI operator (optional)</li>
              <li>PostgreSQL</li>
            </ul>
          </div>

          <div class="hx-link hx-link--dashed" aria-hidden="true"><span>outbound only, mTLS</span></div>

          <div class="hx-node hx-node--host">
            <h3>Each Linux host</h3>
            <p class="hx-node__sub">no inbound port</p>
            <ul>
              <li>haetae-agent: metrics, logs, redaction</li>
              <li>Deploys and scans on command</li>
              <li>Docker and Compose</li>
              <li>Caddy for HTTPS</li>
            </ul>
          </div>

          <div class="hx-node hx-node--ai">
            <h3>AI provider</h3>
            <p class="hx-node__sub">optional, sees redacted data only</p>
          </div>
        </div>
        <figcaption>
          Local models and non-local providers are both supported; with a non-local one, if redaction fails nothing is sent.
          <a :href="withBase('/architecture')">Full architecture and data flows</a>.
        </figcaption>
      </figure>

      <ol class="hx-steps">
        <li v-for="(s, i) in steps" :key="s.t">
          <span class="hx-steps__n" aria-hidden="true">{{ i + 1 }}</span>
          <h3>{{ s.t }}</h3>
          <p>{{ s.d }}</p>
        </li>
      </ol>
    </div>
  </section>
</template>
