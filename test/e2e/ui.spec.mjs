/**
 * Browser acceptance tests against a live haetae-server (driven by e2e_test.go).
 *
 * Playwright import: Node 20 ESM ignores NODE_PATH for bare specifiers. The Go
 * harness symlinks `test/e2e/node_modules/playwright` to the global install from
 * `npm root -g` (also sets NODE_PATH). With that symlink, this bare import works:
 *   import { chromium } from 'playwright'
 *
 * Browser: do not run `playwright install`. Launch uses the system Chromium at
 * E2E_CHROMIUM (default /usr/bin/chromium). HTTPS: context ignoreHTTPSErrors
 * (server uses its own CA); never --ignore-certificate-errors.
 */
import { describe, it, before, after } from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { chromium } from "playwright";

/** Same CSP shape as internal/webui (style-src/script-src 'self', no unsafe-inline). */
const APP_CSP =
  "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'";

const WEB = process.env.E2E_WEB_URL;
const SETUP_CODE_FILE = process.env.E2E_SETUP_CODE_FILE;
const AGENT_BIN = process.env.E2E_AGENT_BIN;
const AGENT_STATE_DIR = process.env.E2E_AGENT_STATE_DIR;
const SHOT_DIR = process.env.E2E_SHOT_DIR;
const CHROMIUM = process.env.E2E_CHROMIUM || "/usr/bin/chromium";

const ADMIN_EMAIL = "admin@example.com";
const ADMIN_PASSWORD = "a long test passphrase";
const ADMIN_PASSWORD_NEW = "a brand new passphrase";
const MEMBER_EMAIL = "member@example.com";
const MEMBER_PASSWORD = "another long passphrase";
const ORG_NAME = "E2E Org";

/** @type {import('playwright').Browser} */
let browser;
/** @type {import('playwright').BrowserContext} */
let context;
/** @type {import('playwright').Page} */
let page;

/** @type {string[]} */
const consoleErrors = [];
/** @type {string[]} */
const pageErrors = [];
/** @type {string[]} */
const cspViolations = [];

/** @type {string} */
let enrollCommand = "";
/** @type {string} */
let hostId = "";
/** @type {import('node:child_process').ChildProcess | null} */
let agentProc = null;

function requireEnv() {
  for (const [k, v] of Object.entries({
    E2E_WEB_URL: WEB,
    E2E_SETUP_CODE_FILE: SETUP_CODE_FILE,
    E2E_AGENT_BIN: AGENT_BIN,
    E2E_AGENT_STATE_DIR: AGENT_STATE_DIR,
    E2E_SHOT_DIR: SHOT_DIR,
  })) {
    if (!v) throw new Error(`missing ${k}`);
  }
}

/**
 * Poll until fn completes without throwing (expect.poll-style).
 * @param {string} message
 * @param {number} timeoutMs
 * @param {() => Promise<void> | void} fn
 */
async function eventuallyOk(message, timeoutMs, fn) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      await fn();
      return;
    } catch (e) {
      lastErr = e;
    }
    await sleep(Math.min(1000, 200));
  }
  let where = "";
  try {
    const pg = globalThis.__e2ePage;
    if (pg) where = ` [page ${pg.url()}: ${(await pg.locator("body").innerText()).replace(/\s+/g, " ").slice(0, 400)}]`;
  } catch {
    // diagnostics only
  }
  throw new Error(`timed out waiting for ${message}: ${lastErr}${where}`);
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function assertCleanPage(label) {
  assert.deepEqual(
    [...consoleErrors],
    [],
    `${label}: console errors: ${consoleErrors.join(" | ")}`,
  );
  assert.deepEqual(
    [...pageErrors],
    [],
    `${label}: page errors: ${pageErrors.join(" | ")}`,
  );
  assert.deepEqual(
    [...cspViolations],
    [],
    `${label}: CSP violations: ${cspViolations.join(" | ")}`,
  );
  consoleErrors.length = 0;
  pageErrors.length = 0;
  cspViolations.length = 0;
}

function isExpectedResourceConsoleError(text) {
  // Chromium logs failed fetch/XHR as console errors. Intentional wrong
  // credentials, setup codes, and duplicate creates are 4xx API responses.
  return /Failed to load resource: the server responded with a status of (401|403|404|409|429)\b/.test(
    text,
  );
}

function attachNoiseListeners(p) {
  p.on("console", (msg) => {
    if (msg.type() === "error") {
      const text = msg.text();
      if (isExpectedResourceConsoleError(text)) return;
      consoleErrors.push(text);
    }
  });
  p.on("pageerror", (err) => {
    pageErrors.push(String(err));
  });
}

/**
 * Prove React CSSOM style props take effect under the real app CSP.
 * @param {string} label
 * @param {"hosts"|"host-overview"} kind
 */
