#!/usr/bin/env node
// AIDC DBD neutrality gate (DWR Networking), evaluation readiness D14. Fails (exit 1) when the product or anything it emits carries
// an internal standards id, a deviation, tenancy or Edge PoP clause id, an open-item or question id, a sample site
// code, a person's title, or an organization name on the banned list. Run it on the repository before a release, and on the unzipped (or zipped) outputs of a test build.
//
//   node tools/neutrality-check.js                      the repository: builder (and the files it embeds), runner, app, provgo
//   node tools/neutrality-check.js <file|folder> ...    outputs: packages, .run files, workbooks, PDFs, viewers, configs
//
// Zips (.zip .xlsx .docx .vsdx), .run payloads, gzip, PDF streams (and their hex strings) and the builder's base64
// script blocks are opened and read through. Nothing here reaches the network.
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib');

// built from parts so this file does not match itself
const P = (s, f) => new RegExp(s, f || 'g');
const BANNED = [
  // an id ends at its digits (a clause range or list may follow): vendor part numbers such as a printer's "EP-901A" in
  // the Electron runtime's device tables do not match
  ['standards id', P('\\bS' + 'TD-[A-Z]{2,5}-\\d{3}(?![A-Za-z0-9])')],
  ['deviation id', P('\\bD' + 'EV-0\\d\\d?(?![A-Za-z0-9])')],
  ['tenancy clause id', P('\\bT' + 'N-\\d{3}(?![A-Za-z0-9])')],
  ['Edge PoP clause id', P('\\bE' + 'P-\\d{3}(?![A-Za-z0-9])')],
  ['title', P('\\b(?:C' + 'aptain|C' + 'APTAIN|c' + 'aptain)\\b')],
  ['company name', P('\\bA' + 'I ?(?:Fabrik|FABRIK|fabrik)\\b|\\bA' + 'IFabrik\\b|\\bAIF' + 'ABRIK\\b')],
  ['company name', P('\\b(?:F' + 'irmus|F' + 'IRMUS)\\b')],
  // Phase 0b: a sample site code, open-item ids (letter, digits; or a lettered suffix) and question ids. Whole tokens only, so
  // part numbers (QFX5240-64OD-AFO, a Q-012A) and base64 runs do not match.
  ['sample site code', P('(?<![A-Za-z0-9+/])S' + 'NTC(?![A-Za-z0-9+/=])')],
  ['open-item id', P('\\bO' + 'D-[A-Z]{1,2}(?:\\d{2,3}|-[A-Z]{2,5})(?![A-Za-z0-9])')],
  ['question id', P('\\bQ' + '-0\\d{2}(?![A-Za-z0-9])')],
  // Phase 0c: requirement and clause ids of the former standards set (two letters, a hyphen, three digits, in the
  // families listed). A whole token: not inside a part or model number (PX3-5547,
  // QFX5240-64OD), an interface (et-0/0/0), a device name (MCS-01) or a hex or base64 run.
  ['clause id', P('(?<![A-Za-z0-9_.+/-])(?:B' + 'E|O' + 'B|I' + 'P|F' + 'E|A' + 'N|C' + 'B|C' + 'A|G' + 'X|F' + 'R|F' + 'T|F' + 'H|D' + 'L|E' + 'A)-\\d{3}(?![A-Za-z0-9+/])')],
  ['sample site code', P('\\b(?:V' + 'ORT-ORD|F' + 'IRE-BAN)\\b')],
];
const ROOT = path.resolve(__dirname, '..');
const REPO = ['builder', 'runner', 'src', 'provgo', 'tools', '.github', 'README.md', 'RELEASE-NOTES.md', 'package.json'];
const SKIP_DIR = new Set(['node_modules', '.git', '__pycache__']);
const hits = [], counts = {}; let files = 0, bytes = 0;

