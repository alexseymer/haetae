import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'
import { githubLinks } from './links'
import { pageMeta, site } from './meta'

const base = '/haetae/'
// Pages with wide tables get a wider content column.
const wideDocs = new Set(['acceptance.md', 'benchmarks-m7.md', 'roadmap.md', 'architecture.md'])

export default withMermaid(
  defineConfig({
    title: site.name,
    titleTemplate: ':title | Haetae',
    description: site.description,
    lang: 'en-US',
    base,
    cleanUrls: true,
    lastUpdated: true,
    srcExclude: ['**/node_modules/**'],

    sitemap: { hostname: site.url },

    head: [
      ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}favicon.svg` }],
      ['meta', { name: 'theme-color', content: '#e9fbf2', media: '(prefers-color-scheme: light)' }],
      ['meta', { name: 'theme-color', content: '#07120e', media: '(prefers-color-scheme: dark)' }],
      ['meta', { name: 'author', content: 'Haetae contributors' }],
      ['meta', { property: 'og:type', content: 'website' }],
      ['meta', { property: 'og:site_name', content: site.name }],
      ['meta', { property: 'og:image', content: `${site.url}og.png` }],
      ['meta', { property: 'og:image:width', content: '1200' }],
      ['meta', { property: 'og:image:height', content: '630' }],
      ['meta', { property: 'og:image:alt', content: 'Haetae: an AI operator for Docker Compose servers' }],
      ['meta', { name: 'twitter:card', content: 'summary_large_image' }],
      ['meta', { name: 'twitter:image', content: `${site.url}og.png` }]
    ],

    transformPageData(pageData) {
      const m = pageMeta[pageData.relativePath]
      if (m) {
        pageData.frontmatter.title ??= m.title
        pageData.frontmatter.description ??= m.description
        pageData.description = m.description
      }
      if (wideDocs.has(pageData.relativePath)) pageData.frontmatter.pageClass ??= 'hx-wide'
    },

    transformHead({ pageData, assets }) {
      const fm = pageData.frontmatter
      const isHome = pageData.relativePath === 'index.md'
      const title = isHome ? `${site.name}: ${site.tagline}` : `${fm.title ?? site.name} | ${site.name}`
      const description = (fm.description as string) ?? site.description
      const urlPath = pageData.relativePath.replace(/(^|\/)index\.md$/, '$1').replace(/\.md$/, '')
      const url = site.url + urlPath
      const preload = assets
        .filter((a) => /(inter-latin-(400|600)|jetbrains-mono-latin-(700|800))-normal[^/]*\.woff2$/.test(a))
        .map((href) => ['link', { rel: 'preload', as: 'font', type: 'font/woff2', href, crossorigin: '' }] as [string, Record<string, string>])
      return [
        ...preload,
        ['link', { rel: 'canonical', href: url }],
        ['meta', { property: 'og:title', content: title }],
        ['meta', { property: 'og:description', content: description }],
        ['meta', { property: 'og:url', content: url }],
        ['meta', { name: 'twitter:title', content: title }],
        ['meta', { name: 'twitter:description', content: description }]
      ]
    },

    markdown: {
      config(md) {
        md.use(githubLinks)
      }
    },

    themeConfig: {
      logo: { src: '/logo.svg', alt: 'Haetae' },
      siteTitle: 'Haetae',

      nav: [
        { text: 'Home', link: '/' },
        {
          text: 'Get started',
          items: [
            { text: 'Install the server', link: '/install' },
            { text: 'Enroll a host', link: '/enroll' },
            { text: 'Deploy an app', link: '/deploy' }
          ]
        },
        {
          text: 'Guides',
          items: [
            { text: 'Chat and alerts', link: '/chat' },
            { text: 'AI operator', link: '/ai-operator' },
            { text: 'Security scanning', link: '/security' },
            { text: 'Troubleshooting', link: '/troubleshooting' }
          ]
        },
        {
          text: 'Reference',
          items: [
            { text: 'Architecture', link: '/architecture' },
            { text: 'Specification', link: '/spec' },
            { text: 'Roadmap', link: '/roadmap' },
            { text: 'Acceptance report', link: '/acceptance' },
            { text: 'Benchmarks', link: '/benchmarks-m7' },
            { text: 'Release', link: '/release' },
            { text: 'Security UI notes', link: '/security-ui' },
            { text: 'ADR 0001: Stack', link: '/adr/0001-stack' }
          ]
        }
      ],

      sidebar: [
        {
          text: 'Get started',
          items: [
            { text: 'Install the server', link: '/install' },
            { text: 'Enroll a host', link: '/enroll' },
            { text: 'Deploy an app', link: '/deploy' }
          ]
        },
        {
          text: 'Guides',
          items: [
            { text: 'Chat and alerts', link: '/chat' },
            { text: 'AI operator', link: '/ai-operator' },
            { text: 'Security scanning', link: '/security' },
            { text: 'Troubleshooting', link: '/troubleshooting' }
          ]
        },
        {
          text: 'Reference',
          items: [
            { text: 'Documentation index', link: '/README' },
            { text: 'Architecture', link: '/architecture' },
            { text: 'Specification', link: '/spec' },
            { text: 'Roadmap', link: '/roadmap' },
            { text: 'Acceptance report', link: '/acceptance' },
            { text: 'Benchmarks', link: '/benchmarks-m7' },
            { text: 'Release', link: '/release' },
            { text: 'Security UI notes', link: '/security-ui' },
            { text: 'Self-hosting (moved)', link: '/self-hosting' }
          ]
        },
        {
          text: 'Decisions and design',
          collapsed: false,
          items: [
            { text: 'ADR 0001: Stack', link: '/adr/0001-stack' },
            { text: 'M5 deploys', link: '/design/m5-deploys' },
            { text: 'M6 alerts and chat', link: '/design/m6-alerts-chat' },
            { text: 'M7 security scanning', link: '/design/m7-security' },
            { text: 'M8 AI operator', link: '/design/m8-operator' }
          ]
        }
      ],

      socialLinks: [{ icon: 'github', link: site.repo, ariaLabel: 'Haetae on GitHub' }],

      editLink: {
        pattern: `${site.repo}/edit/main/docs/:path`,
        text: 'Edit this page on GitHub'
      },

      lastUpdated: { text: 'Last updated', formatOptions: { dateStyle: 'medium' } },

      search: { provider: 'local', options: { detailedView: true } },

      outline: { level: [2, 3], label: 'On this page' },

      docFooter: { prev: 'Previous', next: 'Next' },

      footer: {
        message: 'Released under the AGPL-3.0 license.',
        copyright: 'Haetae is a working name, pending trademark clearance.'
      }
    },

    mermaid: {
      fontFamily: 'Inter, ui-sans-serif, system-ui, sans-serif',
      theme: 'base',
      flowchart: { curve: 'basis', htmlLabels: true, useMaxWidth: false, padding: 12 },
      sequence: { useMaxWidth: false },
      state: { useMaxWidth: false }
    }
  })
)
