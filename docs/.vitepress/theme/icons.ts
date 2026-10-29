// Brand marks come from simple-icons (CC0), imported one by one so they are inlined at build time.
// Marks simple-icons does not have are drawn here as neutral stroke glyphs.
import { siDocker, siPostgresql, siCaddy, siGithub, siTelegram, siSlack, siSignal } from 'simple-icons'

export type IconDef = {
  label: string
  /** simple-icons mark: one filled path in a 24x24 box */
  path?: string
  /** neutral glyph: stroked inner SVG markup in a 24x24 box */
  glyph?: string
  /** brand colour used on hover (hex without #) */
  hex: string
}

const fromSi = (i: { title: string; path: string; hex: string }, label = i.title): IconDef => ({
  label,
  path: i.path,
  hex: i.hex
})

export const icons = {
  docker: fromSi(siDocker),
  compose: {
    label: 'Docker Compose',
    hex: '2496ED',
    glyph:
      '<path d="M12 3 3 7.5l9 4.5 9-4.5L12 3Z"/><path d="m3 12 9 4.5 9-4.5"/><path d="m3 16.5 9 4.5 9-4.5"/>'
  },
  postgres: fromSi(siPostgresql, 'PostgreSQL'),
  caddy: fromSi(siCaddy),
  github: fromSi(siGithub, 'GitHub'),
  telegram: fromSi(siTelegram),
  slack: fromSi(siSlack),
  signal: fromSi(siSignal),
  email: {
    label: 'Email',
    hex: '2350e6',
    glyph: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m3.5 7 8.5 6.5L20.5 7"/>'
  },
  web: {
    label: 'Web',
    hex: '2350e6',
    glyph: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18"/><path d="M6.5 6.5h.01M9 6.5h.01"/>'
  },
  ai: {
    label: 'AI provider',
    hex: '2350e6',
    glyph:
      '<path d="M11 3.5 12.9 9l5.6 1.9-5.6 1.9L11 18.3l-1.9-5.5-5.6-1.9L9.1 9 11 3.5Z"/><path d="M18.5 15.5v4M16.5 17.5h4"/>'
  }
} satisfies Record<string, IconDef>

export type IconName = keyof typeof icons
