// AIDC DBD Desktop (DWR Networking): the AIDC DBD Hyperscale builder with the site runner built in.
//  - The builder runs unchanged in the main window. Its downloads land in the project folder.
//  - Past 512 racks the Final/Review package and the .run are built by the site runner, a separate Node process with the
//    workstation's memory (the builder hands it the design as a site-runner file, fingerprint and all).
//  - The DC Layout Builder opens in its own window with its own memory, and fetches the model from the builder.
'use strict';
const { app, BrowserWindow, ipcMain, dialog, Menu, shell, session } = require('electron');
const path = require('path'), fs = require('fs'), os = require('os'), { spawn } = require('child_process');

const RAM_MB = Math.floor(os.totalmem() / 1048576);
const RENDER_HEAP = Math.max(4096, Math.min(16384, Math.floor(RAM_MB * 0.4)));   // the builder window (design build)
const RUNNER_HEAP = Math.max(8192, Math.floor(RAM_MB * 0.75));                   // the site runner (package and .run)
app.commandLine.appendSwitch('js-flags', '--max-old-space-size=' + RENDER_HEAP);

const RES = app.isPackaged ? process.resourcesPath : path.join(__dirname, '..');
const BUILDER = path.join(RES, 'builder', 'aidc-dbd-hyperscale.html');
const RUNNER = path.join(RES, 'runner', 'aidc-runner.js');
const NODE = app.isPackaged ? path.join(RES, 'node', process.platform === 'win32' ? 'node.exe' : 'node') : (process.env.AIDC_NODE || 'node');

// ---- settings: the project folder
const CFG_FILE = () => path.join(app.getPath('userData'), 'config.json');
let CFG = {};
function loadCfg() { try { CFG = JSON.parse(fs.readFileSync(CFG_FILE(), 'utf8')); } catch (e) { CFG = {}; } }
function saveCfg() { try { fs.mkdirSync(path.dirname(CFG_FILE()), { recursive: true }); fs.writeFileSync(CFG_FILE(), JSON.stringify(CFG, null, 1)); } catch (e) {} }
function chooseProject(win) {
  const r = dialog.showOpenDialogSync(win || undefined, { title: 'Choose the project folder (packages, .run files and exports are saved here)', properties: ['openDirectory', 'createDirectory'], defaultPath: CFG.projectDir || app.getPath('documents') });
  if (r && r[0]) { CFG.projectDir = r[0]; saveCfg(); send('project', CFG.projectDir); }
  return CFG.projectDir || null;
}
function projectDir(win) { if (CFG.projectDir && fs.existsSync(CFG.projectDir)) return CFG.projectDir; return chooseProject(win); }
function uniquePath(dir, name) {   // every version kept: name, name (2), name (3) ...
  let p = path.join(dir, name); if (!fs.existsSync(p)) return p;
  const ext = path.extname(name), base = name.slice(0, name.length - ext.length);
  for (let i = 2; ; i++) { p = path.join(dir, base + ' (' + i + ')' + ext); if (!fs.existsSync(p)) return p; }
}
const stamp = () => new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);

let main = null, closing = false; const layouts = new Map();
function send(ch, v) { if (main && !main.isDestroyed()) main.webContents.send(ch, v); }

function createMain() {
  main = new BrowserWindow({ width: 1600, height: 1000, title: 'AIDC DBD', backgroundColor: '#ffffff',
    webPreferences: { preload: path.join(__dirname, 'preload-designer.js'), contextIsolation: true, nodeIntegration: false,
      additionalArguments: process.env.AIDC_DESK_BIG ? ['--aidc-big=' + process.env.AIDC_DESK_BIG] : [] } });
  main.webContents.setWindowOpenHandler(() => ({ action: 'allow', overrideBrowserWindowOptions: { width: 1500, height: 950 } }));   // up to 512 racks the builder opens its own tabs
  main.on('close', (e) => { if (closing) return; e.preventDefault(); askBeforeClose(); });   // the design is offered for saving first
  main.on('closed', () => { main = null; for (const w of layouts.values()) { try { w.close(); } catch (e) {} } });
  main.loadFile(BUILDER);
}

