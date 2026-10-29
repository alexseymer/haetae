<script setup lang="ts">
import { computed, ref } from 'vue'
import { rafThrottle } from '../composables/motion'

// Screenshots live in docs/images/. The site builds with or without them.
const found = import.meta.glob('../../../images/*.png', { eager: true, query: '?url', import: 'default' }) as Record<string, string>
const urlFor = (file: string): string | undefined => found[`../../../images/${file}`]

const shots = [
  { file: 'hosts.png', title: 'Hosts', caption: 'Every server at a glance: status, versions and a security badge.', alt: 'The Haetae hosts list with status and security badges' },
  { file: 'host-overview.png', title: 'Host overview', caption: 'CPU, memory, disk and network for one host, plus its containers.', alt: 'Overview of one host with metrics charts and containers' },
  { file: 'incident.png', title: 'Incident with evidence', caption: 'What happened, with a link to the data behind each claim.', alt: 'An incident report with linked evidence' },
  { file: 'chat.png', title: 'Chat with the operator', caption: 'Ask in plain language; answers cite evidence or say "cause not found".', alt: 'Web chat with the AI operator answering a question with evidence' },
  { file: 'security.png', title: 'Security tab', caption: 'CVEs with fixed versions, misconfigurations and outbound connections.', alt: 'The security tab of a host listing vulnerabilities and misconfigurations' },
  { file: 'ai-operator.png', title: 'AI operator settings', caption: 'Provider status, autonomy level and the daily token cap.', alt: 'Settings page for the AI operator with autonomy and token cap' },
  { file: 'add-host.png', title: 'Add a host', caption: 'One command and a single-use token enroll a server.', alt: 'The add host dialog showing the enrollment command' }
].map((s) => ({ ...s, src: urlFor(s.file) }))

const dialog = ref<HTMLDialogElement | null>(null)
const current = ref(0)
const available = computed(() => shots.map((s, i) => ({ s, i })).filter((x) => x.s.src))
const hasAny = computed(() => available.value.length > 0)
const active = computed(() => shots[current.value])

function open(i: number) {
  current.value = i
  dialog.value?.showModal()
}
function close() {
  dialog.value?.close()
}
function step(dir: 1 | -1) {
  const list = available.value.map((x) => x.i)
  const pos = list.indexOf(current.value)
  current.value = list[(pos + dir + list.length) % list.length]
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'ArrowRight') step(1)
  else if (e.key === 'ArrowLeft') step(-1)
}
// Soft tilt and parallax on hover: sets --rx/--ry/--px/--py on the frame (rAF throttled).
const tilt = rafThrottle((e) => {
  const el = (e.currentTarget as HTMLElement) || null
  if (!el) return
  const r = el.getBoundingClientRect()
  const x = (e.clientX - r.left) / r.width - 0.5
  const y = (e.clientY - r.top) / r.height - 0.5
  el.style.setProperty('--rx', `${(-y * 5).toFixed(2)}deg`)
  el.style.setProperty('--ry', `${(x * 6).toFixed(2)}deg`)
  el.style.setProperty('--px', `${(-x * 10).toFixed(1)}px`)
  el.style.setProperty('--py', `${(-y * 8).toFixed(1)}px`)
})
function tiltReset(e: PointerEvent) {
  const el = e.currentTarget as HTMLElement
  for (const k of ['--rx', '--ry', '--px', '--py']) el.style.removeProperty(k)
}
function onBackdrop(e: MouseEvent) {
  if (e.target === dialog.value) close()
}
</script>

<template>
  <section class="hx-section" aria-labelledby="hx-shots-title">
    <div class="hx-wrap">
      <p v-reveal class="hx-kicker">The interface</p>
      <h2 id="hx-shots-title" v-reveal="1" class="hx-h2">See what it shows you</h2>
      <p v-if="!hasAny" class="hx-lede">Screenshots of the web interface will appear here.</p>
      <ul class="hx-shots">
        <li v-for="(s, i) in shots" :key="s.file" v-reveal="i % 2">
          <figure>
            <button v-if="s.src" type="button" class="hx-shot" :aria-label="`Enlarge screenshot: ${s.title}`" @click="open(i)" @pointermove="tilt" @pointerleave="tiltReset">
              <img :src="s.src" :alt="s.alt" loading="lazy" decoding="async" />
              <span class="hx-shot__zoom" aria-hidden="true">
                <svg viewBox="0 0 24 24" width="16" height="16"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
              </span>
            </button>
            <div v-else class="hx-shot hx-shot--empty" role="img" :aria-label="`${s.title}: screenshot not available yet`">
              <svg viewBox="0 0 160 100" aria-hidden="true">
                <rect x="4" y="4" width="152" height="92" rx="6" fill="none" stroke="currentColor" stroke-width="1.4" stroke-dasharray="5 4" />
                <path d="M4 20h152" stroke="currentColor" stroke-width="1.4" stroke-dasharray="5 4" />
                <circle cx="14" cy="12" r="2" fill="currentColor" /><circle cx="22" cy="12" r="2" fill="currentColor" /><circle cx="30" cy="12" r="2" fill="currentColor" />
                <path d="M16 34h50M16 46h86M16 58h70M16 70h40" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-dasharray="2 4" />
              </svg>
              <span>Screenshot coming soon</span>
            </div>
            <figcaption><strong>{{ s.title }}</strong><span>{{ s.caption }}</span></figcaption>
          </figure>
        </li>
      </ul>
    </div>

    <dialog ref="dialog" class="hx-lightbox" aria-label="Screenshot viewer" @keydown="onKey" @click="onBackdrop">
      <div v-if="active && active.src" class="hx-lightbox__body">
        <div class="hx-lightbox__bar">
          <p>{{ active.title }}</p>
          <div>
            <button type="button" aria-label="Previous screenshot" @click="step(-1)">&larr;</button>
            <button type="button" aria-label="Next screenshot" @click="step(1)">&rarr;</button>
            <button type="button" aria-label="Close" @click="close">&times;</button>
          </div>
        </div>
        <img :src="active.src" :alt="active.alt" />
        <p class="hx-lightbox__cap">{{ active.caption }}</p>
      </div>
    </dialog>
  </section>
</template>
