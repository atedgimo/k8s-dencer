// Renders assets/brand/social-preview.svg to the PNG GitHub wants for the
// repository social preview (Settings -> General -> Social preview).
//
// This exists because the PNG was first exported by hand at the wrong scale:
// a 640x320 viewport at deviceScaleFactor 2 produces a 1280x640 file that is
// really the top-left quadrant magnified, so the name was cut to "k8s-denc"
// and the tagline ran off the edge. It shipped that way and was live on the
// repository before anyone looked at the pixels. A target that always renders
// the whole viewBox cannot make that mistake twice.
//
// Fonts are inlined from the UI's own dependencies rather than trusted to the
// system: a machine without IBM Plex installed would silently fall back to
// Helvetica and the brand would drift without anything failing.
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const out = process.argv[2] ?? join(repo, 'assets/brand/social-preview.png');

const svg = readFileSync(join(repo, 'assets/brand/social-preview.svg'), 'utf8');
const b64 = (p) => readFileSync(join(repo, p)).toString('base64');
const sans = b64('ui/node_modules/@fontsource-variable/ibm-plex-sans/files/ibm-plex-sans-latin-standard-normal.woff2');
const mono = b64('ui/node_modules/@fontsource/ibm-plex-mono/files/ibm-plex-mono-latin-400-normal.woff2');

const html = `<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:'IBM Plex Sans';src:url(data:font/woff2;base64,${sans}) format('woff2');font-weight:100 900;font-style:normal}
@font-face{font-family:'IBM Plex Mono';src:url(data:font/woff2;base64,${mono}) format('woff2');font-weight:400;font-style:normal}
html,body{margin:0;padding:0;background:#0b0c0e}svg{display:block}
</style>${svg}`;

const browser = await chromium.launch();
// The viewport matches the viewBox, so the whole canvas is in frame and the
// output is the 1280x640 GitHub asks for with no resampling step — which also
// keeps this free of an image tool the Linux runners would not have.
const page = await browser.newPage({
  viewport: { width: 1280, height: 640 },
  deviceScaleFactor: 1,
});
await page.setContent(html, { waitUntil: 'load' });
await page.evaluate(() => document.fonts.ready);
await page.locator('svg').screenshot({ path: out });
await browser.close();

console.log(`rendered ${out}`);