// ---- window.prompt for the builder (Electron has none): a small modal window, answered synchronously
ipcMain.on('prompt-sync', (e, message, def) => {
  const parent = BrowserWindow.fromWebContents(e.sender) || main;
  const w = new BrowserWindow({ parent, modal: true, width: 520, height: 190, resizable: false, minimizable: false, maximizable: false, title: 'AIDC DBD', autoHideMenuBar: true,
    webPreferences: { contextIsolation: true, nodeIntegration: false, preload: path.join(__dirname, 'preload-prompt.js') } });
  let answered = false; const done = (v) => { if (answered) return; answered = true; e.returnValue = v; try { w.close(); } catch (err) {} };
  const onAns = (ev, v) => { if (ev.sender === w.webContents) { ipcMain.removeListener('prompt-answer', onAns); done(v); } };
  ipcMain.on('prompt-answer', onAns);
  w.on('closed', () => { ipcMain.removeListener('prompt-answer', onAns); done(null); });
  const esc = (t) => String(t).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/"/g, '&quot;');
  const html = '<!doctype html><meta charset="utf-8"><body style="font:13px system-ui,Segoe UI,sans-serif;margin:14px"><div style="margin-bottom:8px;white-space:pre-wrap">' + esc(message) + '</div>'
    + '<input id="v" style="width:100%;box-sizing:border-box;height:28px;font:inherit" value="' + esc(def) + '"><div style="margin-top:12px;text-align:right">'
    + '<button id="c" style="height:28px;padding:0 14px;margin-right:6px">Cancel</button><button id="o" style="height:28px;padding:0 18px">OK</button></div>'
    + '<script>var v=document.getElementById("v");v.focus();v.select();document.getElementById("o").onclick=function(){aidcPrompt.answer(v.value)};document.getElementById("c").onclick=function(){aidcPrompt.answer(null)};'
    + 'document.addEventListener("keydown",function(k){if(k.key==="Enter")aidcPrompt.answer(v.value);if(k.key==="Escape")aidcPrompt.answer(null);});<\/script></body>';
  w.loadURL('data:text/html;charset=utf-8,' + encodeURIComponent(html));
});

// ---- Save: a native dialog; and the same offer when the app closes
async function saveDesignFile(o) {
  if (!o || !o.text) return null;
  const r = await dialog.showSaveDialog(main, { title: 'Save the design', defaultPath: path.join(CFG.projectDir || app.getPath('documents'), o.name || 'design.json'), filters: [{ name: 'AIDC DBD design', extensions: ['json'] }] });
  if (r.canceled || !r.filePath) return null;
  fs.writeFileSync(r.filePath, o.text); return r.filePath;
}
ipcMain.handle('save-design', (e, o) => saveDesignFile(o));
let saveSeq = 0; const saveWait = new Map();
ipcMain.on('save-reply', (e, id, r) => { const f = saveWait.get(id); if (f) { saveWait.delete(id); f(r); } });
function requestSave() { return new Promise((resolve) => { if (!main || main.isDestroyed()) { resolve(null); return; } const id = ++saveSeq; saveWait.set(id, resolve); main.webContents.send('save-request', id); }); }
async function askBeforeClose() {
  const o = await requestSave();
  const busy = jobs.size ? '\n\nA site-runner job is still running; closing the app stops it.' : '';
  if (o && !o.error) {
    const r = await dialog.showMessageBox(main, { type: 'question', buttons: ['Save\u2026', "Don't save", 'Cancel'], defaultId: 0, cancelId: 2, noLink: true,
      title: 'AIDC DBD', message: 'Save the current design before closing?', detail: (o.racks ? o.racks.toLocaleString('en-US') + ' racks. ' : '') + 'The design file can be opened again with Load.' + busy });
    if (r.response === 2) return;
    if (r.response === 0) { const p = await saveDesignFile(o); if (!p) return; }
  } else if (busy) {
    const r = await dialog.showMessageBox(main, { type: 'warning', buttons: ['Close', 'Cancel'], defaultId: 1, cancelId: 1, title: 'AIDC DBD', message: 'Close AIDC DBD?', detail: busy.trim() });
    if (r.response === 1) return;
  }
  closing = true; if (main && !main.isDestroyed()) main.close();
}

// ---- downloads: straight into the project folder
function hookDownloads() {
  session.defaultSession.on('will-download', (e, item) => {
    const dir = projectDir(main); if (!dir) { item.cancel(); return; }
    const p = uniquePath(dir, item.getFilename()); item.setSavePath(p);
    item.once('done', (ev, state) => send('saved', { path: p, name: path.basename(p), ok: state === 'completed', state }));
  });
}

// ---- the site runner jobs
const jobs = new Map(); let jobSeq = 0;
ipcMain.handle('job', async (e, req) => {
  const dir = projectDir(main); if (!dir) return { error: 'no project folder chosen' };
  const f = req.file || {}, kind = req.kind, id = ++jobSeq;
  const tag = kind === 'both' ? 'package-and-run' : kind === 'run' ? 'run' : (req.mode === 'review' ? 'review-package' : 'final-package');
  const out = uniquePath(dir, (f.build || 'build') + '-' + tag + '-' + stamp());
  fs.mkdirSync(out, { recursive: true });
  const design = path.join(out, (f.build || 'build') + '-site-runner.json');
  fs.writeFileSync(design, JSON.stringify(f, null, 1));
  const args = ['--max-old-space-size=' + RUNNER_HEAP, RUNNER, '--html', BUILDER, '--design', design, '--out', out, '--heap', String(RUNNER_HEAP)];
  if (kind === 'package') args.push('--package-only'); if (kind === 'run') args.push('--run-only'); if (req.mode === 'review') args.push('--review');
  let child;
  try { child = spawn(NODE, args, { env: Object.assign({}, process.env, { AIDC_RUNNER_CHILD: '1' }), windowsHide: true }); }
  catch (err) { return { error: 'the site runner did not start: ' + err.message }; }
  jobs.set(id, child);
  send('job-start', { id, kind, mode: req.mode || 'final', build: f.build, racks: f.racks, out });
  let buf = '';
  const onData = (d) => { buf += d.toString(); let i; while ((i = buf.indexOf('\n')) >= 0) { const line = buf.slice(0, i).replace(/\r$/, ''); buf = buf.slice(i + 1); if (line.trim()) send('job-log', { id, line }); } };
  child.stdout.on('data', onData); child.stderr.on('data', onData);
  child.on('error', (err) => send('job-log', { id, line: 'STOPPED: the site runner did not start: ' + err.message }));
  child.on('close', (code) => { jobs.delete(id); if (buf.trim()) send('job-log', { id, line: buf.trim() }); send('job-done', { id, code, out }); });
  return { id, out };
});
ipcMain.on('job-cancel', (e, id) => { const c = jobs.get(id); if (c) { try { c.kill(); } catch (err) {} } });
ipcMain.on('open-path', (e, p) => { if (p) shell.openPath(p); });
ipcMain.on('show-item', (e, p) => { if (p) shell.showItemInFolder(p); });
ipcMain.handle('project', () => CFG.projectDir || null);
let BUILDER_INFO = {};
ipcMain.on('ready', (e, info) => { BUILDER_INFO = info || {}; });

