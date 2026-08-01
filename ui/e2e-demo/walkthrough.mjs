/**
 * The scripted walkthrough — the demo that re-records itself.
 *
 * Drives the real UI against the live cluster and records video. Scripted
 * rather than hand-recorded so it never goes stale: after any UI change,
 * `make demo-video` produces a fresh recording of the actual product.
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

// Sign in with the token, the way an operator does.
await page.goto(URL);
await pause(1200);
const tokenBox = page.locator('input[type="password"], textarea, input[type="text"]').first();
if (await tokenBox.isVisible().catch(() => false)) {
  await tokenBox.fill(TOKEN);
  await page.getByRole("button", { name: /sign in|continue|use token/i }).click();
}
await page.waitForSelector(".verdict-line", { timeout: 20000 });
await pause(2500); // the verdict and the field's reveal animation

// The three field renderings.
for (const view of ["Wells", "Panel", "Rack"]) {
  await page.getByRole("button", { name: view, exact: true }).click();
  await pause(2200);
}

// Scrub the plan: the forecast animating over observed facts.
const scrubber = page.locator('input[type="range"]').first();
if (await scrubber.isVisible().catch(() => false)) {
  const max = Number(await scrubber.getAttribute("max")) || 5;
  for (let i = 1; i <= Math.min(max, 6); i++) {
    await scrubber.fill(String(i));
    await pause(700);
  }
  await scrubber.fill("0");
  await pause(800);
}

// The converge consent sheet — shown, not run.
await page.getByRole("button", { name: /run to optimum/i }).click();
await pause(2600);
await page.getByRole("button", { name: /cancel/i }).click();
await pause(600);

// A pod's own card: identity, movement, constraints.
const anyPod = page.locator(".blk").first();
if (await anyPod.isVisible().catch(() => false)) {
  await anyPod.click();
  await pause(2400);
  await page.keyboard.press("Escape");
  await pause(500);
}

// Advice: the fix list as its own surface.
await page.getByRole("button", { name: "Advice", exact: true }).click();
await pause(2600);

// History: the cluster as a line.
await page.getByRole("button", { name: "History", exact: true }).click();
await page.waitForSelector(".history-band", { timeout: 15000 });
await pause(3500);

await ctx.close(); // flushes the video
await browser.close();

// Playwright names the file by hash; give it its real name.
const vids = readdirSync(OUT).filter((f) => f.endsWith(".webm") && f !== "walkthrough.webm");
if (vids.length) renameSync(`${OUT}/${vids[0]}`, `${OUT}/walkthrough.webm`);
console.log("recorded", `${OUT}/walkthrough.webm`);
