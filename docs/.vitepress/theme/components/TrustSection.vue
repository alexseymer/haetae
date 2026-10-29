<script setup lang="ts">
import { withBase } from 'vitepress'

const verified = [
  'Every spec criterion (S1 to S11) has a test and a measured value in the acceptance report.',
  'The agent opens no listening socket and reconnects by itself after a restart or a network cut.',
  'Redaction recall on the labelled corpus: secrets 1.000, personal data 1.000.',
  'A failed scan reports "scan failed", never "no vulnerabilities".'
]
const notYet = [
  'Everything ran on a single VM (2 cores, 8 GB); timings are loopback, not a real fleet.',
  'No run with a live AI provider: the provider was scripted, so real answer quality is unmeasured.',
  'Live chat services were faked: Telegram against a fake Bot API; Slack, Signal and email by unit tests only.',
  'Public certificate issuance was not run, and there is no published container image yet.'
]
</script>

<template>
  <section class="hx-section hx-section--alt" aria-labelledby="hx-trust-title">
    <div class="hx-wrap">
      <p v-reveal class="hx-kicker">Trust</p>
      <h2 id="hx-trust-title" v-reveal="1" class="hx-h2">What is verified, and what is not</h2>
      <p class="hx-lede">
        We would rather you know the limits before you install. The numbers above come from one scripted run,
        and the report lists every narrowing next to them.
      </p>
      <div class="hx-trust">
        <div v-reveal="0" class="hx-trust__col">
          <h3><span class="hx-dot hx-dot--ok" aria-hidden="true" />Verified</h3>
          <ul>
            <li v-for="v in verified" :key="v">{{ v }}</li>
          </ul>
        </div>
        <div v-reveal="1" class="hx-trust__col">
          <h3><span class="hx-dot hx-dot--warn" aria-hidden="true" />Verified only against fakes, or not at all</h3>
          <ul>
            <li v-for="v in notYet" :key="v">{{ v }}</li>
          </ul>
        </div>
      </div>
      <p class="hx-trust__link">
        <a class="hx-btn hx-btn--outline" :href="withBase('/acceptance#what-these-results-do-not-show')">Read the full acceptance report <span aria-hidden="true">&rarr;</span></a>
      </p>
    </div>
  </section>
</template>
