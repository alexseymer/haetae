<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { prefersReducedMotion, onceVisible } from '../composables/motion'

const first = 'git clone https://github.com/alexseymer/haetae.git haetae-src'
const all = [
  '# 1. clone (anywhere except /opt/haetae)',
  first,
  'cd haetae-src',
  '# 2. build the server image (several minutes the first time)',
  'docker build -t haetae-server:local .',
  '# 3. install: prints the URL and a one-time setup code',
  'sudo HAETAE_IMAGE=haetae-server:local HAETAE_NO_PULL=1 sh deploy/install-server.sh'
].join('\n')

const mode = ref<'first' | 'all'>('first')
const copied = ref(false)
const text = computed(() => (mode.value === 'first' ? first : all))
let timer: ReturnType<typeof setTimeout> | undefined

// Typewriter: the full text is always rendered (it sizes the box and is what assistive tech reads);
// on mount a visual copy types over it once. Without JS or with reduced motion nothing changes.
const root = ref<HTMLElement | null>(null)
const typed = ref('')
const typing = ref(false)
let tick: ReturnType<typeof setInterval> | undefined
let stop: (() => void) | void
onMounted(() => {
  if (prefersReducedMotion() || !root.value) return
  stop = onceVisible(root.value, () => {
    const target = first
    let i = 0
    typing.value = true
    tick = setInterval(() => {
      i++
      typed.value = target.slice(0, i)
      if (i >= target.length) {
        clearInterval(tick)
        typing.value = false
      }
    }, 22)
  }, 0.5)
})
onUnmounted(() => {
  clearInterval(tick)
  clearTimeout(timer)
  if (stop) stop()
})

async function copy() {
  const value = text.value
  try {
    await navigator.clipboard.writeText(value)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = value
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    try {
      document.execCommand('copy')
    } finally {
      document.body.removeChild(ta)
    }
  }
  copied.value = true
  clearTimeout(timer)
  timer = setTimeout(() => (copied.value = false), 2000)
}
</script>

<template>
  <div ref="root" class="hx-cmd" role="group" aria-label="Quick start commands">
    <div class="hx-cmd__bar">
      <span class="hx-cmd__label">Copy quick start</span>
      <div class="hx-cmd__tabs" role="group" aria-label="How much to show">
        <button type="button" :aria-pressed="mode === 'first'" @click="mode = 'first'">1 command</button>
        <button type="button" :aria-pressed="mode === 'all'" @click="mode = 'all'">All 3 steps</button>
      </div>
      <button type="button" class="hx-cmd__copy" :aria-label="mode === 'first' ? 'Copy the clone command' : 'Copy all quick start commands'" @click="copy">
        <svg v-if="!copied" viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><rect x="9" y="9" width="11" height="11" rx="2" fill="none" stroke="currentColor" stroke-width="1.8" /><path d="M5 15V6a2 2 0 0 1 2-2h9" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" /></svg>
        <svg v-else viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><path d="m5 12.5 4.5 4.5L19 7.5" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" /></svg>
      </button>
    </div>
    <pre class="hx-cmd__code" tabindex="0" aria-label="Command text"><code :class="{ 'is-typing': typing && mode === 'first' }"><span v-if="mode === 'first'" class="hx-cmd__prompt" aria-hidden="true">$ </span><span class="hx-cmd__text">{{ text }}</span><span v-if="typing && mode === 'first'" class="hx-cmd__typed" aria-hidden="true"><span class="hx-cmd__prompt">$ </span>{{ typed }}</span><span class="hx-cmd__caret" aria-hidden="true" /></code></pre>
    <p class="hx-sr" role="status">{{ copied ? 'Copied to clipboard' : '' }}</p>
  </div>
</template>
