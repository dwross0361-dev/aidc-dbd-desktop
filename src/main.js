// AIDC DBD Desktop (DWR Networking): the AIDC DBD Hyperscale builder with the site runner built in.
//  - The builder runs unchanged in the main window. Its downloads land in the project folder.
//  - Past 512 racks the Final/Review package and the .run are built by the site runner, a separate Node process with the
//    workstation's memory (the builder hands it the design as a site-runner file, fingerprint and all).
//  - The DC Layout Builder opens in its own window. Past 512 racks (2.0.20) its links live in a Layout service of its own, a
//    Node process with the workstation's memory; the window holds the cabinets and asks the service for the rest.
'use strict';
const { app, BrowserWindow, ipcMain, dialog, Menu, shell, session, powerSaveBlocker } = require('electron');
const path = require('path'), fs = require('fs'), os = require('os'), { spawn } = require('child_process');

const RAM_MB = Math.floor(os.totalmem() / 1048576);
// 2.0.20: a window's JavaScript memory stops near 4 GB whatever --max-old-space-size asks (Chromium builds V8 with pointer
// compression). 2.0.14 to 2.0.19 asked for up to 16 GB and reported that figure; the limit shown now is read from each window.
const RENDER_HEAP = 4096;
// the site runner and the Layout service: Node processes, not windows, so they get the workstation's memory
const RUNNER_HEAP = Math.max(8192, Math.min(Math.floor(RAM_MB * 0.75), RAM_MB - 8192));
app.commandLine.appendSwitch('js-flags', '--max-old-space-size=' + RENDER_HEAP);
const HEAPS = new Map();   // window label -> its real JavaScript memory limit (MB)
function measureHeap(w, label) {
  w.webContents.on('did-finish-load', () => { w.webContents.executeJavaScript('(performance.memory && performance.memory.jsHeapSizeLimit) || 0')
    .then((v) => { if (v) HEAPS.set(label, Math.round(v / 1048576)); }).catch(() => {}); });
}

const RES = app.isPackaged ? process.resourcesPath : path.join(__dirname, '..');
const BUILDER = path.join(RES, 'builder', 'aidc-dbd-hyperscale.html');
const RUNNER = path.join(RES, 'runner', 'aidc-runner.js');
const LAYOUT_SVC = path.join(RES, 'runner', 'aidc-layout.js');
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
  main.on('unresponsive', () => { clearTimeout(builderIdle); builderBusy = true; keepAwake(); });   // a build holds the window: keep the machine up
  main.on('responsive', () => { clearTimeout(builderIdle); builderIdle = setTimeout(() => { builderBusy = false; keepAwake(); }, AWAKE_GRACE_MS); });
  watchCrash(main, 'builder window'); measureHeap(main, 'builder window');
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

// ---- a crashed window says so (2.0.14): reason, a crash log, and Reload -- instead of a blank white page
const CRASH_WHY = { oom: 'it ran out of memory', crashed: 'it crashed', killed: 'it was stopped by Windows or another program', 'launch-failed': 'it failed to start', 'integrity-failure': 'a code integrity check failed', 'abnormal-exit': 'it exited abnormally' };
function watchCrash(w, label) {
  w.webContents.on('render-process-gone', async (e, d) => {
    const why = CRASH_WHY[d.reason] || ('it stopped (' + d.reason + ')');
    const lim = HEAPS.get(label) || RENDER_HEAP;
    const line = new Date().toISOString() + '  ' + label + ': ' + d.reason + ' (exit ' + d.exitCode + '), RAM ' + RAM_MB + ' MB, window memory limit ' + lim + ' MB, site runner and Layout service limit ' + RUNNER_HEAP + ' MB, runner jobs running ' + jobs.size + ', Layout services running ' + svcs.size;
    const log = path.join(app.getPath('userData'), 'crash-log.txt');
    try { fs.appendFileSync(log, line + '\n'); } catch (err) {}
    if (label === 'builder window') { builderBusy = false; clearTimeout(builderIdle); keepAwake(); }
    if (w.isDestroyed()) return;
    const r = await dialog.showMessageBox(w, { type: 'error', buttons: ['Reload', 'Close'], defaultId: 0, cancelId: 1, noLink: true, title: 'AIDC DBD',
      message: 'The ' + label + ' stopped: ' + why + '.',
      detail: (d.reason === 'oom' ? 'Close other programs, or wait for a running site-runner job to finish, then reload. ' : '') +
        (d.reason === 'crashed' || d.reason === 'oom' ? 'A window has about ' + lim.toLocaleString('en-US') + ' MB of memory for the design, whatever the workstation has; a very large design can run it out. Packages, the .run, the cable matrix and the DC Layout Builder\'s links past 512 racks are built outside the window. ' : '') +
        (label === 'builder window' ? 'After Reload, open your last saved design with File > Load if it does not come back on its own. ' : '') +
        (jobs.size ? 'Site-runner jobs keep running; they are separate. ' : '') + 'Logged to ' + log });
    if (w.isDestroyed()) return;
    if (r.response === 0) w.webContents.reload(); else if (w !== main) w.close();
  });
}

