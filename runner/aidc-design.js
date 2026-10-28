#!/usr/bin/env node
// AIDC DBD Design service (DWR Networking), 2.0.24: the builder's heavy views past 512 racks.
// A builder window has about 4 GB of JavaScript memory; at 4,096 racks the design alone fills a large share of it, and a view
// that builds the whole physical model or management plan on top (Infra, BoM, Facility, Release check, the OOB link count,
// the Physical model and Day-2 SoT exports) runs it out. This Node process holds a copy of the design, rebuilt from the
// window's site-runner file (fingerprint checked, as the site runner does), runs those views in the builder's own code
// (the same page, headless) and hands the window the view as it stands; the window forwards clicks and changes made in it.
// Downloads are written to the project folder. Nothing reaches the network.
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib'), crypto = require('crypto');
const { JSDOM, VirtualConsole } = require('jsdom');
const { Readable } = require('stream'), { pipeline } = require('stream/promises');
let NC = null; try { NC = require('@napi-rs/canvas'); } catch (e) {}

const argv = process.argv.slice(2);
const arg = (k, d) => { const i = argv.indexOf('--' + k); return i >= 0 && argv[i + 1] != null ? argv[i + 1] : d; };
const HTML = arg('html');
if (!HTML || !process.send) { console.error('usage: started by AIDC DBD with an IPC channel: node aidc-design.js --html <builder .html>'); process.exit(2); }
const T0 = Date.now();
const mem = () => Math.round(process.memoryUsage().heapUsed / 1048576).toLocaleString('en-US') + ' MB';
const log = (m) => console.log(new Date().toISOString() + '  ' + m + ' (heap ' + mem() + ')');
const progress = (t, x) => { log(t); try { process.send({ progress: Object.assign({ text: t }, x || {}) }); } catch (e) {} };
const estOf = (n) => ({ load: 3, rebuild: Math.max(2, n * 29e-6), view: Math.max(2, n * 30e-6) });   // seconds per design link, from the 1,625-rack design

