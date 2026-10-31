<script setup lang="ts">
import { withBase } from 'vitepress'
import { onMounted, onUnmounted } from 'vue'

// The banner scrolls away; the fixed navbar follows it up smoothly.
const H = 38
function sync() {
  const h = Math.max(0, H - window.scrollY)
  document.documentElement.style.setProperty('--vp-layout-top-height', `${h}px`)
}
onMounted(() => {
  sync()
  window.addEventListener('scroll', sync, { passive: true })
})
onUnmounted(() => {
  window.removeEventListener('scroll', sync)
  document.documentElement.style.removeProperty('--vp-layout-top-height')
})
</script>

<template>
  <aside class="hx-banner" aria-label="Announcement">
    <span class="hx-banner__tag">0.8.0</span>
    <p>
      Version 0.8.0 is built. <a :href="withBase('/tested')">Read what is tested and what is not</a>
      <span class="hx-banner__arrow" aria-hidden="true">&rarr;</span>
    </p>
  </aside>
</template>
