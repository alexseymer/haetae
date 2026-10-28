<script setup lang="ts">
import DefaultTheme from 'vitepress/theme-without-fonts'
import { useData } from 'vitepress'
import { nextTick, onMounted, onUnmounted, watch } from 'vue'
import AnnouncementBanner from './components/AnnouncementBanner.vue'
import HomePage from './components/HomePage.vue'
import SiteFooter from './components/SiteFooter.vue'
import NotFound from './components/NotFound.vue'

const { frontmatter, page } = useData()

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
})
onUnmounted(() => observer?.disconnect())
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
