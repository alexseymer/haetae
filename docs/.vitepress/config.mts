import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'
import { githubLinks } from './links'
import { pageMeta, site } from './meta'

const base = '/haetae/'

export default withMermaid(
  defineConfig({
    title: site.name,
    titleTemplate: ':title | Haetae',
    description: site.description,
    lang: 'en-US',
    base,
    cleanUrls: true,
    lastUpdated: true,
    // Contributor material stays in the repository, readable on GitHub, but is not part of the site.
    srcExclude: ['**/node_modules/**', 'README.md', 'contributors/**'],

    sitemap: { hostname: site.url },

    head: [
      ['link', { rel: 'icon', type: 'image/svg+xml', href: `${base}favicon.svg` }],
      ['meta', { name: 'theme-color', content: '#eef3ff', media: '(prefers-color-scheme: light)' }],
      ['meta', { name: 'theme-color', content: '#071226', media: '(prefers-color-scheme: dark)' }],
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
        { text: 'Getting started', link: '/getting-started/', activeMatch: '^/getting-started/' },
        { text: 'Guides', link: '/guides/', activeMatch: '^/guides/' },
        { text: 'Concepts', link: '/concepts/', activeMatch: '^/concepts/' },
        { text: 'Problems', link: '/problems/', activeMatch: '^/problems/' },
        { text: 'What is tested', link: '/tested' }
      ],

      sidebar: [
        {
          text: 'Getting started',
          items: [
            { text: 'Overview', link: '/getting-started/' },
            { text: 'Install the server', link: '/getting-started/install-the-server' },
            { text: 'First login', link: '/getting-started/first-login' },
            { text: 'Enroll a host', link: '/getting-started/enroll-a-host' },
            { text: 'Deploy an app', link: '/getting-started/deploy-an-app' },
            { text: 'Connect a chat channel', link: '/getting-started/connect-chat' },
            { text: 'Turn on the AI operator', link: '/getting-started/turn-on-the-operator' }
          ]
        },
        {
          text: 'Guides',
          items: [
            { text: 'Overview', link: '/guides/' },
            { text: 'Deploy from Git', link: '/guides/deploy-from-git' },
            { text: 'A domain with HTTPS', link: '/guides/domain-and-https' },
            { text: 'Rolling back', link: '/guides/rolling-back' },
            { text: 'Alerts and incidents', link: '/guides/alerts-and-incidents' },
            { text: 'Asking the operator', link: '/guides/asking-the-operator' },
            { text: 'Summarising logs', link: '/guides/summarising-logs' },
            { text: 'Watching vulnerabilities', link: '/guides/watching-vulnerabilities' },
            { text: 'Chat commands', link: '/guides/chat-commands' }
          ]
        },
        {
          text: 'Concepts',
          items: [{ text: 'Concepts in plain words', link: '/concepts/' }]
        },
        {
          text: 'Problems',
          items: [{ text: 'Problems and what to do', link: '/problems/' }]
        },
        {
          text: 'More',
          items: [
            { text: 'What is tested and what is not', link: '/tested' },
            { text: 'Environment variables', link: '/reference/environment-variables' }
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
