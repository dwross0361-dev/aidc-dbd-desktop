#!/usr/bin/env node
// AIDC DBD site runner (DWR Networking). Builds a design's Final Blueprint package and its .run off the browser.
//
//   node aidc-runner.js --html AIDC-DBD-Hyperscale-HS-v1.05.html --design ACME-LAX-site-runner.json --out ./ACME-LAX
//
// The design arrives as a file only (exported from the builder's Site runner button). The runner loads the SAME builder
// release in a headless page, rebuilds the design from the file, and refuses to build unless the design digest matches the
// the browser exported (the design fingerprint). It then builds the Final package and the .run, checks that every device configuration in the
// package is byte-identical to the one in the .run, and writes a run log. Nothing here reaches the network.
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib'), { spawnSync } = require('child_process');

// ---- arguments; re-exec with the heap the workstation allows (the reason this runs off the browser)
const argv = process.argv.slice(2), arg = (k, d) => { const i = argv.indexOf('--' + k); return i >= 0 ? argv[i + 1] : d; };
const HEAP = +arg('heap', 0) || Math.max(8192, Math.floor(require('os').totalmem() / 1048576 * 0.75));
if (!process.env.AIDC_RUNNER_CHILD) {
  const r = spawnSync(process.execPath, ['--max-old-space-size=' + HEAP, __filename].concat(argv), { stdio: 'inherit', env: Object.assign({}, process.env, { AIDC_RUNNER_CHILD: '1' }) });
  process.exit(r.status == null ? 1 : r.status);
}
const HTML = arg('html'), DESIGN = arg('design'), OUT = arg('out', '.');
const PARITY_ONLY = argv.includes('--parity-only');
if (!PARITY_ONLY && (!HTML || !DESIGN)) { console.error('usage: node aidc-runner.js --html <builder .html> --design <site-runner .json> --out <folder> [--heap MB] [--package-only|--run-only|--parity-only] [--review] [--settings [--racks N]]'); process.exit(2); }
const RACKS = +arg('racks', 0) || 0, SETTINGS = argv.includes('--settings') || RACKS > 0;
const REVIEW = argv.includes('--review');
const ONLY = argv.includes('--package-only') ? 'package' : argv.includes('--run-only') ? 'run' : 'both';
const { JSDOM, VirtualConsole } = require('jsdom');
const NC = require('@napi-rs/canvas');

const T0 = Date.now(), t = () => ((Date.now() - T0) / 1000).toFixed(0).padStart(5) + 's';
const mem = () => { const m = process.memoryUsage(); return Math.round(m.heapUsed / 1048576) + ' MB heap, ' + Math.round(m.rss / 1048576) + ' MB RSS'; };
const logLines = [];
const log = (...a) => { const l = t() + '  ' + a.join(' '); logLines.push(l); console.log(l); };
const die = (m) => { log('STOPPED: ' + m); try { fs.mkdirSync(OUT, { recursive: true }); fs.writeFileSync(path.join(OUT, 'site-runner.log'), logLines.join('\n') + '\n'); } catch (e) {} process.exit(1); };

