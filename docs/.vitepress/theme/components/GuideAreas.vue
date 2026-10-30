<script setup lang="ts">
import { withBase } from 'vitepress'

const areas = [
  {
    title: 'Getting started',
    text: 'Install the server, sign in, enroll your first host, deploy an app, connect a chat channel and turn on the AI operator.',
    link: '/getting-started/',
    cta: 'Start here',
    paths: ['M12 3v12', 'm7 8 5-5 5 5', 'M4 15v4a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-4']
  },
  {
    title: 'Guides',
    text: 'Deploy from Git, put an app on a domain with HTTPS, roll back, read alerts and incidents, ask the operator, summarise logs, watch vulnerabilities and use chat commands.',
    link: '/guides/',
    cta: 'Browse the guides',
    paths: ['M4 6h16', 'M4 12h16', 'M4 18h10']
  },
  {
    title: 'Concepts',
    text: 'Hosts, agents, containers, incidents, the three autonomy levels, evidence, redaction and why log text is never trusted, in plain words.',
    link: '/concepts/',
    cta: 'Read the concepts',
    paths: ['M12 3 4 6v6c0 4.5 3.2 8 8 9 4.8-1 8-4.5 8-9V6l-8-3Z', 'm9 12 2 2 4-4']
  },
  {
    title: 'Problems and what to do',
    text: 'A host shows offline, a deploy fails, a scan fails, the operator says "cause not found" or "unavailable", chat is silent, memory is high.',
    link: '/problems/',
    cta: 'Find your problem',
    paths: ['M21 12a8 8 0 0 1-11.6 7.1L4 20l1-4.6A8 8 0 1 1 21 12Z']
  }
]
</script>

<template>
  <section class="hx-section hx-section--alt" aria-labelledby="hx-areas-title">
    <div class="hx-wrap">
      <p v-reveal class="hx-kicker">Documentation</p>
      <h2 id="hx-areas-title" v-reveal="1" class="hx-h2">Find your way around</h2>
      <p class="hx-lede">
        The examples throughout use a small shop: an api, a web frontend and a database on one server called
        hel1-prod.
      </p>
      <ul class="hx-grid">
        <li v-for="(a, i) in areas" :key="a.title" v-reveal="i % 3">
          <a class="hx-card" :href="withBase(a.link)">
            <svg class="hx-card__icon" viewBox="0 0 24 24" width="26" height="26" aria-hidden="true">
              <path v-for="d in a.paths" :key="d" :d="d" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <h3>{{ a.title }}</h3>
            <p>{{ a.text }}</p>
            <span class="hx-card__cta">{{ a.cta }} <span aria-hidden="true">&rarr;</span></span>
          </a>
        </li>
      </ul>
      <p class="hx-trust__link">
        <a class="hx-btn hx-btn--outline" :href="withBase('/tested')">What is tested and what is not <span aria-hidden="true">&rarr;</span></a>
      </p>
    </div>
  </section>
</template>