// ---- keep the machine awake while work is running (2.0.13)
// System sleep suspends the builder and the site runner mid-build. While a site-runner job runs, or while a build holds
// the builder window (and for two minutes after, which covers the plan and health work that follows a build), the app
// asks the OS not to sleep. The display may still turn off. A lid close or a manual Sleep still sleeps.
const AWAKE_GRACE_MS = 120000;
let awakeId = -1, builderBusy = false, builderIdle = null, baseTitle = null;
function keepAwake() {
  const want = jobs.size > 0 || builderBusy;
  if (want && awakeId < 0) {
    try { awakeId = powerSaveBlocker.start('prevent-app-suspension'); } catch (e) { awakeId = -1; return; }
    if (main && !main.isDestroyed()) { baseTitle = main.getTitle(); main.setTitle(baseTitle + '  ·  sleep blocked while building'); }
    for (const id of jobs.keys()) send('job-log', { id, line: 'Sleep blocked while this job runs.' });
  } else if (!want && awakeId >= 0) {
    try { powerSaveBlocker.stop(awakeId); } catch (e) {}
    awakeId = -1;
    if (main && !main.isDestroyed() && baseTitle != null) main.setTitle(baseTitle);
    baseTitle = null;
  }
}
app.on('will-quit', () => { if (awakeId >= 0) { try { powerSaveBlocker.stop(awakeId); } catch (e) {} } });

// ---- the site runner jobs
const jobs = new Map(); let jobSeq = 0;
ipcMain.handle('job', async (e, req) => {
  const dir = projectDir(main); if (!dir) return { error: 'no project folder chosen' };
  const f = req.file || {}, kind = req.kind, id = ++jobSeq;
  const tag = kind === 'cable' ? 'cable-matrix-' + (req.mode === 'pdf' ? 'pdf' : 'xlsx') : kind === 'both' ? 'package-and-run' : kind === 'run' ? 'run' : (req.mode === 'review' ? 'review-package' : 'final-package');
  const out = uniquePath(dir, (f.build || 'build') + '-' + tag + '-' + stamp());
  fs.mkdirSync(out, { recursive: true });
  const design = path.join(out, (f.build || 'build') + '-site-runner.json');
  fs.writeFileSync(design, JSON.stringify(f, null, 1));
  const args = ['--max-old-space-size=' + RUNNER_HEAP, RUNNER, '--html', BUILDER, '--design', design, '--out', out, '--heap', String(RUNNER_HEAP)];
  if (kind === 'cable') args.push('--cable-matrix', req.mode === 'pdf' ? 'pdf' : 'xlsx');   // 2.0.20
  else { if (kind === 'package') args.push('--package-only'); if (kind === 'run') args.push('--run-only'); if (req.mode === 'review') args.push('--review'); }
  let child;
  try { child = spawn(NODE, args, { env: Object.assign({}, process.env, { AIDC_RUNNER_CHILD: '1' }), windowsHide: true }); }
  catch (err) { return { error: 'the site runner did not start: ' + err.message }; }
  jobs.set(id, child);
  if (awakeId >= 0) send('job-log', { id, line: 'Sleep blocked while this job runs.' }); keepAwake();
  send('job-start', { id, kind, mode: req.mode || 'final', build: f.build, racks: f.racks, out });
  let buf = '';
  const onData = (d) => { buf += d.toString(); let i; while ((i = buf.indexOf('\n')) >= 0) { const line = buf.slice(0, i).replace(/\r$/, ''); buf = buf.slice(i + 1); if (line.trim()) send('job-log', { id, line }); } };
  child.stdout.on('data', onData); child.stderr.on('data', onData);
  child.on('error', (err) => send('job-log', { id, line: 'STOPPED: the site runner did not start: ' + err.message }));
  child.on('close', (code) => { jobs.delete(id); keepAwake(); if (!jobs.size && !builderBusy) send('job-log', { id, line: 'Sleep released.' }); if (buf.trim()) send('job-log', { id, line: buf.trim() }); send('job-done', { id, code, out }); });
  return { id, out };
});
ipcMain.on('job-cancel', (e, id) => { const c = jobs.get(id); if (c) { try { c.kill(); } catch (err) {} } });
ipcMain.on('open-path', (e, p) => { if (p) shell.openPath(p); });
ipcMain.on('show-item', (e, p) => { if (p) shell.showItemInFolder(p); });
ipcMain.handle('project', () => CFG.projectDir || null);
let BUILDER_INFO = {};
ipcMain.on('ready', (e, info) => { BUILDER_INFO = info || {}; });

