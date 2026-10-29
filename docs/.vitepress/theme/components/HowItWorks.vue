<script setup lang="ts">
import { withBase } from 'vitepress'
import { onMounted, onUnmounted, ref } from 'vue'
import BrandIcon from './BrandIcon.vue'
import BrandRow from './BrandRow.vue'
import { prefersReducedMotion, onceVisible } from '../composables/motion'

const steps = [
  { t: 'Run the server', d: 'One installer command starts the control plane and PostgreSQL, with its own certificate authority.' },
  { t: 'Enroll a host', d: 'A single-use token. The agent connects outward with mutual TLS and opens no port.' },
  { t: 'Filter on the host', d: 'The agent reads Docker state, metrics and logs and redacts them before anything leaves.' },
  { t: 'Ask in chat', d: 'Rule alerts need no AI. The optional operator answers from your data and cites evidence.' }
]

// Steps highlight in sequence once the section is in view (and light up the matching part of the diagram).
const active = ref(-1)
const root = ref<HTMLElement | null>(null)
let timer: ReturnType<typeof setInterval> | undefined
let stop: (() => void) | void
onMounted(() => {
  if (prefersReducedMotion() || !root.value) return
  stop = onceVisible(root.value, () => {
    active.value = 0
    timer = setInterval(() => (active.value = (active.value + 1) % steps.length), 2200)
  }, 0.3)
})
onUnmounted(() => {
  clearInterval(timer)
  if (stop) stop()
})
</script>

<template>
  <section ref="root" class="hx-section hx-section--alt" aria-labelledby="hx-how-title">
    <div class="hx-wrap">
      <p v-reveal class="hx-kicker">How it works</p>
      <h2 id="hx-how-title" v-reveal="1" class="hx-h2">Hosts call out. Nothing calls in.</h2>

      <figure v-reveal class="hx-arch" :data-step="active">
        <div class="hx-arch__grid" role="group" aria-label="Architecture overview">
          <div class="hx-node hx-node--you">
            <h3>You</h3>
            <p>Browser or chat</p>
            <BrandRow class="hx-node__brands" :items="['web', 'telegram', 'slack', 'signal', 'email']" :size="18" label="Chat channels" />
          </div>

          <div class="hx-link hx-link--solid" aria-hidden="true"><span>chat and web</span></div>

          <div class="hx-node hx-node--cp">
            <h3>Control plane</h3>
            <p class="hx-node__sub">Docker Compose on one machine</p>
            <ul>
              <li>haetae-server: UI, API, agent gateway</li>
              <li>Alert rules, no AI needed</li>
              <li>AI operator (optional)</li>
              <li><BrandIcon name="postgres" :size="15" decorative />PostgreSQL</li>
            </ul>
          </div>

          <div class="hx-link hx-link--dashed" aria-hidden="true"><span>outbound only, mTLS</span></div>

          <div class="hx-node hx-node--host">
            <h3>Each Linux host</h3>
            <p class="hx-node__sub">no inbound port</p>
            <ul>
              <li>haetae-agent: metrics, logs, redaction</li>
              <li>Deploys and scans on command</li>
              <li><BrandIcon name="docker" :size="15" decorative /><BrandIcon name="compose" :size="15" decorative />Docker and Compose</li>
              <li><BrandIcon name="caddy" :size="15" decorative />Caddy for HTTPS</li>
            </ul>
          </div>

          <div class="hx-node hx-node--ai">
            <h3><BrandIcon name="ai" :size="17" decorative />AI provider</h3>
            <p class="hx-node__sub">optional, sees redacted data only</p>
          </div>
        </div>
        <figcaption>
          Local models and non-local providers are both supported; with a non-local one, if redaction fails nothing is sent.
          <a :href="withBase('/architecture')">Full architecture and data flows</a>.
        </figcaption>
      </figure>

      <ol class="hx-steps" aria-label="Steps">
        <li v-for="(s, i) in steps" :key="s.t" v-reveal="i" :data-active="active === i">
          <span class="hx-steps__n" aria-hidden="true">{{ i + 1 }}</span>
          <h3>{{ s.t }}</h3>
          <p>{{ s.d }}</p>
        </li>
      </ol>
    </div>
  </section>
</template>
