import { build } from 'esbuild';
import { readFile, readdir, writeFile, mkdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(root, '../..');
const pdfRoot = path.join(root, 'node_modules/pdfjs-dist');
const zipRoot = path.join(root, 'node_modules/@zip.js/zip.js');
const embeddedData = {};
for (const [kind, directory] of [['cMapUrl', 'cmaps'], ['standardFontDataUrl', 'standard_fonts']]) {
  embeddedData[kind] = {};
  for (const name of (await readdir(path.join(pdfRoot, directory))).sort()) {
    if (/LICENSE/i.test(name)) continue;
    embeddedData[kind][name] = (await readFile(path.join(pdfRoot, directory, name))).toString('base64');
  }
}
const bundled = await build({
  entryPoints: [path.join(root, 'app.js')],
  bundle: true,
  write: false,
  minify: true,
  target: ['es2022'],
  format: 'iife',
  platform: 'browser',
  legalComments: 'inline',
});
const worker = (await readFile(path.join(pdfRoot, 'legacy/build/pdf.worker.min.mjs'), 'utf8'))
  .replace(/^\/\/# sourceMappingURL=.*$/gm, '');
const dataScript = `globalThis.BOOK_PDF_WORKER=${JSON.stringify(worker)};\nglobalThis.BOOK_PDF_DATA=${JSON.stringify(embeddedData)};\n`;
const app = (dataScript + bundled.outputFiles[0].text).replace(/<\/script/gi, '<\\/script');
const notices = [];
for (const [label, file] of [
  ['PDF.js', path.join(pdfRoot, 'LICENSE')],
  ['zip.js', path.join(zipRoot, 'LICENSE')],
  ['PDF CMaps', path.join(pdfRoot, 'cmaps/LICENSE')],
  ['PDF fonts (Foxit)', path.join(pdfRoot, 'standard_fonts/LICENSE_FOXIT')],
  ['PDF fonts (Liberation)', path.join(pdfRoot, 'standard_fonts/LICENSE_LIBERATION')],
]) {
  notices.push(`${label}\n${await readFile(file, 'utf8')}`);
}
await writeFile(path.join(root, 'THIRD-PARTY-NOTICES.txt'), notices.join('\n\n').replace(/[ \t]+$/gm, ''));
let html = await readFile(path.join(root, 'ui.html'), 'utf8');
for (const [key, value] of Object.entries({
  __APP_CSS__: await readFile(path.join(root, 'ui.css'), 'utf8'),
  __APP_BUNDLE__: app,
})) {
  if (!html.includes(key)) throw new Error(`Template placeholder missing: ${key}`);
  html = html.replace(key, () => value);
}
html = html.replace('<!doctype html>', `<!doctype html>\n<!--\n${notices.join('\n\n').replaceAll('--', '—').replace(/[ \t]+$/gm, '')}\n-->`);
await mkdir(path.join(repo, 'downloads'), { recursive: true });
await writeFile(path.join(repo, 'index.html'), html);
await writeFile(path.join(repo, 'downloads/Book-Text-Studio.html'), html);
console.log(`Built standalone book-body converter: ${Buffer.byteLength(html).toLocaleString()} bytes`);
