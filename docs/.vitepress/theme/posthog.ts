const TOKEN = 'phc_vtDCjpmCpyvqumXPRm82xEVTrYiTsootwKpXtea4duY9'
const API_HOST = 'https://eu.i.posthog.com'
const UI_HOST = 'https://eu.posthog.com'

declare global {
  interface Window {
    posthog?: {
      init: (token: string, opts: Record<string, unknown>) => void
      capture: (event: string, props?: Record<string, unknown>) => void
    }
  }
}

let booted = false

/** Load official PostHog EU snippet once; capture SPA navigations via history. */
export function initPostHog(): void {
  if (booted || typeof window === 'undefined' || !TOKEN) return
  booted = true

  const s = document.createElement('script')
  s.async = true
  s.crossOrigin = 'anonymous'
  s.src = API_HOST.replace('.i.posthog.com', '-assets.i.posthog.com') + '/static/array.js'
  s.onload = () => {
    window.posthog?.init(TOKEN, {
      api_host: API_HOST,
      ui_host: UI_HOST,
      person_profiles: 'identified_only',
      capture_pageview: 'history_change',
      capture_pageleave: true,
    })
  }
  document.head.appendChild(s)
}
