#!/usr/bin/env node
// AIDC DBD Layout service (DWR Networking), 2.0.20 (progress stages 2.0.21): the DC Layout Builder's data past 512 racks.
// A Chromium window has about 4 GB of JavaScript memory whatever the app asks for, and a 1,625-rack design with its two
// million links does not fit beside the builder or the Layout Builder's own drawing. This Node process rebuilds the design
// from its site-runner file (fingerprint checked, as the site runner does), builds the physical model, and holds it in the
// Layout Builder's own code (the same page, headless). The Layout window keeps the cabinets and devices and asks here for
// the links of a device or a cabinet, the site-wide checks and schedule, and the site-wide files. Nothing reaches the network.
'use strict';
const fs = require('fs'), path = require('path');
const { JSDOM, VirtualConsole } = require('jsdom');

const argv = process.argv.slice(2);
const arg = (k, d) => { const i = argv.indexOf('--' + k); return i >= 0 && argv[i + 1] != null ? argv[i + 1] : d; };
const HTML = arg('html');
if (!HTML || !process.send) { console.error('usage: started by AIDC DBD with an IPC channel: node aidc-layout.js --html <builder .html>'); process.exit(2); }
const T0 = Date.now(), secs = () => Math.round((Date.now() - T0) / 1000);
const mem = () => Math.round(process.memoryUsage().heapUsed / 1048576).toLocaleString('en-US') + ' MB';
const log = (m) => console.log(new Date().toISOString() + '  ' + m + ' (heap ' + mem() + ')');
// 2.0.21: progress is structured -- the stage, its text, counts, and the time estimates the window's progress panel uses
let lastP = 0;
const progress = (t, x) => { const e = Object.assign({ text: t }, x || {}); if (!e.quiet) log(t);
  const now = Date.now(); if (e.throttle && now - lastP < 300) return; lastP = now; delete e.throttle; delete e.quiet;
  try { process.send({ progress: e }); } catch (err) {} };
// seconds per design link, measured on a 1,625-rack design (rebuild 29 us, physical model 50 us, placement 2 us per link);
// RATE scales them to this workstation once the rebuild has been timed
let RATE = 1;
const estOf = (n, rate) => ({ load: 3, rebuild: Math.max(2, n * 29e-6 * rate), model: Math.max(2, n * 50e-6 * rate), place: Math.max(1, n * 2.2e-6 * rate) });

let SRC = null, SRC_SHA = '';   // the builder release, read once, and its SHA-256 (app 2.0.51)
function src() { if (!SRC) { const b = fs.readFileSync(HTML); SRC_SHA = require('crypto').createHash('sha256').update(b).digest('hex'); SRC = b.toString('utf8'); } return SRC; }
function embedded(id) {   // a script the builder carries: the Layout Builder page (base64) and the PDF library
  const s = src(), at = s.indexOf('id="' + id + '"'); if (at < 0) return null;
  const a = s.indexOf('>', at) + 1, b = s.indexOf('</script>', a); return s.slice(a, b);
}
function shim(w) {
  w.__AIDC_HEADLESS = true; w.__aidcBuilderSha256 = SRC_SHA; w.alert = () => {}; w.confirm = () => true; w.prompt = (m, d) => d;
  w.TextEncoder = TextEncoder; w.TextDecoder = TextDecoder; w.Blob = Blob;
  w.HTMLCanvasElement.prototype.getContext = function () { return null; };
  w.URL.createObjectURL = () => 'blob:aidc-layout/x'; w.URL.revokeObjectURL = () => {};
}

