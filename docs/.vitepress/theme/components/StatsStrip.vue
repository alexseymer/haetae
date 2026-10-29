<script setup lang="ts">
import { withBase } from 'vitepress'
import { onMounted, onUnmounted, ref } from 'vue'
import { prefersReducedMotion, onceVisible } from '../composables/motion'

// Measured values from docs/acceptance.md (one VM, 2 cores, 8 GB, loopback).
const stats = [
  { value: '116 ms', caption: 'from enrollment to an online agent', ref: 'S1' },
  { value: '18.6 MiB', caption: 'agent memory peak on the release build (limit 50 MiB)', ref: 'S10' },
  { value: '96%', caption: 'of routine log lines never sent, after warm-up', ref: 'S5' },
  { value: '~1 s', caption: 'from a new advisory to an alert, on a crafted test database', ref: 'S8' },
  { value: '6.1 s', caption: 'from a cached push to a healthy deploy', ref: 'S2' }
]

// Count-up when scrolled into view. The real value is always in the DOM (and sizes the box);
// while counting, a visual copy shows the running number.
const parts = stats.map((s) => {
  const m = s.value.match(/^(\D*)([\d.]+)(.*)$/)!
  return { pre: m[1], num: parseFloat(m[2]), dec: (m[2].split('.')[1] || '').length, post: m[3] }
})
const shown = ref<string[]>(stats.map((s) => s.value))
const counting = ref(false)
const root = ref<HTMLElement | null>(null)
let raf = 0
let stop: (() => void) | void
onMounted(() => {
  if (prefersReducedMotion() || !root.value) return
  stop = onceVisible(root.value, () => {
    const dur = 1400
    const t0 = performance.now()
    counting.value = true
    const frame = (now: number) => {
      const p = Math.min(1, (now - t0) / dur)
      const e = 1 - Math.pow(1 - p, 3)
      shown.value = parts.map((q) => q.pre + (q.num * e).toFixed(q.dec) + q.post)
      if (p < 1) raf = requestAnimationFrame(frame)
      else counting.value = false
    }
    raf = requestAnimationFrame(frame)
  }, 0.4)
})
onUnmounted(() => {
  cancelAnimationFrame(raf)
  if (stop) stop()
})
</script>

<template>
  <div ref="root" class="hx-stats" role="region" aria-labelledby="hx-stats-title">
    <h2 id="hx-stats-title" class="hx-sr">Measured results</h2>
    <i class="hx-stats__corner hx-stats__corner--tl" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--tr" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--bl" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--br" aria-hidden="true" />
    <ul class="hx-stats__list">
      <li v-for="(s, i) in stats" :key="s.ref">
        <a :href="withBase('/acceptance#criteria')" class="hx-stat">
          <span class="hx-stat__value"><span :class="{ 'is-ghost': counting }">{{ s.value }}</span><span v-if="counting" class="hx-stat__live" aria-hidden="true">{{ shown[i] }}</span></span>
          <span class="hx-stat__caption">{{ s.caption }}</span>
          <span class="hx-stat__ref">{{ s.ref }} in the acceptance report</span>
        </a>
      </li>
    </ul>
  </div>
</template>
