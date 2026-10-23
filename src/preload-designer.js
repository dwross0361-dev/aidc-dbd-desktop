// The builder window's bridge to the app, and the site-runner panel (jobs, progress, saved files).
'use strict';
const { contextBridge, ipcRenderer } = require('electron');

let layoutFn = null, menuFn = null, saveFn = null;
contextBridge.exposeInMainWorld('aidcDesktop', {
  version: 'desktop',
  big: (process.argv.find((a) => a.indexOf('--aidc-big=') === 0) || '').slice(11) || '',
  job: (req) => ipcRenderer.invoke('job', req).then((r) => { if (r && r.error) note('⚠ ' + r.error); return r; }),
  openLayout: (req) => ipcRenderer.send('open-layout', req),
  onLayoutRequest: (fn) => { layoutFn = fn; },
  onMenu: (fn) => { menuFn = fn; },
  ready: (info) => ipcRenderer.send('ready', info),
  saveDesign: (o) => ipcRenderer.invoke('save-design', o),
  promptSync: (m, d) => ipcRenderer.sendSync('prompt-sync', m, d),
  onSaveRequest: (fn) => { saveFn = fn; }
});
ipcRenderer.on('menu', (e, a) => { if (menuFn) menuFn(a); });
ipcRenderer.on('save-request', async (e, id) => { let r = null; try { r = saveFn ? await saveFn() : null; } catch (err) { r = { error: String(err && err.message || err) }; } ipcRenderer.send('save-reply', id, r); });
ipcRenderer.on('layout-request', async (e, id) => {
  let res;
  try { if (!layoutFn) throw new Error('the builder is not ready'); res = await layoutFn((part) => ipcRenderer.send('layout-chunk', id, part)); }
  catch (err) { res = { error: String(err && err.message || err) }; }
  ipcRenderer.send('layout-parts', id, res); res = null;
});

// ---- the panel (bottom right): one card per job, the latest saves
const J = new Map();
function el(tag, css, text) { const e = document.createElement(tag); if (css) e.style.cssText = css; if (text != null) e.textContent = text; return e; }
let panel = null;
function ensurePanel() {
  if (panel && document.body.contains(panel)) return panel;
  panel = el('div', 'position:fixed;right:14px;bottom:14px;width:520px;max-height:60vh;overflow:auto;z-index:2147483000;font:12px/1.4 system-ui,Segoe UI,sans-serif;display:flex;flex-direction:column;gap:8px');
  panel.id = 'aidc-desk-panel'; document.body.appendChild(panel); return panel;
}
function card() { const c = el('div', 'background:#fff;border:1px solid #c9d3dc;border-radius:10px;box-shadow:0 6px 24px rgba(0,0,0,.18);padding:10px 12px'); ensurePanel().appendChild(c); return c; }
function btn(label, fn) { const b = el('button', 'margin:6px 6px 0 0;height:26px;padding:0 10px;border:1px solid #9fb0bf;border-radius:6px;background:#fff;cursor:pointer;font:inherit', label); b.onclick = fn; return b; }
function note(text, path) {
  const c = card(); c.appendChild(el('div', 'white-space:pre-wrap', text));
  const row = el('div'); if (path) row.appendChild(btn('Show in folder', () => ipcRenderer.send('show-item', path)));
  row.appendChild(btn('Dismiss', () => c.remove())); c.appendChild(row);
  if (!path) setTimeout(() => c.remove(), 15000);
}
const KIND = (j) => j.kind === 'both' ? 'Final package and .run' : j.kind === 'run' ? '.run' : (j.mode === 'review' ? 'Review package' : 'Final package');
ipcRenderer.on('job-start', (e, j) => {
  const c = card(), h = el('div', 'font-weight:600', 'Site runner: ' + KIND(j) + ' — ' + (j.build || '') + ' (' + Number(j.racks || 0).toLocaleString('en-US') + ' racks)');
  const st = el('div', 'color:#0b6b6b;margin-top:2px', 'starting…'), log = el('pre', 'margin:6px 0 0;max-height:150px;overflow:auto;background:#f5f7f9;border-radius:6px;padding:6px;font:11px/1.35 Consolas,monospace;white-space:pre-wrap');
  const row = el('div'), t0 = Date.now(), tick = setInterval(() => { const s = Math.round((Date.now() - t0) / 1000); h.title = s + ' s'; st.dataset.t = Math.floor(s / 60) + ' min ' + (s % 60) + ' s'; }, 1000);
  row.appendChild(btn('Cancel', () => ipcRenderer.send('job-cancel', j.id)));
  c.append(h, st, log, row); J.set(j.id, { c, st, log, row, tick, out: j.out, lines: [] });
});
ipcRenderer.on('job-log', (e, m) => { const j = J.get(m.id); if (!j) return;
  j.lines.push(m.line); if (j.lines.length > 400) j.lines.shift();
  j.log.textContent = j.lines.slice(-12).join('\n'); j.log.scrollTop = j.log.scrollHeight;
  const s = m.line.replace(/^\s*\d+s\s+/, '').trim(); j.st.textContent = (j.st.dataset.t ? j.st.dataset.t + ' · ' : '') + s.slice(0, 140); });
ipcRenderer.on('job-done', (e, m) => { const j = J.get(m.id); if (!j) return; clearInterval(j.tick);
  const ok = m.code === 0, last = j.lines.filter((l) => /parity:|done:|STOPPED/.test(l)).map((l) => l.replace(/^\s*\d+s\s+/, '')).join('\n');
  j.st.style.color = ok ? '#0b6b3a' : '#b91c1c';
  j.st.textContent = (ok ? '✔ Finished' : (m.code === null ? '✖ Cancelled' : '✖ Stopped')) + (j.st.dataset.t ? ' after ' + j.st.dataset.t : '') + (last ? '\n' + last : '');
  j.st.style.whiteSpace = 'pre-wrap';
  j.row.innerHTML = ''; j.row.appendChild(btn('Open folder', () => ipcRenderer.send('open-path', m.out)));
  j.row.appendChild(btn('Dismiss', () => { j.c.remove(); J.delete(m.id); })); });
ipcRenderer.on('saved', (e, s) => note((s.ok ? 'Saved ' : 'Not saved (' + s.state + '): ') + s.name, s.ok ? s.path : null));
ipcRenderer.on('project', (e, d) => note('Project folder: ' + d));
