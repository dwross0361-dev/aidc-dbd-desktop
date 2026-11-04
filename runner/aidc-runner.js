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
// Keep the machine awake for the length of the run (2.0.13 / runner 1.4.0): system sleep suspends the build. The holder is a
// separate process that watches this one, so the hold ends when the runner ends, however it ends. Run from the desktop app,
// the app holds it instead (AIDC_RUNNER_CHILD is set there).
function holdSleep(pid) {
  const { spawn } = require('child_process');
  try {
    if (process.platform === 'win32') {
      const ps = "$k=Add-Type -PassThru -Name K -Namespace AidcDbd -MemberDefinition '[DllImport(\"kernel32.dll\")] public static extern uint SetThreadExecutionState(uint f);';" +
        " [void]$k::SetThreadExecutionState(0x80000001); while (Get-Process -Id " + pid + " -ErrorAction SilentlyContinue) { Start-Sleep -Seconds 15 }";
      return { how: 'Windows execution state', c: spawn('powershell.exe', ['-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-Command', ps], { stdio: 'ignore', windowsHide: true }) };
    }
    if (process.platform === 'darwin') return { how: 'caffeinate', c: spawn('caffeinate', ['-i', '-w', String(pid)], { stdio: 'ignore' }) };
    if (spawnSync('systemd-inhibit', ['--what=sleep', '--mode=block', 'true'], { stdio: 'ignore' }).status !== 0) return null;   // present AND permitted (logind reachable)
    return { how: 'systemd-inhibit', c: spawn('systemd-inhibit', ['--what=sleep:idle', '--who=AIDC DBD site runner', '--why=building a site package', '--mode=block', 'tail', '--pid=' + pid, '-f', '/dev/null'], { stdio: 'ignore' }) };
  } catch (e) { return null; }
}
if (!process.env.AIDC_RUNNER_CHILD) {
  const hold = argv.includes('--no-sleep-hold') ? null : holdSleep(process.pid);
  if (hold) { hold.c.on('error', () => {}); console.log('Sleep blocked for the length of the run (' + hold.how + ').'); }
  else console.log('Sleep not blocked' + (argv.includes('--no-sleep-hold') ? ' (--no-sleep-hold).' : ': no sleep inhibitor available on this host.'));
  const r = spawnSync(process.execPath, ['--max-old-space-size=' + HEAP, __filename].concat(argv), { stdio: 'inherit', env: Object.assign({}, process.env, { AIDC_RUNNER_CHILD: '1' }) });
  if (hold) { try { hold.c.kill(); } catch (e) {} console.log('Sleep released.'); }
  process.exit(r.status == null ? 1 : r.status);
}
const HTML = arg('html'), DESIGN = arg('design'), OUT = arg('out', '.');
// 1.11.0 (app 2.0.52, builder HS-v1.58): the customer input sheet. --inputs builds from a filled sheet (.xlsx or long-format CSV)
// instead of a site-runner file; --input-template writes the template (blank, or filled from --design).
const INPUTS = arg('inputs', null), TEMPLATE = arg('input-template', null);
// 1.10.0 (app 2.0.51, builder HS-v1.57): the release is the builder FILE, identified by its SHA-256. The version string
// alone let two different files pass as one release (HS-v1.56 r1, r2 and r3 all said HS-v1.56).
let HTML_BYTES = null, HTML_SHA = '';
const PARITY_ONLY = argv.includes('--parity-only');
if (!PARITY_ONLY && (!HTML || (!DESIGN && !INPUTS && !TEMPLATE))) { console.error('usage: node aidc-runner.js --html <builder .html> (--design <site-runner .json> | --inputs <input sheet .xlsx|.csv>) --out <folder> [--heap MB] [--package-only|--run-only|--parity-only] [--review] [--settings [--racks N]] [--cable-matrix xlsx|pdf] [--no-sleep-hold]\n       node aidc-runner.js --html <builder .html> --input-template <file.xlsx> [--design <site-runner .json> [--settings]]'); process.exit(2); }
if (INPUTS && DESIGN) { console.error('--inputs builds the design from the input sheet: give it without --design'); process.exit(2); }
if (INPUTS && (argv.includes('--settings') || argv.includes('--racks'))) { console.error('--inputs takes the rack count and every setting from the input sheet: give it without --settings or --racks'); process.exit(2); }
const RACKS = +arg('racks', 0) || 0, SETTINGS = argv.includes('--settings') || RACKS > 0;
const REVIEW = argv.includes('--review');
const ONLY = argv.includes('--package-only') ? 'package' : argv.includes('--run-only') ? 'run' : 'both';
const CABLE = arg('cable-matrix', null);   // 1.6.0 (app 2.0.20): the builder's Cable Matrix past 512 racks, measured here
const { JSDOM, VirtualConsole } = require('jsdom');
const NC = require('@napi-rs/canvas');

