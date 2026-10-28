/**
 * Test-only guard: a rendered dialog must never need a scrollbar.
 *
 * jsdom does no layout, so scrollHeight/clientHeight are 0 there and this is a
 * no-op unless a test stubs them (see dialogGuard.test.ts). The real check runs
 * in a browser: test/e2e/ui.spec.mjs opens every dialog at 1280x720 and 390x844.
 */
export function dialogOverflows(root: ParentNode = document): string[] {
  const problems: string[] = [];
  const els = root.querySelectorAll<HTMLElement>('[role="dialog"], [role="dialog"] .modal-body');
  els.forEach((el) => {
    const label = el.getAttribute("aria-labelledby") ? "dialog" : el.className || el.tagName;
    if (el.scrollHeight > el.clientHeight + 1) {
      problems.push(`${label}: scrollHeight ${el.scrollHeight} > clientHeight ${el.clientHeight}`);
    }
    if (el.scrollWidth > el.clientWidth + 1) {
      problems.push(`${label}: scrollWidth ${el.scrollWidth} > clientWidth ${el.clientWidth}`);
    }
  });
  return problems;
}

export function expectNoDialogScroll(root: ParentNode = document): void {
  const problems = dialogOverflows(root);
  if (problems.length > 0) {
    throw new Error(`dialog needs a scrollbar: ${problems.join("; ")}`);
  }
}
