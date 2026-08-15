/**
 * Walk every destination and report what is wrong.
 *
 * Not a screenshot tour: this clicks through the product the way an operator
 * would and complains about what it finds — console errors, failed requests,
 * screens that render nothing, numbers that contradict each other across
 * surfaces. Screenshots are a side effect, taken so a human can check the
 * judgement calls the script cannot make.
 *
 * It exists because a full minute of "No findings against the current
 * cluster" on a cluster with 34 findings survived every unit test, every
 * gate, and a screenshot review — it only showed up when something drove the
 * real UI against a real backend and read the result.
 *
 *   DENCER_URL=http://localhost:8092 DENCER_TOKEN=... SHOT_DIR=/tmp/audit \
 *     node ui/e2e-demo/audit.mjs
 */
import { chromium } from "playwright";
import { mkdirSync } from "node:fs";

const URL = process.env.DENCER_URL || "http://localhost:8092";
const TOKEN = process.env.DENCER_TOKEN || "";
const OUT = process.env.SHOT_DIR || "audit";
const THEME = process.env.THEME || "dark";
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

mkdirSync(OUT, { recursive: true });
const problems = [];
const note = (where, what) => problems.push(`${where}: ${what}`);

const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  colorScheme: THEME,
  deviceScaleFactor: 2,
});
const page = await ctx.newPage();
await page.addInitScript((t) => localStorage.setItem("dencer.theme", t), THEME);

let signedIn = false;
page.on("console", (m) => {
  if (m.type() === "error" && signedIn) note("console", m.text().slice(0, 140));
});
page.on("pageerror", (e) => note("uncaught", String(e).slice(0, 140)));
page.on("response", (r) => {
  // 401s before sign-in are the app discovering it needs a token.
  if (signedIn && r.url().includes("/api/v1/") && r.status() >= 400) {
    note("http", `${r.status()} ${r.url().replace(/^.*\/api\/v1/, "/api/v1").slice(0, 70)}`);
  }
});

await page.goto(URL, { waitUntil: "domcontentloaded" });
await pause(1500);
const tokenBox = page.locator("#token");
if (await tokenBox.isVisible().catch(() => false)) {
  await tokenBox.fill(TOKEN);
  await page.getByRole("button", { name: /sign in with token/i }).click();
} else {
  note("signin", "no token box appeared; auth may be disabled");
}
signedIn = true;
await page.waitForSelector(".rail-item", { timeout: 30000 }).catch(() => note("frame", "rail never rendered"));
await pause(4000);

const shot = async (name) => {
  await page.screenshot({ path: `${OUT}/${name}-${THEME}.png` });
};

/** Visible text of a destination's main region, for emptiness checks. */
const bodyText = async () => (await page.locator(".surface, main, .review-main").first().innerText().catch(() => "")) || (await page.locator("body").innerText().catch(() => ""));

const go = async (dest) => {
  const item = page.locator(`.rail-item:has-text("${dest}")`);
  if (!(await item.isVisible().catch(() => false))) {
    note("rail", `no destination named ${dest}`);
    return false;
  }
  await item.click();
  await pause(2500);
  return true;
};

// ---------------------------------------------------------------- Review
await shot("1-review");
const heroText = await page.locator(".hero, .hero-headline").first().innerText().catch(() => "");
if (!heroText.trim()) note("Review", "hero rendered empty");
const stepRows = await page.locator(".steprow").count().catch(() => 0);
const railSteps = Number((await page.locator('.rail-item:has-text("Review") .rail-badge').innerText().catch(() => "0")) || 0);
if (railSteps > 0 && stepRows === 0) note("Review", `rail says ${railSteps} steps, list shows none`);
if (stepRows > 0) {
  await page.locator(".steprow").first().click();
  await pause(1800);
  const detail = await page.locator(".stepdetail").innerText().catch(() => "");
  if (!detail.trim()) note("Review", "step detail pane empty after selecting a step");
  await shot("2-step-detail");
}

// --------------------------------------------------------------- Cluster
if (await go("Cluster")) {
  for (const lens of ["Rack", "Wells", "Load"]) {
    const btn = page.locator(`.clusterpage-lenses .viewswitch-btn:has-text("${lens}")`);
    if (!(await btn.isVisible().catch(() => false))) {
      note("Cluster", `lens ${lens} not offered`);
      continue;
    }
    await btn.click();
    await pause(1800);
    const field = await page.locator(".clusterpage-field, .field").first().innerText().catch(() => "");
    const nodes = await page.locator(".fieldnode, .well, .loadrow, .racknode").count().catch(() => 0);
    if (nodes === 0 && !field.trim()) note("Cluster", `${lens} lens drew nothing`);
    await shot(`3-cluster-${lens.toLowerCase()}`);
  }
}

// ------------------------------------------------------- Recommendations
if (await go("Recommendations")) {
  const head = await page.locator(".recspage-head").first().innerText().catch(() => "");
  const railHigh = Number((await page.locator('.rail-item:has-text("Recommendations") .rail-badge').innerText().catch(() => "0")) || 0);
  const shownHigh = Number((head.match(/(\d+)\s*high/) || [0, 0])[1]);
  if (railHigh !== shownHigh) note("Recommendations", `rail badge ${railHigh} but page says ${shownHigh} high`);
  const all = page.getByRole("button", { name: /all findings/i });
  if (await all.isVisible().catch(() => false)) {
    await all.click();
    await pause(1500);
  }
  const rows = await page.locator(".recsrow").count().catch(() => 0);
  const total = Number((head.match(/(\d+)\s*findings/) || [0, 0])[1]);
  if (total > 0 && rows === 0) note("Recommendations", `says ${total} findings, renders no rows`);
  if (rows > 0) {
    await page.locator(".recsrow").first().click();
    await pause(1600);
    if (!(await page.locator(".recsdetail").innerText().catch(() => "")).trim())
      note("Recommendations", "detail pane empty after selecting a finding");
  }
  await shot("4-recommendations");
}

// ------------------------------------------------------------ Resilience
if (await go("Resilience")) {
  const txt = await bodyText();
  if (!/at risk|survive|Reading the cluster/i.test(txt)) note("Resilience", "neither findings nor the all-clear rendered");
  await shot("5-resilience");
}

// ----------------------------------------------------------- Rightsizing
if (await go("Rightsizing")) {
  const txt = await bodyText();
  const rows = await page.locator(".rightsizing tbody tr").count().catch(() => 0);
  if (rows === 0 && !/not available|No measured usage|Reading usage/i.test(txt))
    note("Rightsizing", "no rows and no explanation of why");
  await shot("6-rightsizing");
}

// --------------------------------------------------------------- History
if (await go("History")) {
  const txt = await bodyText();
  if (!txt.trim()) note("History", "rendered empty");
  await shot("7-history");
}

await page.locator('.rail-item:has-text("Review")').click().catch(() => {});
await pause(1200);

await ctx.close();
await browser.close();

if (problems.length === 0) {
  console.log(`AUDIT CLEAN (${THEME}) — shots in ${OUT}`);
} else {
  console.log(`AUDIT FOUND ${problems.length} PROBLEM(S) (${THEME}):`);
  for (const p of problems) console.log("  - " + p);
}
