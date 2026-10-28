import DefaultTheme from 'vitepress/theme-without-fonts'
import type { Theme } from 'vitepress'

// Self-hosted fonts (no requests to third parties at runtime).
import '@fontsource/inter/latin-400.css'
import '@fontsource/inter/latin-500.css'
import '@fontsource/inter/latin-600.css'
import '@fontsource/inter/latin-700.css'
import '@fontsource/jetbrains-mono/latin-400.css'
import '@fontsource/jetbrains-mono/latin-500.css'
import '@fontsource/jetbrains-mono/latin-700.css'
import '@fontsource/jetbrains-mono/latin-800.css'

import './style.css'
import './home.css'

import Layout from './Layout.vue'

export default {
  extends: DefaultTheme,
  Layout
} satisfies Theme