// ---- the page: the builder release, with the browser services it uses provided by Node
const captured = [];   // {name, blob}
function shim(w) {
  const store = new Map(); let n = 0;
  w.__AIDC_HEADLESS = true;   // the builder skips drawing its main canvas
  w.Blob = Blob; w.Response = Response; w.CompressionStream = CompressionStream; w.DecompressionStream = DecompressionStream;
  w.TextEncoder = TextEncoder; w.TextDecoder = TextDecoder; w.ReadableStream = ReadableStream; w.WritableStream = WritableStream; w.TransformStream = TransformStream;
  w.URL.createObjectURL = (b) => { const u = 'blob:aidc-runner/' + (++n); store.set(u, b); return u; };
  w.URL.revokeObjectURL = (u) => { store.delete(u); };   // released as the page releases it (the blobs are the package's memory)
  const grab = (a) => { const u = a.href, nm = a.download; if (nm && store.has(u)) { captured.push({ name: nm, blob: store.get(u) }); } };
  w.HTMLAnchorElement.prototype.click = function () { grab(this); };
  const de = w.HTMLAnchorElement.prototype.dispatchEvent;
  w.HTMLAnchorElement.prototype.dispatchEvent = function (ev) { if (ev && ev.type === 'click') { grab(this); return true; } return de.call(this, ev); };
  w.confirm = () => true; w.alert = () => {}; w.prompt = (m, d) => d;
  w.performance.memory = { jsHeapSizeLimit: HEAP * 1048576 };
  // canvas: a Skia canvas per <canvas>; images decoded by the same library (the topology PDFs rasterize SVG)
  w.HTMLCanvasElement.prototype.getContext = function (k) { if (k !== '2d') return null;
    if (!this._nc || this._nc.width !== (this.width || 300) || this._nc.height !== (this.height || 150)) this._nc = NC.createCanvas(this.width || 300, this.height || 150);
    const ctx = this._nc.getContext('2d'), di = ctx.drawImage.bind(ctx);
    ctx.drawImage = function (img, ...r) { return di(img && img._n ? img._n : (img && img._nc ? img._nc : img), ...r); };
    return ctx; };
  w.HTMLCanvasElement.prototype.toDataURL = function (type) { if (!this._nc) this.getContext('2d'); return this._nc.toDataURL(type || 'image/png'); };
  class RImage { constructor() { this.onload = null; this.onerror = null; this.width = 0; this.height = 0; }
    set src(v) { this._src = v; const self = this;
      (async () => { try { const m = /^data:([^;,]+)(;base64)?,([\s\S]*)$/.exec(String(v)); if (!m) throw new Error('not a data URL');
          const buf = m[2] ? Buffer.from(m[3], 'base64') : Buffer.from(decodeURIComponent(m[3]));
          const im = await NC.loadImage(buf); self._n = im; self.width = im.width; self.height = im.height; self.naturalWidth = im.width; self.naturalHeight = im.height;
          setTimeout(() => { if (self.onload) self.onload(); }, 0); }
        catch (e) { setTimeout(() => { if (self.onerror) self.onerror(e); }, 0); } })(); }
    get src() { return this._src; } }
  w.Image = RImage;
}
const vc = new VirtualConsole(); const pageErr = [];
vc.on('jsdomError', (e) => { const m = String(e && e.message || e); if (/Not implemented: (navigation|window\.scrollTo|HTMLCanvasElement\.prototype\.getContext)/.test(m)) return; pageErr.push(m.slice(0, 300)); });
vc.on('error', (e) => pageErr.push(String(e).slice(0, 300)));

