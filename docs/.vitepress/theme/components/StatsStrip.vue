<script setup lang="ts">
import { withBase } from 'vitepress'

// Measured values from docs/acceptance.md (one VM, 2 cores, 8 GB, loopback).
const stats = [
  { value: '116 ms', caption: 'from enrollment to an online agent', ref: 'S1' },
  { value: '18.6 MiB', caption: 'agent memory peak on the release build (limit 50 MiB)', ref: 'S10' },
  { value: '96%', caption: 'of routine log lines never sent, after warm-up', ref: 'S5' },
  { value: '~1 s', caption: 'from a new advisory to an alert, on a crafted test database', ref: 'S8' },
  { value: '6.1 s', caption: 'from a cached push to a healthy deploy', ref: 'S2' }
]
</script>

<template>
  <div class="hx-stats" role="region" aria-labelledby="hx-stats-title">
    <h2 id="hx-stats-title" class="hx-sr">Measured results</h2>
    <i class="hx-stats__corner hx-stats__corner--tl" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--tr" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--bl" aria-hidden="true" />
    <i class="hx-stats__corner hx-stats__corner--br" aria-hidden="true" />
    <ul class="hx-stats__list">
      <li v-for="s in stats" :key="s.ref">
        <a :href="withBase('/acceptance#criteria')" class="hx-stat">
          <span class="hx-stat__value">{{ s.value }}</span>
          <span class="hx-stat__caption">{{ s.caption }}</span>
          <span class="hx-stat__ref">{{ s.ref }} in the acceptance report</span>
        </a>
      </li>
    </ul>
  </div>
</template>