// ---- the DC Layout Builder in its own window (its own memory); it asks for the model, the builder makes it
let layoutSeq = 0; const pending = new Map();
ipcMain.on('open-layout', (e, req) => {
  const n = ++layoutSeq, file = path.join(app.getPath('temp'), 'aidc-dbd-layout-' + process.pid + '-' + n + '.html');
  fs.writeFileSync(file, req.html);
  const w = new BrowserWindow({ width: 1500, height: 950, title: 'DC Layout Builder (' + Number(req.racks || 0).toLocaleString('en-US') + ' racks)',
    webPreferences: { preload: path.join(__dirname, 'preload-layout.js'), contextIsolation: true, nodeIntegration: false } });
  layouts.set(w.id, w);
  w.on('closed', () => { layouts.delete(w.id); try { fs.unlinkSync(file); } catch (err) {} });
  w.loadFile(file);
});
let reqSeq = 0;
ipcMain.handle('layout-model', () => new Promise((resolve, reject) => {
  if (!main || main.isDestroyed()) { reject(new Error('the builder window is closed')); return; }
  const id = ++reqSeq; pending.set(id, { resolve, reject, parts: [] }); main.webContents.send('layout-request', id);
}));
ipcMain.on('layout-chunk', (e, id, part) => { const p = pending.get(id); if (p) p.parts.push(part); });   // the model arrives part by part
ipcMain.on('layout-parts', (e, id, res) => { const p = pending.get(id); if (!p) return; pending.delete(id);
  if (res && res.error) p.reject(new Error(res.error)); else p.resolve({ head: res.head, parts: p.parts }); });

// ---- menu
function menu() {
  const act = (a) => () => send('menu', a);
  const tpl = [
    { label: 'File', submenu: [
      { label: 'Choose project folder…', click: () => chooseProject(main) },
      { label: 'Open project folder', click: () => { const d = projectDir(main); if (d) shell.openPath(d); } },
      { type: 'separator' }, { role: 'quit' } ] },
    { label: 'Site build', submenu: [
      { label: 'Final package and .run (with the parity check)', click: act('job-both') },
      { label: 'Final package', click: act('job-package') },
      { label: 'Review package (watermarked)', click: act('job-review') },
      { label: '.run only', click: act('job-run') },
      { type: 'separator' },
      { label: 'DC Layout Builder', click: act('layout') },
      { label: 'Export site-runner file', click: act('site-runner') } ] },
    { label: 'View', submenu: [ { role: 'reload' }, { role: 'toggleDevTools' }, { type: 'separator' }, { role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' }, { role: 'togglefullscreen' } ] },
    { label: 'Help', submenu: [ { label: 'About AIDC DBD', click: () => dialog.showMessageBox(main, { type: 'info', title: 'AIDC DBD',
      message: 'AIDC DBD ' + app.getVersion() + ' — DWR Networking',
      detail: 'Builder ' + (BUILDER_INFO.builder || '?') + '\nSite runner built in (Node ' + NODE_VER() + ')\nBuilder window memory ' + RENDER_HEAP.toLocaleString('en-US') + ' MB, site runner ' + RUNNER_HEAP.toLocaleString('en-US') + ' MB\nProject folder: ' + (CFG.projectDir || 'not chosen yet') }) } ] } ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(tpl));
}
let _nv = null; function NODE_VER() { if (_nv) return _nv; try { _nv = require('child_process').execFileSync(NODE, ['-v'], { windowsHide: true }).toString().trim(); } catch (e) { _nv = 'not found'; } return _nv; }

app.whenReady().then(() => { loadCfg(); hookDownloads(); menu(); createMain(); });
app.on('window-all-closed', () => { for (const c of jobs.values()) { try { c.kill(); } catch (e) {} } app.quit(); });
