// Drives Chromium over the live demo stack started by screenshots_test.go and
// writes the README screenshots. Configuration comes from SHOT_* variables.
// Playwright resolves through NODE_PATH (the global install); no browser download.
const { chromium } = require("playwright");
const path = require("node:path");

const WEB = process.env.SHOT_WEB_URL;
const EMAIL = process.env.SHOT_EMAIL;
const PASSWORD = process.env.SHOT_PASSWORD;
const HOST_ID = process.env.SHOT_HOST_ID;
const OUT = process.env.SHOT_OUT_DIR;
const CHROMIUM = process.env.SHOT_CHROMIUM || "/usr/bin/chromium";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function settle(page, ms = 1200) {
  await page.waitForLoadState("networkidle", { timeout: 15000 }).catch(() => {});
  await sleep(ms);
}

// Every chart on the page has a line with more than one point.
async function waitForCharts(page) {
  await page.waitForFunction(
    () => {
      const charts = [...document.querySelectorAll(".chart")];
      if (charts.length === 0) return false;
      return charts.every((c) => {
        const p = c.querySelector("svg path");
        return p && /L/i.test(p.getAttribute("d") || "");
      });
    },
    null,
    { timeout: 60000 },
  );
}

async function shot(page, name) {
  await page.mouse.move(1430, 890); // park the pointer away from tooltips
  await page.screenshot({ path: path.join(OUT, name + ".png") });
  console.log("wrote " + name + ".png");
}

(async () => {
  const browser = await chromium.launch({ executablePath: CHROMIUM });
  const context = await browser.newContext({
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
    deviceScaleFactor: 1,
    colorScheme: "light",
    locale: "en-US",
    timezoneId: "UTC",
  });
  const page = await context.newPage();
  const problems = [];
  page.on("pageerror", (e) => problems.push("pageerror: " + e.message));

  // Log in as the admin.
  await page.goto(`${WEB}/login`);
  await page.getByLabel("Email").fill(EMAIL);
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD);
  await Promise.all([
    page.waitForResponse((r) => r.url().includes("/api/login") && r.request().method() === "POST"),
    page.getByRole("button", { name: /log in|sign in/i }).click(),
  ]);
  await page.getByRole("heading", { name: "Hosts" }).waitFor({ timeout: 15000 });

  // 1. Hosts list.
  await page.getByText("hel1-prod").first().waitFor({ timeout: 15000 });
  await settle(page, 2500);
  await shot(page, "hosts");

  // 2. Host overview with metrics.
  await page.goto(`${WEB}/hosts/${HOST_ID}?tab=overview`);
  await page.getByRole("heading", { name: "hel1-prod" }).waitFor({ timeout: 15000 });
  await waitForCharts(page);
  await settle(page, 1500);
  await shot(page, "host-overview");

  // 3. The incident from the crash loop, with the operator's explanation.
  await page.goto(`${WEB}/incidents`);
  await page.locator("a[href^='/incidents/']").first().click();
  await page.locator(".evidence-block").first().waitFor({ timeout: 30000 });
  await settle(page, 1500);
  await shot(page, "incident");

  // 4. Web chat.
  await page.goto(`${WEB}/chat`);
  const answer = page.locator("article", { hasText: "Before every exit" }).first();
  await answer.waitFor({ timeout: 15000 });
  // Scroll the log so the answer and the incident message below it are in view.
  await answer.evaluate((a) => {
    const log = a.closest(".chat-log");
    log.scrollTop += a.getBoundingClientRect().top - log.getBoundingClientRect().top - 6;
  });
  await settle(page, 1500);
  await shot(page, "chat");

  // 5. Security tab.
  await page.goto(`${WEB}/hosts/${HOST_ID}?tab=security`);
  await page.getByRole("button", { name: /Show findings/ }).last().click();
  await page.getByText(/CVE-2026-31407/).first().waitFor({ timeout: 30000 });
  // Start at shop-web so its findings table, the worker and the misconfigurations share the viewport.
  await page.getByText("shop-web", { exact: true }).first().evaluate((el) => {
    el.scrollIntoView({ block: "start" });
    window.scrollBy(0, -110);
  });
  await settle(page, 1500);
  await shot(page, "security");

  // 6. Settings, AI operator.
  await page.goto(`${WEB}/settings?tab=operator`);
  await page.getByTestId("operator-usage").waitFor({ timeout: 15000 });
  await settle(page, 1000);
  await shot(page, "ai-operator");

  // 7. Add host dialog, step 2 (the install command). Last, because it
  // creates a pending enrollment on the demo stack.
  await page.goto(`${WEB}/`);
  await page.getByRole("button", { name: "Add host" }).first().click();
  await page.getByRole("dialog", { name: "Add host" }).waitFor();
  await page.getByLabel("Host name").fill("hel2-staging");
  await page.getByRole("button", { name: "Get install command" }).click();
  await page.locator(".copy-block pre").waitFor({ state: "visible", timeout: 15000 });
  await settle(page, 1000);
  await shot(page, "add-host");

  await browser.close();
  if (problems.length) {
    console.log("page problems:\n" + problems.join("\n"));
    process.exitCode = 1;
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