// ---- the builder page with the browser services it uses provided by Node (as the site runner does)
let captured = [], clip = null;
function shim(w) {
  const store = new Map(); let n = 0;
  w.__AIDC_HEADLESS = true;
  if (process.env.AIDC_DESK_BIG) w.__aidcBig = +process.env.AIDC_DESK_BIG;   // testing: the app's lowered threshold
  w.__aidcSha256 = (b) => crypto.createHash('sha256').update(b).digest('hex');
  if (typeof zlib.crc32 === 'function') w.__aidcCrc32 = (u) => zlib.crc32(u) >>> 0;
  w.Blob = Blob; w.Response = Response; w.CompressionStream = CompressionStream; w.DecompressionStream = DecompressionStream;
  w.TextEncoder = TextEncoder; w.TextDecoder = TextDecoder; w.ReadableStream = ReadableStream; w.WritableStream = WritableStream; w.TransformStream = TransformStream;
  w.URL.createObjectURL = (b) => { const u = 'blob:aidc-design/' + (++n); store.set(u, b); return u; };
  w.URL.revokeObjectURL = (u) => { setTimeout(() => store.delete(u), 60000); };
  const grab = (a) => { const u = a.href, nm = a.download; if (nm && store.has(u)) captured.push({ name: nm, blob: store.get(u) }); };
  w.HTMLAnchorElement.prototype.click = function () { grab(this); };
  const de = w.HTMLAnchorElement.prototype.dispatchEvent;
  w.HTMLAnchorElement.prototype.dispatchEvent = function (ev) { if (ev && ev.type === 'click' && this.download) { grab(this); return true; } return de.call(this, ev); };
  w.confirm = () => true; w.alert = () => {}; w.prompt = (m, d) => d;
  try { Object.defineProperty(w.navigator, 'clipboard', { value: { writeText: (t) => { clip = String(t); return Promise.resolve(); } }, configurable: true }); } catch (e) {}
  w.document.execCommand = function (c) { if (c === 'copy') { const a = w.document.activeElement; clip = a && a.value != null ? String(a.value) : String(w.getSelection ? w.getSelection() : ''); } return true; };
  w.performance.memory = { jsHeapSizeLimit: 64 * 1024 * 1048576, usedJSHeapSize: 0 };
  if (NC) {
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
  } else w.HTMLCanvasElement.prototype.getContext = function () { return null; };
  w.__aidcMeasure = (html, model) => {   // the cable-run measurement: the Layout Builder headless (as the site runner)
    const lvc = new VirtualConsole(); lvc.on('jsdomError', () => {});
    const d = new JSDOM(html, { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-design/layout.html', virtualConsole: lvc });
    try { const lw = d.window; return typeof lw.__dctMeasureModel === 'function' ? lw.__dctMeasureModel(model) : null; }
    catch (e) { return null; } finally { try { d.window.close(); } catch (e) {} }
  };
}

let DOM = null, FP = null, SRC = null;
const W = () => DOM.window;
async function ensureDesign(file) {
  if (!file || file.format !== 'aidc-dbd-site-runner/1') throw new Error('the builder window did not send a site-runner design');
  if (DOM && FP === file.fingerprint) return false;
  if (DOM) { try { DOM.window.close(); } catch (e) {} DOM = null; FP = null; }
  const est = estOf(Number(file.links || 0));
  progress('Loading the builder in the design service…', { stage: 'load', est });
  if (!SRC) SRC = fs.readFileSync(HTML, 'utf8');
  const vc = new VirtualConsole(); vc.on('jsdomError', () => {});
  const dom = new JSDOM(SRC, { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-design/builder.html', virtualConsole: vc, beforeParse: shim });
  await new Promise((r) => setTimeout(r, 1500));
  const R = dom.window.__aidcRunner;
  if (!R) { dom.window.close(); throw new Error('this builder release has no service entry points'); }
  if (R.version() !== file.builder) { dom.window.close(); throw new Error('the design comes from ' + file.builder + ' but the app carries ' + R.version()); }
  progress('Rebuilding the design (' + Number(file.racks || 0).toLocaleString('en-US') + ' racks) from its settings…', { stage: 'rebuild', est });
  const L = R.load(file, {});
  if (dom.window.__aidcMemoHold) dom.window.__aidcMemoHold('off');
  if (!L.match) { dom.window.close(); throw new Error('the rebuilt design (fingerprint ' + L.fingerprint + ') is not the one in the builder window (' + L.expected + '). The design service rebuilds the design from its settings; hand edits on the canvas are not carried. Save the design, load it again, then try again.'); }
  DOM = dom; FP = file.fingerprint; return true;
}
const settle = async (ms) => { await new Promise((r) => setTimeout(r, ms || 60)); };
function uniquePath(dir, name) {
  let p = path.join(dir, name); if (!fs.existsSync(p)) return p;
  const ext = path.extname(name), base = name.slice(0, name.length - ext.length);
  for (let i = 2; ; i++) { p = path.join(dir, base + ' (' + i + ')' + ext); if (!fs.existsSync(p)) return p; }
}
async function flushFiles(out) {   // downloads the page made, written to the project folder
  const done = [];
  for (const f of captured.splice(0)) { const p = uniquePath(out || '.', f.name.replace(/[^A-Za-z0-9._ ()-]/g, '_')); await pipeline(Readable.fromWeb(f.blob.stream()), fs.createWriteStream(p)); f.blob = null; done.push(p); }   // streamed: a large file is never held whole a second time
  return done;
}
const MODALS = { infra: { id: 'mgmt-modal', open: 'mgmtbtn' }, bom: { id: 'bom-modal', open: 'bombtn' }, facility: { id: 'fac-modal', open: 'facbtn' }, release: { id: 'rel-modal', open: 'relbtn' } };
function snapshot(kind) {
  const d = W().document, m = d.getElementById(MODALS[kind].id), box = m && m.querySelector('.modal-box');
  if (!box) throw new Error('the ' + kind + ' view did not open');
  const vals = {}; box.querySelectorAll('select[id],input[id],textarea[id]').forEach((el) => { vals[el.id] = el.type === 'checkbox' || el.type === 'radio' ? { checked: !!el.checked } : { value: el.value }; });
  const st = d.getElementById('status');
  return { html: box.innerHTML, open: m.classList.contains('on'), vals, status: st ? st.textContent : '' };
}
function byPath(kind, p) {
  let el = W().document.getElementById(MODALS[kind].id).querySelector('.modal-box');
  for (const i of p) { el = el && el.children[i]; }
  if (!el) throw new Error('that control is no longer in the view (it changed while you clicked)');
  return el;
}

const OPS = {
  async open(a) { await ensureDesign(a.file); progress('Building the view…', { stage: 'view' }); captured = []; clip = null;
    const b = W().document.getElementById(MODALS[a.kind].open); if (!b || !b.onclick) throw new Error('no ' + a.kind + ' view in this builder');
    if (W().__aidcMemoHold) W().__aidcMemoHold('off');
    b.onclick(); await settle(200);
    if (W().__aidcMemoHold) W().__aidcMemoHold('fresh');   // one memo for the clicks in this view that change nothing
    const r = snapshot(a.kind); r.files = await flushFiles(a.out); progress('Ready', { stage: 'ready' }); return r; },
  async event(a) { if (!DOM) throw new Error('the design service has no design loaded (it restarted); open the view again');
    captured = []; clip = null; const el = byPath(a.kind, a.path), w = W();
    // a device picked in Mgmt switch configs, or Copy / Download / PDF / Visio, changes nothing: the memo is kept; anything else starts it fresh
    const keep = a.type === 'click' && !!(el.closest('.cfg-tab') || /^mgmt(copy|download|pdf|visio)$/.test(el.id || ''));
    if (w.__aidcMemoHold) w.__aidcMemoHold(keep ? 'keep' : 'off');   // a change runs as the window runs it
    if (a.type === 'change' || a.type === 'input') { if ('checked' in a) el.checked = !!a.checked; else el.value = a.value; el.dispatchEvent(new w.Event(a.type, { bubbles: true })); }
    else el.dispatchEvent(new w.MouseEvent('click', { bubbles: true, cancelable: true }));
    await settle(a.wait || 120);
    if (!keep && w.__aidcMemoHold) w.__aidcMemoHold('fresh');
    if (!keep && a.kind === 'infra' && w.__aidcWarmOob) { const v = w.document.getElementById('mgmtview'); if (v && v.value === 'config') { progress('Building the OOB fabric for the switch configs…', { stage: 'view' }); w.__aidcWarmOob(); } }
    const r = snapshot(a.kind); r.files = await flushFiles(a.out); r.clip = clip; return r; },
  async oob(a) { await ensureDesign(a.file); if (W().__aidcMemoHold) W().__aidcMemoHold('fresh'); progress('Counting the OOB links (building the physical model)…', { stage: 'view' });
    const w = W(); if (typeof w.__aidcOobData !== 'function') throw new Error('this builder release has no OOB service entry point');
    try { w.__aidcPhysWarm && w.__aidcPhysWarm(); } catch (e) {}
    const r = w.__aidcOobData(); progress('Ready', { stage: 'ready' }); return JSON.parse(JSON.stringify(r)); },
  async export(a) { await ensureDesign(a.file); if (W().__aidcMemoHold) W().__aidcMemoHold('fresh'); progress('Writing the ' + (a.what === 'sot' ? 'Day-2 SoT' : 'physical model') + '…', { stage: 'view' }); captured = [];
    const id = a.what === 'sot' ? 'sotbtn' : 'physjson', b = W().document.getElementById(id); if (!b || !b.onclick) throw new Error('no ' + id + ' in this builder');
    b.onclick(); for (let i = 0; i < 600 && !captured.length; i++) await settle(500); await settle(300);
    const files = await flushFiles(a.out); if (!files.length) throw new Error('nothing was written'); progress('Ready', { stage: 'ready' }); return { files }; }
};
process.on('message', async (msg) => {
  if (!msg || msg.id == null) return;
  const t = Date.now();
  try { const f = OPS[msg.op]; if (!f) throw new Error('unknown request ' + msg.op);
    const res = await f(msg.args || {}); if (Date.now() - t > 2000) log(msg.op + ' took ' + ((Date.now() - t) / 1000).toFixed(1) + ' s');
    process.send({ id: msg.id, res });
  } catch (e) { log(msg.op + ' failed: ' + (e && e.stack || e)); process.send({ id: msg.id, err: String(e && e.message || e) }); }
});
process.on('disconnect', () => process.exit(0));
log('AIDC DBD Design service: node ' + process.version + ', builder ' + path.basename(HTML) + (NC ? '' : ' (no canvas library: PDF drawings unavailable)'));
