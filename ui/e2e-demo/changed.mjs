/**
 * Screenshot only the surfaces a change touched.
 *
 * shots.mjs takes the whole tour, which is right when the cluster is the
 * subject. This is for review: the screens that moved, in both themes, so a
 * change can be looked at rather than described. Light is not an afterthought
 * here — the handoff's verdict colours are redrawn per theme rather than
 * inverted, and a screen that was only ever checked in dark is a screen whose
 * light half nobody has seen.
 *
 *   DENCER_URL=http://localhost:8092 DENCER_TOKEN=... SHOT_DIR=/tmp/shots \
 *     node ui/e2e-demo/changed.mjs
 */
import { chromium } from "playwright";
import { mkdirSync } from "node:fs";

const URL = process.env.DENCER_URL || "http://localhost:8092";
const TOKEN = process.env.DENCER_TOKEN || "";
const OUT = process.env.SHOT_DIR || "shots";
const pause = (ms) => new Promise((r) => setTimeout(r, ms));

mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();

for (const theme of ["dark", "light"]) {
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    colorScheme: theme,
    deviceScaleFactor: 2,
  });
  const page = await ctx.newPage();
  await page.addInitScript((t) => localStorage.setItem("dencer.theme", t), theme);

  const shot = async (name) => {
    await page.screenshot({ path: `${OUT}/${name}-${theme}.png` });
    console.log(`  ${name}-${theme}.png`);
  };
  const go = async (dest) => {
    await page.locator(`.rail-item:has-text("${dest}")`).click().catch(() => {});
    await pause(2200);
  };

  await page.goto(URL, { waitUntil: "domcontentloaded" });
  await pause(1500);
  const tokenBox = page.locator("#token");
  if (await tokenBox.isVisible().catch(() => false)) {
    await tokenBox.fill(TOKEN);
    await page.getByRole("button", { name: /sign in with token/i }).click();
  }
  await page.waitForSelector(".rail-item", { timeout: 30000 }).catch(() => {});
  await pause(2500);

  // The top bar, with no disabled Settings square in it any more.
  await shot("1-topbar-no-settings");

  // The new destination.
  await go("Resilience");
  await shot("2-resilience");

  // The cross-link: a recommendation for a measured workload offers the
  // measurements rather than repeating them.
  await go("Recommendations");
  // The screen opens on "Blocking a step", which is empty on a cluster where
  // nothing is held back — the healthy case, and the one that hid the link.
  const all = page.getByRole("button", { name: /all findings/i });
  if (await all.isVisible().catch(() => false)) {
    await all.click();
    await pause(1200);
  }
  const rec = page.locator(".recsrow").first();
  if (await rec.isVisible().catch(() => false)) {
    await rec.click();
    await pause(1600);
  }
  await shot("3-recommendation-links-out");

  // And the other direction: a measured workload says a finding is open.
  await go("Rightsizing");
  await shot("4-rightsizing-finding-open");

  await ctx.close();
}

await browser.close();
console.log(`shots in ${OUT}`);