const T0 = Date.now(), t = () => ((Date.now() - T0) / 1000).toFixed(0).padStart(5) + 's';
const mem = () => { const m = process.memoryUsage(); return Math.round(m.heapUsed / 1048576) + ' MB heap, ' + Math.round(m.rss / 1048576) + ' MB RSS'; };
const logLines = [];
// 1.8.0 (app 2.0.41), 1.9.0 (app 2.0.44): the watchdog. A builder step runs as one stretch of JavaScript; while it runs, the status line cannot
// change, so a step that takes hours looks the same as one that has stopped. When the main thread has been busy for 3 minutes,
// a worker thread asks the debugger where it is and logs the call stack, then again every 3 minutes until the step ends.
// Off with --no-watchdog.
function startWatchdog() {
  if (argv.includes('--no-watchdog')) return;
  let W; try {
    const { Worker } = require('worker_threads'), hb = new Int32Array(new SharedArrayBuffer(4));
    const beat = () => Atomics.store(hb, 0, Math.floor((Date.now() - T0) / 1000)); beat(); setInterval(beat, 1000).unref();
    W = new Worker(`
const { parentPort, workerData } = require('worker_threads'), inspector = require('inspector'), fs = require('fs');
const { hb, T0, STALL } = workerData; let s = null, lastAt = 0, busy = false;
const el = () => Math.floor((Date.now() - T0) / 1000);
function line(blocked, txt) {
  const l = String(el()).padStart(5) + 's  watchdog: busy ' + (blocked >= 120 ? Math.round(blocked / 60) + ' min' : blocked + ' s') + ' in one step: ' + txt + '  (' + Math.round(process.memoryUsage().rss / 1048576) + ' MB RSS)';
  try { fs.writeSync(1, l + '\\n'); } catch (e) {} parentPort.postMessage(l); }
// 1.9.0: a pause lands only when the builder is running JavaScript. Inside one long native call (a large JSON or text
// encode, a sort's internals, garbage collection) it waits until that returns; until then each period says so, and the
// stack is logged when the pause lands. The session stays connected while a pause is pending, so the pause is always resumed.
function report(blocked) {
  if (busy) { line(blocked, 'inside one long native call or garbage collection (no JavaScript stack until it returns)'); return; }
  busy = true;
  try { if (!s) { s = new inspector.Session(); s.connectToMainThread(); } } catch (e) { busy = false; return; }
  const done = (txt) => { try { s.post('Debugger.disable'); s.disconnect(); } catch (e) {} s = null; busy = false; line(el() - Atomics.load(hb, 0), txt); };
  s.once('Debugger.paused', (m) => { let f = [];
    try { f = m.params.callFrames.slice(0, 14).map((c) => (c.functionName || '(anonymous)') + ':' + (c.location.lineNumber + 1)); } catch (e) {}
    try { s.post('Debugger.resume'); } catch (e) {} done(f.join(' < ') || 'no stack'); });
  s.post('Debugger.enable', (err) => { if (err) return done('debugger unavailable'); s.post('Debugger.pause', (e2) => { if (e2) done('pause refused'); }); });
}
setInterval(() => { const b = el() - Atomics.load(hb, 0); if (b >= STALL && el() - lastAt >= STALL) { lastAt = el(); report(b); } }, 5000);
`, { eval: true, workerData: { hb, T0, STALL: +process.env.AIDC_WATCHDOG_S || 180 } });   // AIDC_WATCHDOG_S: testing only
    W.on('message', (l) => logLines.push(l)); W.on('error', (e) => { logLines.push('watchdog stopped: ' + (e && e.message || e)); }); W.unref();
  } catch (e) { /* no worker threads: run without the watchdog */ }
}
const log = (...a) => { const l = t() + '  ' + a.join(' '); logLines.push(l); console.log(l); };
const die = (m) => { log('STOPPED: ' + m); try { fs.mkdirSync(OUT, { recursive: true }); fs.writeFileSync(path.join(OUT, 'site-runner.log'), logLines.join('\n') + '\n'); } catch (e) {} process.exit(1); };