(async () => {
  if (PARITY_ONLY) {   // re-check a finished output folder without rebuilding: the newest package zip and .run in --out
    const pick = (re) => fs.readdirSync(OUT).filter((n) => re.test(n)).map((n) => path.join(OUT, n)).sort((x, y) => fs.statSync(y).mtimeMs - fs.statSync(x).mtimeMs)[0];
    const pkg = pick(/-Blueprint-(Final|Review)\.zip$/), run = pick(/\.run$/);
    if (!pkg || !run) die(OUT + ' needs a -Blueprint-Final.zip (or -Review.zip) and a .run');
    log('AIDC DBD site runner -- parity only: ' + path.basename(pkg) + ' and ' + path.basename(run));
    checkParity(pkg, run); log('done: parity only');
    fs.writeFileSync(path.join(OUT, 'site-runner-parity.log'), logLines.join('\n') + '\n'); process.exit(0);
  }
  log('AIDC DBD site runner -- node ' + process.version + ', heap limit ' + HEAP + ' MB');
  const file = JSON.parse(fs.readFileSync(DESIGN, 'utf8'));
  if (file.format !== 'aidc-dbd-site-runner/1') die(DESIGN + ' is not a site-runner file (export it with the Site runner button in the builder)');
  log('design ' + path.basename(DESIGN) + ': build ' + file.build + ', design fingerprint ' + file.fingerprint + ', ' + file.racks + ' racks, ' + Number(file.links).toLocaleString('en-US') + ' links, exported from ' + file.builder);
  const dom = new JSDOM(fs.readFileSync(HTML, 'utf8'), { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-runner/builder.html', virtualConsole: vc, beforeParse: shim });
  const w = dom.window;
  await new Promise((r) => setTimeout(r, 1500));
  const R = w.__aidcRunner;
  if (!R) die(path.basename(HTML) + ' has no site-runner entry points (a builder release before HS-v1.05)' + (pageErr.length ? ': ' + pageErr[0] : ''));
  log('builder ' + R.app() + ' ' + R.version() + ' loaded');
  if (SETTINGS) {   // built from the settings alone: there is no approved fingerprint to hold it to
    if (RACKS) { file.design.ctrls = Object.assign({}, file.design.ctrls, { rackcount: String(RACKS) }); log('settings: rack count set to ' + RACKS + ' (the file was ' + file.racks + ')'); }
    if (R.version() !== file.builder) log('settings: the file is from ' + file.builder + ', building it in ' + R.version());
  } else if (R.version() !== file.builder) die('the design was exported from ' + file.builder + ' but ' + path.basename(HTML) + ' is ' + R.version() + ': use the same release');

  // ---- rebuild and the digest gate
  let L; try { L = R.load(file, { settings: SETTINGS }); } catch (e) { die('the design did not load: ' + e.message); }
  log('rebuilt: ' + L.racks + ' racks, ' + L.devices.toLocaleString('en-US') + ' devices, ' + L.links.toLocaleString('en-US') + ' links (' + mem() + ')');
  if (SETTINGS) log('BUILT FROM SETTINGS: design fingerprint ' + L.fingerprint + '. No approved design is behind this build; for testing only, never for delivery.');
  else if (!L.match) die('design fingerprint ' + L.fingerprint + ' does not match the exported ' + L.expected + '. The runner rebuilds a design from its settings; hand edits on the canvas are not carried. Nothing was built.');
  if (!SETTINGS) log('design fingerprint ' + L.fingerprint + ' matches the exported design: building build ' + L.build);
  fs.mkdirSync(OUT, { recursive: true });

  // ---- a status line every 15 s (the page reports its steps on its status line)
  let last = ''; const tick = setInterval(() => { const s = R.status(); if (s !== last) { last = s; log('  ' + s.slice(0, 160) + '  (' + mem() + ')'); } }, 15000);
  const waitFile = async (re, ms) => { const end = Date.now() + ms; while (Date.now() < end) { const f = captured.find((c) => re.test(c.name)); if (f) return f; await new Promise((r) => setTimeout(r, 1000)); } return null; };
  const save = async (f) => { const p = path.join(OUT, (SETTINGS ? 'TEST-FROM-SETTINGS-' : '') + f.name.replace(/[^A-Za-z0-9._-]/g, '_')); fs.writeFileSync(p, Buffer.from(await f.blob.arrayBuffer())); f.blob = null; return p; };

  let pkg = null, run = null, planDg = '';
  if (ONLY !== 'run') {
    log((REVIEW ? 'Review' : 'Final') + ' Blueprint package...');
    const res = await R.blueprint(REVIEW ? 'review' : 'final');
    const f = await waitFile(/-Blueprint-(Final|Review)\.zip$/, 60000);
    if (!f) die('the ' + (REVIEW ? 'Review' : 'Final') + ' package did not complete' + (res && res.errors && res.errors.length ? ': ' + res.errors.join('; ') : ''));
    pkg = await save(f); try { const rd = readZip(fs.readFileSync(pkg)).get('00-README.md'); const m = rd && /Design digest \| ([0-9a-f]+)/.exec(rd().toString('utf8')); if (m) planDg = m[1]; } catch (e) {}
    log((REVIEW ? 'Review' : 'Final') + ' package: ' + path.basename(pkg) + ', ' + (res && res.files ? res.files.length : '?') + ' files' + (res && res.errors && res.errors.length ? ', ' + res.errors.length + ' export(s) failed: ' + res.errors.join('; ') : '') + ' (' + mem() + ')');
    if (res && res.errors && res.errors.length) die('the package has failed exports; it is written but must not be delivered');
  }
  if (ONLY !== 'package') {
    log('deployment bundle (.run)...');
    R.run();
    const f = await waitFile(/\.run$/, 6 * 3600 * 1000);
    if (!f) die('the .run did not complete');
    run = await save(f); log('.run: ' + path.basename(run) + ' (' + mem() + ')');
  }
  clearInterval(tick);

  // ---- parity: every device configuration in the package byte-identical to the one the .run installs
  if (pkg && run) checkParity(pkg, run);
  if (pageErr.length) log('page messages: ' + pageErr.slice(0, 5).join(' | '));
  log('done: build ' + L.build + ', design fingerprint ' + L.fingerprint + (planDg ? ', plan digest ' + planDg : ''));
  fs.writeFileSync(path.join(OUT, 'site-runner.log'), logLines.join('\n') + '\n');
  process.exit(0);
})().catch((e) => die(String(e && e.stack || e)));

// parity: every device configuration in the package byte-identical to the one the .run installs
function checkParity(pkg, run) {
    const P = readZip(fs.readFileSync(pkg)), RZ = readZip(runPayload(fs.readFileSync(run)));
    const runBy = {}; for (const [n, e] of RZ) runBy[n.indexOf('/') >= 0 ? n.slice(n.indexOf('/') + 1) : n] = e;
    const map = (n) => { n = n.slice('06-device-configs/'.length); if (n.startsWith('back-end/')) return 'configs/' + n.slice(9); if (n.startsWith('front-end/')) return 'configs/frontend/' + n.slice(10);
      if (n.startsWith('out-of-band/')) return 'configs/mgmt/' + n.slice(12); if (n.startsWith('services/')) return n.slice(9); return null; };
    let tot = 0, same = 0; const bad = [];
    for (const [n, e] of P) { if (!n.startsWith('06-device-configs/') || (n.endsWith('README.md') && n.split('/').length === 2)) continue;
      const k = map(n); if (k == null) continue; tot++; const r = runBy[k];
      if (!r) { bad.push('missing in .run: ' + n); continue; }
      if (Buffer.compare(e(), r()) === 0) same++; else bad.push('differs: ' + n); }
    log('parity: ' + same + ' of ' + tot + ' device configurations identical in the package and the .run' + (bad.length ? ' -- ' + bad.slice(0, 5).join('; ') : ''));
    if (bad.length || !tot) die('package and .run disagree: do not deliver');
}

// a minimal zip reader (stored and deflate), entries -> lazy byte readers. Walks the central directory by its size, so an
// archive past 65,535 entries reads whole whether or not it carries ZIP64 end records (the entry count is 16 bits).
function readZip(buf) {
  let e = buf.length - 22; while (e >= 0 && buf.readUInt32LE(e) !== 0x06054b50) e--;
  if (e < 0) throw new Error('not a zip');
  let cdSize = buf.readUInt32LE(e + 12), cdOff = buf.readUInt32LE(e + 16);
  if (e >= 20 && buf.readUInt32LE(e - 20) === 0x07064b50) { const z = Number(buf.readBigUInt64LE(e - 12));
    if (buf.readUInt32LE(z) === 0x06064b50) { cdSize = Number(buf.readBigUInt64LE(z + 40)); cdOff = Number(buf.readBigUInt64LE(z + 48)); } }
  const out = new Map(); let p = cdOff; const end = cdOff + cdSize;
  while (p < end && buf.readUInt32LE(p) === 0x02014b50) {
    const method = buf.readUInt16LE(p + 10), nlen = buf.readUInt16LE(p + 28), xlen = buf.readUInt16LE(p + 30), clen = buf.readUInt16LE(p + 32);
    let csize = buf.readUInt32LE(p + 20), usize = buf.readUInt32LE(p + 24), off = buf.readUInt32LE(p + 42);
    const name = buf.slice(p + 46, p + 46 + nlen).toString('utf8');
    for (let x = p + 46 + nlen, xe = x + xlen; x + 4 <= xe; ) { const id = buf.readUInt16LE(x), sz = buf.readUInt16LE(x + 2); let q = x + 4;
      if (id === 1) { if (usize === 0xFFFFFFFF) { usize = Number(buf.readBigUInt64LE(q)); q += 8; } if (csize === 0xFFFFFFFF) { csize = Number(buf.readBigUInt64LE(q)); q += 8; } if (off === 0xFFFFFFFF) { off = Number(buf.readBigUInt64LE(q)); } }
      x += 4 + sz; }
    p += 46 + nlen + xlen + clen;
    out.set(name, () => { const ln = buf.readUInt16LE(off + 26), lx = buf.readUInt16LE(off + 28), s = off + 30 + ln + lx, d = buf.slice(s, s + csize); return method === 8 ? zlib.inflateRawSync(d) : d; });
  }
  return out;
}

// the .run's payload: base64 lines after the marker, decoded in slices (no single string the size of the payload)
function runPayload(body) {
  const mk = body.indexOf('\n__PAYLOAD_BELOW__\n'); if (mk < 0) throw new Error('no payload marker in the .run');
  const parts = []; let acc = Buffer.alloc(4 * 1024 * 1024), n = 0;
  const flush = (all) => { const k = all ? n : n - (n % 4); if (!k) return; parts.push(Buffer.from(acc.slice(0, k).toString('latin1'), 'base64')); acc.copy(acc, 0, k, n); n -= k; };
  for (let i = mk + 19; i < body.length; i++) { const c = body[i]; if (c === 10 || c === 13 || c === 32 || c === 9) continue; acc[n++] = c; if (n === acc.length) flush(false); }
  flush(true); return Buffer.concat(parts);
}