// ---- the design -> the physical model, in a builder page of its own (closed afterwards: only the model stays)
async function buildModel(file) {
  if (!file || file.format !== 'aidc-dbd-site-runner/1') throw new Error('the builder window did not send a site-runner design');
  const nL = Number(file.links || 0);
  progress('Loading the builder (' + Number(file.racks || 0).toLocaleString('en-US') + ' racks)…', { stage: 'load', est: estOf(nL, RATE) });
  const errs = [], vc = new VirtualConsole(); vc.on('jsdomError', (e) => { const m = String(e && e.message || e); if (!/Not implemented/.test(m)) errs.push(m.slice(0, 200)); });
  const dom = new JSDOM(src(), { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-layout/builder.html', virtualConsole: vc, beforeParse: shim });
  try {
    await new Promise((r) => setTimeout(r, 1500));
    const R = dom.window.__aidcRunner;
    if (!R || typeof R.phys !== 'function') throw new Error('this builder release has no Layout service entry point (HS-v1.26 or later)' + (errs.length ? ': ' + errs[0] : ''));
    if (R.version() !== file.builder) throw new Error('the design comes from ' + file.builder + ' but the app carries ' + R.version());
    if (file.builderSha256 && file.builderSha256 !== SRC_SHA) throw new Error('release check: the design comes from builder ' + file.builder + ' with SHA-256 ' + file.builderSha256 + ', but the app carries a builder with SHA-256 ' + SRC_SHA);
    progress('Rebuilding the design from its settings…', { stage: 'rebuild' });
    const t1 = Date.now(), L = R.load(file, {});
    if (nL > 20000) { RATE = Math.min(20, Math.max(0.05, ((Date.now() - t1) / 1000) / (nL * 29e-6))); }   // this workstation against the reference
    if (!L.match) throw new Error('the rebuilt design (fingerprint ' + L.fingerprint + ') is not the one in the builder window (' + L.expected + '). The Layout service rebuilds the design from its settings; hand edits on the canvas are not carried. Save the design, load it again, then open the DC Layout Builder.');
    progress('Building the physical model (' + L.devices.toLocaleString('en-US') + ' devices, ' + L.links.toLocaleString('en-US') + ' design links)…', { stage: 'model', devices: L.devices, est: estOf(nL, RATE) });
    return R.phys();
  } finally { try { dom.window.close(); } catch (e) {} }
}

// ---- the Layout Builder page, headless, holding the model
let PAGE = null, OUT = '.', LAST = null, EXPORT_N = 0;
function uniquePath(dir, name) {   // every version kept: name, name (2), name (3) ...
  let p = path.join(dir, name); if (!fs.existsSync(p)) return p;
  const ext = path.extname(name), base = name.slice(0, name.length - ext.length);
  for (let i = 2; ; i++) { p = path.join(dir, base + ' (' + i + ')' + ext); if (!fs.existsSync(p)) return p; }
}
function page() {
  if (PAGE) return PAGE.window.__dctRemote;
  const b64 = embedded('dct-src'); if (!b64) throw new Error('the builder release carries no DC Layout Builder');
  let html = Buffer.from(b64.trim(), 'base64').toString('utf8');
  html = html.replace('__DEMO__', 'null').replace('<script>window.__DCT_LAUNCH=true;</script>', '<script>window.__DCT_SERVICE=true;window.__DCT_HEADLESS=true;</script>');
  const pdf = embedded('jspdf-src'); if (pdf) html = html.replace('</head>', () => '<script>' + pdf + '</script></head>');
  const vc = new VirtualConsole(); vc.on('jsdomError', () => {});
  PAGE = new JSDOM(html, { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-layout/layout.html', virtualConsole: vc, beforeParse: shim });
  const w = PAGE.window;
  w.__dctOut = (name, data) => { const p = uniquePath(OUT, name); fs.writeFileSync(p, typeof data === 'string' ? data : Buffer.from(data)); w.__dctLast = LAST = p; };
  w.__dctOpen = (name) => { const p = uniquePath(OUT, name), fd = fs.openSync(p, 'w'); let rows = 0;
    return { write: (t) => { fs.writeSync(fd, t); let i = -1; while ((i = t.indexOf('\n', i + 1)) >= 0) rows++; progress('Writing ' + name, { stage: 'export', done: rows, total: EXPORT_N, throttle: true, quiet: true }); },
      close: () => { fs.closeSync(fd); w.__dctLast = LAST = p; return p; } }; };
  w.__dctTick = (i, n) => progress('Writing the PDF', { stage: 'export', done: i, total: n, throttle: true, quiet: true });
  if (!w.__dctRemote) throw new Error('the DC Layout Builder page has no service side (HS-v1.26 or later)');
  return w.__dctRemote;
}
const plain = (x) => JSON.parse(JSON.stringify(x));   // objects of the page's realm, as plain data for the channel

const OPS = {
  async start(a) { const D = page(), m = await buildModel(a.file); progress('Placing ' + m.cabinets.length.toLocaleString('en-US') + ' cabinets and checking ' + m.links.length.toLocaleString('en-US') + ' links…', { stage: 'place', cabinets: m.cabinets.length, links: m.links.length });
    const r = D.load(m, false); progress('Ready: ' + m.cabinets.length.toLocaleString('en-US') + ' cabinets, ' + m.links.length.toLocaleString('en-US') + ' links (' + secs() + ' s)', { stage: 'ready', cabinets: m.cabinets.length, links: m.links.length }); return plain({ head: r.head, note: '' }); },
  async refresh(a) { const D = page(), m = await buildModel(a.file); progress('Applying the refreshed model, keeping your edits…', { stage: 'place', cabinets: m.cabinets.length, links: m.links.length });
    const r = D.load(m, true); progress('Ready: ' + m.cabinets.length.toLocaleString('en-US') + ' cabinets, ' + m.links.length.toLocaleString('en-US') + ' links', { stage: 'ready', cabinets: m.cabinets.length, links: m.links.length }); return plain({ head: r.head, note: r.note }); },
  sync(a) { return plain(page().sync(a)); },
  links(a) { return plain(page().links(a)); },
  cable(a) { return plain(page().cable(a)); },
  export(a) { OUT = a.out || OUT; EXPORT_N = page().counts().links; progress('Writing all links (' + a.kind.toUpperCase() + ')…', { stage: 'export', done: 0, total: EXPORT_N }); LAST = null; const p = page().exportAll(a.kind); if (!p && !LAST) throw new Error('nothing was written'); return { path: p || LAST }; },
  save(a) { OUT = a.out || OUT; LAST = null; page().save(); if (!LAST) throw new Error('nothing was written'); return { path: LAST }; },
  openFile(a) {
    const st = fs.statSync(a.path); if (st.size > 400 * 1048576) throw new Error(path.basename(a.path) + ' is ' + Math.round(st.size / 1048576) + ' MB. A large-site layout saved here is small (its links come from the design); a full physical model this size is opened by refreshing from the Fabric Designer instead.');
    const obj = JSON.parse(fs.readFileSync(a.path, 'utf8')), D = page();
    if (!obj || obj.schema !== 'gpu-fabric-physical/1') throw new Error(path.basename(a.path) + ' is not a DC Layout Builder model or layout (schema gpu-fabric-physical/1)');
    if (obj.linksOmitted) { if (!PAGE.window.__dct || !PAGE.window.__dct.rows) throw new Error('no model is loaded yet'); const r = D.layoutOnto(obj);
      return plain({ head: r.head, note: 'Layout applied: geometry, cabinet order and device positions from ' + path.basename(a.path) + ', onto the design the Fabric Designer sent.' }); }
    const r = D.load(obj, false); return plain({ head: r.head, note: '' });
  },
  counts() { return plain(page().counts()); }
};

process.on('message', async (msg) => {
  if (!msg || msg.id == null) return;
  const t = Date.now();
  try { const f = OPS[msg.op]; if (!f) throw new Error('unknown request ' + msg.op);
    const res = await f(msg.args || {});
    if (Date.now() - t > 2000) log(msg.op + ' took ' + ((Date.now() - t) / 1000).toFixed(1) + ' s');
    process.send({ id: msg.id, res });
  } catch (e) { log(msg.op + ' failed: ' + (e && e.stack || e)); process.send({ id: msg.id, err: String(e && e.message || e) }); }
});
process.on('disconnect', () => process.exit(0));
log('AIDC DBD Layout service: node ' + process.version + ', builder ' + path.basename(HTML));