async function assertStylesUnderCsp(label, kind) {
  if (kind === "host-overview") {
    const chart = page.locator(".chart").first();
    await chart.locator("svg").hover({ position: { x: 220, y: 90 } });
    await eventuallyOk("chart tooltip visible", 5_000, async () => {
      assert.ok((await page.locator(".chart-tooltip").count()) >= 1);
    });
  }
  const evidence = await page.evaluate((mode) => {
    const styleAttrCount = document.querySelectorAll("[style]").length;
    const tooltip = document.querySelector(".chart-tooltip");
    const swatches = [...document.querySelectorAll(".chart-swatch")];
    const headerRow = document.querySelector(".page > .row");
    return {
      styleAttrCount,
      tooltipPosition: tooltip ? getComputedStyle(tooltip).position : null,
      tooltipLeft: tooltip ? getComputedStyle(tooltip).left : null,
      swatchBackgrounds: swatches.map((el) => getComputedStyle(el).backgroundColor),
      headerJustify:
        mode === "hosts" && headerRow ? getComputedStyle(headerRow).justifyContent : null,
    };
  }, kind);
  console.log(`CSP style evidence (${label}):`, JSON.stringify(evidence));
  assert.ok(
    evidence.styleAttrCount > 0,
    `${label}: expected elements with style attributes from React CSSOM`,
  );
  if (kind === "hosts") {
    assert.equal(
      evidence.headerJustify,
      "space-between",
      `${label}: hosts header row justify-content should apply via CSSOM`,
    );
  }
  if (kind === "host-overview") {
    assert.equal(
      evidence.tooltipPosition,
      "absolute",
      `${label}: chart tooltip computed position`,
    );
    assert.ok(
      evidence.swatchBackgrounds.length > 0,
      `${label}: expected legend swatches`,
    );
    for (const bg of evidence.swatchBackgrounds) {
      assert.ok(
        bg && bg !== "rgba(0, 0, 0, 0)" && bg !== "transparent",
        `${label}: swatch background should be non-transparent, got ${bg}`,
      );
    }
  }
  return evidence;
}

/**
 * Install CSP violation reporting that survives navigations (Node-side array).
 * @param {import('playwright').BrowserContext} ctx
 * @param {string[]} [sink]
 */
async function installCspReporting(ctx, sink = cspViolations) {
  await ctx.exposeBinding("reportCspViolation", (_source, violation) => {
    sink.push(String(violation));
  });
  await ctx.addInitScript(() => {
    window.addEventListener("securitypolicyviolation", (e) => {
      // report immediately to Node so navigations cannot wipe the list
      window.reportCspViolation(
        `${e.violatedDirective}:${e.blockedURI || e.sourceFile || ""}`,
      );
    });
  });
}

async function signIn(email, password) {
  await page.goto(`${WEB}/login`);
  await eventuallyOk("login form", 10_000, async () => {
    await page.getByRole("heading", { name: "Sign in" }).waitFor({
      state: "visible",
      timeout: 2000,
    });
  });
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  const respPromise = page.waitForResponse(
    (r) => r.url().includes("/api/login") && r.request().method() === "POST",
    { timeout: 15_000 },
  );
  await page.getByRole("button", { name: "Sign in" }).click();
  const resp = await respPromise;
  assert.ok(resp.ok(), `login HTTP ${resp.status()}`);
  await eventuallyOk("hosts after sign in", 15_000, async () => {
    await page.getByRole("heading", { name: "Hosts" }).waitFor({ state: "visible", timeout: 2000 });
  });
}

/** Re-enter as admin if a prior scenario left us signed out. */
async function ensureAdminSession(password = ADMIN_PASSWORD) {
  // Host detail and settings also show the account control when signed in.
  const account = page.getByRole("button", { name: ADMIN_EMAIL });
  if ((await account.count()) > 0 && (await account.first().isVisible())) {
    return;
  }
  await signIn(ADMIN_EMAIL, password);
}

function parseEnrollArgs(command) {
  const token = command.match(/--token\s+'([^']+)'/)?.[1];
  const caPin = command.match(/--ca-pin\s+'([^']+)'/)?.[1];
  const server = command.match(/--server\s+'([^']+)'/)?.[1];
  if (!token || !caPin || !server) {
    throw new Error(`could not parse enroll args from command:\n${command}`);
  }
  return { token, caPin, server };
}

function killAgent() {
  if (!agentProc || agentProc.killed) return;
  try {
    process.kill(-agentProc.pid, "SIGKILL");
  } catch {
    try {
      agentProc.kill("SIGKILL");
    } catch {
      /* ignore */
    }
  }
  agentProc = null;
}

