/**
 * The scripted walkthrough — the demo that re-records itself.
 *
 * Drives the real UI against the live cluster and records video. Scripted
 * rather than hand-recorded so it never goes stale: after any UI change,
 * `make demo-video` produces a fresh recording of the actual product.
 *
 * The route follows the redesign's own argument: sign in past the thesis,
 * read the plan (hero → step list → detail pane), rehearse it, tour the
 * three Cluster lenses, open the Recommendations queue, read the History
 * ledger, and end back on Review in the light theme — both themes are
 * first-class and the recording should say so.
 *
 *   DENCER_URL=http://localhost:8090 DENCER_TOKEN=$(make -s token) \
 *     node ui/e2e-demo/walkthrough.mjs
 */
import { chromium } from "playwright";
import { mkdirSync, renameSync, readdirSync } from "node:fs";

const URL = process.env.DENCER_URL || "http://localhost:8090";
const TOKEN = process.env.DENCER_TOKEN || "";
const OUT = "assets/demo";
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  recordVideo: { dir: OUT, size: { width: 1440, height: 900 } },
  colorScheme: "dark",
});
const page = await ctx.newPage();
// The recording starts in dark regardless of what an earlier session chose.
await page.addInitScript(() => localStorage.setItem("dencer.theme", "dark"));

// The sign-in screen: the lockup, the thesis, the ambient loop — worth two
// breaths on camera before the token goes in.
await page.goto(URL);
await pause(2600);
const tokenBox = page.locator("#token");
if (await tokenBox.isVisible().catch(() => false)) {
  await tokenBox.fill(TOKEN);
  await pause(600);
  await page.getByRole("button", { name: /sign in with token/i }).click();
}

// Review: the hero states what approval buys; the step list is the screen.
await page.waitForSelector(".hero-headline", { timeout: 20000 });
await pause(3000);

// Read two steps through the detail pane — a safe one, then a judgement.
const rows = page.locator(".steprow");
if ((await rows.count()) > 1) {
  await rows.nth(0).click();
  await pause(2200);
  const caution = page.locator(".steprow-verdict-yellow").first();
  if (await caution.isVisible().catch(() => false)) {
    await caution.click();
    await pause(2600);
  }
}

// The triage cards filter the list.
await page.locator(".triage-card-red").click();
await pause(1800);
await page.locator(".triage-card-red").click();
await pause(800);

// Rehearse: nothing cordoned, nothing evicted, the same trail as a real run.
await page.locator("button.reviewfooter-rehearse").click();
await pause(700);
const sheet = page.locator(".sheet .btn-primary");
if (await sheet.isVisible().catch(() => false)) await sheet.click();
await page.waitForSelector(".runscreen", { timeout: 30000 });
await pause(5500); // the result tiles and the trail
await page.getByRole("button", { name: "Discard" }).click();
await pause(1200);

// The Cluster destination, through all three lenses.
await page.locator('.rail-item:has-text("Cluster")').click();
await pause(2200);
for (const lens of ["Wells", "Load", "Rack"]) {
  await page.locator(`.clusterpage-lenses .viewswitch-btn:has-text("${lens}")`).click();
  await pause(2400);
}

// Recommendations: the queue ranked by nodes unlocked.
await page.locator('.rail-item:has-text("Recommendations")').click();
await pause(2600);
const firstRec = page.locator(".recsrow").first();
if (await firstRec.isVisible().catch(() => false)) {
  await firstRec.click();
  await pause(2200);
}

// History: the estate over time, over the audit ledger.
await page.locator('.rail-item:has-text("History")').click();
await pause(3000);

// Back to Review, and into the light — both themes are first-class.
await page.locator('.rail-item:has-text("Review")').click();
await pause(1500);
await page.getByRole("button", { name: /switch to the light theme/i }).click();
await pause(3200);
await page.getByRole("button", { name: /switch to the dark theme/i }).click();
await pause(1500);

await ctx.close(); // flushes the video
await browser.close();

// Playwright names the file by hash; give it its real name.
const vids = readdirSync(OUT).filter((f) => f.endsWith(".webm") && f !== "walkthrough.webm");
if (vids.length) renameSync(`${OUT}/${vids[0]}`, `${OUT}/walkthrough.webm`);
console.log("recorded", `${OUT}/walkthrough.webm`);
