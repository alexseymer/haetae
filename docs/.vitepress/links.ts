import { existsSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import type MarkdownIt from 'markdown-it'

const REPO = 'https://github.com/alexseymer/haetae'
// docs/.vitepress/links.ts -> repository root
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')

/**
 * The Markdown keeps relative links so it works on GitHub. In the built site,
 * links that leave docs/ (../README.md, ../examples/hello, ../deploy/...) and
 * links to directories become absolute GitHub URLs. A target that does not
 * exist in the repository fails the build.
 */
export function githubLinks(md: MarkdownIt) {
  md.core.ruler.push('haetae_github_links', (state) => {
    const rel: string | undefined = state.env?.relativePath
    if (!rel) return
    const dir = path.posix.dirname(rel)
    for (const block of state.tokens) {
      if (block.type !== 'inline' || !block.children) continue
      for (const tok of block.children) {
        if (tok.type !== 'link_open') continue
        const href = tok.attrGet('href')
        if (!href || /^([a-z][a-z0-9+.-]*:|\/\/|#|\/)/i.test(href)) continue
        const m = /^([^?#]*)([?#].*)?$/.exec(href)!
        const target = m[1]
        const suffix = m[2] ?? ''
        if (!target) continue
        const repoPath = path.posix.normalize(path.posix.join('docs', dir, target))
        const abs = path.join(repoRoot, repoPath)
        const outside = !repoPath.startsWith('docs/')
        const isDir = target.endsWith('/') || (existsSync(abs) && statSync(abs).isDirectory())
        // In-docs pages are handled by VitePress (and dead-link checked).
        if (!outside && !isDir) continue
        if (!existsSync(abs)) {
          throw new Error(`docs/${rel}: link "${href}" points to ${repoPath}, which does not exist`)
        }
        const kind = isDir ? 'tree' : 'blob'
        const clean = repoPath.replace(/\/$/, '')
        tok.attrSet('href', `${REPO}/${kind}/main/${clean}${isDir ? '' : suffix}`)
      }
    }
  })
}