describe("haetae web e2e", { concurrency: 1 }, () => {
  before(async () => {
    requireEnv();
    browser = await chromium.launch({
      headless: true,
      executablePath: CHROMIUM,
    });
    context = await browser.newContext({
      ignoreHTTPSErrors: true,
      permissions: ["clipboard-read", "clipboard-write"],
      viewport: { width: 1280, height: 800 },
    });
    await installCspReporting(context);
    page = await context.newPage();
    globalThis.__e2ePage = page;
    attachNoiseListeners(page);
  });

  after(async () => {
    killAgent();
    try {
      await context?.close();
    } catch {
      /* ignore */
    }
    try {
      await browser?.close();
    } catch {
      /* ignore */
    }
  });

  it("0. negative control: harness records CSP violations for inline script/style", async () => {
    const html = `<!DOCTYPE html><html><head><title>csp-neg</title></head><body>
<div id="styled" style="color: red; background: blue">styled</div>
<script>window.__inlineRan = 1</script>
</body></html>`;
    const server = http.createServer((req, res) => {
      res.writeHead(200, {
        "Content-Type": "text/html; charset=utf-8",
        "Content-Security-Policy": APP_CSP,
      });
      res.end(html);
    });
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address();
    const url = `http://127.0.0.1:${port}/`;
    // Separate page so closing the local server cannot poison the main page's
    // console-error sink with ERR_CONNECTION_RESET.
    const negPage = await context.newPage();
    try {
      cspViolations.length = 0;
      await negPage.goto(url);
      await eventuallyOk("CSP violations recorded for negative control", 5_000, () => {
        assert.ok(
          cspViolations.length >= 1,
          `expected CSP violations, got ${JSON.stringify(cspViolations)}`,
        );
      });
      const joined = cspViolations.join(" | ");
      assert.match(joined, /script-src|style-src|inline/i);
      console.log("negative-control CSP violations:", cspViolations.slice());
      // Discard so later scenarios start clean.
      cspViolations.length = 0;
      consoleErrors.length = 0;
      pageErrors.length = 0;
    } finally {
      await negPage.close();
      await new Promise((resolve) => server.close(resolve));
    }
  });

  it("1. setup: redirect, wrong code, create admin, empty hosts", async () => {
    const code = fs.readFileSync(SETUP_CODE_FILE, "utf8").trim();
    await page.goto(WEB + "/");
    await eventuallyOk("redirect to /setup", 15_000, async () => {
      assert.match(page.url(), /\/setup/);
      await page.getByRole("heading", { name: "Set up Haetae" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });

    await page.getByLabel("Setup code").fill("wrong-code");
    await page.getByLabel("Organisation name").fill(ORG_NAME);
    await page.getByLabel("Email").fill(ADMIN_EMAIL);
    await page.getByLabel("Password", { exact: true }).fill(ADMIN_PASSWORD);
    await page.getByRole("button", { name: "Create organisation" }).click();
    await eventuallyOk("wrong setup code error", 10_000, async () => {
      const alert = page.getByRole("alert");
      await alert.waitFor({ state: "visible", timeout: 2000 });
      const text = await alert.innerText();
      assert.ok(text.length > 0, "expected setup error text");
    });

    await page.getByLabel("Setup code").fill(code);
    await page.getByRole("button", { name: "Create organisation" }).click();
    await eventuallyOk("hosts empty state", 45_000, async () => {
      await page.getByRole("heading", { name: "Hosts" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
      await page.getByText(/No hosts yet/i).waitFor({ state: "visible", timeout: 2000 });
      await page.getByText(/Add host/i).first().waitFor({ state: "visible", timeout: 2000 });
    });
    assertCleanPage("1.setup");
  });

  it("2. sign out, wrong password, sign in", async () => {
    await page.getByRole("button", { name: ADMIN_EMAIL }).click();
    await page.getByRole("menuitem", { name: "Sign out" }).waitFor({ state: "visible" });
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await eventuallyOk("login page", 15_000, async () => {
      assert.match(page.url(), /\/login/);
      await page.getByRole("heading", { name: "Sign in" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });

    await page.getByLabel("Email").fill(ADMIN_EMAIL);
    await page.getByLabel("Password", { exact: true }).fill("definitely the wrong password");
    const badLogin = page.waitForResponse(
      (r) => r.url().includes("/api/login") && r.request().method() === "POST",
      { timeout: 15_000 },
    );
    await page.getByRole("button", { name: "Sign in" }).click();
    const badResp = await badLogin;
    assert.equal(badResp.status(), 401);
    await eventuallyOk("wrong password alert", 10_000, async () => {
      const alert = page.getByRole("alert");
      await alert.waitFor({ state: "visible", timeout: 2000 });
      assert.match(await alert.innerText(), /invalid email or password/i);
    });

    await page.getByLabel("Password", { exact: true }).fill(ADMIN_PASSWORD);
    const goodLogin = page.waitForResponse(
      (r) => r.url().includes("/api/login") && r.request().method() === "POST",
      { timeout: 15_000 },
    );
    await page.getByRole("button", { name: "Sign in" }).click();
    assert.ok((await goodLogin).ok());
    await eventuallyOk("hosts after good login", 15_000, async () => {
      await page.getByRole("heading", { name: "Hosts" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });
    assertCleanPage("2.login");
  });

  it("3. Add host dialog: Esc, create, copy, token not reshown", async () => {
    await ensureAdminSession();
    const addBtn = page.getByRole("button", { name: "Add host" });
    await addBtn.focus();
    await addBtn.click();
    await page.getByRole("dialog", { name: "Add host" }).waitFor({ state: "visible" });
    await page.keyboard.press("Escape");
    await eventuallyOk("dialog closed and focus on Add host", 5_000, async () => {
      assert.equal(await page.getByRole("dialog").count(), 0);
      const active = await page.evaluate(() => document.activeElement?.textContent?.trim());
      assert.equal(active, "Add host");
    });

    await addBtn.click();
    await page.getByLabel("Host name").fill("e2e-host");
    await page.getByRole("button", { name: "Get install command" }).click();
    await eventuallyOk("enrollment command shown", 10_000, async () => {
      const pre = page.locator(".copy-block pre");
      await pre.waitFor({ state: "visible", timeout: 2000 });
      enrollCommand = await pre.innerText();
      assert.match(enrollCommand, /--token/);
      assert.match(enrollCommand, /--ca-pin/);
    });

    // Step 2 shows a live waiting state until the host connects.
    await page
      .getByRole("status")
      .filter({ hasText: /Waiting for the host to connect/ })
      .waitFor({ state: "visible", timeout: 5000 });
    await page.getByRole("button", { name: "Copy" }).click();
    await eventuallyOk("clipboard has command", 5_000, async () => {
      const clip = await page.evaluate(() => navigator.clipboard.readText());
      assert.ok(clip.includes("--token"), "clipboard missing --token");
      assert.ok(clip.includes("--ca-pin"), "clipboard missing --ca-pin");
    });

    await page.getByRole("button", { name: "Done" }).click();
    await eventuallyOk("dialog closed after Done", 5_000, async () => {
      assert.equal(await page.getByRole("dialog").count(), 0);
    });

    // Token must not reappear after close (form, not command view).
    await addBtn.click();
    await page.getByRole("dialog", { name: "Add host" }).waitFor({ state: "visible" });
    assert.equal(await page.locator(".copy-block pre").count(), 0, "token command reshown");
    await page.keyboard.press("Escape");
    await eventuallyOk("dialog closed", 5_000, async () => {
      assert.equal(await page.getByRole("dialog").count(), 0);
    });
    // Duplicate-name 409 is asserted in scenario 4 after the host exists:
    // enrollment tokens alone do not create a host row (API lists hosts only).
    assertCleanPage("3.add-host");
  });

  it("4. enroll real agent; host Online with containers and OS", async () => {
    await ensureAdminSession();
    assert.ok(enrollCommand, "scenario 3 must have captured the enroll command");
    const { token, caPin, server } = parseEnrollArgs(enrollCommand);
    const enroll = spawn(
      AGENT_BIN,
      ["enroll", "--server", server, "--token", token, "--ca-pin", caPin, "--state-dir", AGENT_STATE_DIR],
      { stdio: ["ignore", "pipe", "pipe"] },
    );
    let enrollOut = "";
    enroll.stdout.on("data", (d) => (enrollOut += d));
    enroll.stderr.on("data", (d) => (enrollOut += d));
    const enrollCode = await new Promise((resolve) => enroll.on("close", resolve));
    assert.equal(enrollCode, 0, `enroll failed: ${enrollOut}`);

    // Stay in the node process group (no detached) so Go harness kill(-pgid) reaps agent.
    agentProc = spawn(AGENT_BIN, ["run", "--state-dir", AGENT_STATE_DIR], {
      stdio: ["ignore", "pipe", "pipe"],
    });

    // Stay on hosts page; polling should flip the row to Online without reload.
    await eventuallyOk("host Online without reload", 30_000, async () => {
      const row = page.locator("table.data tbody tr", { hasText: "e2e-host" });
      await row.waitFor({ state: "visible", timeout: 2000 });
      const text = await row.innerText();
      assert.match(text, /Online/);
      assert.match(text, /\d+\/\d+/); // running/total
      assert.match(text, /linux/i);
      const href = await row.getByRole("link", { name: "e2e-host", exact: true }).getAttribute("href");
      assert.ok(href && href.startsWith("/hosts/"));
      hostId = href.replace("/hosts/", "");
    });

    await assertStylesUnderCsp("4.hosts", "hosts");

    // Duplicate name 409 only applies once the host row exists (not for pending tokens).
    const addBtn = page.getByRole("button", { name: "Add host" });
    await addBtn.click();
    await page.getByLabel("Host name").fill("e2e-host");
    await page.getByRole("button", { name: "Get install command" }).click();
    await eventuallyOk("duplicate name 409 message", 10_000, async () => {
      const alert = page.getByRole("alert");
      await alert.waitFor({ state: "visible", timeout: 2000 });
      assert.match(await alert.innerText(), /already exists/i);
    });
    await page.keyboard.press("Escape");
    await eventuallyOk("dialog closed after 409", 5_000, async () => {
      assert.equal(await page.getByRole("dialog").count(), 0);
    });
    assertCleanPage("4.enroll");
  });

  it("5. host overview charts, containers, logs", async () => {
    await ensureAdminSession();
    assert.ok(hostId, "scenario 4 must have set hostId");
    await page.goto(`${WEB}/hosts/${hostId}`);
    await eventuallyOk("host page heading", 15_000, async () => {
      await page.getByRole("heading", { name: "e2e-host" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });

    // Charts with data within 90s (agent samples every 10s → need ≥2 points).
    // Overview polls metrics; keep short locator timeouts so we can retry.
    await eventuallyOk("CPU and memory charts have multi-point paths", 90_000, async () => {
      const cpu = page.locator(".chart", { has: page.locator(".chart-title", { hasText: "CPU %" }) });
      const mem = page.locator(".chart", { has: page.locator(".chart-title", { hasText: "Memory" }) });
      for (const chart of [cpu, mem]) {
        const pathCount = await chart.locator("svg path").count();
        assert.ok(pathCount >= 1, "expected an SVG path");
        const pathD = await chart.locator("svg path").first().getAttribute("d", { timeout: 1000 });
        assert.ok(pathD && /L/i.test(pathD), `chart path needs >1 point: ${pathD}`);
        const rows = await chart.locator("table.sr-only tbody tr").count();
        assert.ok(rows >= 2, `accessible fallback table needs ≥2 rows, got ${rows}`);
      }
    });

    // Time-range selector changes from/to on requests.
    const metricReqs = [];
    const onReq = (req) => {
      const u = req.url();
      if (u.includes("/metrics?")) metricReqs.push(u);
    };
    page.on("request", onReq);
    await page.getByRole("button", { name: "6h" }).click();
    await eventuallyOk("metrics request with from/to after range change", 15_000, async () => {
      const hit = metricReqs.find((u) => /from=\d+/.test(u) && /to=\d+/.test(u));
      assert.ok(hit, "expected metrics URL with from/to");
      const from = Number(new URL(hit).searchParams.get("from"));
      const to = Number(new URL(hit).searchParams.get("to"));
      const span = to - from;
      assert.ok(span > 5 * 3600_000 && span < 7 * 3600_000, `expected ~6h span, got ${span}`);
    });
    page.off("request", onReq);

    await page.getByRole("tab", { name: "Containers" }).click();
    await eventuallyOk("busybox container listed running", 30_000, async () => {
      const body = await page.locator(".page").innerText();
      assert.match(body, /busybox|haetae-e2e/i);
      assert.match(body, /no stack|\(no stack\)/i);
      const badge = page.locator(".badge", { hasText: /^running$/i }).first();
      await badge.waitFor({ state: "visible", timeout: 2000 });
    });

    await page.locator("table.data tbody tr.clickable").first().click();
    await eventuallyOk("container panel with charts", 15_000, async () => {
      await page.locator(".panel").waitFor({ state: "visible", timeout: 2000 });
      assert.ok((await page.locator(".panel .chart").count()) >= 1);
    });

    await page.getByRole("tab", { name: "Logs" }).click();
    await eventuallyOk("error log event with count and sample", 60_000, async () => {
      // The agent flushes a new event every ~12 s, so several cards can match.
      const card = page
        .locator("li.card", {
          hasText: /payment failed for order 1234/,
        })
        .first();
      await card.waitFor({ state: "visible", timeout: 2000 });
      const text = await card.innerText();
      assert.match(text, /error/i);
      assert.match(text, /×\d+/);
      assert.match(text, /payment failed/);
      assert.ok(await card.locator(".badge", { hasText: /^error$/i }).count());
      assert.ok(await card.locator("pre.log-sample").count());
    });

    // Back to overview so charts (and React style props) are in the DOM under CSP.
    await page.getByRole("tab", { name: "Overview" }).click();
    await eventuallyOk("overview charts again", 30_000, async () => {
      const cpu = page.locator(".chart", { has: page.locator(".chart-title", { hasText: "CPU %" }) });
      assert.ok((await cpu.locator("svg path").count()) >= 1);
    });
    await assertStylesUnderCsp("5.host-overview", "host-overview");
    assertCleanPage("5.host");
  });

  it("6. XSS: hostile log is text only; __xss unset; no injected nodes", async () => {
    await page.getByRole("tab", { name: "Logs" }).click();
    await eventuallyOk("hostile log visible as text", 30_000, async () => {
      const sample = page.locator("pre.log-sample", {
        hasText: /window\.__xss/,
      }).first();
      await sample.waitFor({ state: "visible", timeout: 2000 });
      const html = await sample.evaluate((el) => el.innerHTML);
      const text = await sample.innerText();
      assert.match(text, /<script>/);
      assert.match(text, /<img /);
      assert.equal(await sample.locator("script").count(), 0);
      assert.equal(await sample.locator("img").count(), 0);
      assert.ok(!html.includes("<script>"), "raw script element in sample HTML");
      assert.ok(!/<img\b/i.test(html), "raw img element in sample HTML");
    });
    const xss = await page.evaluate(() => window.__xss);
    assert.equal(xss, undefined);
    assertCleanPage("6.xss");
  });

  it("7. settings password, add member, member permissions", async () => {
    await ensureAdminSession(ADMIN_PASSWORD);
    await page.goto(`${WEB}/settings`);
    await page.getByLabel("Current password").fill("wrong current pass here");
    await page.getByLabel("New password").fill(ADMIN_PASSWORD_NEW);
    await page.getByRole("button", { name: "Update password" }).click();
    await eventuallyOk("wrong current password rejected", 10_000, async () => {
      assert.match(await page.getByRole("alert").innerText(), /current password is wrong/i);
    });

    await page.getByLabel("Current password").fill(ADMIN_PASSWORD);
    await page.getByLabel("New password").fill(ADMIN_PASSWORD_NEW);
    await page.getByRole("button", { name: "Update password" }).click();
    await eventuallyOk("password updated", 10_000, async () => {
      await page.getByRole("status").filter({ hasText: /Password updated/i }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });

    await page.getByRole("button", { name: ADMIN_EMAIL }).click();
    await page.getByRole("menuitem", { name: "Sign out" }).waitFor({ state: "visible" });
    await page.getByRole("menuitem", { name: "Sign out" }).click();
    await eventuallyOk("login after password change", 15_000, async () => {
      assert.match(page.url(), /\/login/);
      await page.getByRole("heading", { name: "Sign in" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });
    await signIn(ADMIN_EMAIL, ADMIN_PASSWORD_NEW);

    await page.goto(`${WEB}/settings`);
    await page.locator("#new-user-email").fill(MEMBER_EMAIL);
    await page.locator("#new-user-password").fill(MEMBER_PASSWORD);
    await page.locator("#new-user-role").selectOption("member");
    await page.getByRole("button", { name: "Add user" }).click();
    await eventuallyOk("member listed", 15_000, async () => {
      await page.getByText(MEMBER_EMAIL).waitFor({ state: "visible", timeout: 2000 });
    });

    const memberCtx = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: { width: 1280, height: 800 },
    });
    try {
      const memberCsp = [];
      await installCspReporting(memberCtx, memberCsp);
      const memberPage = await memberCtx.newPage();
      const memberNoise = { console: [], page: [] };
      memberPage.on("console", (msg) => {
        if (msg.type() !== "error") return;
        const text = msg.text();
        if (isExpectedResourceConsoleError(text)) return;
        memberNoise.console.push(text);
      });
      memberPage.on("pageerror", (err) => memberNoise.page.push(String(err)));
      await memberPage.goto(`${WEB}/login`);
      await memberPage.getByLabel("Email").fill(MEMBER_EMAIL);
      await memberPage.getByLabel("Password", { exact: true }).fill(MEMBER_PASSWORD);
      await memberPage.getByRole("button", { name: "Sign in" }).click();
      await eventuallyOk("member hosts page", 15_000, async () => {
        await memberPage.getByRole("heading", { name: "Hosts" }).waitFor({
          state: "visible",
          timeout: 2000,
        });
      });
      assert.equal(await memberPage.getByRole("button", { name: "Add host" }).count(), 0);

      await memberPage.goto(`${WEB}/settings`);
      await eventuallyOk("settings without add-user", 10_000, async () => {
        await memberPage.getByRole("heading", { name: "Settings" }).waitFor({
          state: "visible",
          timeout: 2000,
        });
        assert.equal(await memberPage.getByRole("heading", { name: "Add user" }).count(), 0);
        assert.equal(await memberPage.locator("#new-user-email").count(), 0);
      });

      const getHost = await memberPage.request.get(`${WEB}/api/hosts/${hostId}`);
      assert.equal(getHost.status(), 200, await getHost.text());
      const postEnroll = await memberPage.request.post(`${WEB}/api/hosts/enrollment`, {
        data: { name: "member-should-fail", ttlHours: 1 },
        headers: { "X-Haetae-Request": "1" },
      });
      assert.equal(postEnroll.status(), 403, await postEnroll.text());
      assert.deepEqual(memberNoise.console, [], `member console: ${memberNoise.console}`);
      assert.deepEqual(memberNoise.page, [], `member pageerror: ${memberNoise.page}`);
    } finally {
      await memberCtx.close();
    }
    assertCleanPage("7.settings");
  });

  it("8. security headers, cookie flags, no token in storage", async () => {
    // Password was changed in scenario 7.
    await ensureAdminSession(ADMIN_PASSWORD_NEW);
    await page.goto(`${WEB}/`);
    await eventuallyOk("hosts for cookie check", 15_000, async () => {
      await page.getByRole("heading", { name: "Hosts" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });

    const resp = await page.goto(`${WEB}/`);
    assert.ok(resp);
    const csp = resp.headers()["content-security-policy"] || "";
    assert.match(csp, /script-src[^;]*'self'/);
    assert.doesNotMatch(csp, /unsafe-inline/);
    assert.ok(resp.headers()["strict-transport-security"], "HSTS missing");
    assert.equal(resp.headers()["x-frame-options"]?.toUpperCase(), "DENY");

    const cookies = await context.cookies();
    const session = cookies.find((c) => c.name === "haetae_session");
    assert.ok(session, "haetae_session cookie missing");
    assert.equal(session.httpOnly, true);
    assert.equal(session.secure, true);
    assert.equal(session.sameSite, "Strict");

    const storage = await page.evaluate(() => {
      const dump = (store) => {
        const out = {};
        for (let i = 0; i < store.length; i++) {
          const k = store.key(i);
          out[k] = store.getItem(k);
        }
        return out;
      };
      return { local: dump(localStorage), session: dump(sessionStorage) };
    });
    const blob = JSON.stringify(storage).toLowerCase();
    for (const needle of ["token", "jwt", "bearer", "haetae_session", "password"]) {
      assert.ok(!blob.includes(needle), `storage looks token-like: ${needle} in ${blob}`);
    }
    assertCleanPage("8.security");
  });

  it("9. responsive and accessibility smoke", async () => {
    await ensureAdminSession(ADMIN_PASSWORD_NEW);
    await page.setViewportSize({ width: 375, height: 800 });
    await page.goto(`${WEB}/`);
    await eventuallyOk("hosts at mobile width", 15_000, async () => {
      await page.getByRole("heading", { name: "Hosts" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });
    const scroll = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
    }));
    assert.ok(
      scroll.scrollWidth <= scroll.innerWidth + 1,
      `horizontal scroll: scrollWidth=${scroll.scrollWidth} innerWidth=${scroll.innerWidth}`,
    );

    await page.locator("body").click({ position: { x: 2, y: 2 } });
    let reachedNav = false;
    let reachedAdd = false;
    for (let i = 0; i < 30; i++) {
      await page.keyboard.press("Tab");
      const info = await page.evaluate(() => {
        const el = document.activeElement;
        if (!el) return { text: "", href: "", tag: "" };
        return {
          text: (el.textContent || "").trim(),
          href: el.getAttribute?.("href") || "",
          tag: el.tagName,
          aria: el.getAttribute?.("aria-label") || "",
        };
      });
      if (
        info.text === "Haetae" ||
        info.href === "/" ||
        /haetae/i.test(info.text)
      ) {
        reachedNav = true;
      }
      if (info.text === "Add host") reachedAdd = true;
      if (reachedNav && reachedAdd) break;
    }
    assert.ok(reachedNav, "Tab never reached main navigation (brand)");
    assert.ok(reachedAdd, "Tab never reached Add host button");

    // Open dialog so inputs exist, then check every input has an accessible name.
    await page.getByRole("button", { name: "Add host" }).click();
    await page.getByRole("dialog").waitFor({ state: "visible" });
    const unnamed = await page.evaluate(() => {
      const inputs = [...document.querySelectorAll("input")];
      return inputs
        .filter((el) => {
          const id = el.id;
          const byLabel = id && document.querySelector(`label[for="${CSS.escape(id)}"]`);
          const aria = el.getAttribute("aria-label") || el.getAttribute("aria-labelledby");
          const wrapped = el.closest("label");
          return !(byLabel || aria || wrapped);
        })
        .map((el) => el.outerHTML.slice(0, 120));
    });
    assert.deepEqual(unnamed, [], `inputs without accessible name: ${unnamed}`);
    await page.keyboard.press("Escape");
    await page.setViewportSize({ width: 1280, height: 800 });
    assertCleanPage("9.a11y");
  });

  it("10. screenshots light and dark", async () => {
    fs.mkdirSync(SHOT_DIR, { recursive: true });
    await ensureAdminSession(ADMIN_PASSWORD_NEW);
    await page.goto(`${WEB}/`);
    await eventuallyOk("hosts for screenshots", 15_000, async () => {
      await page.getByRole("heading", { name: "Hosts" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });

    await page.emulateMedia({ colorScheme: "light" });
    await page.screenshot({
      path: path.join(SHOT_DIR, "hosts-light.png"),
      fullPage: true,
    });
    await page.emulateMedia({ colorScheme: "dark" });
    await page.screenshot({
      path: path.join(SHOT_DIR, "hosts-dark.png"),
      fullPage: true,
    });

    await page.goto(`${WEB}/hosts/${hostId}`);
    await eventuallyOk("host overview for screenshots", 15_000, async () => {
      await page.getByRole("heading", { name: "e2e-host" }).waitFor({
        state: "visible",
        timeout: 2000,
      });
    });
    await page.emulateMedia({ colorScheme: "light" });
    await page.screenshot({
      path: path.join(SHOT_DIR, "host-overview-light.png"),
      fullPage: true,
    });
    await page.emulateMedia({ colorScheme: "dark" });
    await page.screenshot({
      path: path.join(SHOT_DIR, "host-overview-dark.png"),
      fullPage: true,
    });

    for (const name of [
      "hosts-light.png",
      "hosts-dark.png",
      "host-overview-light.png",
      "host-overview-dark.png",
    ]) {
      const st = fs.statSync(path.join(SHOT_DIR, name));
      assert.ok(st.size > 1000, `${name} too small`);
    }
    assertCleanPage("10.shots");
  });

  it("11. dialogs never scroll (1280x720 and 390x844); popovers stay on screen", async () => {
    await ensureAdminSession(ADMIN_PASSWORD_NEW);
    assert.ok(hostId, "scenario 4 must have set hostId");

    /**
     * A dialog must fit: no vertical or horizontal scrollbar on the dialog or
     * its body, nothing wider than the dialog, and the dialog inside the viewport.
     */
    async function assertDialogFits(label) {
      const dlg = page.getByRole("dialog");
      await dlg.waitFor({ state: "visible", timeout: 5000 });
      const m = await dlg.evaluate((el) => {
        const body = el.querySelector(".modal-body");
        const r = el.getBoundingClientRect();
        const wide = [...el.querySelectorAll("*")]
          .filter((c) => !c.closest(".sr-only") && !c.classList.contains("sr-only"))
          .filter((c) => c.getBoundingClientRect().right > r.right + 1)
          .map((c) => c.tagName + "." + c.className)
          .slice(0, 5);
        const clipped = [...el.querySelectorAll("*")]
          .filter((c) => !c.closest(".sr-only") && !c.classList.contains("sr-only"))
          .filter((c) => c.getBoundingClientRect().bottom > r.bottom + 1)
          .map((c) => c.tagName + "." + c.className)
          .slice(0, 5);
        return {
          sh: el.scrollHeight,
          ch: el.clientHeight,
          sw: el.scrollWidth,
          cw: el.clientWidth,
          bsh: body ? body.scrollHeight : 0,
          bch: body ? body.clientHeight : 0,
          top: r.top,
          bottom: r.bottom,
          left: r.left,
          right: r.right,
          vw: window.innerWidth,
          vh: window.innerHeight,
          docScrollW: document.documentElement.scrollWidth,
          wide,
          clipped,
        };
      });
      const where = `${label} @${m.vw}x${m.vh}: ${JSON.stringify(m)}`;
      assert.ok(m.sh <= m.ch + 1, `dialog scrolls vertically: ${where}`);
      assert.ok(m.sw <= m.cw + 1, `dialog scrolls horizontally: ${where}`);
      assert.ok(m.bsh <= m.bch + 1, `dialog body scrolls vertically: ${where}`);
      assert.deepEqual(m.wide, [], `content wider than dialog: ${where}`);
      assert.deepEqual(m.clipped, [], `content cut off at the bottom: ${where}`);
      assert.ok(m.top >= -1 && m.bottom <= m.vh + 1, `dialog outside viewport: ${where}`);
      assert.ok(m.left >= -1 && m.right <= m.vw + 1, `dialog outside viewport: ${where}`);
      assert.ok(m.docScrollW <= m.vw + 1, `page scrolls horizontally: ${where}`);
    }

    async function closeDialog() {
      await page.keyboard.press("Escape");
      await eventuallyOk("dialog closed", 5_000, async () => {
        assert.equal(await page.getByRole("dialog").count(), 0);
      });
    }

    // A project to reach the project dialogs (created through the API, as the UI would).
    const created = await page.request.post(`${WEB}/api/hosts/${hostId}/projects`, {
      headers: { "X-Haetae-Request": "1" },
      data: {
        name: "dialog-check",
        source: {
          kind: "inline",
          inlineCompose: "services:\n  web:\n    image: busybox\n    command: sleep 1000\n",
        },
        domain: "",
        service: "",
        port: 0,
        autoDeploy: false,
      },
    });
    assert.ok(created.ok() || created.status() === 409, `create project: ${created.status()} ${await created.text()}`);
    const listed = await page.request.get(`${WEB}/api/projects?hostId=${hostId}`);
    const projectId = (await listed.json()).projects.find((p) => p.name === "dialog-check")?.id;
    assert.ok(projectId, "project dialog-check must exist");

    for (const vp of [
      { width: 1280, height: 720 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(vp);
      const tag = `${vp.width}x${vp.height}`;

      // Add host: step 1, step 2 (long command + waiting state), duplicate-name error.
      await page.goto(`${WEB}/`);
      await page.getByRole("heading", { name: "Hosts", level: 1 }).waitFor({ state: "visible" });
      await page.getByRole("button", { name: "Add host" }).click();
      await assertDialogFits(`add host step 1 (${tag})`);
      await page.getByLabel("Host name").fill("e2e-host");
      await page.getByRole("button", { name: "Get install command" }).click();
      await page.getByRole("alert").waitFor({ state: "visible", timeout: 5000 });
      await assertDialogFits(`add host error (${tag})`);
      await page.getByLabel("Host name").fill(`dialog-check-${vp.width}`);
      await page.getByRole("button", { name: "Get install command" }).click();
      await page.locator(".copy-block pre").waitFor({ state: "visible", timeout: 10_000 });
      await assertDialogFits(`add host step 2 (${tag})`);
      // The InfoTip popover inside the dialog opens and closes without closing the dialog.
      await page.getByRole("button", { name: /What is Install command/ }).click();
      await page.getByRole("group", { name: "Install command" }).waitFor({ state: "visible" });
      await page.keyboard.press("Escape");
      assert.equal(await page.getByRole("group", { name: "Install command" }).count(), 0);
      assert.equal(await page.getByRole("dialog").count(), 1, "Esc on a popover must not close the dialog");
      await closeDialog();

      // Remove host.
      await page.goto(`${WEB}/hosts/${hostId}`);
      await page.getByRole("heading", { name: "e2e-host", level: 1 }).waitFor({ state: "visible" });
      await page.getByRole("button", { name: "Remove host" }).click();
      await assertDialogFits(`remove host (${tag})`);
      await closeDialog();

      // Deploy tab: the project form is an inline panel, never a dialog.
      await page.goto(`${WEB}/hosts/${hostId}?tab=deploy`);
      await page.getByRole("button", { name: "New project" }).click();
      await page.getByRole("heading", { name: "New project" }).waitFor({ state: "visible" });
      assert.equal(await page.getByRole("dialog").count(), 0, "new project must not be a dialog");

      // Project page: edit is inline, remove is a dialog.
      await page.goto(`${WEB}/projects/${projectId}`);
      await page.getByRole("heading", { name: "dialog-check", level: 1 }).waitFor({ state: "visible" });
      await page.getByRole("button", { name: "Edit" }).click();
      await page.getByRole("heading", { name: "Edit project" }).waitFor({ state: "visible" });
      assert.equal(await page.getByRole("dialog").count(), 0, "edit project must not be a dialog");
      await page.goto(`${WEB}/projects/${projectId}`);
      await page.getByRole("button", { name: "Remove", exact: true }).click();
      await assertDialogFits(`remove project (${tag})`);
      await closeDialog();

      // Popovers stay inside the viewport and close on Esc.
      await page.goto(`${WEB}/hosts/${hostId}?tab=logs`);
      const tip = page.locator(".tab-intro .infotip-btn").first();
      await tip.click();
      const pop = page.locator(".infotip-pop");
      await pop.waitFor({ state: "visible" });
      const box = await pop.evaluate((el) => {
        const r = el.getBoundingClientRect();
        return { l: r.left, r: r.right, vw: window.innerWidth, sh: el.scrollHeight, ch: el.clientHeight };
      });
      assert.ok(box.l >= 0 && box.r <= box.vw, `popover off screen (${tag}): ${JSON.stringify(box)}`);
      assert.ok(box.sh <= box.ch + 1, `popover scrolls (${tag}): ${JSON.stringify(box)}`);
      await page.keyboard.press("Escape");
      assert.equal(await page.locator(".infotip-pop").count(), 0);
    }

    await page.setViewportSize({ width: 1280, height: 800 });
    assertCleanPage("11.dialogs");
  });
});
