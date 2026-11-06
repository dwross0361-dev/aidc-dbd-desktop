#!/usr/bin/env node
// AIDC DBD user guide, the reference half (HS v1.71, desktop 2.0.66). Loads the builder headless (the site runner's jsdom),
// reads window.__aidcRunner.helpRef() -- the input sheet keys and what each takes, the toolbar controls, the platform
// catalogs -- and writes it into builder/aidc-dbd-help.html between the REF markers, so the guide matches its builder.
//
//   node tools/gen-help-ref.js            write the reference into the guide
//   node tools/gen-help-ref.js --check    exit 1 if the guide's reference differs from the builder's (tools/help-ref-check.js)
'use strict';
const fs = require('fs'), path = require('path'), crypto = require('crypto');
const ROOT = path.resolve(__dirname, '..'), BUILDER = path.join(ROOT, 'builder', 'aidc-dbd-hyperscale.html'), GUIDE = path.join(ROOT, 'builder', 'aidc-dbd-help.html');
const { JSDOM, VirtualConsole } = require(path.join(ROOT, 'runner', 'node_modules', 'jsdom'));

function load() {
  const bytes = fs.readFileSync(BUILDER), vc = new VirtualConsole(); vc.on('jsdomError', () => {});
  const dom = new JSDOM(bytes.toString('utf8'), { runScripts: 'dangerously', pretendToBeVisual: true, url: 'file:///aidc-runner/builder.html', virtualConsole: vc, beforeParse(w) {
    w.__AIDC_HEADLESS = true; w.__aidcBuilderSha256 = crypto.createHash('sha256').update(bytes).digest('hex');
    w.__aidcSha256 = (b) => crypto.createHash('sha256').update(b).digest('hex');
    w.Blob = Blob; w.TextEncoder = TextEncoder; w.TextDecoder = TextDecoder; w.confirm = () => true; w.alert = () => {}; w.prompt = (m, d) => d;
    w.URL.createObjectURL = () => 'blob:x'; w.URL.revokeObjectURL = () => {}; w.HTMLCanvasElement.prototype.getContext = () => null;
    w.performance.memory = { jsHeapSizeLimit: 4096 * 1048576 }; } });
  return new Promise((ok, no) => setTimeout(() => { try { const r = dom.window.__aidcRunner.helpRef(); dom.window.close(); ok(r); } catch (e) { no(e); } }, 1500));
}
const E = (s) => String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
const tab = (head, rows) => '<table>\n<tr>' + head.map((h) => '<th>' + E(h) + '</th>').join('') + '</tr>\n' + rows.map((r) => '<tr>' + r.map((c, i) => '<td>' + (i === 0 && c.code ? '<code>' + E(c.code) + '</code>' : E(c)) + '</td>').join('') + '</tr>').join('\n') + '\n</table>';

