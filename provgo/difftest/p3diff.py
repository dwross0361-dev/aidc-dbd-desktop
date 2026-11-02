#!/usr/bin/env python3
"""Phase 3 differential test: the Python service and aidc-prov side by side, driven through configuration pushes,
runners (external scripts: bnk, weka, dpu, rails, nccl, accept, isolation), tuning, RMA, hand-over and Release DWR
access, alerts (fake webhook and SMTP receivers), cross-site identity (fake peer sites over pinned HTTPS), and a
standby host pair (state sync, Kea on the standby, promote mid-build), with failures injected.

Called from difftest.py (--p3 INSTALLER.run, --p3ha INSTALLER.run). Devices answer from recorded outputs
(reader_sim_dir, written by p2diff.Answers from the planned configurations); their management ports are fakedev.py
listeners; ssh is a fake on PATH that stands in for the remote hosts (it runs the bundle's scripts locally, or plays
bfb-install); push_configs.py is a fake that reports per device.
"""
import collections, concurrent.futures, copy, hashlib, http.server, io, json, os, re, shutil, socket, socketserver, sqlite3
import ssl, subprocess, sys, threading, time, zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import difftest as D
import p2diff as P2

HERE = os.path.dirname(os.path.abspath(__file__))
PUSH_USER, PUSH_PW = "pushuser", "push-secret-for-tests"
INV_TOKEN = "inventory-token-for-tests"
SYNC_TOKEN = "sync-token-for-tests"

# ------------------------------------------------------------------------------------------------ fakes on disk

FAKE_SSH = r'''#!/bin/bash
# fake ssh: the last two arguments are user@host and the remote command
remote="${@: -1}"; target="${@: -2:1}"; host="${target#*@}"
export FAKE_HOST="$host"
if [[ "$remote" == *bfb-install* ]]; then
  cat >/dev/null
  if [ -e "$FAKESSH_CTL/nicmode-$host" ]; then echo 'NIC mode: set to DPU mode'; exit 42; fi
  echo "bfb-install: image installed on $host"; exit 0
fi
exec bash -c "$remote"
'''

PUSH_PY = r'''#!/usr/bin/env python3
import os, sys, time
a = sys.argv[1:]
dev = a[a.index("--only") + 1] if "--only" in a else "?"
cfg = a[a.index("--config") + 1] if "--config" in a else ""
ctl = os.environ.get("FAKESSH_CTL", "")
print("push_configs: %s as %s" % (dev, os.environ.get("FABRIC_USER", "")), flush=True)
if cfg:
    lines = open(cfg).read().splitlines()
    print("using %s (%d lines, preemption lines: %d)" % (os.path.basename(cfg), len(lines), sum(1 for l in lines if "preemption" in l)))
time.sleep(0.3)
if os.path.exists(os.path.join(ctl, "push-fail-" + dev)):
    print("ERROR: commit failed on %s: configuration check-out failed" % dev)
    sys.exit(3)
print("commit complete on %s" % dev)
'''

SCRIPTS = {
    "provision/run-bnk.sh": 'echo "BNK declarations with KUBECONFIG=$KUBECONFIG"\nif [ -e "$FAKESSH_CTL/bnk-slow" ]; then sleep 30; fi\necho done\n',
    "weka/01-node-network.sh": 'echo "node network on $FAKE_HOST NIC0_DEV=$NIC0_DEV CAPACITY=$CAPACITY"\n',
    "weka/02-cluster-create.sh": 'echo "cluster create on $FAKE_HOST DRIVE_CORES=$DRIVE_CORES"\n',
    "weka/03-checks.sh": 'if [ -e "$FAKESSH_CTL/weka-fail" ]; then echo "FAIL protection level 1 of 2"; else echo "OK cluster healthy"; fi\n',
    "provision/rail_check.sh": 'if [ -e "$FAKESSH_CTL/railbad-$FAKE_HOST" ]; then echo "FAIL rail 7: no link"; echo "RAILS ok $((RAILS-1))"; exit 1; fi\necho "RAILS ok $RAILS"\n',
    "provision/accept.sh": r'''t=$1; p=$2
case $t in
T1) echo "ACCEPT T1 port_gbps=400 min_gbps=392.5"; echo "NIC T1 rx_discards_phy 0";;
T2) echo "ACCEPT T2 min_gbps=90 max_gbps=97 mean_gbps=95.25"; echo "NIC T2 np_ecn_marked_roce_packets 12";;
T3) echo "ACCEPT T3 busbw=401.5";;
T4) if [ -e "$FAKESSH_CTL/accept-t4-low" ]; then echo "ACCEPT T4 busbw=300.5"; else echo "ACCEPT T4 busbw=383.25"; fi;;
esac
echo "pod $p"
''',
    "provision/iso.sh": 'echo "ISOLATION TENANT-01 bind PASS vip bound"\necho "ISOLATION TENANT-01 snat PASS own pool"\n',
}


def counters_json():
    q = {"egress-queue": {"3": {"tx-frames": 1000, "ecn-marked": 0, "tx-drops": 0}}, "pfc": {"3": {"pause-tx": 0, "pause-rx": 0}}}
    return json.dumps(q, indent=2)


# ------------------------------------------------------------------------------------------------ fake receivers

class Webhook(threading.Thread):
    daemon = True

    def __init__(self, port):
        threading.Thread.__init__(self)
        self.got = []
        outer = self

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                n = int(self.headers.get("Content-Length", "0"))
                b = self.rfile.read(n)
                outer.got.append({"ctype": self.headers.get("Content-Type"), "body": b.decode()})
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"ok")
        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), H)

    def run(self):
        self.srv.serve_forever()


class SMTPSink(threading.Thread):
    """A minimal SMTP server: EHLO (no extensions), MAIL, RCPT, DATA, QUIT; records envelope and message."""
    daemon = True

    def __init__(self, port):
        threading.Thread.__init__(self)
        self.got = []
        self.s = socket.socket()
        self.s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.s.bind(("127.0.0.1", port))
        self.s.listen(64)

    def run(self):
        while True:
            c, _ = self.s.accept()
            threading.Thread(target=self.one, args=(c,), daemon=True).start()

    def one(self, c):
        f = c.makefile("rb")
        send = lambda t: c.sendall(t.encode() + b"\r\n")
        env = {"mail": None, "rcpt": []}
        try:
            send("220 sink ESMTP")
            while True:
                ln = f.readline()
                if not ln:
                    return
                cmd = ln.decode().rstrip("\r\n")
                u = cmd.upper()
                if u.startswith("EHLO") or u.startswith("HELO"):
                    send("250 sink")
                elif u.startswith("MAIL FROM:"):
                    env["mail"] = cmd[10:]
                    send("250 ok")
                elif u.startswith("RCPT TO:"):
                    env["rcpt"].append(cmd[8:])
                    send("250 ok")
                elif u == "DATA":
                    send("354 go")
                    data = b""
                    while True:
                        l = f.readline()
                        if l in (b".\r\n", b""):
                            break
                        data += l
                    self.got.append({"mail": env["mail"], "rcpt": env["rcpt"], "data": data.decode()})
                    env = {"mail": None, "rcpt": []}
                    send("250 queued")
                elif u == "QUIT":
                    send("221 bye")
                    return
                else:
                    send("250 ok")
        except OSError:
            pass
        finally:
            c.close()


def make_cert(d, cn):
    os.makedirs(d, exist_ok=True)
    crt, key = os.path.join(d, cn + ".crt"), os.path.join(d, cn + ".key")
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30", "-subj", "/CN=" + cn,
                    "-keyout", key, "-out", crt], capture_output=True, check=True)
    return crt, key


def cert_fp(crt):
    return hashlib.sha256(ssl.PEM_cert_to_DER_cert(open(crt).read())).hexdigest()


class PeerSite(threading.Thread):
    """A peer estate site's /inventory over HTTPS (its own self-signed certificate)."""
    daemon = True

    def __init__(self, port, crt, key, site, items, token):
        threading.Thread.__init__(self)
        outer = self
        self.hits = 0

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                outer.hits += 1
                if self.path != "/inventory" or self.headers.get("X-Estate-Inventory") != token:
                    b = b'{"error": "inventory token"}'
                    self.send_response(403)
                else:
                    b = json.dumps({"site": site, "items": items}).encode()
                    self.send_response(200)
                self.send_header("Content-Length", str(len(b)))
                self.end_headers()
                self.wfile.write(b)
        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", port), H)
        self.srv.handle_error = lambda *a: None   # a first contact (fingerprint only) drops the connection
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(crt, key)
        self.srv.socket = ctx.wrap_socket(self.srv.socket, server_side=True)

    def run(self):
        self.srv.serve_forever()


# ------------------------------------------------------------------------------------------------ comparison helpers

def norm3(v):
    if isinstance(v, tuple):
        v = list(v)
    v = P2.norm2(v)
    if isinstance(v, str):
        v = re.sub(r"[0-9a-f]{64}", "<SHA>", v)
        v = re.sub(r"\(sha256 [0-9a-f]{12}\.\.\.\)", "(sha256 <S>...)", v)
        v = re.sub(r"SHA-256 \([0-9a-f]{6}\.\.\.\)", "SHA-256 (<S>...)", v)
        v = re.sub(r"\d{8}T\d{6}Z", "<TS>", v)
        v = re.sub(r"estate\.db\.standby-\d+", "estate.db.standby-<N>", v)
        v = re.sub(r"127\.0\.0\.1:[12]8(\d\d\d)", r"127.0.0.1:8\1", v)
        v = re.sub(r"\b([A-Za-z0-9._-]+-\d+\.\d+)-[0-9a-f]{8}\b", r"\1-<H>", v)   # a tuned profile id (its hash covers a time)
        return v
    if isinstance(v, dict):
        return {norm3(k) if isinstance(k, str) else k: norm3(x) for k, x in v.items()}
    if isinstance(v, list):
        return [norm3(x) for x in v]
    return v


_SWK = [0]