function scanText(where, text) {
  files++; bytes += text.length;
  for (const [what, re] of BANNED) { re.lastIndex = 0; let m;
    while ((m = re.exec(text))) { counts[what] = (counts[what] || 0) + 1;
      if (hits.length < 400) { const a = Math.max(0, m.index - 60), b = Math.min(text.length, m.index + m[0].length + 60);
        hits.push(where + ': ' + what + ' "' + m[0] + '": ...' + text.slice(a, b).replace(/\s+/g, ' ') + '...'); } } }
}
// a zip, entries -> byte readers (stored and deflate; ZIP64 sizes and offsets)
function readZip(buf) {
  let e = buf.length - 22; while (e >= 0 && buf.readUInt32LE(e) !== 0x06054b50) e--;
  if (e < 0) return null;
  let cdSize = buf.readUInt32LE(e + 12), cdOff = buf.readUInt32LE(e + 16);
  if (e >= 20 && buf.readUInt32LE(e - 20) === 0x07064b50) { const z = Number(buf.readBigUInt64LE(e - 12));
    if (buf.readUInt32LE(z) === 0x06064b50) { cdSize = Number(buf.readBigUInt64LE(z + 40)); cdOff = Number(buf.readBigUInt64LE(z + 48)); } }
  const out = []; let p = cdOff; const end = cdOff + cdSize;
  while (p < end && buf.readUInt32LE(p) === 0x02014b50) {
    const method = buf.readUInt16LE(p + 10), nlen = buf.readUInt16LE(p + 28), xlen = buf.readUInt16LE(p + 30), clen = buf.readUInt16LE(p + 32);
    let csize = buf.readUInt32LE(p + 20), usize = buf.readUInt32LE(p + 24), off = buf.readUInt32LE(p + 42);
    const name = buf.slice(p + 46, p + 46 + nlen).toString('utf8');
    for (let x = p + 46 + nlen, xe = x + xlen; x + 4 <= xe; ) { const id = buf.readUInt16LE(x), sz = buf.readUInt16LE(x + 2); let q = x + 4;
      if (id === 1) { if (usize === 0xFFFFFFFF) { usize = Number(buf.readBigUInt64LE(q)); q += 8; } if (csize === 0xFFFFFFFF) { csize = Number(buf.readBigUInt64LE(q)); q += 8; } if (off === 0xFFFFFFFF) off = Number(buf.readBigUInt64LE(q)); }
      x += 4 + sz; }
    p += 46 + nlen + xlen + clen;
    out.push([name, () => { const ln = buf.readUInt16LE(off + 26), lx = buf.readUInt16LE(off + 28), s = off + 30 + ln + lx, d = buf.slice(s, s + csize); return method === 8 ? zlib.inflateRawSync(d) : d; }]);
  }
  return out;
}
function runPayload(buf) {   // the .run: a shell header, then base64 lines after the marker
  const mk = buf.indexOf('\n__PAYLOAD_BELOW__\n'); if (mk < 0) return null;
  return { head: buf.slice(0, mk), zip: Buffer.from(buf.slice(mk + 19).toString('latin1').replace(/\s+/g, ''), 'base64') };
}
function pdfText(buf) {   // every stream inflated where it inflates, plus the hex strings inside (UTF-16BE or 8-bit)
  const s = buf.toString('latin1'), parts = [s]; let i = 0;
  while ((i = s.indexOf('stream', i)) >= 0) { let a = i + 6; if (s[a] === '\r') a++; if (s[a] === '\n') a++;
    const b = s.indexOf('endstream', a); if (b < 0) break;
    try { parts.push(zlib.inflateSync(buf.slice(a, b)).toString('latin1')); } catch (e) {} i = b + 9; }
  const t = parts.join('\n'), hex = [];
  t.replace(/<([0-9A-Fa-f\s]{8,})>/g, (m, h) => { h = h.replace(/\s+/g, ''); if (h.length % 2) return m; const x = Buffer.from(h, 'hex');
    hex.push(x.toString('latin1')); if (x.length % 2 === 0) { const y = Buffer.alloc(x.length); for (let k = 0; k + 1 < x.length; k += 2) { y[k] = x[k + 1]; y[k + 1] = x[k]; } hex.push(y.toString('utf16le')); } return m; });
  return t + '\n' + hex.join('\n');
}
function scanBytes(where, buf, depth) {
  const low = where.toLowerCase();
  if (depth > 6) { scanText(where, buf.toString('latin1')); return; }
  if (buf.length > 4 && buf.readUInt32LE(0) === 0x04034b50 || /\.(zip|xlsx|docx|pptx|vsdx)$/.test(low)) { const z = readZip(buf);
    if (z) { for (const [n, rd] of z) { if (n.endsWith('/')) continue; let b; try { b = rd(); } catch (e) { hits.push(where + '!' + n + ': unreadable entry (' + e.message + ')'); continue; } scanBytes(where + '!' + n, b, depth + 1); } return; } }
  if (low.endsWith('.run')) { const r = runPayload(buf); if (r) { scanText(where + '(header)', r.head.toString('utf8')); scanBytes(where + '(payload).zip', r.zip, depth + 1); return; } }
  if (buf.length > 2 && buf[0] === 0x1f && buf[1] === 0x8b) { try { scanBytes(where.replace(/\.gz$/i, '') + '(gunzip)', zlib.gunzipSync(buf), depth + 1); return; } catch (e) {} }
  if (low.endsWith('.pdf') || buf.slice(0, 5).toString('latin1') === '%PDF-') { scanText(where, pdfText(buf)); return; }
  const text = buf.toString(/\.(exe|bin|dll|node|so|png|jpg|ico|woff2?|ttf|pak)$/.test(low) || /\0/.test(buf.slice(0, 4096).toString('latin1')) ? 'latin1' : 'utf8');
  scanText(where, text);
  if (/\.html?$/.test(low)) {   // the builder's embedded pages and binaries: base64 script blocks, gzip inside some
    const re = /<script type="text\/plain" id="([^"]+)">\s*([A-Za-z0-9+\/=\s]{200,}?)\s*<\/script>/g; let m;
    while ((m = re.exec(text))) { const b = Buffer.from(m[2].replace(/\s+/g, ''), 'base64'); if (b.length) scanBytes(where + '#' + m[1], b, depth + 1); }
  }
}
function walk(p, label) {
  let st; try { st = fs.statSync(p); } catch (e) { console.error('not found: ' + p); process.exitCode = 2; return; }
  if (st.isDirectory()) { for (const n of fs.readdirSync(p).sort()) { if (SKIP_DIR.has(n)) continue; walk(path.join(p, n), label + '/' + n); } return; }
  scanBytes(label, fs.readFileSync(p), 0);
}
const args = process.argv.slice(2);
const targets = args.length ? args.map((a) => [path.resolve(a), a]) : REPO.map((r) => [path.join(ROOT, r), r]).filter(([p]) => fs.existsSync(p));
for (const [p, l] of targets) walk(p, l);
const n = Object.values(counts).reduce((a, b) => a + b, 0) + hits.filter((h) => /unreadable entry/.test(h)).length;
console.log('neutrality gate: ' + files.toLocaleString('en-US') + ' files read (' + Math.round(bytes / 1048576).toLocaleString('en-US') + ' MB of text) in ' + targets.map((t) => t[1]).join(', '));
if (!n) { console.log('neutrality gate: PASS, 0 hits'); process.exit(process.exitCode || 0); }
for (const h of hits) console.log('  ' + h);
console.log('neutrality gate: FAIL, ' + n + ' hit(s): ' + Object.entries(counts).map(([k, v]) => k + ' ' + v).join(', '));
process.exit(1);