// ---- the page: the builder release, with the browser services it uses provided by Node
const captured = [];   // {name, blob}
function shim(w) {
  const store = new Map(); let n = 0;
  w.__AIDC_HEADLESS = true;
  if (process.env.AIDC_CM_VOL) w.__cmVolPages = +process.env.AIDC_CM_VOL;   // test hook: smaller PDF volumes   // the builder skips drawing its main canvas
  // native hashes for the package manifest, the .run checksums and the zip CRCs (1.5.0; builder HS-v1.20 uses them, older ones ignore them)
  const crypto = require('crypto');
  w.__aidcSha256 = (b) => crypto.createHash('sha256').update(b).digest('hex');
  w.__aidcBuilderSha256 = HTML_SHA;   // 1.10.0: the builder file's own SHA-256 (its site-runner export and package README carry it)
  if (typeof zlib.crc32 === 'function') w.__aidcCrc32 = (u) => zlib.crc32(u) >>> 0;
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
  // runner 1.3.0: the cable-run measurement. The builder runs the DC Layout Builder in a hidden frame to measure every run; the
  // runner has no frames, so the Layout Builder gets a page of its own here (headless: it measures, it draws nothing).
  w.__aidcMeasure = (html, model) => {
    const lvc = new VirtualConsole(); lvc.on('jsdomError', () => {});
    const d = new JSDOM(html, { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-runner/layout.html', virtualConsole: lvc });
    try { const lw = d.window; if (typeof lw.__dctMeasureModel !== 'function') return null; return lw.__dctMeasureModel(model); }
    catch (e) { pageErr.push('layout measurement: ' + String(e && e.message || e).slice(0, 200)); return null; }
    finally { try { d.window.close(); } catch (e) {} }
  };
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
  const file = DESIGN ? JSON.parse(fs.readFileSync(DESIGN, 'utf8')) : null;
  if (file && file.format !== 'aidc-dbd-site-runner/1') die(DESIGN + ' is not a site-runner file (export it with the Site runner button in the builder)');
  if (file) log('design ' + path.basename(DESIGN) + ': build ' + file.build + ', design fingerprint ' + file.fingerprint + ', ' + file.racks + ' racks, ' + Number(file.links).toLocaleString('en-US') + ' links, exported from ' + file.builder);
  let SHEET = null;
  if (INPUTS) { try { SHEET = fs.readFileSync(INPUTS); } catch (e) { die('the input sheet ' + INPUTS + ' could not be read: ' + e.message); }
    log('input sheet ' + path.basename(INPUTS) + ': ' + SHEET.length.toLocaleString('en-US') + ' bytes, SHA-256 ' + require('crypto').createHash('sha256').update(SHEET).digest('hex')); }
  startWatchdog();
  HTML_BYTES = fs.readFileSync(HTML); HTML_SHA = require('crypto').createHash('sha256').update(HTML_BYTES).digest('hex');
  const dom = new JSDOM(HTML_BYTES.toString('utf8'), { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-runner/builder.html', virtualConsole: vc, beforeParse: shim });
  const w = dom.window;
  await new Promise((r) => setTimeout(r, 1500));
  const R = w.__aidcRunner;
  if (!R) die(path.basename(HTML) + ' has no site-runner entry points (a builder release before HS-v1.05)' + (pageErr.length ? ': ' + pageErr[0] : ''));
  log('builder ' + R.app() + ' ' + R.version() + ' loaded, SHA-256 ' + HTML_SHA);
  const needIn = (what) => { if (typeof R.inputs !== 'function') die(path.basename(HTML) + ' has no input sheet entry points (' + what + ' needs HS-v1.58 or later)'); };
  if (TEMPLATE && !file) {   // the blank template: generated from this builder's catalog
    needIn('--input-template');
    const u = await R.inputTemplate(false); fs.mkdirSync(path.dirname(path.resolve(TEMPLATE)), { recursive: true }); fs.writeFileSync(TEMPLATE, Buffer.from(u));
    log('input sheet template (blank): ' + TEMPLATE + ', ' + u.length.toLocaleString('en-US') + ' bytes'); process.exit(0); }
  const FSHA = file && typeof file.builderSha256 === 'string' ? file.builderSha256.toLowerCase() : '';
  if (INPUTS) {   // built from the input sheet: the design is made here, by this builder file, so there is no exported fingerprint to hold it to
    log('input sheet: the design is built from the sheet by this builder file (SHA-256 ' + HTML_SHA + ')');
  } else if (SETTINGS) {   // built from the settings alone: there is no approved fingerprint to hold it to
    if (RACKS) { file.design.ctrls = Object.assign({}, file.design.ctrls, { rackcount: String(RACKS) }); log('settings: rack count set to ' + RACKS + ' (the file was ' + file.racks + ')'); }
    if (R.version() !== file.builder || (FSHA && FSHA !== HTML_SHA)) log('settings: the file is from ' + file.builder + (FSHA ? ' (SHA-256 ' + FSHA + ')' : '') + ', building it in ' + R.version() + ' (SHA-256 ' + HTML_SHA + ')');
  } else if (!FSHA) die('release check: the design file names builder ' + file.builder + ' but carries no builder SHA-256, so the release cannot be checked. Export the site-runner file from the AIDC DBD app (HS-v1.57 or later), or use --settings for a test build. Nothing was built.');
  else if (FSHA !== HTML_SHA) die('release check: the design was exported from builder ' + file.builder + ' with SHA-256 ' + FSHA + ', but ' + path.basename(HTML) + ' (' + R.version() + ') has SHA-256 ' + HTML_SHA + '. Use the same builder file. Nothing was built.');
  else log('release check: the builder file is the one the design was exported from (SHA-256 ' + HTML_SHA + ')');

  // ---- rebuild and the digest gate
  let L, SI = null;
  if (INPUTS) {
    needIn('--inputs');
    try { SI = await R.inputs(new Uint8Array(SHEET), path.basename(INPUTS)); } catch (e) { die('the input sheet was not applied: ' + (e && e.message || e)); }
    L = { racks: SI.build.racks, devices: SI.build.devices, links: SI.build.links, fingerprint: SI.build.fingerprint, build: SI.build.build, match: true };
  } else { try { L = R.load(file, { settings: SETTINGS }); } catch (e) { die('the design did not load: ' + e.message); } }
  log('rebuilt: ' + L.racks + ' racks, ' + L.devices.toLocaleString('en-US') + ' devices, ' + L.links.toLocaleString('en-US') + ' links (' + mem() + ')');
  if (TEMPLATE) {   // filled from the design just rebuilt (the importer's round trip)
    needIn('--input-template');
    const u = await R.inputTemplate(true); fs.mkdirSync(path.dirname(path.resolve(TEMPLATE)), { recursive: true }); fs.writeFileSync(TEMPLATE, Buffer.from(u));
    log('input sheet filled from the design (fingerprint ' + L.fingerprint + '): ' + TEMPLATE); process.exit(0); }
  if (SI) {   // the record: the session input file (customer data, never in the delivery), the import report and the site-runner file
    const cnt = (st) => SI.report.filter((r) => r.status === st).length;
    log('input sheet applied: ' + cnt('applied') + ' applied, ' + cnt('derived') + ' derived, ' + cnt('nearest supported') + ' nearest supported (design choices), ' + cnt('not supported') + ' not supported, ' + cnt('not modeled') + ' not modeled; design fingerprint ' + L.fingerprint);
    for (const r of SI.report) if (r.status !== 'applied' && r.status !== 'derived') log('  ' + r.status + ': ' + r.row + ' ' + r.key + ' ' + JSON.stringify(r.requested) + (r.applied !== '' ? ' -> ' + JSON.stringify(r.applied) : '') + (r.reason ? ' (' + r.reason + ')' : ''));
    const cd = path.join(OUT, 'customer-data'); fs.mkdirSync(cd, { recursive: true });
    const pre = String(L.build || 'design').replace(/[^A-Za-z0-9._-]/g, '_');
    fs.writeFileSync(path.join(cd, pre + '-session-input.json'), JSON.stringify(SI, null, 1));
    const q = (v) => { v = String(v == null ? '' : v); return /[",\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v; };
    fs.writeFileSync(path.join(cd, pre + '-import-report.csv'), ['row,key,requested,unit,applied,status,reason,class,source'].concat(SI.report.map((r) => [r.row, r.key, r.requested, r.unit, r.applied, r.status, r.reason, r.class, r.source].map(q).join(','))).join('\n') + '\n');
    fs.writeFileSync(path.join(OUT, pre + '-site-runner.json'), JSON.stringify(R.siteRunner(), null, 1));
    log('session input file, import report: ' + path.join('customer-data', pre + '-session-input.json') + ', ' + pre + '-import-report.csv; site-runner file ' + pre + '-site-runner.json');
  }
  if (SETTINGS) log('BUILT FROM SETTINGS: design fingerprint ' + L.fingerprint + '. No approved design is behind this build; for testing only, never for delivery.');
  else if (!L.match) die('design fingerprint ' + L.fingerprint + ' does not match the exported ' + L.expected + '. The runner rebuilds a design from its settings; hand edits on the canvas are not carried. Nothing was built.');
  if (!SETTINGS && !SI) log('design fingerprint ' + L.fingerprint + ' matches the exported design: building build ' + L.build);
  if (SI) log('building build ' + L.build + ' from the input sheet');
  fs.mkdirSync(OUT, { recursive: true });

  // ---- a status line every 15 s (the page reports its steps on its status line)
  // 1.9.0: an error the page raises outside a step (a rejected promise nobody handled) is logged at once
  process.on('unhandledRejection', (r) => { pageErr.push('unhandled: ' + String(r && r.message || r).slice(0, 300)); });
  let pageSeen = pageErr.length;
  // 1.9.0: a status unchanged for 10 minutes is logged again ("still:"), so a step that yields while it works is not silent
  let last = '', lastAt = Date.now(); const tick = setInterval(() => { const s = R.status();
    if (s !== last) { last = s; lastAt = Date.now(); log('  ' + s.slice(0, 160) + '  (' + mem() + ')'); }
    else if (Date.now() - lastAt >= 600000) { lastAt = Date.now(); log('  still: ' + s.slice(0, 140) + '  (' + mem() + ')'); }
    while (pageSeen < pageErr.length) log('  page error: ' + pageErr[pageSeen++]); }, 15000);
  const waitFile = async (re, ms, failed) => { const end = Date.now() + ms; while (Date.now() < end) { const f = captured.find((c) => re.test(c.name)); if (f) return f; if (failed && failed()) return null; await new Promise((r) => setTimeout(r, 1000)); } return null; };
  const pageMsgs = () => (pageErr.length ? ' (page: ' + pageErr.slice(0, 3).join(' | ') + ')' : '');
  const save = async (f) => {   // 1.7.0 (app 2.0.30): a file the page handed over empty stops the run, saying so
    if (!f.blob || typeof f.blob.arrayBuffer !== 'function') die('the builder handed over ' + f.name + ' with no content' + (R.status() ? ': ' + R.status().slice(0, 200) : '') + pageMsgs());
    const p = path.join(OUT, (SETTINGS ? 'TEST-FROM-SETTINGS-' : '') + f.name.replace(/[^A-Za-z0-9._-]/g, '_')); fs.writeFileSync(p, Buffer.from(await f.blob.arrayBuffer())); f.blob = null; return p; };

  if (CABLE) {   // the Cable Matrix alone: every cable once, with its measured length
    if (typeof R.cable !== 'function') die(path.basename(HTML) + ' has no Cable matrix entry point (HS-v1.26 or later)');
    log('Cable matrix (' + (CABLE === 'pdf' ? 'PDF' : 'Excel') + '): placing cabinets and measuring every run...');
    // 1.7.0: a large PDF arrives as several files (one per build step, in volumes); each is written to disk as it arrives
    let fin = false, ferr = null; const saved = [];
    Promise.resolve().then(() => R.cable(CABLE)).then(() => { fin = true; }, (e) => { ferr = e; fin = true; });
    const drain = async () => { for (const c of captured) { if (c.saved || !/-cable-matrix.*\.(xlsx|pdf)$/.test(c.name)) continue; c.saved = true;
      const p = await save(c); saved.push(p); log('cable matrix: ' + path.basename(p) + ' (' + mem() + ')'); } };
    const end = Date.now() + 6 * 3600 * 1000;
    // the page's job ends when its status says Exported (the Excel workbook is written after the export call returns) or failed
    const over = () => /^Exported|^\u26a0|failed/i.test(R.status() || '');
    let finAt = 0;
    while (Date.now() < end) { if (fin && !finAt) finAt = Date.now(); if (fin && (over() || Date.now() - finAt > 600000)) break; await drain(); await new Promise((r) => setTimeout(r, 1000)); }
    await drain(); clearInterval(tick);
    const st = R.status() || '';
    if (ferr) die('the cable matrix failed: ' + (ferr && ferr.message || ferr) + (saved.length ? ' (' + saved.length + ' file(s) written before it stopped)' : '') + pageMsgs());
    if (!fin) die('the cable matrix did not finish in 6 hours' + (saved.length ? ' (' + saved.length + ' file(s) written)' : ''));
    if (/^⚠|failed/i.test(st)) die(st.slice(0, 300) + (saved.length ? ' (' + saved.length + ' file(s) written before it stopped)' : '') + pageMsgs());
    if (!saved.length) die('the cable matrix did not complete' + (st ? ': ' + st.slice(0, 200) : '') + pageMsgs());
    if (st) log(st.slice(0, 240));
    if (pageErr.length) log('page messages: ' + pageErr.slice(0, 5).join(' | '));
    log('done: cable matrix, ' + saved.length + ' file' + (saved.length > 1 ? 's' : '') + ', build ' + L.build + ', design fingerprint ' + L.fingerprint);
    fs.writeFileSync(path.join(OUT, 'site-runner.log'), logLines.join('\n') + '\n'); process.exit(0);
  }
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
    const f = await waitFile(/\.run$/, 6 * 3600 * 1000, () => !!w.__aidcBuildFailed);   // 1.9.0: a failed build ends the wait (builder HS-v1.52 sets it)
    if (!f) die('the .run did not complete' + (w.__aidcBuildFailed ? ': ' + String(w.__aidcBuildFailed).slice(0, 400) : R.status() ? ': ' + R.status().slice(0, 300) : '') + pageMsgs());
    run = await save(f); log('.run: ' + path.basename(run) + ' (' + mem() + ')');
    if (w.__aidcBuildWarn) die(String(w.__aidcBuildWarn).slice(0, 400));   // 1.9.0: written with a failed section (builder HS-v1.52)
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