def switch_probe(rep, W, eng, tag, tmpl0, web, pw, console, extra_devices=()):
    """Engine switch mid-build (Phase 4): each engine's state directory is snapshotted (the database with the sqlite3
    backup API, so a write in progress is not caught half done) and opened by BOTH engines, each on its own copy, with no
    alert receivers, peers or TLS. What the two readers show of one snapshot must be the same: Python reading Go's state
    shows what Go shows from it, and the other way round."""
    snaps = {}
    for E in eng:
        src = os.path.join(E.base, "state")
        dst = os.path.join(W, "sw-%s-%s" % (tag, E.name))
        shutil.rmtree(dst, ignore_errors=True)
        shutil.copytree(src, dst, ignore=shutil.ignore_patterns("estate.db", "estate.db-journal", "estate.db-wal", "estate.db-shm"))
        a, b = sqlite3.connect(os.path.join(src, "estate.db"), timeout=60), sqlite3.connect(os.path.join(dst, "estate.db"))
        a.backup(b)
        b.close()
        a.close()
        snaps[E.name] = (dst, E)
    for owner in ("python", "go"):
        dst, OW = snaps[owner]
        plan = json.load(open(OW.plan))
        readers = []
        try:
            for R in eng:
                _SWK[0] += 1
                port = 18900 + 3 * _SWK[0]
                base = os.path.join(W, "sx%d" % _SWK[0])   # short: the fake Kea socket path must stay under the AF_UNIX limit
                shutil.rmtree(base, ignore_errors=True)
                c = json.load(open(OW.conf))
                for k in [k for k in c if k in ("alert_webhook", "smtp_host", "smtp_port", "sites") or k.startswith(("peer", "sync"))]:
                    c.pop(k, None)
                c.update({"state_dir": os.path.join(base, "state"), "tls_cert": os.path.join(base, "fabric", "none.crt"),
                          "tls_key": os.path.join(base, "fabric", "none.key"), "role": "primary" if c.get("role") != "standby" else "standby",
                          "sites_every_s": 1e6, "check_every_s": 1e9, "backup_every_h": 1000,
                          "vendor_access": os.path.join(base, "fabric", "vendor-access.json")})
                for k in ("http_port", "console_port", "internal_port", "kea_live", "local_token_file", "hook_token_file", "http_bind",
                          "console_bind", "bundle_dir", "secrets"):
                    c.pop(k, None)
                X = D.Engine(R.name, R.argv0, base, (port, port + 1, port + 2), plan, tmpl0, web, c)
                shutil.copytree(dst, os.path.join(base, "state"))
                for f in ("secrets.env", "vendor-access.json"):
                    if os.path.exists(os.path.join(OW.base, "fabric", f)):
                        shutil.copy(os.path.join(OW.base, "fabric", f), os.path.join(base, "fabric", f))
                X.start(console if R.name == "python" else None)
                readers.append(X)
            time.sleep(3)
            views = []
            for X in readers:
                st_, ck, _ = X.login("admin", pw)
                v = {"login": st_}
                for pth in ("/api/whoami", "/api/plan", "/api/users", "/api/state", "/api/readiness"):
                    r = X.con("GET", pth, cookie=ck, csrf=False)
                    try:
                        b = json.loads(r[2])
                    except ValueError:
                        b = r[2].decode(errors="replace")
                    if pth == "/api/state" and isinstance(b, dict):
                        b["devices"] = sort_items(b.get("devices") or {})
                        b.pop("server_time", None)
                        if isinstance(b.get("runners"), dict):
                            b["runners"] = dict(sorted(b["runners"].items()))
                    v[pth] = (r[0], b)
                for n in extra_devices:
                    r = X.con("GET", "/api/device/" + n, cookie=ck, csrf=False)
                    b = json.loads(r[2]) if r[0] == 200 else r[2].decode(errors="replace")
                    if isinstance(b, dict):
                        # each reader runs its own backup pass once it is up (times differ; the backups themselves are the
                        # snapshot's): its backup events, times and backup_seen times are left out
                        b["events"] = sorted(json.dumps({k: x[k] for k in x if k not in ("id", "ts")}, sort_keys=True) for x in b.get("events") or []
                                             if "configuration backed up" not in x.get("text", ""))
                        b["backup_seen"] = sorted(b.get("backup_seen") or {})
                    v["device " + n] = (r[0], b)
                views.append(norm3(v))
            pv, gv = views
            for k in pv:
                rep.same("p4 switch %s: %s state read by python vs go: %s" % (tag, owner, k), pv[k], gv.get(k))
            print("  engine switch %s: %s state opened by python and go (%d views, login %s/%s)" % (tag, owner, len(pv), pv["login"], gv["login"]))
        finally:
            for X in readers:
                X.stop()
            for X in readers:
                shutil.rmtree(X.base, ignore_errors=True)
        shutil.rmtree(dst, ignore_errors=True)


def rv(r):
    st, h, b = r
    return {"status": st, "body": norm3(D.jload(b)) if b else ""}


def sort_items(state_devices):
    """Runner per-host items: Python records rails hosts in completion order (DECISIONS P3-4): compared as sets."""
    out = copy.deepcopy(state_devices)
    for n, s in out.items():
        if isinstance(s, dict) and isinstance(s.get("items"), dict):
            s["items"] = dict(sorted(s["items"].items()))
    return out


def rma_rows(state_dir):
    db = sqlite3.connect(os.path.join(state_dir, "estate.db"))
    rows = [tuple(r) for r in db.execute("SELECT device, ticket, old_kind, old_value, new_kind, new_value, state, user, checklist FROM rma ORDER BY id")]
    db.close()
    return rows


def zip_view(body):
    z = zipfile.ZipFile(io.BytesIO(body))
    out = {}
    for n in z.namelist():
        t = z.read(n).decode(errors="replace")
        key = re.sub(r"^[^/]+-handover-\d{8}T\d{6}Z/", "TOP/", n)
        t = norm3(re.sub(r"1[5-9]\d{8}\.\d+", "<T>", t))
        if key.endswith("events.csv"):
            # "<build> gate blocked: ..." lists the checks blocked at that tick (timing): as in Phase 2
            t = re.sub(r"(gate blocked: [^;\n]*?) -- [^\n]*", r"\1 -- ...", t)
        if key.endswith("events.csv"):
            ls = t.splitlines()
            bk = sorted(set(re.sub(r"\((after build|scheduled)\): (unchanged since )?", "(KIND): ", l) for l in ls if "configuration backed up" in l))
            ck = sorted(set(l for l in ls if re.search(r" gate: '.*' (pass|warn|blocked) -- ", l)))
            ee = sorted(set(l for l in ls if "engine error" in l))
            ls = [l for l in ls if "engine error" not in l] + ee   # an engine error repeated every tick: once each
            t = "\n".join(sorted(l for l in ls if "configuration backed up" not in l and not re.search(r" gate: '.*' (pass|warn|blocked) -- ", l))) + \
                "\n--backups--\n" + "\n".join(bk) + "\n--checks--\n" + "\n".join(ck)
        elif key.endswith("audit.csv"):
            t = "\n".join(sorted(t.splitlines()))
        out[key] = t
    return out


def state_files(sd):
    out = {}
    for root, dirs, files in os.walk(sd):
        for f in files:
            p = os.path.join(root, f)
            rel = os.path.relpath(p, sd)
            if rel.startswith("backups/") or rel.endswith((".db", ".db.tmp")) or "standby-" in rel or rel.endswith("-journal"):
                continue
            try:
                out[rel] = norm3(open(p).read())
            except UnicodeDecodeError:
                out[rel] = "<binary>"
    return out


# ------------------------------------------------------------------------------------------------ the scenario

def prep_engines(args, run, name, xc_extra=None, ports=((18280, 18643, 18281), (28280, 28643, 28281)), keep_runners=True):
    # a short directory name: the fake Kea control socket path must stay under the AF_UNIX limit (108 bytes)
    W = os.path.join(args.work, "p3ha" if name.endswith("-p3ha") else "p3")
    shutil.rmtree(W, ignore_errors=True)
    os.makedirs(W)
    D.extract(run, W)
    web = os.path.join(W, "web")
    # every served file as setup.sh leaves it once secrets.env is rendered (Phase 1 covers the unfilled ones)
    for root, _, files in os.walk(web):
        for f in files:
            p = os.path.join(root, f)
            b = open(p, "rb").read()
            nb = re.sub(rb"<([A-Z][A-Za-z0-9_. &-]*)>", rb"filled", b)
            if nb != b:
                open(p, "wb").write(nb)
    plan0 = json.load(open(os.path.join(W, "estate.json")))
    tmpl0 = json.load(open(os.path.join(W, "kea-dhcp4.json")))
    plan = D.test_plan(plan0)
    if not keep_runners:
        plan.pop("runners", None)
    k = 40000
    for d in plan["devices"]:
        if d.get("probe_ips"):
            d["probe_ips"] = [D.loop_ip(k + i) for i in range(len(d["probe_ips"]))]
            k += len(d["probe_ips"])
    sv = plan["site"].get("services") or {}
    for i, key in enumerate(sorted(sv)):
        sv[key] = ["127.9.%d.%d" % (i, j + 1) for j in range(len(sv[key]))]
    for b in plan["builds"]:
        for c in b["gate"]:
            if c["type"] == "auto":
                c["grace_s"] = -1
    for r in plan.get("runners", []):
        if r["key"] == "bnk":
            r["timeout_s"] = 4   # the first attempt is made to hang: killed at 4 s (exit 124)
    return W, web, plan, tmpl0


def write_world(W, web, plan, eng, ctl):
    """Recorded device outputs, fake ssh, fake scripts, images, licenses, control files."""
    sim0 = os.path.join(W, "sim0")
    os.makedirs(sim0)
    A = P2.Answers(plan, web, sim0)
    byn = {d["name"]: d for d in plan["devices"]}
    for d in plan["devices"]:
        A.device(d["name"])
        if d.get("os") == "cumulus":
            ifs = sorted(set(re.findall(r"nv set interface (\S+) qos pfc-watchdog", A.planned(d["name"]) or "")))
            for i in ifs:
                A.write(d["name"], "nv show interface %s counters qos -o json" % i, counters_json())
            A.write(d["name"], "nv show qos pfc-watchdog -o json", json.dumps({"poll-interval": 100, "storms": 0}))
    # license installs and MNHA sync for the RMA replacements
    for ser in ("SNREPL01", "SNREPL02"):
        for n in ("ACME-ACME-SEC-FW01", "ACME-ACME-SEC-FW02"):
            A.write(n, "request system license add http://%s/licenses/%s.lic" % (plan["site"]["prov_ip"], ser), "License key added successfully")
    for n in ("ACME-ACME-SEC-FW02",):
        A.write(n, "show chassis high-availability information", "Node Status: ONLINE\nServices Redundancy Group: 0\n  Sync Status: SYNC IN PROGRESS\n"
                "Services Redundancy Group: 1\n  Current State: BACKUP\n")
    os.makedirs(ctl, exist_ok=True)
    fb = os.path.join(W, "fakebin")
    os.makedirs(fb)
    open(os.path.join(fb, "ssh"), "w").write(FAKE_SSH)
    os.chmod(os.path.join(fb, "ssh"), 0o755)
    os.makedirs(os.path.join(web, "images"), exist_ok=True)
    open(os.path.join(web, "images", "bf-bundle-2.9.1-30_25.02_ubuntu-22.04_prod.bfb"), "wb").write(b"\0" * 2048)
    for E in eng:
        shutil.copytree(sim0, os.path.join(E.base, "sim"))
        open(os.path.join(E.base, "push_configs.py"), "w").write(PUSH_PY)
        for sc in {d["push"]["cmd"][0] for d in plan["devices"] if d.get("push")}:   # push_srlinux.py etc. (Phase 4: SR Linux plans)
            open(os.path.join(E.base, sc), "w").write(PUSH_PY)
        for rel, txt in SCRIPTS.items():
            p = os.path.join(E.base, rel)
            os.makedirs(os.path.dirname(p), exist_ok=True)
            open(p, "w").write(txt)
        for r in plan.get("runners", []):
            for x in r.get("dpus", []):
                p = os.path.join(E.base, x["cfg"])
                os.makedirs(os.path.dirname(p), exist_ok=True)
                open(p, "w").write("BFB config for %s\n" % x["host"])
        for ser in ("SNREPL01", "SNREPL02"):
            open(os.path.join(E.base, "fabric", "licenses", ser + ".lic"), "w").write("LICENSE " + ser + "\n")
        json.dump({"accounts": ["nosuchdwr1"], "sudoers": [], "ssh_keys": [], "recorded": "2026-11-01T00:00:00Z"},
                  open(os.path.join(E.base, "fabric", "vendor-access.json"), "w"), indent=1)
    return A, byn, fb


