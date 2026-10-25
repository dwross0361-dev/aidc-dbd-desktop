// Show DSX Air test files (2.0.15): takes air/ out of a .run on the workstation, so the topology can be imported into DSX Air
// without a Linux machine. The package inside the .run is checked against its SHA-256 first; nothing is installed or run.
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib'), crypto = require('crypto');

function payload(runFile) {
  const buf = fs.readFileSync(runFile);
  const mark = Buffer.from('\n__PAYLOAD_BELOW__\n'), at = buf.indexOf(mark);
  if (at < 0) throw new Error('not an AIDC DBD .run (no payload marker)');
  const head = buf.subarray(0, at).toString('utf8');
  const sha = (/^SITE="([^"]*)"; SITE_CODE="[^"]*"; VER="([^"]*)"; SHA="([0-9a-f]{64})"/m.exec(head) || []);
  if (!sha[3]) throw new Error('the .run header has no package checksum');
  // base64 in chunks (a large site's .run is past the longest string Node can hold)
  const b64 = buf.subarray(at + mark.length), parts = []; let carry = Buffer.alloc(0);
  for (let i = 0; i < b64.length; i += 32 << 20) {
    const c = Buffer.concat([carry, b64.subarray(i, i + (32 << 20))]), clean = Buffer.from(c.filter((x) => x !== 10 && x !== 13 && x !== 32));
    const n = clean.length - (clean.length % 4); parts.push(Buffer.from(clean.subarray(0, n).toString('latin1'), 'base64')); carry = clean.subarray(n);
  }
  if (carry.length) parts.push(Buffer.from(carry.toString('latin1'), 'base64'));
  const zip = Buffer.concat(parts);
  const got = crypto.createHash('sha256').update(zip).digest('hex');
  if (got !== sha[3]) throw new Error('checksum mismatch: the .run is damaged or truncated; copy it again');
  return { zip, site: sha[1], ver: sha[2] };
}

function entries(zip) {   // the central directory (no ZIP64: the builder's packages stay under 4 GB)
  let e = zip.length - 22; while (e >= 0 && zip.readUInt32LE(e) !== 0x06054b50) e--;
  if (e < 0) throw new Error('the package is not a zip');
  const n = zip.readUInt16LE(e + 10), out = []; let p = zip.readUInt32LE(e + 16);
  for (let i = 0; i < n; i++) {
    if (zip.readUInt32LE(p) !== 0x02014b50) throw new Error('bad zip directory');
    const meth = zip.readUInt16LE(p + 10), csz = zip.readUInt32LE(p + 20), nl = zip.readUInt16LE(p + 28), xl = zip.readUInt16LE(p + 30), cl = zip.readUInt16LE(p + 32);
    const off = zip.readUInt32LE(p + 42), name = zip.subarray(p + 46, p + 46 + nl).toString('utf8');
    out.push({ name, meth, csz, off }); p += 46 + nl + xl + cl;
  }
  return out;
}
function read(zip, en) {
  const p = en.off, nl = zip.readUInt16LE(p + 26), xl = zip.readUInt16LE(p + 28), d = zip.subarray(p + 30 + nl + xl, p + 30 + nl + xl + en.csz);
  return en.meth === 8 ? zlib.inflateRawSync(d) : Buffer.from(d);
}

// Extracts <SITE>-deploy/air/ into <run name>-air/ next to the .run (a new folder each time: name, name (2) ...). Returns it.
function extractAir(runFile, uniquePath) {
  const { zip, site, ver } = payload(runFile);
  const pre = site + '-deploy/air/', E = entries(zip).filter((x) => x.name.indexOf(pre) === 0 && !x.name.endsWith('/'));
  if (!E.length) throw new Error('this .run has no DSX Air kit (the design has no Cumulus switches, or it is older than HS-v1.07)');
  const base = path.basename(runFile).replace(/\.run$/i, '') + '-air';
  const dir = uniquePath ? uniquePath(path.dirname(runFile), base) : path.join(path.dirname(runFile), base);
  fs.mkdirSync(dir, { recursive: true });
  for (const en of E) {
    const rel = en.name.slice(pre.length), out = path.resolve(dir, rel);
    if (out.indexOf(path.resolve(dir) + path.sep) !== 0) continue;   // never outside the folder
    fs.mkdirSync(path.dirname(out), { recursive: true }); fs.writeFileSync(out, read(zip, en));
  }
  return { dir, n: E.length, site, ver };
}
module.exports = { extractAir };
