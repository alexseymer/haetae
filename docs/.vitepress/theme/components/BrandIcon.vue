<script setup lang="ts">
import { computed } from 'vue'
import { icons, type IconName } from '../icons'

// Monochrome (currentColor), brand colour on hover via --brand. Accessible name from the label,
// or aria-hidden when `decorative` (the visible text next to it already names it).
const props = withDefaults(defineProps<{ name: IconName; size?: number; decorative?: boolean }>(), {
  size: 20,
  decorative: false
})
const def = computed(() => icons[props.name] as { label: string; path?: string; glyph?: string; hex: string })
</script>

<template>
  <svg
    class="hx-icon"
    :class="def.path ? 'hx-icon--fill' : 'hx-icon--stroke'"
    :style="{ '--brand': '#' + def.hex }"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    :role="decorative ? undefined : 'img'"
    :aria-label="decorative ? undefined : def.label"
    :aria-hidden="decorative ? 'true' : undefined"
    focusable="false"
  >
    <path v-if="def.path" :d="def.path" fill="currentColor" />
    <g v-else v-html="def.glyph" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
  </svg>
</template>