def fake_devices(W, plan):
    fdevs = []
    for d in plan["devices"]:
        if d.get("ip"):
            ports = sorted(set(list(d.get("verify_ports") or [22]) + ([d["push"].get("port", 22)] if d.get("push") else [])))
            fdevs.append({"name": d["name"], "os": "member", "ip": d["ip"], "ports": ports})
        for i, ip in enumerate(d.get("probe_ips") or []):
            fdevs.append({"name": "%s#%d" % (d["name"], i), "os": "member", "ip": ip, "ports": [d.get("probe_port", 22)]})
    json.dump(fdevs, open(os.path.join(W, "fakedevs.json"), "w"))
    os.makedirs(os.path.join(W, "fsim"), exist_ok=True)
    return subprocess.Popen([sys.executable, "-I", os.path.join(HERE, "fakedev.py"), os.path.join(W, "fakedevs.json"), os.path.join(W, "fsim")],
                            stderr=open(os.path.join(W, "fakedev.log"), "w"))


def run_p3(args, run, rep):
    name = os.path.basename(os.path.dirname(run)) + "-p3"
    print("== %s (phase 3)" % name)
    W, web, plan, tmpl0 = prep_engines(args, run, name)
    args = D.shipped(args, W)
    ctl = os.path.join(W, "ctl")
    hosts = {}
    for r in plan.get("runners", []):
        for h in r.get("hosts", []) + [x["host"] for x in r.get("dpus", [])]:
            hosts.setdefault(h, "10.77.%d.%d" % (len(hosts) // 250, len(hosts) % 250 + 1))
    weka = {n: "10.78.0.%d" % (i + 1) for i, n in enumerate(t for r in plan.get("runners", []) if r["kind"] == "ssh-seq" for t in r["targets"])}
    # peer sites
    crtA, keyA = make_cert(os.path.join(W, "certs"), "peer-a")
    crtB, keyB = make_cert(os.path.join(W, "certs"), "peer-b")
    PeerSite(19601, crtA, keyA, "PEERA", [{"device": "PEERA-LF01", "kind": "sn", "value": "FORSN001"},
                                          {"device": "PEERA-LF02", "kind": "mac", "value": "02-EE-EE-00-00-01"}], INV_TOKEN).start()
    PeerSite(19602, crtB, keyB, "PEERB", [{"device": "PEERB-LF01", "kind": "sn", "value": "FORSN002"}], INV_TOKEN).start()
    sites = [{"url": "https://127.0.0.1:19601", "code": "PEERA"},
             {"url": "https://127.0.0.1:19602", "code": "PEERB", "cert_sha256": "ab" * 32},
             {"url": "https://127.0.0.1:19603", "code": "PEERC"},
             {"url": "https://127.0.0.1:19601", "code": "PEERA-BADTOKEN", "token": "wrong", "cert_sha256": cert_fp(crtA)}]
    hooks = {"python": (Webhook(19611), SMTPSink(19612)), "go": (Webhook(29611), SMTPSink(29612))}
    for w, s in hooks.values():
        w.start()
        s.start()
    xc = {"probe_every_s": 2, "done_probe_s": 10, "check_every_s": 1e9, "backup_every_h": 1000, "reader_workers": 16, "tftp": False,
          "host_ssh": hosts, "weka_nodes": weka, "NIC0_DEV": "ens1f0np0", "NIC1_DEV": "ens1f1np1", "DRIVE_CORES": 6, "COMPUTE_CORES": 12,
          "NVME_DEVICES": "/dev/nvme0n1 /dev/nvme1n1", "DATA_DRIVES": 5, "PARITY_DRIVES": 2, "CAPACITY": "100TB (Acme's 'tier 1')",
          "nccl_cmd": "echo 'all_reduce_perf: PASS busbw 401.5 GB/s'", "accept_cmd": "bash provision/accept.sh {TEST} {POD}",
          "isolation_cmd": "bash provision/iso.sh", "sites": sites, "sites_every_s": 1e6, "smtp_to": "noc@example.net, ops@example.net",
          "smtp_from": "estate@example.net", "break_glass_user": "netadmin"}
    PY = D.Engine("python", [sys.executable, "-I", args.py], os.path.join(W, "py"), (18280, 18643, 18281), plan, tmpl0, web,
                  dict(xc, alert_webhook="http://127.0.0.1:19611/hook", smtp_host="127.0.0.1", smtp_port=19612,
                       vendor_access=os.path.join(W, "py", "fabric", "vendor-access.json")))
    GO = D.Engine("go", [args.go], os.path.join(W, "go"), (28280, 28643, 28281), plan, tmpl0, web,
                  dict(xc, alert_webhook="http://127.0.0.1:29611/hook", smtp_host="127.0.0.1", smtp_port=29612,
                       vendor_access=os.path.join(W, "go", "fabric", "vendor-access.json")))
    eng = (PY, GO)
    A, byn, fakebin = write_world(W, web, plan, eng, ctl)
    for E in eng:
        open(os.path.join(E.base, "fabric", "secrets.env"), "w").write(
            "TARGET_BUSBW=380\nBNK_KUBECONFIG=/etc/fabric/bnk.kubeconfig\nINVENTORY_TOKEN=%s\nESTATE_SSH_KEY=/etc/fabric/no-such-key\n" % INV_TOKEN)
    os.environ["PATH"] = fakebin + os.pathsep + os.environ["PATH"]
    os.environ["FAKESSH_CTL"] = ctl
    # injected failures (each fixed later in the run)
    # Phase 4: the scenario adapts to the plan (platform mix, which runners and builds exist); B-NV-SN5600 runs it as before
    pushers = [d["name"] for d in plan["devices"] if d.get("push")]
    pf = "ACME-ACME-OOB-MCS02" if "ACME-ACME-OOB-MCS02" in pushers else (pushers[0] if pushers else None)
    rkeys = {r["key"] for r in plan.get("runners", [])}
    ref_dev = "ACME-ACME-SEC-FW02" if "ACME-ACME-SEC-FW02" in byn0(plan) else plan["devices"][0]["name"]
    if pf:
        open(os.path.join(ctl, "push-fail-" + pf), "w").close()
    open(os.path.join(ctl, "bnk-slow"), "w").close()
    open(os.path.join(ctl, "weka-fail"), "w").close()
    nic_name = nic_host = rail_host = None
    if runner_of(plan, "dpu") and len(runner_of(plan, "dpu")["dpus"]) > 5:
        nic_name = plan_runner(plan, "dpu")["dpus"][5]["host"]
        nic_host = hosts[nic_name]
        open(os.path.join(ctl, "nicmode-" + nic_host), "w").close()
    if runner_of(plan, "rails") and len(runner_of(plan, "rails")["hosts"]) > 3:
        rail_host = hosts[plan_runner(plan, "rails")["hosts"][3]]
        open(os.path.join(ctl, "railbad-" + rail_host), "w").close()
    print("  scenario: push failure on %s; runners %s; tuning on %s" % (pf, ",".join(sorted(rkeys)), (plan.get("tuning") or {}).get("os")))

    def both(f):
        with concurrent.futures.ThreadPoolExecutor(2) as ex:
            return list(ex.map(f, eng))

    def cmp(what, f, view=lambda r: r):
        a, b = both(f)
        rep.same(what, view(a), view(b))
        return a, b

    for E in eng:
        E.cmd("init", "--admin-pass", D.ADMIN_PW)
    ck, cck = {}, {}
    fake = None
    try:
        fake = fake_devices(W, plan)
        time.sleep(2)
        for E in eng:
            E.start(args.console if E is PY else None)
        for E in eng:
            _, c, _ = E.login("admin", D.ADMIN_PW)
            E.con("POST", "/api/password", {"current": D.ADMIN_PW, "new": D.NEW_PW}, cookie=c)
            _, ck[E.name], _ = E.login("admin", D.NEW_PW)

        def st(E):
            return json.loads(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)[2])

        def post(E, path, data, cookie=None):
            return E.con("POST", path, data, cookie=cookie or ck[E.name], timeout=150)

        def bstate(E, bk):
            return next(b for b in st(E)["builds"] if b["key"] == bk)

        def wait(pred, what, timeout=600, step=1.0):
            t0 = time.time()
            while time.time() - t0 < timeout:
                try:
                    if all(pred(E) for E in eng):
                        return time.time() - t0
                except (KeyError, ValueError, StopIteration, TypeError):
                    pass
                time.sleep(step)
            try:
                print("  TIMEOUT waiting for %s: %s" % (what, [(E.name, [(b["key"], b["state"], b["held"]) for b in st(E)["builds"]]) for E in eng]))
            except (KeyError, ValueError):
                print("  TIMEOUT waiting for %s" % what)
            rep.same("timeout: " + what, True, False)
            return None

        def quiet_wait(pred, timeout):
            t0 = time.time()
            while time.time() - t0 < timeout:
                try:
                    if all(pred(E) for E in eng):
                        return True
                except (KeyError, ValueError, StopIteration, TypeError):
                    pass
                time.sleep(1)
            return False

        def devview(n):
            return lambda E: norm3(st(E)["devices"][n])

        # ---- users: a BE operator, two customer admins (they download the export and release DWR access)
        for E in eng:
            for nm, role, planes, org in (("opbe", "operator", "be", "dwr"), ("cust1", "admin", "", "customer"), ("cust2", "admin", "", "customer")):
                pw = json.loads(post(E, "/api/users/add", {"name": nm, "role": role, "planes": planes, "org": org})[2])["password"]
                _, c, _ = E.login(nm, pw)
                E.con("POST", "/api/password", {"current": pw, "new": "Changed-%s-Pass-1" % nm}, cookie=c)
                _, cck.setdefault(nm, {})[E.name], _ = E.login(nm, "Changed-%s-Pass-1" % nm)
        op = {E.name: cck["opbe"][E.name] for E in eng}

        # ---- before the run: every Phase 3 endpoint's refusals
        cmp("p3 push-credential without password", lambda E: rv(post(E, "/api/push-credential", {"user": PUSH_USER})))
        cmp("p3 push-credential by an operator", lambda E: rv(post(E, "/api/push-credential", {"user": PUSH_USER, "password": PUSH_PW}, op[E.name])))
        cmp("p3 runner retry unknown", lambda E: rv(post(E, "/api/runner/retry", {"key": "nope"})))
        cmp("p3 promote on a primary", lambda E: rv(post(E, "/api/promote", {})))
        cmp("p3 local promote without a snapshot", lambda E: rv(E.internal_post("/local/promote", b"{}", E.local_token())))
        cmp("p3 rma start without a device", lambda E: rv(post(E, "/api/rma/start", {"ticket": "T0"}))["status"])
        cmp("p3 rma identity without an RMA", lambda E: rv(post(E, "/api/rma/identity", {"device": ref_dev, "value": "X"})))
        cmp("p3 rma upload without an RMA", lambda E: rv(post(E, "/api/rma/upload", {"device": ref_dev, "text": "x"})))
        cmp("p3 rma apply without an RMA", lambda E: rv(post(E, "/api/rma/apply", {"device": ref_dev, "source": "backup"})))
        cmp("p3 mnha-restore not waiting", lambda E: rv(post(E, "/api/rma/mnha-restore", {"device": ref_dev})))
        cmp("p3 tuning profile before approval", lambda E: rv(post(E, "/api/tuning/profile", {})))
        cmp("p3 tuning rollout before approval", lambda E: rv(post(E, "/api/tuning/rollout", {})))
        cmp("p3 tuning approve unknown version", lambda E: rv(post(E, "/api/tuning/approve", {"v": 7})))
        cmp("p3 tuning apply bad version", lambda E: rv(post(E, "/api/tuning/apply", {"v": "x"})))
        cmp("p3 tuning apply unknown version", lambda E: rv(post(E, "/api/tuning/apply", {"v": 3})))
        cmp("p3 tuning approve by an operator", lambda E: rv(post(E, "/api/tuning/approve", {"v": 0}, op[E.name])))
        cmp("p3 tuning propose errors", lambda E: [rv(post(E, "/api/tuning/propose", x, op[E.name])) for x in (
            {"values": {"ECN_MIN_KB": "abc"}}, {"values": {"ECN_MIN_KB": 5}}, {"values": {"ECN_MIN_KB": 150.5}},
            {"values": {"ECN_MIN_KB": 1600}}, {"values": {"ECN_MIN_KB": "", "NOPE": 4}}, {"values": {}})])
        cmp("p3 handover received before an export", lambda E: rv(post(E, "/api/handover/received", {"sha": "ab" * 32}, cck["cust1"][E.name])))
        cmp("p3 handover received by a DWR admin", lambda E: rv(post(E, "/api/handover/received", {"sha": "ab" * 32})))
        cmp("p3 export.zip by a DWR admin", lambda E: rv(E.con("GET", "/api/handover/export.zip", cookie=ck[E.name], csrf=False)))
        cmp("p3 release-vendor by a DWR admin", lambda E: rv(post(E, "/api/release-vendor", {"typed": "RELEASE"})))
        cmp("p3 release-vendor before the export", lambda E: rv(post(E, "/api/release-vendor", {"typed": "RELEASE"}, cck["cust1"][E.name])))
        cmp("p3 final backup by an operator", lambda E: rv(post(E, "/api/handover/final-backup", {}, op[E.name])))
        cmp("p3 inventory without token", lambda E: rv(E.con("GET", "/inventory", csrf=False)))
        cmp("p3 sync upload on a primary", lambda E: rv(E.req(E.console, "POST", "/sync/upload", b"x", {"X-Estate-Sync": SYNC_TOKEN})))
        time.sleep(3)
        cmp("p3 foreign sync after the first pass", lambda E: norm3({k: st(E)[k] for k in ("foreign_sync", "foreign_n")}))

        # ---- the run
        for E in eng:
            post(E, "/api/readiness/confirm", {"key": "_accept", "note": "lab"})
        # be/t6 stays unconfirmed until the end of the tuning flow: the back-end gate must not pass in the middle of it
        confirms = [(b["key"], c["key"]) for b in plan["builds"] for c in b["gate"] if c["type"] == "confirm" and (b["key"], c["key"]) != ("be", "t6")]
        cmp("p3 gate confirmations", lambda E: [rv(post(E, "/api/gate/confirm", {"build": bk, "check": c, "note": "by hand"})) for bk, c in confirms])
        cmp("p3 readiness (push credential missing)", lambda E: [i for i in json.loads(E.con("GET", "/api/readiness", cookie=ck[E.name], csrf=False)[2])["items"] if i["key"] == "push"])
        cmp("p3 run all", lambda E: rv(post(E, "/api/run", {"plane": "all"})))
        time.sleep(3)
        devs = plan["devices"]

        def callins(E, only=None):
            out = []
            for i, d in enumerate(devs):
                if only is not None and d["name"] not in only:
                    continue
                if d.get("ip"):
                    out.append(D.resp_view(D.hookpost(E, d, i)))
                for f in d.get("files", [])[:1]:
                    if d.get("ip"):
                        out.append(D.resp_view(E.boot("/" + f, src=d["ip"])))
                if d.get("aggregate") or d.get("push") or d.get("config_signal") == "runner":
                    continue
                if d.get("config_signal") == "manual" or not d.get("ip"):
                    out.append(rv(post(E, "/api/mark-configured", {"device": d["name"], "note": "lab"})))
                elif d.get("config_signal") != "reachable":
                    out.append(D.resp_view(E.boot("/ci/%s/config" % d["name"], src="127.0.0.1")))
            return out

        # an unknown device whose serial belongs to a peer site
        cmp("p3 a peer site's device calls in", lambda E: rv(E.internal_post("/hook/lease?event=leases4_committed",
            b"LEASES4_SIZE=1\nLEASES4_AT0_ADDRESS=10.66.6.6\nLEASES4_AT0_HWADDR=02:ee:ee:00:00:01\nLEASES4_AT0_CLIENT_ID=00:" +
            "FORSN001".encode().hex().encode() + b"\nQUERY4_GIADDR=0.0.0.0\n", D.TOKEN)))
        cmp("p3 call-ins and check-ins", callins)
        idx = {d["name"]: i for i, d in enumerate(devs)}
        last_retry = {}

        def retry_waiting(E):
            """A device that called in before its wave opened asks again once the wave is open (DHCP retries), as in p2diff."""
            if time.time() - last_retry.get(E.name, 0) < 4:
                return True
            last_retry[E.name] = time.time()
            s_ = st(E)
            opn = {n for b in s_["builds"] for w in b["waves"] if w.get("open") for n in w["devices"]}
            for n, x in s_["devices"].items():
                if x.get("flag") == "waiting" and n in opn and n in idx and byn[n].get("ip"):
                    D.hookpost(E, byn[n], idx[n])
                    for f in byn[n].get("files", [])[:1]:
                        E.boot("/" + f, src=byn[n]["ip"])
            # a runner the harness has no secrets for (FMC deployment on FTD plans): the operator marks its targets configured
            for r in plan.get("runners", []):
                if (s_["runners"].get(r["key"]) or {}).get("state") == "not configured":
                    for n in r["targets"]:
                        if 1 <= s_["devices"][n]["stage"] < 4 and not byn[n].get("aggregate"):
                            post(E, "/api/mark-configured", {"device": n, "note": "lab: configured by hand (%s)" % r["label"]})
            return True
        oob_push = [n for n in plan["builds"][0]["waves"][0]["devices"] if byn[n].get("push")]
        wait(lambda E: all(st(E)["devices"][n]["detail"] == "waiting for the push credential" for n in oob_push), "push devices waiting for the credential", timeout=120)
        cmp("p3 push devices wait for the credential", lambda E: {n: norm3(st(E)["devices"][n]) for n in oob_push})
        cmp("p3 push-credential", lambda E: rv(post(E, "/api/push-credential", {"user": PUSH_USER, "password": PUSH_PW})))
        cmp("p3 readiness (push credential held)", lambda E: [i for i in json.loads(E.con("GET", "/api/readiness", cookie=ck[E.name], csrf=False)[2])["items"] if i["key"] == "push"])
        # a push fails mid-wave (MCS02), then is retried after the fix
        if pf:
            pfb = D_build(plan, pf)
            wait(lambda E: st(E)["devices"][pf]["flag"] == "failed" and not st(E)["pushing"], "push failure", timeout=600)
            quiet_wait(lambda E: pf.split("-", 2)[-1] in bstate(E, pfb)["held"], 10)   # held is set at the next tick
            cmp("p3 push failed mid-wave", lambda E: (devview(pf)(E), norm3(bstate(E, pfb)["held"])))
            cmp("p3 push log of the failure", lambda E: E.con("GET", "/api/device/" + pf, cookie=ck[E.name], csrf=False)[2] and
                json.loads(E.con("GET", "/api/device/" + pf, cookie=ck[E.name], csrf=False)[2])["push_log"])
            os.remove(os.path.join(ctl, "push-fail-" + pf))
            cmp("p3 retry the failed push", lambda E: rv(post(E, "/api/retry", {"device": pf})))
            cmp("p3 %s calls in again" % pf.split("-")[-1], lambda E: callins(E, {pf}))

        # ---- build by build
        keys = [b["key"] for b in plan["builds"] if b["waves"]]

        def gate_view(E, bk):
            b = bstate(E, bk)
            return {"state": b["state"], "gate": norm3(b["gate"])}

        silent = None
        for bk in keys:
            wait(lambda E: [retry_waiting(X) for X in eng] and (bstate(E, bk)["state"] in ("gate", "blocked", "complete") or bk in ("edge", "fe", "be") and runner_blocked(st(E), plan, bk)),
                 bk + " waves (or a runner holds them)", timeout=900)
            if bk == "edge" and "bnk" in rkeys and "ACME-ACME-EDG-LB01" in byn:
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"].get("bnk") or {}).get("state") == "failed", "bnk timeout", timeout=120)
                wait(lambda E: [retry_waiting(X) for X in eng] and "LB01" in bstate(E, "edge")["held"], "edge wave held by the LBs", timeout=60)
                cmp("p3 edge: bnk timed out (exit 124)", lambda E: (norm3(st(E)["runners"]["bnk"]), devview("ACME-ACME-EDG-LB01")(E)))
                cmp("p3 edge: bnk runner log", lambda E: norm3(E.con("GET", "/api/runner-log/bnk", cookie=ck[E.name], csrf=False)[2].decode()))
                os.remove(os.path.join(ctl, "bnk-slow"))
                cmp("p3 edge: retry bnk", lambda E: rv(post(E, "/api/runner/retry", {"key": "bnk"})))
                cmp("p3 edge: retry the LBs", lambda E: [rv(post(E, "/api/retry", {"device": n})) for n in ("ACME-ACME-EDG-LB01", "ACME-ACME-EDG-LB02")])
            if bk == "fe" and "weka" in rkeys and "ACME-ACME-STO-WK01" in byn:
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"].get("weka") or {}).get("state") == "failed", "weka failure", timeout=300)
                wait(lambda E: "STO-WK01" in bstate(E, "fe")["held"], "fe wave held by the WEKA nodes", timeout=60)
                cmp("p3 fe: weka check failed", lambda E: (norm3(st(E)["runners"]["weka"]), devview("ACME-ACME-STO-WK01")(E)))
                cmp("p3 fe: weka log", lambda E: norm3(E.con("GET", "/api/runner-log/weka", cookie=ck[E.name], csrf=False)[2].decode()))
                os.remove(os.path.join(ctl, "weka-fail"))
                cmp("p3 fe: retry weka", lambda E: rv(post(E, "/api/runner/retry", {"key": "weka"})))
                cmp("p3 fe: retry the WEKA nodes", lambda E: [rv(post(E, "/api/retry", {"device": n})) for n in sorted(weka)])
            if bk == "fe" and nic_name:
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"].get("dpu") or {}).get("state") == "failed", "dpu failure", timeout=400)
                dpu_all = next(n for n in byn if n.endswith("DPU-ALL"))
                cmp("p3 fe: dpu NIC mode", lambda E: (norm3(st(E)["runners"]["dpu"]), norm3(st(E)["devices"][dpu_all])))
                cmp("p3 fe: dpu host log", lambda E: norm3(open(os.path.join(E.base, "state", "runner-dpu.log." + re.sub(r"[^A-Za-z0-9_-]", "-", nic_name))).read()))
                os.remove(os.path.join(ctl, "nicmode-" + nic_host))
                cmp("p3 fe: retry dpu", lambda E: rv(post(E, "/api/runner/retry", {"key": "dpu"})))
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"].get("dpu") or {}).get("state") == "done", "dpu done", timeout=400)
            if bk == "be" and rail_host:
                rail_all = next(n for n in byn if n.endswith("RAIL-ALL"))
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"].get("rails") or {}).get("state") in ("failed", "done"), "rails", timeout=300)
                cmp("p3 be: rails with a bad host", lambda E: (norm3(st(E)["runners"]["rails"]), norm3(sort_items(st(E)["devices"])[rail_all])))
                os.remove(os.path.join(ctl, "railbad-" + rail_host))
                # P3-c (fixed in both engines, approved): each attempt starts the host log afresh, so a retry after the fix passes
                cmp("p3 be: retry rails", lambda E: rv(post(E, "/api/runner/retry", {"key": "rails"}, op[E.name])))
                wait(lambda E: all((st(E)["runners"].get(k) or {}).get("state") in ("done", "failed") for k in ("rails", "nccl", "accept")) and
                     (st(E)["runners"].get("rails") or {}).get("state") == "done", "rails, nccl, accept", timeout=600)
                cmp("p3 be: runners done", lambda E: norm3({k: st(E)["runners"][k] for k in ("rails", "nccl", "accept")}))
                cmp("p3 be: results", lambda E: norm3(st(E)["results"]))
                # tuning: propose, apply to the test pod, approve, roll out pod by pod (smoke tests), profile
                tv1, tv2 = tune_values(plan)
                cmp("p3 be: tuning propose v1", lambda E: rv(post(E, "/api/tuning/propose", {"values": tv1, "note": "lab: wider band"}, op[E.name])))
                cmp("p3 be: tuning propose v2", lambda E: rv(post(E, "/api/tuning/propose", {"values": tv2}, op[E.name])))
                # the switches will run v1: their running configuration shows its lines from now on, next to the package's (the lossless
                # check reads it; written before any apply so that no check reads a half-written set)
                for E in eng:
                    for n in plan_runner(plan, "accept")["devices"] + [x for x in tuned_rest(plan)]:
                        bcmd = P2.Answers.CMDS["backup"].get(byn[n].get("os"))
                        if bcmd:
                            bkf = os.path.join(E.base, "sim", n, P2.slug(bcmd) + ".txt")
                            open(bkf, "a").write("\n" + "\n".join(tune_lines(plan, tv1)) + "\n")
                cmp("p3 be: tuning apply v1 (test)", lambda E: rv(post(E, "/api/tuning/apply", {"v": 1, "scope": "test"}, op[E.name])))
                wait(lambda E: "Tuning v1 on test" in json.dumps(E.con("GET", "/api/events?since=0&limit=100000", cookie=ck[E.name], csrf=False)[2].decode()), "tuning v1 applied", timeout=120)
                cmp("p3 be: tuning apply to a scope with no switch", lambda E: rv(post(E, "/api/tuning/apply", {"v": 1, "scope": "pod:99"}, op[E.name])))
                cmp("p3 be: tuning approve v1", lambda E: rv(post(E, "/api/tuning/approve", {"v": 1})))
                cmp("p3 be: tuning rollout", lambda E: rv(post(E, "/api/tuning/rollout", {}, op[E.name])))
                wait(lambda E: (st(E)["tuning"].get("rollout") or {}).get("state") not in (None, "running"), "rollout", timeout=600)
                cmp("p3 be: tuning view after the rollout", lambda E: norm3(st(E)["tuning"]))
                cmp("p3 be: smoke log pod 1", lambda E: norm3(E.con("GET", "/api/runner-log/accept-smoke-pod1", cookie=ck[E.name], csrf=False)[2].decode()))
                cmp("p3 be: applied lines on a test switch", lambda E: open(os.path.join(E.base, "sim", plan_runner(plan, "accept")["devices"][0], "applied.txt")).read())
                cmp("p3 be: recheck with the tuned lines in effect", lambda E: rv(post(E, "/api/gate/recheck", {"build": "be"}, op[E.name]))["status"])
                # the smoke tests replaced the acceptance record with T2/T3: run the acceptance again (T4 low first)
                open(os.path.join(ctl, "accept-t4-low"), "w").close()
                cmp("p3 be: acceptance again (T4 low)", lambda E: rv(post(E, "/api/runner/retry", {"key": "accept"}, op[E.name])))
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"]["accept"]).get("state") in ("done", "failed") and "retry" not in (st(E)["runners"]["accept"].get("note") or ""), "accept rerun", timeout=300)
                cmp("p3 be: acceptance blocked on T4", lambda E: norm3((st(E)["runners"]["accept"], st(E)["results"]["accept"])))
                wait(lambda E: next(c for c in bstate(E, "be")["gate"] if c["key"] == "accept")["result"] == "blocked", "accept check blocked", timeout=120)
                time.sleep(3)   # one tick: the gate's blocked event
                cmp("p3 be: profile while acceptance fails", lambda E: rv(post(E, "/api/tuning/profile", {"nic": "CX-8 <b>", "port": "800G"}, op[E.name])))
                os.remove(os.path.join(ctl, "accept-t4-low"))
                cmp("p3 be: acceptance again", lambda E: rv(post(E, "/api/runner/retry", {"key": "accept"}, op[E.name])))
                wait(lambda E: [retry_waiting(X) for X in eng] and (st(E)["runners"]["accept"]).get("state") == "done", "accept passes", timeout=300)
                cmp("p3 be: tuned profile", lambda E: profile_view(post(E, "/api/tuning/profile", {"nic": "ConnectX-8™", "port": "800G"}, op[E.name])))
                switch_probe(rep, W, eng, "after-tuning", tmpl0, web, D.NEW_PW, args.console)
            wait(lambda E: [retry_waiting(X) for X in eng] and bstate(E, bk)["state"] in ("gate", "blocked", "complete"), bk + " at its gate", timeout=900)
            time.sleep(12)   # the checker runs the popped checks; one tick
            wait(lambda E: bstate(E, bk)["state"] == "complete" or all(c.get("note") != "automatic check queued" for c in bstate(E, bk)["gate"] if c["type"] == "auto"),
                 bk + " checks evaluated", timeout=300)
            time.sleep(3)
            need = [c["key"] for c in bstate(PY, bk)["gate"] if c["result"] in ("needs confirmation",)]
            cmp("p3 %s: gate" % bk, lambda E: gate_view(E, bk))
            if need:
                cmp("p3 %s: confirm what the reader cannot check" % bk, lambda E: [rv(post(E, "/api/gate/confirm", {"build": bk, "check": c, "note": "checked by hand"})) for c in need])
            blocked = [c["key"] for E in eng for c in bstate(E, bk)["gate"] if c["result"] in ("blocked", "running")]
            if blocked:
                cmp("p3 %s: recheck" % bk, lambda E: rv(post(E, "/api/gate/recheck", {"build": bk})))
                time.sleep(4)
            if not quiet_wait(lambda E: bstate(E, bk)["state"] == "complete", 150):
                # a check this harness's recorded outputs cannot satisfy on this platform mix: compared, then overridden
                cmp("p3 %s: gate held after the re-check" % bk, lambda E: gate_view(E, bk))
                cmp("p3 %s: gate override" % bk, lambda E: rv(post(E, "/api/gate/override", {"build": bk, "reason": "lab: recorded outputs do not cover this check"})))
            wait(lambda E: bstate(E, bk)["state"] == "complete", bk + " gate passed", timeout=300)
            cmp("p3 %s: passed" % bk, lambda E: norm3(bstate(E, bk)))
        wait(lambda E: st(E)["run"]["state"] == "complete" or all(b["state"] == "complete" for b in st(E)["builds"] if b["waves"]), "run complete", timeout=300)
        empty = [b["key"] for b in plan["builds"] if not any(w["devices"] for w in b["waves"])]
        if empty:
            # P4-10 (fixed in both engines): the run of all marks each empty build complete when it starts (one event each)
            # and no tick raises
            def empty_view(E):
                ev = D.events_of(os.path.join(E.base, "state"))
                return {"states": {k: bstate(E, k)["state"] for k in empty},
                        "events": [x[4] for x in ev if x[4].startswith("No devices in plane ")],
                        "engine errors": sorted(set(x[4] for x in ev if "engine error" in x[4]))}
            a, _ = cmp("p3 empty builds: complete at the run's start, no engine error", empty_view)
            rep.same("p3 empty builds: as P4-10 requires (python)", a, {"states": {k: "complete" for k in empty}, "events":
                     ["No devices in plane %s: build marked complete" % k for k in empty], "engine errors": []})
        if "isolation" in rkeys:
            wait(lambda E: (st(E)["runners"].get("isolation") or {}).get("state") == "done", "isolation runner", timeout=120)
        cmp("p3 runners at the end", lambda E: norm3(st(E)["runners"]))

        # ---- RMA: an MNHA firewall (license, preemption held, sync, restored), a Cumulus leaf from backup, a quarantined device
        mnha = [n for n in ("ACME-ACME-SEC-FW01", "ACME-ACME-SEC-FW02") if n in byn and byn[n].get("os") == "junos" and "MNHA" in byn[n].get("role", "")]
        if len(mnha) == 2:
            fw = "ACME-ACME-SEC-FW02"
            fwi = [d["name"] for d in devs].index(fw)
            cmp("p3 rma start (MNHA firewall)", lambda E: rv(post(E, "/api/rma/start", {"device": fw, "ticket": "RMA-1001"})))
            cmp("p3 rma state after start", devview(fw))
            cmp("p3 rma identity refused", lambda E: rv(post(E, "/api/rma/identity", {"device": fw, "value": "a b"})))
            cmp("p3 rma identity", lambda E: rv(post(E, "/api/rma/identity", {"device": fw, "value": "snrepl02"})))
            cmp("p3 rma apply planned", lambda E: rv(post(E, "/api/rma/apply", {"device": fw, "source": "planned"})))
            cmp("p3 rma check keys", lambda E: rv(post(E, "/api/rma/check", {"device": fw, "key": "keys"})))

            def repl_hook(E, d, ser):
                x = "LEASES4_SIZE=1\nLEASES4_AT0_ADDRESS=%s\nLEASES4_AT0_HWADDR=02:dd:00:00:00:99\nLEASES4_AT0_CLIENT_ID=00:%s\nQUERY4_GIADDR=0.0.0.0\n" % (d["ip"], ser.encode().hex())
                return D.resp_view(E.internal_post("/hook/lease?event=leases4_committed", x.encode(), D.TOKEN))
            cmp("p3 rma replacement calls in", lambda E: [repl_hook(E, byn[fw], "SNREPL02"), D.resp_view(E.boot("/" + byn[fw]["files"][0], src=byn[fw]["ip"]))])
            wait(lambda E: st(E)["devices"][fw]["detail"].startswith("pushed with preemption") or "await" in json.dumps(
                json.loads(E.con("GET", "/api/device/" + fw, cookie=ck[E.name], csrf=False)[2])["state"]["rma"]), "MNHA hold push", timeout=180)
            cmp("p3 rma: pushed with preemption held", lambda E: norm3(json.loads(E.con("GET", "/api/device/" + fw, cookie=ck[E.name], csrf=False)[2])["state"]))
            cmp("p3 rma: the hold file", lambda E: open(os.path.join(E.base, "state", "rma-hold-%s.conf" % fw)).read())
            cmp("p3 rma: push log with the hold file", lambda E: norm3(json.loads(E.con("GET", "/api/device/" + fw, cookie=ck[E.name], csrf=False)[2])["push_log"]))
            for E in eng:
                A.sim = os.path.join(E.base, "sim")
                A.write(fw, "show chassis high-availability information", "Node Status: ONLINE\nServices Redundancy Group: 0\n  Sync Status: IN SYNC\n")
            wait(lambda E: st(E)["devices"][fw]["stage"] == 6, "RMA firewall Done", timeout=240)
            cmp("p3 rma: firewall done, RMA closed", lambda E: norm3(json.loads(E.con("GET", "/api/device/" + fw, cookie=ck[E.name], csrf=False)[2])["state"]))
            # a second MNHA firewall: the sync cannot be read, the operator confirms it
            fw1 = "ACME-ACME-SEC-FW01"
            for E in eng:
                p = os.path.join(E.base, "sim", fw1, P2.slug("show chassis high-availability information") + ".txt")
                if os.path.exists(p):
                    os.remove(p)
            cmp("p3 rma start FW01", lambda E: rv(post(E, "/api/rma/start", {"device": fw1, "ticket": None})))
            cmp("p3 rma identity FW01", lambda E: rv(post(E, "/api/rma/identity", {"device": fw1, "value": "SNREPL01"})))
            cmp("p3 rma FW01 calls in", lambda E: [repl_hook(E, byn[fw1], "SNREPL01"), D.resp_view(E.boot("/" + byn[fw1]["files"][0], src=byn[fw1]["ip"]))])
            wait(lambda E: "cannot read MNHA sync" in E.con("GET", "/api/events?since=0&limit=100000", cookie=ck[E.name], csrf=False)[2].decode(), "MNHA read warning", timeout=240)
            cmp("p3 rma FW01 waits for sync", lambda E: norm3(json.loads(E.con("GET", "/api/device/" + fw1, cookie=ck[E.name], csrf=False)[2])["state"]))
            cmp("p3 rma FW01 mnha-restore", lambda E: rv(post(E, "/api/rma/mnha-restore", {"device": fw1})))
            wait(lambda E: st(E)["devices"][fw1]["stage"] == 6, "RMA FW01 Done", timeout=240)
            cmp("p3 rma FW01 done", devview(fw1))
        # a Cumulus leaf restored from the customer's backup (the ZTP script is rebuilt around it)
        lf = next((d["name"] for d in devs if d.get("os") == "cumulus" and d.get("ip") and d.get("files") and "FEF-LF" in d["name"]), None) or \
            next(d["name"] for d in devs if d.get("os") == "cumulus" and d.get("ip") and d.get("files") and not d.get("push"))
        cmp("p3 rma start (Cumulus leaf)", lambda E: rv(post(E, "/api/rma/start", {"device": lf, "ticket": "RMA-2002"})))
        cmp("p3 rma upload empty", lambda E: rv(post(E, "/api/rma/upload", {"device": lf, "text": "  "})))
        bk_txt = "nv set system hostname %s\nnv set interface swp1 description restored\nnv unset interface swp9\nnv config apply -y\n# comment\n" % lf
        cmp("p3 rma upload", lambda E: rv(post(E, "/api/rma/upload", {"device": lf, "text": bk_txt})))
        cmp("p3 rma identity (MAC)", lambda E: rv(post(E, "/api/rma/identity", {"device": lf, "value": "02:DD:00:00:00:42"})))
        cmp("p3 rma served override", lambda E: D.resp_view(E.boot("/" + byn[lf]["files"][0], src=byn[lf]["ip"])))
        cmp("p3 rma leaf configured", lambda E: D.resp_view(E.boot("/ci/%s/config" % lf, src="127.0.0.1")))
        wait(lambda E: st(E)["devices"][lf]["stage"] == 6, "RMA leaf Done", timeout=120)
        cmp("p3 rma leaf done", devview(lf))
        # RMA on a quarantined device
        q = next((d["name"] for d in devs if d.get("os") == "cumulus" and d.get("ip") and d["name"] != lf and "FEF-LF" in d["name"]), None) or \
            next(d["name"] for d in devs if d.get("os") == "cumulus" and d.get("ip") and d["name"] != lf and not d.get("push"))
        cmp("p3 quarantine", lambda E: rv(post(E, "/api/quarantine", {"device": q, "reason": "bad optics"})))
        cmp("p3 rma on a quarantined device", lambda E: rv(post(E, "/api/rma/start", {"device": q, "ticket": "RMA-3003"})))
        cmp("p3 rma apply backup without a backup", lambda E: rv(post(E, "/api/rma/apply", {"device": q, "source": "backup"})))
        cmp("p3 rma identity serial on a MAC platform", lambda E: rv(post(E, "/api/rma/identity", {"device": q, "value": "SNX12345"})))
        cmp("p3 quarantined RMA device state", lambda E: (devview(q)(E), norm3([u for u in st(E)["unclaimed"] if u["key"].startswith("manual:")])))
        switch_probe(rep, W, eng, "after-rma", tmpl0, web, D.NEW_PW, args.console, extra_devices=[x for x in ("ACME-ACME-SEC-FW01", "ACME-ACME-SEC-FW02", lf, q) if x in byn])

        # ---- hand-over
        cmp("p3 final backup", lambda E: rv(post(E, "/api/handover/final-backup", {})))
        wait(lambda E: (st(E)["handover"].get("final") or {}).get("state") == "done", "final backup", timeout=300)
        cmp("p3 handover after the final backup", lambda E: norm3(st(E)["handover"]))
        zips = {}

        def getzip(E):
            r = E.con("GET", "/api/handover/export.zip", cookie=cck["cust1"][E.name], csrf=False, timeout=120)
            zips[E.name] = r
            return {"status": r[0], "ctype": r[1].get("content-type"), "cd": norm3(r[1].get("content-disposition")),
                    "sha_ok": r[1].get("x-sha256") == hashlib.sha256(r[2]).hexdigest()}
        cmp("p3 export.zip", getzip)
        rep.same("p3 export.zip contents", zip_view(zips["python"][2]), zip_view(zips["go"][2]))
        cmp("p3 handover received: checksum mismatch", lambda E: rv(post(E, "/api/handover/received", {"sha": "0" * 64}, cck["cust1"][E.name])))
        cmp("p3 handover received: too short", lambda E: rv(post(E, "/api/handover/received", {"sha": zips[E.name][1]["x-sha256"][:8]}, cck["cust1"][E.name])))
        cmp("p3 handover received", lambda E: rv(post(E, "/api/handover/received", {"sha": zips[E.name][1]["x-sha256"].upper()[:20]}, cck["cust1"][E.name])))
        rel = {"typed": "RELEASE", "tacacs_vpn": "yes", "backup_system": "yes", "dwr_attest": "DWR-ATT-77", "password": "netadmin-pass"}
        steps = [dict(rel, typed="release"), dict(rel, tacacs_vpn="no"), dict(rel, backup_system=""), dict(rel, dwr_attest="  "), dict(rel, password="")]
        cmp("p3 release-vendor refusals", lambda E: [rv(post(E, "/api/release-vendor", x, cck["cust1"][E.name])) for x in steps])
        cmp("p3 release-vendor", lambda E: rv(post(E, "/api/release-vendor", rel, cck["cust1"][E.name])))
        wait(lambda E: (json.loads(E.req(E.internal, "GET", "/local/handover")[2]).get("release") or {}).get("state") not in (None, "running"), "release", timeout=300)
        cmp("p3 handover after the release", lambda E: norm3(json.loads(E.req(E.internal, "GET", "/local/handover")[2])))
        cmp("p3 DWR login after the release", lambda E: rv(E.con("GET", "/api/whoami", cookie=ck[E.name], csrf=False)))
        cmp("p3 push credential cleared", lambda E: json.loads(E.con("GET", "/api/state", cookie=cck["cust1"][E.name], csrf=False)[2])["push_cred"])
        outd = {E.name: os.path.join(E.base, "export-out") for E in eng}
        for d in outd.values():
            os.makedirs(d, exist_ok=True)
        cmp("p3 cli handover-export", lambda E: (lambda r: (r[0], norm3(r[1])))(E.cmd("handover-export", "--out", outd[E.name])))
        zf = {E.name: os.path.join(outd[E.name], os.listdir(outd[E.name])[0]) for E in eng}
        rep.same("p3 cli export contents", zip_view(open(zf["python"], "rb").read()), zip_view(open(zf["go"], "rb").read()))
        rep.same("p3 cli export mode", oct(os.stat(zf["python"]).st_mode & 0o777), oct(os.stat(zf["go"]).st_mode & 0o777))
        cmp("p3 inventory", lambda E: rv(E.req(E.console, "GET", "/inventory", None, {"X-Estate-Inventory": INV_TOKEN})))
        cmp("p3 final state", lambda E: rv(E.con("GET", "/api/state", cookie=cck["cust1"][E.name], csrf=False)))
        cmp("p3 final readiness", lambda E: rv(E.con("GET", "/api/readiness", cookie=cck["cust1"][E.name], csrf=False)))
        time.sleep(15)   # the alert queues drain
    finally:
        for E in eng:
            E.stop()
        if fake:
            fake.terminate()
            fake.wait()
    S = [os.path.join(E.base, "state") for E in eng]
    ea, eb = D.events_of(S[0]), D.events_of(S[1])
    print("  events: python %d, go %d" % (len(ea), len(eb)))
    gb = lambda x: list(x[:4]) + [re.sub(r"^(.* gate blocked: [^;]*?) -- .*$", r"\1 -- ...", x[4], flags=re.S)] + list(x[5:])
    isbk = lambda x: "configuration backed up" in x[4]
    # a check's result event is written each time it is evaluated after its result was dropped (a tuning apply or a
    # result drops it); how many drops one evaluation covers depends on the 10 s checker phase: compared as a set
    isck = lambda x: re.search(r" gate: '.*' (pass|warn|blocked) -- ", x[4]) is not None
    def dedup(L):   # an engine error repeated every tick (before P4-10, an empty build): how many depends on how long it ran
        seen, out = set(), []
        for x in L:
            if "engine error" in x[4]:
                if x[4] in seen:
                    continue
                seen.add(x[4])
            out.append(x)
        return out
    nb = lambda L: sorted(map(str, norm3([gb(x) for x in dedup(L) if not isbk(x) and not isck(x)])))
    rep.same("p3 events (multiset, backups and check results aside)", nb(ea), nb(eb))
    cset = lambda L: sorted(set(map(str, norm3([list(x) for x in L if isck(x)]))))
    rep.same("p3 check result events (set)", cset(ea), cset(eb))
    # which pass of the 30 s backup worker reached a device first (after build / scheduled) is timing: as in Phase 2
    bset = lambda L: sorted(set(map(str, norm3([[x[1], re.sub(r"\((after build|scheduled)\): (unchanged since )?", "(KIND): ", x[4])] for x in L if isbk(x)]))))
    rep.same("p3 backup events (per device)", bset(ea), bset(eb))
    da, db_ = D.db_dump(S[0]), D.db_dump(S[1])
    for d in (da, db_):
        d["devices"] = sort_items(d["devices"])
        d["kv"]["runners"] = dict(sorted((d["kv"].get("runners") or {}).items()))
        d["kv"].pop("auto_res", None)
    rep.same("p3 database contents", norm3(da), norm3(db_))
    rep.same("p3 rma table", norm3(rma_rows(S[0])), norm3(rma_rows(S[1])))
    ra, rb = D.raw_json_texts(S[0]), D.raw_json_texts(S[1])
    items_dev = {n for n, d in byn.items() if d.get("aggregate")}
    for n in sorted(set(ra["devices"]) | set(rb["devices"])):
        if n in items_dev:
            continue
        rep.same("p3 stored JSON text: device " + n, norm3(ra["devices"].get(n)), norm3(rb["devices"].get(n)))
    for key in sorted(set(ra["kv"]) | set(rb["kv"])):
        if key in ("auto_res", "runners"):
            continue
        rep.same("p3 stored JSON text: kv " + key, norm3(ra["kv"].get(key)), norm3(rb["kv"].get(key)))
    rep.same("p3 state dir files", sorted(norm3(os.listdir(S[0]))), sorted(norm3(os.listdir(S[1]))))
    fa, fb2 = state_files(S[0]), state_files(S[1])
    rep.same("p3 state dir file contents", fa, {k: v for k, v in fb2.items()})
    bk = lambda s: {d: sorted(open(os.path.join(s, "backups", d, f)).read() for f in os.listdir(os.path.join(s, "backups", d))) for d in sorted(os.listdir(os.path.join(s, "backups")))}
    rep.same("p3 backup contents", bk(S[0]), bk(S[1]))
    ka = open(json.load(open(PY.conf))["kea_live"]).read()
    kb = open(json.load(open(GO.conf))["kea_live"]).read().replace("/go/kea.sock", "/py/kea.sock")
    rep.same("p3 kea-live.conf (byte-identical)", ka, kb)
    # alerts
    eeonce = lambda L: sorted(set(x for x in L if "engine error" in x)) + [x for x in L if "engine error" not in x]
    wa = sorted(eeonce([json.dumps(norm3(json.loads(x["body"])), sort_keys=True) for x in hooks["python"][0].got]))
    wb = sorted(eeonce([json.dumps(norm3(json.loads(x["body"])), sort_keys=True) for x in hooks["go"][0].got]))
    print("  alerts: webhook python %d, go %d; mail python %d, go %d" % (len(wa), len(wb), len(hooks["python"][1].got), len(hooks["go"][1].got)))
    rep.same("p3 webhook alerts (multiset)", wa, wb)
    rep.same("p3 webhook content type", sorted(set(x["ctype"] for x in hooks["python"][0].got)), sorted(set(x["ctype"] for x in hooks["go"][0].got)))
    rep.same("p3 webhook raw JSON form (first)", norm3(hooks["python"][0].got[0]["body"]) if hooks["python"][0].got else None,
             norm3(hooks["go"][0].got[0]["body"]) if hooks["go"][0].got else None)
    ma = sorted(eeonce([json.dumps(norm3(x), sort_keys=True) for x in hooks["python"][1].got]))
    mb = sorted(eeonce([json.dumps(norm3(x), sort_keys=True) for x in hooks["go"][1].got]))
    rep.same("p3 mail alerts (envelope and message, multiset)", ma, mb)
    # rma-restore (service stopped): a maintenance install from the hand-over export
    for E in eng:
        sw = os.path.join(E.base, "restore-state")
        shutil.rmtree(sw, ignore_errors=True)
        c = json.load(open(E.conf))
        c["state_dir"] = sw
        json.dump(c, open(E.conf, "w"))
    cmp("p3 cli rma-restore usage", lambda E: E.cmd("rma-restore", zf[E.name])[:2])
    cmp("p3 cli rma-restore unknown device", lambda E: E.cmd("rma-restore", zf[E.name], "--device", "NOPE")[:2])
    cmp("p3 cli rma-restore", lambda E: norm3(E.cmd("rma-restore", zf[E.name], "--device", "ACME-ACME-FEF-SP01" if "ACME-ACME-FEF-SP01" in byn else lf)[:2]))
    S2 = [os.path.join(E.base, "restore-state") for E in eng]
    da, db_ = D.db_dump(S2[0]), D.db_dump(S2[1])
    rep.same("p3 rma-restore database", norm3(da), norm3(db_))
    rep.same("p3 rma-restore rma table", norm3(rma_rows(S2[0])), norm3(rma_rows(S2[1])))
    ra, rb = D.raw_json_texts(S2[0]), D.raw_json_texts(S2[1])
    rep.same("p3 rma-restore stored JSON text", ra, rb)


V1_LINES = ["nv set qos congestion-control default-global traffic-class 3 min-threshold 204800",
            "nv set qos congestion-control default-global traffic-class 3 max-threshold 1843200",
            "nv set qos congestion-control default-global traffic-class 3 probability 100"]


def profile_view(r):
    """The profile's id and SHA-256 cover its approval time: checked against Python's own canonical form, then masked."""
    v = json.loads(r[2])
    p = v.get("profile")
    if p:
        canon = lambda o: json.dumps(o, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        body = {k: x for k, x in p.items() if k not in ("id", "sha256")}
        idok = p["id"].endswith("-" + hashlib.sha256(canon(body).encode()).hexdigest()[:8])
        shaok = p["sha256"] == hashlib.sha256(canon(dict(body, id=p["id"])).encode()).hexdigest()
        p["id"] = re.sub(r"-[0-9a-f]{8}$", "-<H>", p["id"])
        v["msg"] = re.sub(r"-[0-9a-f]{8}$", "-<H>", v["msg"])
        p["sha256"] = "<SHA>"
        v["checks"] = {"id": idok, "sha256": shaok}
    return {"status": r[0], "body": norm3(v)}


def tuned_rest(plan):
    """Every lossless back-end switch (the rollout's scope)."""
    for b in plan["builds"]:
        if b["key"] == "be":
            for c in b["gate"]:
                if c.get("kind") == "lossless":
                    return list(c.get("devices", []))
    return []


def plan_runner(plan, kind):
    return next(r for r in plan.get("runners", []) if r["kind"] == kind)


def runner_of(plan, kind):
    return next((r for r in plan.get("runners", []) if r["kind"] == kind), None)


def byn0(plan):
    return {d["name"]: d for d in plan["devices"]}


def D_build(plan, n):
    for b in plan["builds"]:
        for w in b["waves"]:
            if n in w["devices"]:
                return b["key"]
    return plan["builds"][0]["key"]


def tune_values(plan):
    """Two proposals the plan's tuning accepts (B-NV-SN5600: the values Phase 3 used)."""
    ps = (plan.get("tuning") or {}).get("params", [])
    keys = [p["key"] for p in ps]
    if "ECN_MIN_KB" in keys and "ECN_MAX_KB" in keys and "ECN_PROB" in keys:
        return {"ECN_MIN_KB": 200, "ECN_MAX_KB": "1800"}, {"ECN_PROB": 90.0}
    def other(p, k):
        st = p.get("step") or 1
        v = p["base"] + st * k
        if v > p["max"]:
            v = p["base"] - st * k
        return round(max(p["min"], min(p["max"], v)), 6)
    if not ps:
        return {"X": 1}, {"X": 2}
    v1 = {ps[0]["key"]: other(ps[0], 2)}
    if len(ps) > 1:
        v1[ps[1]["key"]] = str(other(ps[1], 1))
    v2 = {ps[-1]["key"]: float(other(ps[-1], 3))}
    return v1, v2


def tune_lines(plan, values):
    """Python's tuning_lines for these values over the plan's base values."""
    tp = plan.get("tuning") or {}
    vals = {p["key"]: p["base"] for p in tp.get("params", [])}
    vals.update(values)
    def fmt(x):
        x = float(x)
        return str(int(x)) if x == int(x) else ("%g" % x)
    def f(m):
        x = float(vals[m.group(1)])
        if m.group(2):
            x = x * float(m.group(3)) if m.group(2) == "*" else x / float(m.group(3))
        return fmt(round(x, 3))
    return [re.sub(r"\{([A-Z0-9_]+)(?:([*/])(\d+(?:\.\d+)?))?\}", f, l) for l in tp.get("lines", [])]


def runner_blocked(state, plan, bk):
    """A wave held by a runner that failed (the scenario fixes it and retries)."""
    for r in plan.get("runners", []):
        rs = state["runners"].get(r["key"]) or {}
        if rs.get("state") == "failed":
            return True
    return False


# ------------------------------------------------------------------------------------------------ standby pair

def run_ha(args, run, rep):
    name = os.path.basename(os.path.dirname(run)) + "-p3ha"
    print("== %s (phase 3: standby host)" % name)
    W, web, plan, tmpl0 = prep_engines(args, run, name, keep_runners=False)
    args = D.shipped(args, W)
    ctl = os.path.join(W, "ctl")
    sc = {"probe_every_s": 2, "done_probe_s": 10, "check_every_s": 1e9, "backup_every_h": 1000, "tftp": False,
          "alert_webhook": "http://127.0.0.1:19699/down", "smtp_host": "127.0.0.1", "smtp_port": 19698, "smtp_to": "noc@example.net"}
    pairs = {}
    for tag, base_port in (("python", 18380), ("go", 28380)):
        crt, key = make_cert(os.path.join(W, "certs-" + tag), "standby")
        pc, pk = make_cert(os.path.join(W, "certs-" + tag), "primary")
        argv = [sys.executable, "-I", args.py] if tag == "python" else [args.go]
        sb_port = base_port + 63
        P = D.Engine(tag, argv, os.path.join(W, tag[:2] + "-primary"), (base_port, base_port + 3, base_port + 1), plan, tmpl0, web,
                     dict(sc, peer_url="https://127.0.0.1:%d" % sb_port, sync_every_s=2))
        # the standby probes nothing (its prober would race the primary's syncs); after promote it is restarted as primary
        S = D.Engine(tag, argv, os.path.join(W, tag[:2] + "-standby"), (base_port + 60, sb_port, base_port + 61), plan, tmpl0, web,
                     dict(sc, role="standby", http_bind="127.0.0.1", tls_cert=crt, tls_key=key, probe_every_s=1e9, done_probe_s=1e9))
        for E in (P, S):
            open(os.path.join(E.base, "fabric", "secrets.env"), "w").write("ESTATE_SYNC_TOKEN=%s\n" % SYNC_TOKEN)
            E.cmd("init", "--admin-pass", D.ADMIN_PW)
        shutil.copy(pc, os.path.join(P.base, "fabric", "console.crt"))
        shutil.copy(pk, os.path.join(P.base, "fabric", "console.key"))
        pairs[tag] = (P, S)
    eng_p = (pairs["python"][0], pairs["go"][0])
    eng_s = (pairs["python"][1], pairs["go"][1])
    allE = eng_p + eng_s
    sim0 = os.path.join(W, "sim0")
    os.makedirs(sim0)
    A = P2.Answers(plan, web, sim0)
    for d in plan["devices"]:
        A.device(d["name"])
    for E in allE:
        shutil.copytree(sim0, os.path.join(E.base, "sim"))
        open(os.path.join(E.base, "push_configs.py"), "w").write(PUSH_PY)
        for sc in {d["push"]["cmd"][0] for d in plan["devices"] if d.get("push")}:
            open(os.path.join(E.base, sc), "w").write(PUSH_PY)
    os.makedirs(ctl, exist_ok=True)
    os.environ["FAKESSH_CTL"] = ctl
    ck = {}

    def cmp(what, f, E2):
        a, b = [f(E) for E in E2]
        rep.same(what, a, b)
        return a, b

    def login(E):
        import http.client
        c = http.client.HTTPSConnection("127.0.0.1", E.console, timeout=10, context=ssl._create_unverified_context())
        c.request("POST", "/api/login", body=json.dumps({"user": "admin", "password": D.ADMIN_PW}), headers={"Content-Type": "application/json"})
        r = c.getresponse()
        m = re.search(r"estate_session=([A-Za-z0-9_-]+)", r.getheader("Set-Cookie", ""))
        r.read()
        return m.group(1) if m else None

    def https(E, method, path, data=None, cookie=None):
        import http.client
        c = http.client.HTTPSConnection("127.0.0.1", E.console, timeout=30, context=ssl._create_unverified_context())
        hs = {"X-Requested-With": "estate-console", "Content-Type": "application/json"}
        if cookie:
            hs["Cookie"] = "estate_session=" + cookie
        c.request(method, path, body=json.dumps(data) if data is not None else None, headers=hs)
        r = c.getresponse()
        b = r.read()
        c.close()
        return r.status, dict((k.lower(), v) for k, v in r.getheaders()), b

    fake = None
    try:
        fake = fake_devices(W, plan)
        time.sleep(2)
        for E in allE:
            E.start(args.console if E.name == "python" else None)
        for E in allE:
            c = login(E)
            https(E, "POST", "/api/password", {"current": D.ADMIN_PW, "new": D.NEW_PW}, c)
            ck[E.base] = None
        import http.client

        def login2(E):
            c = http.client.HTTPSConnection("127.0.0.1", E.console, timeout=10, context=ssl._create_unverified_context())
            c.request("POST", "/api/login", body=json.dumps({"user": "admin", "password": D.NEW_PW}), headers={"Content-Type": "application/json"})
            r = c.getresponse()
            m = re.search(r"estate_session=([A-Za-z0-9_-]+)", r.getheader("Set-Cookie", ""))
            r.read()
            return m.group(1)
        for E in allE:
            ck[E.base] = login2(E)

        def st(E):
            return json.loads(https(E, "GET", "/api/state", cookie=ck[E.base])[2])

        def st2(E):
            return json.loads(https(E, "GET", "/api/state", cookie=login2(E))[2])

        def wait(E2, pred, what, timeout=300):
            t0 = time.time()
            while time.time() - t0 < timeout:
                try:
                    if all(pred(E) for E in E2):
                        return True
                except (KeyError, ValueError, TypeError):
                    pass
                time.sleep(1)
            rep.same("timeout: " + what, True, False)
            return False

        cmp("ha standby refuses actions", lambda E: rv(https(E, "POST", "/api/run", {"plane": "all"}, ck[E.base])), eng_s)
        cmp("ha sync upload without token", lambda E: rv(https(E, "POST", "/sync/upload", None)), eng_s)
        for E in eng_p:
            https(E, "POST", "/api/readiness/confirm", {"key": "_accept", "note": "lab"}, ck[E.base])
            https(E, "POST", "/api/push-credential", {"user": PUSH_USER, "password": PUSH_PW}, ck[E.base])
        cmp("ha run all on the primary", lambda E: rv(https(E, "POST", "/api/run", {"plane": "all"}, ck[E.base])), eng_p)
        time.sleep(3)
        devs = plan["devices"]
        wave1 = plan["builds"][0]["waves"][0]["devices"] + plan["builds"][0]["waves"][1]["devices"]

        def callins(E, names):
            out = []
            for i, d in enumerate(devs):
                if d["name"] not in names or not d.get("ip"):
                    continue
                out.append(D.resp_view(D.hookpost(E, d, i)))
                for f in d.get("files", [])[:1]:
                    out.append(D.resp_view(E.boot("/" + f, src=d["ip"])))
                if d.get("config_signal") == "checkin":
                    out.append(D.resp_view(E.boot("/ci/%s/config" % d["name"], src="127.0.0.1")))
            for d in devs:
                if d["name"] in names and (d.get("config_signal") == "manual" and not d.get("push") or not d.get("ip")) and not d.get("aggregate"):
                    out.append(rv(https(E, "POST", "/api/mark-configured", {"device": d["name"], "note": "lab"}, ck[E.base])))
            return out
        cmp("ha call-ins (first waves)", lambda E: callins(E, set(wave1)), eng_p)
        wait(eng_p, lambda E: bstate_open(st(E), "oob") >= 3, "oob wave 3 open", timeout=300)
        wait(eng_s, lambda E: st(E)["ha"]["last_sync"] and bstate_open(st(E), "oob") >= 3, "standby synced", timeout=120)
        time.sleep(6)
        cmp("ha pins recorded on first contact", lambda E: norm3([x for x in json.loads(json.dumps(D.events_of(os.path.join(E.base, "state")))) if "Pinned the standby" in x[4]][:1]), eng_p)
        cmp("ha standby state view", lambda E: norm3({k: st(E)[k] for k in ("run", "builds", "devices")}), eng_s)
        cmp("ha standby kea status", lambda E: norm3(st(E)["kea"]), eng_s)
        ka = open(json.load(open(eng_s[0].conf))["kea_live"]).read()
        kb = open(json.load(open(eng_s[1].conf))["kea_live"]).read().replace("/go-standby/kea.sock", "/py-standby/kea.sock")
        rep.same("ha standby kea-live.conf (byte-identical)", ka, kb)
        cmp("ha primary state view", lambda E: norm3({k: st(E)[k] for k in ("run", "builds", "devices")}), eng_p)
        cmp("ha promote on the primary (local)", lambda E: rv(E.internal_post("/local/promote", b"{}", E.local_token())), eng_p)
        # the primary fails mid-build: the standby is promoted (CLI) and carries on
        for E in eng_p:
            E.stop()
        cmp("ha cli promote", lambda E: (lambda r: (r[0], norm3(json.loads(r[1]))))(E.cmd("promote")), eng_s)
        cmp("ha promoted conf", lambda E: norm3({k: (re.sub(r"/certs-(python|go)/", "/certs-X/", re.sub(r"/(py|go)-standby", "/X-standby", v)) if isinstance(v, str) else v)
                                                 for k, v in json.load(open(E.conf)).items() if not k.endswith("_port")}), eng_s)
        cmp("ha promoted conf text (json.dump indent=1)", lambda E: re.sub(r'("\w+_port": )\d+', r"\1N", re.sub(r"/certs-(python|go)/", "/certs-X/", re.sub(r"/(py|go)-standby", "/X-standby", open(E.conf).read()))), eng_s)
        cmp("ha promote again from the console (before the restart)", lambda E: rv(https(E, "POST", "/api/promote", {}, login2(E))), eng_s)
        cmp("ha after promote, before the restart: state", lambda E: norm3({k: st2(E)[k] for k in ("run", "builds", "devices", "ha")}), eng_s)
        # the operator moves prov_ip's role to this host and restarts the service (here: binds and probe intervals set back)
        for E in eng_s:
            E.stop()
            c = json.load(open(E.conf))
            c.update(http_bind="127.0.0.1", console_bind="127.0.0.1", probe_every_s=2, done_probe_s=10)
            json.dump(c, open(E.conf, "w"), indent=1)
            E.start(args.console if E.name == "python" else None)
        for E in eng_s:   # the promoted database is the primary's: its sessions, not the standby's
            ck[E.base] = login2(E)
            https(E, "POST", "/api/push-credential", {"user": PUSH_USER, "password": PUSH_PW}, ck[E.base])
        time.sleep(4)
        cmp("ha after promote: state", lambda E: norm3({k: st(E)[k] for k in ("run", "builds", "devices", "ha")}), eng_s)
        rest = [d["name"] for b in plan["builds"][:1] for w in b["waves"][2:] for d2 in [w] for d in devs if d["name"] in d2["devices"]]
        cmp("ha after promote: the rest of oob calls in", lambda E: callins(E, set(rest)), eng_s)
        wait(eng_s, lambda E: bstate_state(st(E), "oob") in ("gate", "blocked", "complete"), "oob at its gate after promote", timeout=600)
        wait(eng_s, lambda E: all(c.get("note") != "automatic check queued" for c in next(b for b in st(E)["builds"] if b["key"] == "oob")["gate"]),
             "oob checks evaluated after promote", timeout=300)
        time.sleep(4)
        cmp("ha after promote: oob", lambda E: norm3(next(b for b in st(E)["builds"] if b["key"] == "oob")), eng_s)
        switch_probe(rep, W, eng_s, "after-promote", tmpl0, web, D.NEW_PW, args.console)
    finally:
        for E in allE:
            E.stop()
        if fake:
            fake.terminate()
            fake.wait()
    for nm, E2 in (("primary", eng_p), ("standby", eng_s)):
        S = [os.path.join(E.base, "state") for E in E2]
        ea, eb = D.events_of(S[0]), D.events_of(S[1])
        if nm == "standby":
            # the standby's own events (merged as "[standby] ...") depend on whether its first probe of a device came before
            # or after the sync that carried the primary's result: compared as a set of kinds, the rest exactly
            kinds = lambda L: sorted(set(re.sub(r"^\[standby\] [^:]*: ", "", re.sub(r"\(.*", "", x[4])) for x in L if x[4].startswith("[standby]")))
            rep.same("ha standby: its own events (kinds)", norm3(kinds(ea)), norm3(kinds(eb)))
            ea = [x for x in ea if not x[4].startswith("[standby]")]
            eb = [x for x in eb if not x[4].startswith("[standby]")]
            merged = lambda L: [list(x[:4]) + [re.sub(r"standby events merged: \d+", "standby events merged: N", x[4])] + list(x[5:]) for x in L]
            ea, eb = merged(ea), merged(eb)
        # the backup worker runs every 30 s in each engine: which Done devices it reached before the stop is timing
        ea = [x for x in ea if "configuration backed up" not in x[4]]
        eb = [x for x in eb if "configuration backed up" not in x[4]]
        rep.same("ha %s events (multiset)" % nm, sorted(map(str, norm3([list(x) for x in ea]))), sorted(map(str, norm3([list(x) for x in eb]))))
        da, db_ = D.db_dump(S[0]), D.db_dump(S[1])
        for d in (da, db_):
            d["kv"].pop("auto_res", None)
            d["kv"].pop("last_sync", None)
            d["kv"].pop("promoted_from", None)   # the number of events before the merge (see above)
        rep.same("ha %s database" % nm, norm3(da), norm3(db_))
        rep.same("ha %s state dir files" % nm, sorted(norm3(os.listdir(S[0]))), sorted(norm3(os.listdir(S[1]))))


def bstate_open(state, bk):
    b = next(x for x in state["builds"] if x["key"] == bk)
    return sum(1 for w in b["waves"] if w["open"])


def bstate_state(state, bk):
    return next(x for x in state["builds"] if x["key"] == bk)["state"]
