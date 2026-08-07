/**
 * Screenshot the real UI at a named phase, for evidence rather than video.
 *
 * The walkthrough records a scripted tour; this takes stills of whatever the
 * product is showing right now, which is what you want when the cluster
 * underneath is real, costs money, and will not exist in an hour.
 *
 *   DENCER_URL=http://localhost:8092 DENCER_TOKEN=... SHOT_DIR=/path/to/phase \
 *     node ui/e2e-demo/shots.mjs
 */
import { chromium } from "playwright";
import { mkdirSync } from "node:fs";

const URL = process.env.DENCER_URL || "http://localhost:8092";
const TOKEN = process.env.DENCER_TOKEN || "";
const OUT = process.env.SHOT_DIR || "shots";
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: "dark" });
const page = await ctx.newPage();
await page.addInitScript(() => localStorage.setItem("dencer.theme", "dark"));

const shot = async (name) => {
  await page.screenshot({ path: `${OUT}/${name}.png` });
  console.log(`  ${name}.png`);
};

await page.goto(URL, { waitUntil: "domcontentloaded" });
await pause(1500);
const tokenBox = page.locator("#token");
if (await tokenBox.isVisible().catch(() => false)) {
  await tokenBox.fill(TOKEN);
  await page.getByRole("button", { name: /sign in with token/i }).click();
}

// Review — the hero and the step list, which is the whole product in one frame.
await page.waitForSelector(".hero-headline", { timeout: 30000 }).catch(() => {});
await pause(2500);
await shot("1-review");

// A step opened, so the Safety Guard reasoning is on the record.
const rows = page.locator(".steprow");
if ((await rows.count().catch(() => 0)) > 0) {
  await rows.nth(0).click();
  await pause(1800);
  await shot("2-step-detail");
}

// The three Cluster lenses — Wells carries the packing ceiling.
await page.locator('.rail-item:has-text("Cluster")').click().catch(() => {});
await pause(2000);
for (const lens of ["Wells", "Load", "Rack"]) {
  const btn = page.locator(`.clusterpage-lenses .viewswitch-btn:has-text("${lens}")`);
  if (await btn.isVisible().catch(() => false)) {
    await btn.click();
    await pause(1800);
    await shot(`3-lens-${lens.toLowerCase()}`);
  }
}

await page.locator('.rail-item:has-text("Recommendations")').click().catch(() => {});
await pause(2000);
await shot("4-recommendations");

// History — the savings ledger, which only means anything on real machines.
await page.locator('.rail-item:has-text("History")').click().catch(() => {});
await pause(2500);
await shot("5-history");

await page.locator('.rail-item:has-text("Review")').click().catch(() => {});
await pause(1200);

await ctx.close();
await browser.close();
console.log(`shots in ${OUT}`);
