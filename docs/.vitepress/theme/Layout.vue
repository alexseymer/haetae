<script setup lang="ts">
import DefaultTheme from 'vitepress/theme-without-fonts'
import { useData } from 'vitepress'
import { nextTick, onMounted, onUnmounted, provide, watch } from 'vue'
import AnnouncementBanner from './components/AnnouncementBanner.vue'
import HomePage from './components/HomePage.vue'
import SiteFooter from './components/SiteFooter.vue'
import NotFound from './components/NotFound.vue'

const { frontmatter, page, isDark } = useData()

// Theme switch: circular reveal with the View Transitions API, a plain colour fade otherwise.
const reduced = () => window.matchMedia('(prefers-reduced-motion: reduce)').matches
provide('toggle-appearance', async ({ clientX: x, clientY: y }: MouseEvent) => {
  const root = document.documentElement
  if (reduced()) {
    isDark.value = !isDark.value
    return
  }
  if (!('startViewTransition' in document)) {
    root.classList.add('hx-theme-fade')
    isDark.value = !isDark.value
    setTimeout(() => root.classList.remove('hx-theme-fade'), 450)
    return
  }
  const radius = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y))
  const clipPath = [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`]
  await (document as any)
    .startViewTransition(async () => {
      isDark.value = !isDark.value
      await nextTick()
    })
    .ready
  root.animate(
    { clipPath: isDark.value ? clipPath.slice().reverse() : clipPath },
    {
      duration: 420,
      easing: 'ease-in',
      pseudoElement: `::view-transition-${isDark.value ? 'old' : 'new'}(root)`
    }
  )
})

// Shrinking blurred navigation bar once the page has scrolled.
function onScroll() {
  document.documentElement.classList.toggle('hx-scrolled', window.scrollY > 12)
}

// Diagrams are rendered client-side and may scroll: make them keyboard reachable.
function markDiagrams() {
  document.querySelectorAll<HTMLElement>('.vp-doc .mermaid:not([tabindex])').forEach((el) => {
    el.setAttribute('tabindex', '0')
    el.setAttribute('role', 'group')
    el.setAttribute('aria-label', 'Diagram (scrollable)')
  })
}
let observer: MutationObserver | undefined
onMounted(() => {
  observer = new MutationObserver(markDiagrams)
  observer.observe(document.body, { childList: true, subtree: true })
  markDiagrams()
  onScroll()
  window.addEventListener('scroll', onScroll, { passive: true })
})
onUnmounted(() => {
  observer?.disconnect()
  window.removeEventListener('scroll', onScroll)
})
watch(() => page.value.relativePath, () => nextTick(markDiagrams))
const { Layout } = DefaultTheme
</script>

<template>
  <Layout>
    <template #layout-top>
      <AnnouncementBanner v-if="frontmatter.layout === 'home'" />
    </template>
    <template #home-hero-before>
      <HomePage />
    </template>
    <template #layout-bottom>
      <SiteFooter v-if="frontmatter.layout === 'home'" />
    </template>
    <template #not-found>
      <NotFound />
    </template>
  </Layout>
</template>
