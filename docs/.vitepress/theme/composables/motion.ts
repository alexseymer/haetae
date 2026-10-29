import type { Directive } from 'vue'

export const prefersReducedMotion = () =>
  typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches

let io: IntersectionObserver | undefined
function observer() {
  if (!io) {
    io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            e.target.classList.add('is-in')
            io!.unobserve(e.target)
          }
        }
      },
      { rootMargin: '0px 0px -8% 0px', threshold: 0.08 }
    )
  }
  return io
}

/**
 * v-reveal="index": fade and rise when scrolled into view, staggered by index.
 * Elements already in the first viewport are left alone (no flash), and nothing is hidden
 * without JS, with an unsupported browser or under prefers-reduced-motion.
 */
export const vReveal: Directive<HTMLElement, number | undefined> = {
  mounted(el, { value }) {
    if (prefersReducedMotion() || typeof IntersectionObserver === 'undefined') return
    if (el.getBoundingClientRect().top < window.innerHeight * 0.92) return
    el.style.setProperty('--hx-d', `${Math.min(value ?? 0, 8) * 70}ms`)
    el.classList.add('hx-reveal')
    observer().observe(el)
  },
  unmounted(el) {
    io?.unobserve(el)
  }
}

/** Call fn once when el first becomes visible. */
export function onceVisible(el: Element, fn: () => void, threshold = 0.35) {
  if (typeof IntersectionObserver === 'undefined') return fn()
  const o = new IntersectionObserver(
    (es) => {
      if (es.some((e) => e.isIntersecting)) {
        o.disconnect()
        fn()
      }
    },
    { threshold }
  )
  o.observe(el)
  return () => o.disconnect()
}

/** rAF-throttled pointermove handler factory. */
export function rafThrottle<T extends (e: PointerEvent) => void>(fn: T) {
  let raf = 0
  let last: PointerEvent | undefined
  return (e: PointerEvent) => {
    last = e
    if (raf) return
    raf = requestAnimationFrame(() => {
      raf = 0
      if (last) fn(last)
    })
  }
}
