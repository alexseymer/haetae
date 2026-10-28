<script setup lang="ts">
import { withBase } from 'vitepress'
</script>

<template>
  <main id="hx-main" class="hx-404">
    <p class="hx-404__code"><span class="hx-hl">404</span></p>
    <h1 class="hx-404__title">This page is not here</h1>
    <p class="hx-404__text">
      It may have moved, or the link may be wrong. Try the search (press <kbd>/</kbd> or <kbd>Ctrl</kbd> <kbd>K</kbd>) or start from one of these.
    </p>
    <div class="hx-actions hx-actions--center">
      <a class="hx-btn hx-btn--primary" :href="withBase('/')">Back to home</a>
      <a class="hx-btn hx-btn--soft" :href="withBase('/README')">Documentation index</a>
      <a class="hx-btn hx-btn--soft" :href="withBase('/install')">Install</a>
    </div>
  </main>
</template>
