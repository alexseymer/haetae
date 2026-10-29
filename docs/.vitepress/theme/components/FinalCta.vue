<script setup lang="ts">
import { withBase } from 'vitepress'
import BrandIcon from './BrandIcon.vue'
</script>

<template>
  <section v-reveal class="hx-cta" aria-labelledby="hx-cta-title">
    <i class="hx-stats__corner hx-stats__corner--tl" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--tr" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--bl" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--br" aria-hidden="true" />
    <h2 id="hx-cta-title" class="hx-title hx-title--sm">
      <span class="hx-hl">Try it on one server.</span>
    </h2>
    <p>One machine can be both the control plane and a host for a trial. Install to first login takes about half a minute once the image is built.</p>
    <div class="hx-actions hx-actions--center">
      <a class="hx-btn hx-btn--primary" :href="withBase('/install')">Get started <span aria-hidden="true">&rarr;</span></a>
      <a class="hx-btn hx-btn--soft" href="https://github.com/alexseymer/haetae"><BrandIcon name="github" :size="18" decorative />View on GitHub</a>
    </div>
  </section>
</template>
