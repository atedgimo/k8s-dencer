/**
 * Does the read path survive a narrow viewport?
 *
 * The complaint that produced this was concrete: the maintainer read the UI
 * from a phone and got a 224px rail crushed against a horizontally scrolling
 * table. So the assertions are the concrete failures, not "looks responsive".
 *
 * At each width, on every destination:
 *   - the page body must not scroll sideways (a horizontal page scrollbar
 *     hides the rail and the footer, which is what turned reading into
 *     hunting)
 *   - the destination must render something — a breakpoint that collapses a
 *     pane to zero height passes a screenshot review and fails a reader
 *   - nothing that evicts a pod may be visible at phone width
 *
 *   DENCER_URL=http://localhost:5173 DENCER_TOKEN=... SHOT_DIR=/tmp/narrow \
 *     node ui/e2e-demo/narrow.mjs
 */
import { chromium } from "playwright";
import { mkdirSync } from "node:fs";

const URL = process.env.DENCER_URL || "http://localhost:5173";
const TOKEN = process.env.DENCER_TOKEN || "";
const OUT = process.env.SHOT_DIR || "narrow";
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

// The three tiers plus the width the design was drawn at, so a regression in
// the wide case shows up in the same run.
const WIDTHS = [
  { w: 1440, h: 900, name: "wide" },
  { w: 1100, h: 860, name: "stacked" }, // ≤1200: panes stack
  { w: 900, h: 800, name: "icons" }, //    ≤1000: rail is icons
  { w: 390, h: 844, name: "phone" }, //    ≤720:  rail is a strip, read-only
];

const DESTINATIONS = ["Review", "Cluster", "Recommendations", "Resilience", "Rightsizing", "History"];

mkdirSync(OUT, { recursive: true });
const problems = [];
const note = (where, what) => problems.push(`${where}: ${what}`);

const browser = await chromium.launch();

for (const { w, h, name } of WIDTHS) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  await page.goto(URL, { waitUntil: "domcontentloaded" });
  await pause(1200);

  const box = page.locator("#token");
  if (await box.isVisible().catch(() => false)) {
    await box.fill(TOKEN);
    await page.getByRole("button", { name: /sign in with token/i }).click();
  }
  await page.waitForSelector(".rail-item", { timeout: 30000 }).catch(() => note(name, "rail never rendered"));
  await pause(3000);

  for (const dest of DESTINATIONS) {
    // At phone width the rail is a scrolling strip, so the destination may be
    // out of view — click it by force rather than reporting a false miss.
    const item = page.locator(`.rail-item:has-text("${dest}")`);
    if (!(await item.count())) {
      note(`${name}/${dest}`, "destination missing from the rail");
      continue;
    }
    await item.first().scrollIntoViewIfNeeded().catch(() => {});
    await item.first().click({ force: true }).catch(() => {});
    await pause(2000);

    // The page body must never scroll sideways.
    const over = await page.evaluate(() => {
      const d = document.documentElement;
      return { scroll: d.scrollWidth, client: d.clientWidth };
    });
    if (over.scroll > over.client + 1) {
      note(`${name}/${dest}`, `body scrolls sideways: ${over.scroll}px in ${over.client}px`);
    }

    // Something has to be on screen. A stacked pane that collapsed to nothing
    // renders as a blank column and reads as a broken product.
    const text = await page.locator(".frame-content").innerText().catch(() => "");
    if (text.trim().length < 20) {
      note(`${name}/${dest}`, "rendered essentially nothing");
    }

    // A pane can also survive with height zero while its SIBLING renders
    // fine, which is what the first stacked layout did: the step list lost
    // its space to the detail pane and the screen showed a toolbar sitting
    // directly on a step detail for a step no list was offering. Every
    // element on screen was present and the page was unusable, so presence
    // is not the test — visible height is.
    for (const [pane, label] of [
      [".steplist", "step list"],
      [".recsqueue", "findings queue"],
    ]) {
      const el = page.locator(pane).first();
      if (!(await el.count())) continue;
      const h = await el.evaluate((n) => n.getBoundingClientRect().height).catch(() => 0);
      if (h < 80) note(`${name}/${dest}`, `${label} collapsed to ${Math.round(h)}px`);
    }

    if (dest === "Review") {
      const railSteps = Number(
        (await page.locator('.rail-item:has-text("Review") .rail-badge').innerText().catch(() => "0")) || 0,
      );
      const rows = await page.locator(".steprow").count().catch(() => 0);
      if (railSteps > 0 && rows === 0) {
        note(`${name}/Review`, `rail says ${railSteps} steps, the list renders none`);
      }
    }

    await page.screenshot({ path: `${OUT}/${name}-${dest.toLowerCase()}.png` });
  }

  // Phone width is a reading surface. Nothing here may evict a pod.
  if (w <= 720) {
    await page.locator('.rail-item:has-text("Review")').first().click({ force: true }).catch(() => {});
    await pause(1500);
    for (const sel of [".reviewfooter-actions", ".steprow-box", ".stepdetail-actions"]) {
      if (await page.locator(sel).first().isVisible().catch(() => false)) {
        note(`${name}/Review`, `${sel} is visible — execute controls should be gone at this width`);
      }
    }
  }

  await ctx.close();
}

await browser.close();

if (problems.length === 0) {
  console.log(`NARROW CLEAN — shots in ${OUT}`);
} else {
  console.log(`NARROW FOUND ${problems.length} PROBLEM(S):`);
  for (const p of problems) console.log("  - " + p);
  process.exitCode = 1;
}