// ---- the DC Layout Builder in its own window (its own memory). Past 512 racks (2.0.20) the window gets a Layout service: a
// Node process that rebuilds the design from the builder's site-runner file, holds every link and answers the window.
let layoutSeq = 0; const svcs = new Map();   // layout window id -> its service
function startSvc(w, file) {
  const s = { child: null, file: file || null, seq: 0, pend: new Map(), chain: Promise.resolve(), dead: null };
  const logf = path.join(app.getPath('userData'), 'layout-service.log');
  try { s.child = spawn(NODE, ['--max-old-space-size=' + RUNNER_HEAP, LAYOUT_SVC, '--html', BUILDER], { stdio: ['ignore', 'pipe', 'pipe', 'ipc'], serialization: 'advanced', windowsHide: true }); }
  catch (err) { s.dead = 'the Layout service did not start: ' + err.message; return s; }
  const out = (d) => { try { fs.appendFileSync(logf, d); } catch (e) {} };
  s.child.stdout.on('data', out); s.child.stderr.on('data', out);
  s.child.on('message', (m) => {
    if (m && m.progress != null) { if (!w.isDestroyed()) w.webContents.send('layout-progress', typeof m.progress === 'object' ? m.progress : String(m.progress)); return; }   // 2.0.21: stages, counts, estimates
    const p = m && s.pend.get(m.id); if (!p) return; s.pend.delete(m.id); if (m.err) p.reject(new Error(m.err)); else p.resolve(m.res); });
  const gone = (why) => { if (s.dead) return; s.dead = why + '. Refresh from Fabric Designer starts it again (details: ' + logf + ')';
    for (const p of s.pend.values()) p.reject(new Error(s.dead)); s.pend.clear(); };
  s.child.on('error', (err) => gone('the Layout service stopped: ' + err.message));
  s.child.on('exit', (code, sig) => gone('the Layout service stopped (' + (sig || 'exit ' + code) + ')'));
  return s;
}
function svcCall(s, op, args) {   // one request at a time, in the order the window sent them
  const run = () => new Promise((resolve, reject) => {
    if (s.dead) { reject(new Error(s.dead)); return; }
    const id = ++s.seq; s.pend.set(id, { resolve, reject });
    try { s.child.send({ id, op, args }); } catch (err) { s.pend.delete(id); reject(err); } });
  const p = s.chain.then(run, run); s.chain = p.catch(() => {}); return p;
}
let fileSeq = 0; const fileWait = new Map();
ipcMain.on('layout-file-reply', (e, id, r) => { const f = fileWait.get(id); if (f) { fileWait.delete(id); f(r || {}); } });
function builderFile() {   // the design as it stands in the builder window now (Refresh from Fabric Designer)
  return new Promise((resolve, reject) => {
    if (!main || main.isDestroyed()) { reject(new Error('the builder window is closed')); return; }
    const id = ++fileSeq; fileWait.set(id, (r) => { if (r.error || !r.file) reject(new Error(r.error || 'the builder window sent no design')); else resolve(r.file); });
    main.webContents.send('layout-file-request', id); });
}
ipcMain.on('open-layout', (e, req) => {
  const n = ++layoutSeq, file = path.join(app.getPath('temp'), 'aidc-dbd-layout-' + process.pid + '-' + n + '.html');
  fs.writeFileSync(file, req.html);
  const label = 'DC Layout Builder (' + Number(req.racks || 0).toLocaleString('en-US') + ' racks)';
  const w = new BrowserWindow({ width: 1500, height: 950, title: label,
    webPreferences: { preload: path.join(__dirname, 'preload-layout.js'), contextIsolation: true, nodeIntegration: false } });
  const id = w.id; layouts.set(id, w); watchCrash(w, label); measureHeap(w, label);
  if (req.file) svcs.set(id, startSvc(w, req.file));
  w.on('closed', () => { layouts.delete(id); try { fs.unlinkSync(file); } catch (err) {}
    const s = svcs.get(id); if (s && s.child) { s.dead = 'closed'; try { s.child.kill(); } catch (err) {} } svcs.delete(id); });
  w.loadFile(file);
});
ipcMain.handle('layout-q', async (e, op, args) => {
  const w = BrowserWindow.fromWebContents(e.sender); let s = w && svcs.get(w.id);
  if (!s) throw new Error('this window has no Layout service');
  args = args || {};
  if (op === 'start' || op === 'refresh') {
    let file = s.file;
    if (op === 'refresh') file = await builderFile();
    if (s.dead) { s = startSvc(w, file); svcs.set(w.id, s); op = 'start'; }   // a new service: the model as the builder has it
    s.file = file;
    return svcCall(s, op, { file });
  }
  if (op === 'open') {
    const r = await dialog.showOpenDialog(w, { title: 'Open a saved layout', defaultPath: CFG.projectDir || app.getPath('documents'),
      filters: [{ name: 'DC Layout Builder layout or model', extensions: ['json'] }], properties: ['openFile'] });
    if (r.canceled || !r.filePaths || !r.filePaths[0]) return null;
    return svcCall(s, 'openFile', { path: r.filePaths[0] });
  }
  if (op === 'save' || op === 'export') { const dir = projectDir(w); if (!dir) throw new Error('no project folder chosen'); args.out = dir; }
  return svcCall(s, op, args);
});

