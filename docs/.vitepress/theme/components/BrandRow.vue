<script setup lang="ts">
import { icons, type IconName } from '../icons'
import BrandIcon from './BrandIcon.vue'

// A row of brand marks. Each item has an accessible name and a tooltip (hover and keyboard focus).
withDefaults(defineProps<{ items: IconName[]; size?: number; label?: string; tabbable?: boolean }>(), { tabbable: true })
</script>

<template>
  <ul class="hx-brands" :aria-label="label">
    <li v-for="n in items" :key="n">
      <span class="hx-brand" :tabindex="tabbable ? 0 : undefined" :style="{ '--brand': '#' + icons[n].hex }">
        <BrandIcon :name="n" :size="size ?? 22" />
        <span class="hx-brand__tip" aria-hidden="true">{{ icons[n].label }}</span>
      </span>
    </li>
  </ul>
</template>
