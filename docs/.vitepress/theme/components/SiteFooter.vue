<script setup lang="ts">
import { withBase } from 'vitepress'
const repo = 'https://github.com/alexseymer/haetae'
</script>

<template>
  <footer class="hx-footer">
    <div class="hx-wrap hx-footer__grid">
      <div class="hx-footer__brand">
        <a :href="withBase('/')" class="hx-footer__logo"><img :src="withBase('/logo.svg')" alt="" width="28" height="28" />Haetae</a>
        <p>A self-hosted AI operator for Docker Compose servers.</p>
        <p class="hx-footer__small">Licensed AGPL-3.0-only. Haetae is a working name, pending trademark clearance.</p>
      </div>
      <nav aria-label="Documentation">
        <h2>Docs</h2>
        <ul>
          <li><a :href="withBase('/install')">Install the server</a></li>
          <li><a :href="withBase('/enroll')">Enroll a host</a></li>
          <li><a :href="withBase('/deploy')">Deploy an app</a></li>
          <li><a :href="withBase('/acceptance')">Acceptance report</a></li>
        </ul>
      </nav>
      <nav aria-label="Project">
        <h2>Project</h2>
        <ul>
          <li><a :href="repo">GitHub</a></li>
          <li><a :href="`${repo}/blob/main/SECURITY.md`">Security policy</a></li>
          <li><a :href="`${repo}/blob/main/CONTRIBUTING.md`">Contributing</a></li>
          <li><a :href="`${repo}/blob/main/LICENSE`">License (AGPL-3.0)</a></li>
        </ul>
      </nav>
    </div>
  </footer>
</template>