// ---- DSX Air test files out of a .run (2.0.15): no Linux machine needed to get air/topology.json for the import
function showAirFiles() {
  const r = dialog.showOpenDialogSync(main || undefined, { title: 'Choose the .run whose DSX Air test files you want', defaultPath: CFG.projectDir || app.getPath('documents'),
    filters: [{ name: 'AIDC DBD installer', extensions: ['run'] }], properties: ['openFile'] });
  if (!r || !r[0]) return;
  try {
    const x = require('./airfiles').extractAir(r[0], uniquePath);
    shell.openPath(x.dir);
    dialog.showMessageBox(main, { type: 'info', title: 'AIDC DBD', message: 'DSX Air test files (' + x.n + ') from ' + path.basename(r[0]),
      detail: 'Package checksum verified. Saved to ' + x.dir + '\n\nImport topology.json in DSX Air, then follow README.md. The .run itself is still what goes on oob-mgmt-server for run-tests.sh.' });
  } catch (err) { dialog.showMessageBox(main, { type: 'error', title: 'AIDC DBD', message: 'No DSX Air files taken from ' + path.basename(r[0]), detail: err.message }); }
}

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
      { label: 'Export site-runner file', click: act('site-runner') },
      { type: 'separator' },
      { label: 'Show DSX Air test files…', click: () => showAirFiles() } ] },
    { label: 'View', submenu: [ { role: 'reload' }, { role: 'toggleDevTools' }, { type: 'separator' }, { role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' }, { role: 'togglefullscreen' } ] },
    { label: 'Help', submenu: [ { label: 'About AIDC DBD', click: () => dialog.showMessageBox(main, { type: 'info', title: 'AIDC DBD',
      message: 'AIDC DBD ' + app.getVersion() + ' — DWR Networking',
      detail: 'Builder ' + (BUILDER_INFO.builder || '?') + '\nSite runner built in (Node ' + NODE_VER() + ')\nBuilder window memory ' + (HEAPS.get('builder window') || RENDER_HEAP).toLocaleString('en-US') + ' MB (a window\'s limit), site runner and Layout service ' + RUNNER_HEAP.toLocaleString('en-US') + ' MB\nProject folder: ' + (CFG.projectDir || 'not chosen yet') }) } ] } ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(tpl));
}
let _nv = null; function NODE_VER() { if (_nv) return _nv; try { _nv = require('child_process').execFileSync(NODE, ['-v'], { windowsHide: true }).toString().trim(); } catch (e) { _nv = 'not found'; } return _nv; }

app.whenReady().then(() => { loadCfg(); hookDownloads(); menu(); createMain(); });
app.on('window-all-closed', () => { for (const c of jobs.values()) { try { c.kill(); } catch (e) {} } app.quit(); });
