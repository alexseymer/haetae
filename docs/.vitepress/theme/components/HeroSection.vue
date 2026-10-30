<script setup lang="ts">
import { withBase } from 'vitepress'
import CommandBox from './CommandBox.vue'
import HeroArt from './HeroArt.vue'
import StatsStrip from './StatsStrip.vue'
import BrandRow from './BrandRow.vue'

// Decorative texture: real strings from the product, faint, bottom of the hero.
const lines = [
  'agent: connect outbound tls=mtls host=hel1-prod ok 116ms',
  'logpipe: 20000 lines -> 68 events (96.0% suppressed)',
  'redact: secrets=0 personal=0 policy=fail-closed',
  'deploy: push main@3f9a1c -> healthy in 6.1s, previous version kept',
  'scan: 41 packages, 2 findings, fixed in 1.4.2, severity=high',
  'conntrack: new outbound 203.0.113.7:443 (first seen)',
  'operator: cause not found; evidence: 3 events, 0 matches',
  'alerts: rule=crash-loop container=shop-web restarts=5',
  'caddy: route shop.example.com -> shop-web:3000 tls=ok'
]
const texture = Array.from({ length: 28 }, (_, i) => lines[i % lines.length]).join('\n')
</script>

<template>
  <section class="hx-hero" aria-labelledby="hx-hero-title">
    <div class="hx-hero__bg" aria-hidden="true"><i class="hx-hero__mesh hx-hero__mesh--a" /><i class="hx-hero__mesh hx-hero__mesh--b" /><i class="hx-hero__grid" /></div>
    <pre class="hx-hero__texture" aria-hidden="true">{{ texture }}</pre>
    <div class="hx-hero__inner">
      <div class="hx-hero__copy">
        <p class="hx-eyebrow">Self-hosted <span aria-hidden="true">/</span> AGPL-3.0 <span aria-hidden="true">/</span> for 2 to 20 servers</p>
        <h1 id="hx-hero-title" class="hx-title">
          <span class="hx-hl">An AI operator</span>
          <span class="hx-hl">for Docker Compose</span>
          <span class="hx-hl">servers.</span>
        </h1>
        <p class="hx-sub">
          Deploy apps, watch metrics, scan containers and get answers in chat, from one tool you run
          yourself. Every statement links to its evidence; when the data shows no cause, it says so.
        </p>
        <div class="hx-actions">
          <a class="hx-btn hx-btn--primary" :href="withBase('/getting-started/')">
            Get started
            <svg class="hx-arrow" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M3 8h9M8.5 4l4 4-4 4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
          </a>
          <a class="hx-btn hx-btn--soft" :href="withBase('/concepts/')">Concepts in plain words</a>
        </div>
        <p class="hx-note">No published image yet: you build the server from source in a few minutes.</p>
        <CommandBox />
        <div class="hx-works">
          <span class="hx-works__label" id="hx-works-label">Works with</span>
          <BrandRow :items="['docker', 'compose', 'caddy', 'postgres', 'github', 'telegram', 'slack', 'signal', 'email']" :size="22" label="Works with" />
        </div>
      </div>
      <HeroArt class="hx-hero__art" />
    </div>
    <StatsStrip />
  </section>
</template>