function render(R) {
  const tabs = []; R.sheet.forEach((s) => { if (tabs.indexOf(s.tab) < 0) tabs.push(s.tab); });
  const sheet = tabs.map((t) => '<h3>' + E(t) + '</h3>\n' + tab(['Key', 'Row', 'What it is', 'Takes'], R.sheet.filter((s) => s.tab === t).map((s) => [{ code: s.key }, s.label + (s.unit ? ' (' + s.unit + ')' : ''), s.about, s.allowed]))).join('\n');
  const kinds = { button: 'button', list: 'list', box: 'box', checkbox: 'check box' };
  const ctlRow = (c) => [c.name + ' (' + (kinds[c.kind] || c.kind) + ')', c.title || c.sets || (c.id === 'registerbtn' ? 'The site registry: the sites of each region and their numbering' : ''), c.key];
  const ctlTab = (L) => tab(['Control', 'What it does', 'Input sheet key'], L.map(ctlRow));
  const ctl = [1, 2].map((r) => { const L = R.controls.filter((c) => c.row === r), G = []; L.forEach((c) => { if (c.group && G.indexOf(c.group) < 0) G.push(c.group); });   // 2.0.71: row 2 by tool group
    if (!G.length) return '<h3>Toolbar row ' + r + '</h3>\n' + ctlTab(L);
    const U = L.filter((c) => !c.group);   // a control outside every group is still listed
    return G.map((g) => '<h3>Toolbar row ' + r + ': ' + E(g) + '</h3>\n' + ctlTab(L.filter((c) => c.group === g))).join('\n') + (U.length ? '\n<h3>Toolbar row ' + r + ': other</h3>\n' + ctlTab(U) : ''); }).join('\n');
  // condensed: a few wide columns, related facts grouped in a cell
  const J = (a) => a.filter((x) => x !== '' && x != null).join(' \u00b7 ');
  const prof = tab(['Platform', 'Per rack', 'Back end and DPU', 'Notes'], R.profiles.map((p) => [p.name + ' \u2014 ' + p.label,
    J([p.gpus ? p.gpus + ' GPUs' : '', p.rackKW ? p.rackKW + ' kW' : '', p.cooling]), J([p.planes ? 'planes ' + p.planes : '', p.dpu]), p.note]));
  const be = tab(['Switch', 'Hardware', 'Notes'], R.be.map((m) => [{ code: m.name }, J([m.label + (m.lane ? ' (NVL72 platforms only)' : ''), m.ru ? m.ru + 'RU' : '', m.cooling, m.ports, m.maxPorts ? 'up to ' + m.maxPorts + ' logical ports' : '']), m.note]));
  const sw = (L) => tab(['Switch', 'Hardware'], L.map((m) => [{ code: m.name }, J([m.label, m.roles, m.ru ? m.ru + 'RU' : '', m.access, m.maxW ? 'max ' + m.maxW + ' W' : ''])]));
  const body =
    '<section id="ref-sheet">\n<h2>Reference: input sheet keys</h2>\n<p>Every row the input sheet takes, as builder ' + E(R.builder) + ' reads it. A numbered row (<code>constraint.6</code>, <code>site.obstruction.4</code>) is read like its family.</p>\n' + sheet + '\n</section>\n' +
    '<section id="ref-controls">\n<h2>Reference: toolbar controls</h2>\n<p>The controls on the builder\'s two toolbar rows, with their tooltips. Lists offer what fits the open design.</p>\n' + ctl + '\n</section>\n' +
    '<section id="ref-platforms">\n<h2>Reference: platforms</h2>\n<p>The catalogs in builder ' + E(R.builder) + '. A figure marked CONFIRM is still to confirm with the vendor.</p>\n<h3>Compute platforms</h3>\n' + prof + '\n<h3>Back-end switches</h3>\n' + be + '\n<h3>Front-end leaf and spine</h3>\n' + sw(R.fe) + '\n<h3>Edge, border, gateway and hub</h3>\n' + sw(R.edge) + '\n</section>';
  const nav = '<h4>Reference</h4>\n  <a href="#ref-sheet">Input sheet keys</a>\n  <a href="#ref-controls">Toolbar controls</a>\n  <a href="#ref-platforms">Platforms</a>';
  return { body, nav };
}
function splice(html, tag, inner) {
  const a = '<!-- ' + tag + ':BEGIN (generated by tools/gen-help-ref.js; do not edit) -->', b = '<!-- ' + tag + ':END -->';
  const i = html.indexOf(a), j = html.indexOf(b);
  if (i < 0 || j < i) throw new Error('the guide has no ' + tag + ' markers');
  return html.slice(0, i + a.length) + '\n' + inner + '\n' + html.slice(j);
}
(async () => {
  const R = await load(), out = render(R), html = fs.readFileSync(GUIDE, 'utf8');
  const next = splice(splice(html, 'REFNAV', out.nav), 'REF', out.body).replace(/Builder HS-v[\d.]+ &middot;/, 'Builder ' + R.builder + ' &middot;');
  if (process.argv.includes('--check')) {
    if (next !== html) { console.error('help reference check: FAIL -- the guide differs from builder ' + R.builder + '. Run: node tools/gen-help-ref.js'); process.exit(1); }
    console.log('help reference check: PASS (' + R.builder + ', ' + R.sheet.length + ' keys, ' + R.controls.length + ' controls, ' + (R.profiles.length + R.be.length + R.fe.length + R.edge.length) + ' platforms)'); return;
  }
  fs.writeFileSync(GUIDE, next); console.log('help reference written: ' + R.builder + ', ' + R.sheet.length + ' keys, ' + R.controls.length + ' controls, ' + (R.profiles.length + R.be.length + R.fe.length + R.edge.length) + ' platforms');
})().catch((e) => { console.error(e && e.stack || e); process.exit(1); });
