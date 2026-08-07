// Renders a repository SVG to PNG with the product's own fonts inlined.
//
//   node ui/brand/render-svg.mjs <input.svg> <output.png>
//
// This exists because the social preview was once exported by hand at the
// wrong scale: a 640x320 viewport at deviceScaleFactor 2 produces a 1280x640
// file that is really the top-left quadrant magnified, so the name was cut to
// "k8s-denc". It shipped that way and was live on the repository before anyone
// looked at the pixels — the filename, the dimensions and the file format were
// all correct. Rendering the whole viewBox from a target cannot make that
// mistake twice.
//
// Fonts are inlined from the UI's own dependencies rather than trusted to the
// system: a machine without IBM Plex installed would silently fall back to
// Helvetica and the brand would drift with nothing failing.
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, resolve } from 'node:path';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const [input, output] = process.argv.slice(2);
if (!input || !output) {
  console.error('usage: render-svg.mjs <input.svg> <output.png>');
  process.exit(1);
}

const svg = readFileSync(resolve(input), 'utf8');
const b64 = (p) => readFileSync(join(repo, p)).toString('base64');
const sans = b64('ui/node_modules/@fontsource-variable/ibm-plex-sans/files/ibm-plex-sans-latin-standard-normal.woff2');
const mono = b64('ui/node_modules/@fontsource/ibm-plex-mono/files/ibm-plex-mono-latin-400-normal.woff2');

const box = svg.match(/viewBox="0 0 (\d+) (\d+)"/);
if (!box) {
  console.error(`${input}: needs a viewBox="0 0 W H" to render from`);
  process.exit(1);
}
const [w, h] = [Number(box[1]), Number(box[2])];

const html = `<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:'IBM Plex Sans';src:url(data:font/woff2;base64,${sans}) format('woff2');font-weight:100 900;font-style:normal}
@font-face{font-family:'IBM Plex Mono';src:url(data:font/woff2;base64,${mono}) format('woff2');font-weight:400;font-style:normal}
html,body{margin:0;padding:0;background:#0b0c0e}svg{display:block}
</style>${svg}`;

const browser = await chromium.launch();
// The viewport matches the viewBox, so the whole canvas is in frame and the
// output needs no resampling step — which also keeps this free of an image
// tool the Linux runners would not have.
const page = await browser.newPage({ viewport: { width: w, height: h }, deviceScaleFactor: 1 });
await page.setContent(html, { waitUntil: 'load' });
await page.evaluate(() => document.fonts.ready);
await page.locator('svg').screenshot({ path: resolve(output) });
await browser.close();

console.log(`rendered ${output} (${w}x${h})`);
