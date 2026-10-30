<script setup lang="ts">
import { withBase } from 'vitepress'
import BrandRow from './BrandRow.vue'
import { rafThrottle } from '../composables/motion'
import type { IconName } from '../icons'

// Glow edge that follows the pointer: sets --mx/--my on the hovered card (one throttled listener).
const onMove = rafThrottle((e) => {
  const card = (e.target as HTMLElement).closest<HTMLElement>('.hx-card')
  if (!card) return
  const r = card.getBoundingClientRect()
  card.style.setProperty('--mx', `${e.clientX - r.left}px`)
  card.style.setProperty('--my', `${e.clientY - r.top}px`)
})

const features = [
  {
    title: 'Deploy from Git or compose.yaml',
    text: 'Push to deploy by polling the branch, build log, HTTPS through a managed Caddy and one-click rollback. A failed deploy keeps the previous version running.',
    link: '/guides/deploy-from-git',
    cta: 'Deploy from Git',
    brands: ['github', 'docker', 'compose', 'caddy'] as IconName[],
    paths: ['M12 3v12', 'm7 8 5-5 5 5', 'M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4']
  },
  {
    title: 'Metrics and logs',
    text: 'CPU, memory, disk and network on by default. Logs are filtered and redacted on the host, so routine lines are never sent.',
    link: '/concepts/',
    cta: 'Concepts in plain words',
    paths: ['M4 20V10', 'M10 20V4', 'M16 20v-8', 'M22 20H2']
  },
  {
    title: 'Security scanning',
    text: 'Per container: CVEs with the fixed version, misconfigurations and an outbound connection watch. A failed scan says "scan failed", never "no vulnerabilities".',
    link: '/guides/watching-vulnerabilities',
    cta: 'Watching vulnerabilities',
    paths: ['M12 3 4 6v6c0 4.5 3.2 8 8 9 4.8-1 8-4.5 8-9V6l-8-3Z', 'm9 12 2 2 4-4']
  },
  {
    title: 'Chat where you already are',
    text: 'Web, Telegram, Slack, email and Signal. Incident alerts and one short daily briefing, working even when the AI is down.',
    link: '/guides/alerts-and-incidents',
    cta: 'Alerts and incidents',
    brands: ['web', 'telegram', 'slack', 'signal', 'email'] as IconName[],
    paths: ['M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12Z']
  },
  {
    title: 'An operator that cites evidence',
    text: 'Plain-language questions about your own data. Each statement links to its evidence, and without a cause in the data the answer is "cause not found".',
    link: '/guides/asking-the-operator',
    cta: 'Asking the operator',
    brands: ['ai'] as IconName[],
    paths: ['m4 7 5 5-5 5', 'M12 18h8']
  },
  {
    title: 'Safe by design',
    text: 'Outbound-only agent with mutual TLS, no listening port, redaction on the host with a fail-closed rule, and autonomy levels that default to report only.',
    link: '/concepts/#redaction-and-what-leaves-your-server',
    cta: 'What leaves your server',
    paths: ['M6 11V8a6 6 0 0 1 12 0v3', 'M5 11h14v10H5z', 'M12 15v2']
  }
]
</script>

<template>
  <section class="hx-section" aria-labelledby="hx-features-title">
    <div class="hx-wrap">
      <p v-reveal class="hx-kicker">What you get</p>
      <h2 id="hx-features-title" v-reveal="1" class="hx-h2">One tool where you had a stack</h2>
      <p class="hx-lede">
        A deploy tool, a monitoring stack and a scanner, replaced by a small agent on each server and one
        control plane you run with Docker Compose.
      </p>
      <ul class="hx-grid" @pointermove="onMove">
        <li v-for="(f, i) in features" :key="f.title" v-reveal="i % 3">
          <a class="hx-card" :href="withBase(f.link)">
            <svg class="hx-card__icon" viewBox="0 0 24 24" width="26" height="26" aria-hidden="true">
              <path v-for="d in f.paths" :key="d" :d="d" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <h3>{{ f.title }}</h3>
            <p>{{ f.text }}</p>
            <BrandRow v-if="f.brands" class="hx-card__brands" :items="f.brands" :size="20" :tabbable="false" />
            <span class="hx-card__cta">{{ f.cta }} <span aria-hidden="true">&rarr;</span></span>
          </a>
        </li>
      </ul>
    </div>
  </section>
</template>
