#!/usr/bin/env python3
"""Differential test: the Python estate service and aidc-prov side by side on the same plan and scenario.

usage: difftest.py --py estate_build.py --console console.html --go aidc-prov --work DIR --run INSTALLER.run [--run ...]

For each installer: the plan, the Kea template and the web root are taken from its payload. Two variants of the plan
are used: the plan as built (for `check`), and a test variant whose management addresses are moved onto 127.0.0.0/8
(so this host can fetch as any device, by source address) with serial/MAC identities added to some devices. Each
engine gets its own config, state dir, Kea file, tokens and a fake Kea control socket; both share the web root.
Every response is compared after normalizing timestamps, tokens and passwords. Exit 1 on any mismatch.
"""
import argparse, base64, collections, copy, http.client, io, json, os, re, shutil, socket, sqlite3, subprocess, sys, threading, time, zipfile

STEP_WAIT = 3.2          # one engine tick (2 s) and some
TOKEN = "difftest-token-0123456789abcdef"
ADMIN_PW = "Initial-Admin-Pass-1"
NEW_PW = "Changed-Admin-Pass-22"

# ------------------------------------------------------------------------------------------------ preparation

WEB_DIRS = ("bootstrap", "configs", "ztp", "tftp", "eos", "srl", "sros", "hbn", "xr", "images")


def extract(run, out):
    t = open(run, "rb").read()
    i = t.index(b"\n__PAYLOAD_BELOW__\n")
    z = zipfile.ZipFile(io.BytesIO(base64.b64decode(b"".join(t[i + 19:].split()))))
    os.makedirs(os.path.join(out, "web"))
    for n in z.namelist():
        if n.endswith("/") or "/" not in n:
            continue
        rel = n.split("/", 1)[1]
        if rel in ("provision/estate_build.py", "provision/console.html"):   # Python as shipped (shipped() below)
            os.makedirs(os.path.join(out, "shipped"), exist_ok=True)
            open(os.path.join(out, "shipped", os.path.basename(rel)), "wb").write(z.read(n))
            continue
        if rel in ("provision/estate.json", "provision/kea-dhcp4.json"):
            dst = os.path.join(out, os.path.basename(rel))
        elif rel.split("/")[0] in WEB_DIRS:
            dst = os.path.join(out, "web", rel)
        else:
            continue
        dst = os.path.abspath(dst)
        assert dst.startswith(os.path.abspath(out) + os.sep)
        os.makedirs(os.path.dirname(dst), exist_ok=True)
        data = z.read(n)
        if dst.startswith(os.path.join(os.path.abspath(out), "web") + os.sep) and sum(rel.encode()) % 2 == 0:
            # half the files as setup.sh leaves them once secrets.env is rendered (placeholders filled)
            data = re.sub(rb"<([A-Z][A-Za-z0-9_. &-]*)>", rb"filled", data)
        with open(dst, "wb") as f:
            f.write(data)


def shipped(args, W):
    """The Python service and console as the installer ships them (VERSION stamped with the builder release, console.html
    without the reference copy's leading newline). Checked against --py/--console: the same code apart from those two."""
    py, con = os.path.join(W, "shipped", "estate_build.py"), os.path.join(W, "shipped", "console.html")
    ref = open(args.py, encoding="utf-8").read().lstrip("\n")
    got = open(py, encoding="utf-8").read()
    ver = re.search(r'^VERSION = "([^"]*)"', got, re.M).group(1)
    fx = os.path.join(os.path.dirname(os.path.abspath(args.py)), "fixes.json")
    if os.path.exists(fx):   # Phase 4: --py carries the approved Python fixes (HS v1.56); installers built before them get them here
        for x, y in json.load(open(fx)):
            if got.count(x) == 1:
                got = got.replace(x, y)
        open(py, "w", encoding="utf-8").write(got)
    assert got == ref.replace('VERSION = "__BUILDER_VER__"', 'VERSION = "%s"' % ver, 1), "the installer's estate_build.py is not --py"
    assert open(con, "rb").read() == open(args.console, "rb").read().lstrip(b"\n"), "the installer's console.html is not --console"
    a = argparse.Namespace(**vars(args))
    a.py, a.console = py, con
    return a


def loop_ip(k):
    return "127.%d.%d.%d" % (20 + k // 65536, (k // 256) % 256, k % 256)


def test_plan(plan):
    """Management addresses onto 127/8; identities on some devices."""
    p = copy.deepcopy(plan)
    k = 1
    for i, d in enumerate(p["devices"]):
        for key in ("ip", "seed_ip"):
            if d.get(key):
                d[key] = loop_ip(k); k += 1
        for L in d.get("ztp_links", []):
            L["ip"] = loop_ip(k); k += 1
        forms = (d.get("dhcp") or {}).get("sn_forms", [])
        if d.get("dhcp"):
            if forms and i % 3 == 0:
                d["id"] = {"kind": "sn", "value": "SN%05d" % i}
            elif i % 3 == 1:
                d["id"] = {"kind": "mac", "value": "02:00:00:%02x:%02x:%02x" % (i >> 16 & 255, i >> 8 & 255, i & 255)}
            elif forms and i % 7 == 2:
                d["id"] = {"kind": "sn", "value": "SN%05d" % i, "mac": "02:aa:00:00:%02x:%02x" % (i >> 8 & 255, i & 255)}
    return p


def test_template(tmpl, sock):
    t = copy.deepcopy(tmpl)
    t["Dhcp4"]["control-socket"]["socket-name"] = sock
    t["Dhcp4"].setdefault("subnet4", []).append({"id": 50, "subnet": "127.0.0.0/8", "reservations": []})
    return t


class FakeKea(threading.Thread):
    daemon = True

    def __init__(self, path):
        threading.Thread.__init__(self)
        self.path, self.n = path, 0
        self.s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.s.bind(path)
        self.s.listen(16)

    def run(self):
        while True:
            c, _ = self.s.accept()
            try:
                c.settimeout(3)
                buf = b""
                while True:
                    ch = c.recv(65536)
                    if not ch:
                        break
                    buf += ch
                    try:
                        json.loads(buf.decode())
                        break
                    except ValueError:
                        continue
                self.n += 1
                c.sendall(json.dumps({"result": 0, "text": "Configuration successful."}).encode())
            except OSError:
                pass
            finally:
                c.close()


# ------------------------------------------------------------------------------------------------ engines

class Engine:
    def __init__(self, name, argv0, base, ports, plan, tmpl_src, web, extra_conf=None):
        self.name, self.argv0, self.base = name, argv0, base
        self.http, self.console, self.internal = ports
        os.makedirs(base, exist_ok=True)
        self.sock = os.path.join(base, "kea.sock")
        self.kea = FakeKea(self.sock)
        self.kea.start()
        self.plan = os.path.join(base, "estate.json")
        json.dump(plan, open(self.plan, "w"), indent=1)
        self.tmpl = os.path.join(base, "kea-dhcp4.json")
        json.dump(test_template(tmpl_src, self.sock), open(self.tmpl, "w"), indent=2)
        os.makedirs(os.path.join(base, "fabric", "licenses"), exist_ok=True)
        open(os.path.join(base, "fabric", "hook.token"), "w").write(TOKEN + "\n")
        self.conf = os.path.join(base, "estate.conf")
        c = {"http_bind": "127.0.0.1", "http_port": self.http, "console_bind": "127.0.0.1", "console_port": self.console,
             "internal_port": self.internal, "web_root": web, "state_dir": os.path.join(base, "state"),
             "bundle_dir": base, "kea_live": os.path.join(base, "kea-live.conf"),
             "secrets": os.path.join(base, "fabric", "secrets.env"), "tls_cert": os.path.join(base, "fabric", "console.crt"),
             "tls_key": os.path.join(base, "fabric", "console.key"), "hook_token_file": os.path.join(base, "fabric", "hook.token"),
             "local_token_file": os.path.join(base, "fabric", "local.token"), "sniff": False, "tftp": False,
             "reader_sim_dir": os.path.join(base, "sim"), "role": "primary"}
        c.update(extra_conf or {})
        json.dump(c, open(self.conf, "w"), indent=1)
        self.proc = None
        self.log = None

    def cmd(self, *args, env=None):
        argv = list(self.argv0) + list(args) + ["--plan", self.plan, "--conf", self.conf, "--kea-template", self.tmpl]
        e = dict(os.environ)
        e.update(env or {})
        r = subprocess.run(argv, capture_output=True, text=True, env=e, timeout=120)
        return r.returncode, r.stdout, r.stderr

    def start(self, console=None):
        argv = list(self.argv0) + ["serve", "--plan", self.plan, "--conf", self.conf, "--kea-template", self.tmpl]
        if console:
            argv += ["--console", console]
        self.log = open(os.path.join(self.base, "serve.log"), "a")
        self.proc = subprocess.Popen(argv, stdout=self.log, stderr=subprocess.STDOUT)
        for _ in range(200):
            try:
                socket.create_connection(("127.0.0.1", self.console), timeout=0.2).close()
                socket.create_connection(("127.0.0.1", self.http), timeout=0.2).close()
                return
            except OSError:
                time.sleep(0.1)
        raise RuntimeError("%s did not start (see %s)" % (self.name, self.log.name))

    def stop(self):
        if self.proc:
            self.proc.terminate()
            try:
                self.proc.wait(10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
                self.proc.wait()
            self.proc = None
        if self.log:
            self.log.close()
            self.log = None

    # HTTP --------------------------------------------------------------------------------------------
    def req(self, port, method, path, body=None, headers=None, src="127.0.0.1", timeout=30):
        """One request on its own connection ("Connection: close"), read to EOF: the reply is complete only once the
        server has finished the request (the Python service records a fetch after sending the file)."""
        hs = {"Host": "127.0.0.1:%d" % port, "Connection": "close"}
        hs.update(headers or {})
        if body is not None:
            hs["Content-Length"] = str(len(body))
        raw = ("%s %s HTTP/1.1\r\n" % (method, path) + "".join("%s: %s\r\n" % kv for kv in hs.items()) + "\r\n").encode() + (body or b"")
        try:
            s = socket.create_connection(("127.0.0.1", port), timeout=timeout, source_address=(src, 0))
        except OSError as e:
            return "dropped:" + type(e).__name__, {}, b""
        try:
            s.sendall(raw)
            buf = b""
            while True:
                ch = s.recv(1 << 20)
                if not ch:
                    break
                buf += ch
        except OSError as e:
            if not buf:
                return "dropped:" + type(e).__name__, {}, b""
        finally:
            s.close()
        if not buf:
            return "dropped:empty", {}, b""
        head, _, data = buf.partition(b"\r\n\r\n")
        lines = head.decode("latin-1").split("\r\n")
        status = int(lines[0].split()[1])
        h = {}
        for ln in lines[1:]:
            k, _, v = ln.partition(":")
            h[k.strip().lower()] = v.strip()
        if "chunked" in h.get("transfer-encoding", ""):
            out, rest = b"", data
            while rest:
                n, _, rest = rest.partition(b"\r\n")
                n = int(n.split(b";")[0], 16)
                if n == 0:
                    break
                out, rest = out + rest[:n], rest[n + 2:]
            data = out
        if method == "HEAD":
            data = b""
        return status, h, data

    def boot(self, path, src="127.0.0.1", method="GET"):
        return self.req(self.http, method, path, src=src)

    def con(self, method, path, data=None, cookie=None, csrf=True, raw=None, timeout=30):
        hs = {}
        if cookie:
            hs["Cookie"] = "estate_session=" + cookie
        if csrf:
            hs["X-Requested-With"] = "estate-console"
        body = raw if raw is not None else (json.dumps(data).encode() if data is not None else None)
        if body is not None:
            hs["Content-Type"] = "application/json"
        return self.req(self.console, method, path, body, hs, timeout=timeout)

    def internal_post(self, path, body, token):
        hs = {"X-Estate-Token": token} if token is not None else {}
        return self.req(self.internal, "POST", path, body, hs)

    def local_token(self):
        try:
            return open(os.path.join(self.base, "fabric", "local.token")).read().strip()
        except OSError:
            return ""

    def login(self, user, pw):
        st, h, b = self.con("POST", "/api/login", {"user": user, "password": pw}, csrf=False)
        m = re.search(r"estate_session=([A-Za-z0-9_-]+)", h.get("set-cookie", ""))
        return st, (m.group(1) if m else None), b


def ev_for(i, d):
    """The DHCP evidence of plan device number i (module level, for the Phase 2 scenario)."""
    ident = d.get("id") or {}
    x = {"ip": d["ip"]}
    if ident.get("kind") == "mac":
        x["mac"] = ident["value"]
    elif ident.get("kind") == "sn" and "client-id" in (d.get("dhcp") or {}).get("sn_forms", []):
        x["cid"] = "00:" + ident["value"].encode().hex()
        x["mac"] = "02:bb:00:00:%02x:%02x" % (i >> 8 & 255, i & 255)
    else:
        x["mac"] = "02:cc:00:00:%02x:%02x" % (i >> 8 & 255, i & 255)
    return x


def hookpost(E, d, i, event="leases4_committed"):
    x = ev_for(i, d)
    lines = ["LEASES4_SIZE=1", "LEASES4_AT0_ADDRESS=%s" % x.get("ip", ""), "LEASES4_AT0_HWADDR=%s" % x.get("mac", ""),
             "LEASES4_AT0_CLIENT_ID=%s" % x.get("cid", ""), "QUERY4_GIADDR=0.0.0.0"]
    return E.internal_post("/hook/lease?event=" + event, "\n".join(lines).encode(), TOKEN)


ACCEPT_TEXT = ("ACCEPT T1 port_gbps=400 min_gbps=392.5\nNIC T1 rx_discards_phy 0\nACCEPT T2 min_gbps=90 max_gbps=97 mean_gbps=95.25\n"
               "NIC T2 np_ecn_marked_roce_packets 12\nACCEPT T3 busbw=401.5\nACCEPT T4 busbw=383.25\n")


# ------------------------------------------------------------------------------------------------ comparison

TS_LO, TS_HI = 1.5e9, 2.5e9
TS_KEYS = {"server_time", "ts", "first", "last", "created", "expires", "last_seen", "clock", "started", "passed", "gate_ts",
           "opened", "closed"}


def norm(v, key=None):
    if isinstance(v, float) and TS_LO < v < TS_HI:
        return "<T>"
    if isinstance(v, dict):
        out = {}
        for k, x in v.items():
            if key in ("ts", "fetched", "wave_ts") and isinstance(x, (int, float)) and not isinstance(x, bool):
                out[k] = "<T>" if x else x
            elif k in ("password",) and isinstance(x, str):
                out[k] = "<PW>"
            else:
                out[k] = norm(x, k)
        return out
    if isinstance(v, list):
        return [norm(x, key) for x in v]
    if isinstance(v, str):
        v = re.sub(r"\d+\.\d GB free", "<N> GB free", v)
        v = re.sub(r"127\.0\.0\.1:[12]80(\d\d)", r"127.0.0.1:PORT\1", v)
        v = re.sub(r"127\.0\.0\.1:[12]84(\d\d)", r"127.0.0.1:CPORT\1", v)
        v = v.replace("/go/", "/py/").replace("/go-t/", "/py-t/")
        v = re.sub(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", "<ISO>", v)
    return v


def jload(b):
    try:
        return json.loads(b.decode())
    except (ValueError, UnicodeDecodeError):
        return b.decode(errors="replace")


class Report:
    def __init__(self):
        self.n, self.bad = 0, []

    def same(self, what, a, b):
        self.n += 1
        if a != b:
            self.bad.append(what)
            sa, sb = json.dumps(a, sort_keys=True, default=str), json.dumps(b, sort_keys=True, default=str)
            k = next((i for i in range(min(len(sa), len(sb))) if sa[i] != sb[i]), min(len(sa), len(sb)))
            print("  MISMATCH %s\n    py: ...%s\n    go: ...%s" % (what, sa[max(0, k - 160):k + 200], sb[max(0, k - 160):k + 200]))
            if isinstance(a, list) and isinstance(b, list):   # Phase 5: a list compared as a multiset: what each side has more of
                import collections
                ca, cb = collections.Counter(map(str, a)), collections.Counter(map(str, b))
                print("    only py: %s\n    only go: %s" % ([x[:300] for x in (ca - cb)][:8], [x[:300] for x in (cb - ca)][:8]))
            return False
        return True


def resp_view(r, ctype=True):
    st, h, b = r
    out = {"status": st, "body": norm(jload(b)) if b else ""}
    if ctype:
        out["ctype"] = h.get("content-type")
    return out


def events_of(state_dir):
    db = sqlite3.connect(os.path.join(state_dir, "estate.db"))
    rows = [tuple(r) for r in db.execute("SELECT level, device, stage, flag, text, user FROM events ORDER BY id")]
    db.close()
    return rows


def db_dump(state_dir):
    db = sqlite3.connect(os.path.join(state_dir, "estate.db"))
    db.row_factory = sqlite3.Row
    out = {}
    out["devices"] = {r["name"]: norm(json.loads(r["data"])) for r in db.execute("SELECT * FROM devices")}
    out["unclaimed"] = {r["key"]: norm(json.loads(r["data"])) for r in db.execute("SELECT * FROM unclaimed")}
    out["kv"] = {r["k"]: norm(json.loads(r["v"])) for r in db.execute("SELECT * FROM kv")}
    out["bindings"] = sorted((r["name"], r["kind"], r["value"], r["source"], r["user"]) for r in db.execute("SELECT * FROM bindings"))
    out["blocked"] = sorted(tuple(r)[:2] for r in db.execute("SELECT * FROM blocked"))
    out["audit"] = [(r["user"], r["action"], r["target"], r["detail"]) for r in db.execute("SELECT * FROM audit ORDER BY id")]
    out["users"] = [(r["name"], r["role"], r["must_change"], r["planes"], r["disabled"], r["org"], r["pw"][:14] == "pbkdf2$240000$")
                    for r in db.execute("SELECT * FROM users ORDER BY name")]
    out["gates"] = sorted((r["build"], r["chk"], r["result"], r["user"], r["note"]) for r in db.execute("SELECT * FROM gates"))
    out["schema"] = sorted(tuple(r) for r in db.execute("SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'"))
    db.close()
    return out


def raw_json_texts(state_dir):
    """The stored JSON text itself (Python json.dumps form), timestamps masked: proves byte compatibility."""
    db = sqlite3.connect(os.path.join(state_dir, "estate.db"))
    f = lambda s: re.sub(r"1[5-9]\d{8}\.\d+|2[0-4]\d{8}\.\d+", "T", s)
    out = {"devices": {n: f(d) for n, d in db.execute("SELECT name, data FROM devices")},
           "kv": {k: f(v) for k, v in db.execute("SELECT k, v FROM kv")}}
    db.close()
    return out


# ------------------------------------------------------------------------------------------------ scenario

def console_reads(e, ck, plan):
    v = {}
    for p in ("/api/whoami", "/api/users", "/api/plan", "/api/state", "/api/readiness", "/api/events?since=0&limit=100000",
              "/api/gate-evidence?build=oob&check=software", "/api/runner-log/bnk", "/api/nope"):
        v[p] = resp_view(e.con("GET", p, cookie=ck, csrf=False))
    for x in ("asbuilt", "sot-status", "audit", "events", "readiness", "bogus"):
        st, h, b = e.con("GET", "/api/export/%s.csv" % x, cookie=ck, csrf=False)
        body = norm(b.decode(errors="replace"))
        if x in ("audit", "events"):
            body = "\n".join(sorted(body.splitlines()))
        v["export " + x] = {"status": st, "ctype": h.get("content-type"), "cd": h.get("content-disposition"), "body": body}
    for d in plan["devices"][:6] + plan["devices"][-3:]:
        r = resp_view(e.con("GET", "/api/device/" + d["name"], cookie=ck, csrf=False))
        if isinstance(r["body"], dict):
            evs = r["body"].get("events") or []
            r["body"]["events"] = sorted(json.dumps({k: x[k] for k in x if k != "id"}, sort_keys=True) for x in evs)
        v["device " + d["name"]] = r
    v["device unknown"] = resp_view(e.con("GET", "/api/device/NOPE", cookie=ck, csrf=False))
    v["device diff unknown"] = resp_view(e.con("GET", "/api/device/NOPE/diff", cookie=ck, csrf=False))
    ev = v.pop("/api/events?since=0&limit=100000")
    v["events(sorted)"] = sorted(json.dumps({k: x[k] for k in x if k != "id"}, sort_keys=True) for x in ev["body"]) if isinstance(ev["body"], list) else ev
    return v


def compare_views(rep, tag, a, b):
    for k in a:
        rep.same("%s %s" % (tag, k), a[k], b.get(k))


def run_plan(args, run, rep):
    name = os.path.basename(os.path.dirname(run))
    W = os.path.join(args.work, name[-12:])   # short: the fake Kea socket path must stay under the AF_UNIX limit
    if os.path.exists(W):
        shutil.rmtree(W)
    os.makedirs(W)
    print("== %s" % name)
    extract(run, W)
    args = shipped(args, W)
    web = os.path.join(W, "web")
    plan0 = json.load(open(os.path.join(W, "estate.json")))
    tmpl0 = json.load(open(os.path.join(W, "kea-dhcp4.json")))
    plan = test_plan(plan0)
    # a license for one serial-identified device
    PY = Engine("python", [sys.executable, "-I", args.py], os.path.join(W, "py"), (18080, 18443, 18081), plan, tmpl0, web)
    GO = Engine("go", [args.go], os.path.join(W, "go"), (28080, 28443, 28081), plan, tmpl0, web)
    sn_dev = next((d for d in plan["devices"] if (d.get("id") or {}).get("kind") == "sn"), None)
    for E in (PY, GO):
        if sn_dev:
            open(os.path.join(E.base, "fabric", "licenses", sn_dev["id"]["value"] + ".lic"), "w").write("LICENSE " + sn_dev["id"]["value"] + "\n")
    eng = (PY, GO)

    def both(f):
        return [f(E) for E in eng]

    def cmp(what, f, view=lambda r: r):
        a, b = both(f)
        if os.environ.get("DIFFTEST_DUMP"):   # the Python side of each comparison, for inspection
            with open(os.environ["DIFFTEST_DUMP"], "a") as fh:
                fh.write(json.dumps({"what": what, "py": a}, default=str) + "\n")
        rep.same(what, view(a), view(b))
        return a, b

    # ---- CLI: check (plan as built + test plan), init, user
    for label, pl, tp in (("as built", plan0, tmpl0), ("test", plan, None)):
        outs = []
        for E in eng:
            p = os.path.join(E.base, "plan-%s.json" % label.replace(" ", ""))
            json.dump(pl, open(p, "w"))
            k = os.path.join(E.base, "kea-%s.json" % label.replace(" ", ""))
            json.dump(tp if tp else test_template(tmpl0, E.sock), open(k, "w"))
            r = subprocess.run(list(E.argv0) + ["check", "--plan", p, "--conf", E.conf, "--kea-template", k], capture_output=True, text=True)
            outs.append((r.returncode, r.stdout))
        rep.same("check (%s)" % label, outs[0], outs[1])
    cmp("check --readiness", lambda E: E.cmd("check", "--readiness")[:2], lambda r: norm(list(r)))
    cmp("init short password", lambda E: E.cmd("init", "--admin-pass", "short")[:2])
    cmp("init", lambda E: E.cmd("init", "--admin-pass", ADMIN_PW)[:2])
    cmp("init again", lambda E: E.cmd("init")[:2])
    cmp("user add op1", lambda E: E.cmd("user", "add", "op1", "--role", "operator", "--planes", "fe,oob")[:2], lambda r: (r[0], re.sub(r": \S+   ", ": <PW>   ", r[1])))
    cmp("user add dup", lambda E: E.cmd("user", "add", "op1")[:2])
    cmp("user add badplane", lambda E: E.cmd("user", "add", "op2", "--role", "operator", "--planes", "fe,xx")[:2])
    cmp("user disable admin", lambda E: E.cmd("user", "disable", "admin")[:2])
    cmp("user reset nobody", lambda E: E.cmd("user", "reset", "nobody")[:2])
    cmp("user list", lambda E: E.cmd("user", "list")[:2])
    cmp("user usage", lambda E: E.cmd("user")[:2])

    for E in eng:
        E.start(args.console if E is PY else None)
    try:
        time.sleep(STEP_WAIT)
        # ---- console: page, login, forced password change
        a, b = both(lambda E: E.con("GET", "/", csrf=False))
        rep.same("GET / (console page bytes)", (a[0], a[1].get("content-type"), a[2]), (b[0], b[1].get("content-type"), b[2]))
        rep.same("console page == console.html", b[2], open(args.console, "rb").read())
        cmp("GET /api/state no login", lambda E: resp_view(E.con("GET", "/api/state", csrf=False)))
        cmp("login wrong", lambda E: E.login("admin", "nope")[::2])
        cmp("login unknown", lambda E: E.login("ghost", "nope")[::2])
        cks = both(lambda E: E.login("admin", ADMIN_PW)[1])
        rep.same("login ok (cookie issued)", bool(cks[0]), bool(cks[1]))
        ck = dict(zip(("python", "go"), cks))
        cmp("whoami must_change", lambda E: resp_view(E.con("GET", "/api/whoami", cookie=ck[E.name], csrf=False)))
        cmp("state while must_change", lambda E: resp_view(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)))
        cmp("post csrf missing", lambda E: resp_view(E.con("POST", "/api/run", {}, cookie=ck[E.name], csrf=False)))
        cmp("post while must_change", lambda E: resp_view(E.con("POST", "/api/run", {}, cookie=ck[E.name])))
        cmp("password too short", lambda E: resp_view(E.con("POST", "/api/password", {"current": ADMIN_PW, "new": "short"}, cookie=ck[E.name])))
        cmp("password wrong current", lambda E: resp_view(E.con("POST", "/api/password", {"current": "x", "new": NEW_PW}, cookie=ck[E.name])))
        cmp("password same", lambda E: resp_view(E.con("POST", "/api/password", {"current": ADMIN_PW, "new": ADMIN_PW}, cookie=ck[E.name])))
        cmp("password change", lambda E: resp_view(E.con("POST", "/api/password", {"current": ADMIN_PW, "new": NEW_PW}, cookie=ck[E.name])))
        cmp("bad json", lambda E: resp_view(E.con("POST", "/api/run", raw=b"{nope", cookie=ck[E.name])))
        # users
        cmp("users/add bad name", lambda E: resp_view(E.con("POST", "/api/users/add", {"name": "x"}, cookie=ck[E.name])))
        cmp("users/add admin2", lambda E: resp_view(E.con("POST", "/api/users/add", {"name": "admin2", "role": "admin", "org": "dwr"}, cookie=ck[E.name])))
        cmp("users/add viewer", lambda E: resp_view(E.con("POST", "/api/users/add", {"name": "view1", "role": "viewer"}, cookie=ck[E.name])))
        cmp("users/add op list planes", lambda E: resp_view(E.con("POST", "/api/users/add", {"name": "op3", "role": "operator", "planes": ["edge", "oob"]}, cookie=ck[E.name])))
        cmp("users/add exists", lambda E: resp_view(E.con("POST", "/api/users/add", {"name": "op1"}, cookie=ck[E.name])))
        cmp("users/update demote", lambda E: resp_view(E.con("POST", "/api/users/update", {"name": "admin2", "role": "operator", "planes": "be"}, cookie=ck[E.name])))
        cmp("users/update last admins", lambda E: resp_view(E.con("POST", "/api/users/update", {"name": "admin", "disabled": True}, cookie=ck[E.name])))
        cmp("users/update nobody", lambda E: resp_view(E.con("POST", "/api/users/update", {"name": "zz"}, cookie=ck[E.name])))
        cmp("users/reset", lambda E: resp_view(E.con("POST", "/api/users/reset", {"name": "view1"}, cookie=ck[E.name])))
        cmp("users/reset nobody", lambda E: resp_view(E.con("POST", "/api/users/reset", {"name": "zz"}, cookie=ck[E.name])))
        # viewer and operator logins (temporary password from user add on the CLI is unknown here: reset through the API)
        vpw = {}
        for E in eng:
            st, h, b = E.con("POST", "/api/users/reset", {"name": "op1"}, cookie=ck[E.name])
            vpw[E.name] = json.loads(b)["password"]
        opck = {}
        for E in eng:
            _, c, _ = E.login("op1", vpw[E.name])
            E.con("POST", "/api/password", {"current": vpw[E.name], "new": "Operator-Pass-123"}, cookie=c)
            _, opck[E.name], _ = E.login("op1", "Operator-Pass-123")
        cmp("operator whoami", lambda E: resp_view(E.con("GET", "/api/whoami", cookie=opck[E.name], csrf=False)))
        cmp("operator users", lambda E: resp_view(E.con("GET", "/api/users", cookie=opck[E.name], csrf=False)))
        cmp("operator run all", lambda E: resp_view(E.con("POST", "/api/run", {"plane": "all"}, cookie=opck[E.name])))
        cmp("operator users/add", lambda E: resp_view(E.con("POST", "/api/users/add", {"name": "qq"}, cookie=opck[E.name])))
        be_dev = next((n for b in plan["builds"] if b["key"] == "be" for w in b["waves"] for n in w["devices"]), plan["devices"][-1]["name"])
        cmp("operator outside its planes", lambda E: resp_view(E.con("POST", "/api/retry", {"device": be_dev}, cookie=opck[E.name])))

        cmp("initial console reads", lambda E: console_reads(E, ck[E.name], plan), lambda r: r)

        # ---- DHCP evidence before the run: waiting, unclaimed, reserved address, retired
        devs = [d for d in plan["devices"] if d.get("ip")]
        def hook(E, evs, event="leases4_committed", token=TOKEN):
            lines = ["LEASES4_SIZE=%d" % len(evs)]
            for i, x in enumerate(evs):
                lines += ["LEASES4_AT%d_ADDRESS=%s" % (i, x.get("ip", "")), "LEASES4_AT%d_HWADDR=%s" % (i, x.get("mac", "")),
                          "LEASES4_AT%d_CLIENT_ID=%s" % (i, x.get("cid", ""))]
            if evs and evs[0].get("circuit"):
                lines.append("QUERY4_OPTION_82_SUB_OPTION_1=0x" + evs[0]["circuit"].encode().hex())
            lines.append("QUERY4_GIADDR=0.0.0.0")
            return E.internal_post("/hook/lease?event=" + event, "\n".join(lines).encode(), token)

        def ev_for(i, d):
            ident = d.get("id") or {}
            x = {"ip": d["ip"]}
            if ident.get("kind") == "mac":
                x["mac"] = ident["value"]
            elif ident.get("kind") == "sn" and "client-id" in (d.get("dhcp") or {}).get("sn_forms", []):
                x["cid"] = "00:" + ident["value"].encode().hex()
                x["mac"] = "02:bb:00:00:%02x:%02x" % (i >> 8 & 255, i & 255)
            else:
                x["mac"] = "02:cc:00:00:%02x:%02x" % (i >> 8 & 255, i & 255)
            return x

        cmp("hook no token", lambda E: resp_view(hook(E, [], token=None)))
        cmp("hook bad token", lambda E: resp_view(hook(E, [], token="bad")))
        cmp("local no token", lambda E: resp_view(E.internal_post("/local/start", b"{}", None)))
        early = devs[: max(3, len(devs) // 4)]
        cmp("hook early", lambda E: [resp_view(hook(E, [ev_for(i, d)])) for i, d in enumerate(early)])
        unknown = [{"ip": "10.9.9.%d" % k, "mac": "02:ee:00:00:00:%02x" % k,
                    "circuit": ("%s:%s" % (d["mgmt_link"]["switch"], d["mgmt_link"]["port"])) if d.get("mgmt_link") else ""}
                   for k, d in enumerate(devs[:12])]
        cmp("hook unknown", lambda E: [resp_view(hook(E, [x])) for x in unknown])
        cmp("hook renew + serial client-id", lambda E: resp_view(E.internal_post("/hook/lease?event=lease4_renew",
            b"LEASE4_ADDRESS=10.8.8.8\nLEASE4_HWADDR=\nLEASE4_CLIENT_ID=00:46:4f:58:31:32:33\n", TOKEN)))
        cmp("local status", lambda E: resp_view(E.req(E.internal, "GET", "/local/status")))
        cmp("local handover", lambda E: resp_view(E.req(E.internal, "GET", "/local/handover")))

        # ---- run: readiness hold, accept, start
        cmp("run held by readiness", lambda E: resp_view(E.con("POST", "/api/run", {"plane": "all"}, cookie=ck[E.name])))
        cmp("readiness confirm no note", lambda E: resp_view(E.con("POST", "/api/readiness/confirm", {"key": "ra"}, cookie=ck[E.name])))
        cmp("readiness confirm ra", lambda E: resp_view(E.con("POST", "/api/readiness/confirm", {"key": "ra", "note": "tested"}, cookie=ck[E.name])))
        cmp("readiness confirm bogus", lambda E: resp_view(E.con("POST", "/api/readiness/confirm", {"key": "zz", "note": "x"}, cookie=ck[E.name])))
        cmp("readiness accept", lambda E: resp_view(E.con("POST", "/api/readiness/confirm", {"key": "_accept", "note": "lab"}, cookie=ck[E.name])))
        cmp("pause idle", lambda E: resp_view(E.con("POST", "/api/pause", {}, cookie=ck[E.name])))
        cmp("run edge before oob", lambda E: resp_view(E.con("POST", "/api/run", {"plane": "edge"}, cookie=ck[E.name])))
        cmp("run bogus plane", lambda E: resp_view(E.con("POST", "/api/run", {"plane": "zz"}, cookie=ck[E.name])))
        cmp("run all", lambda E: resp_view(E.con("POST", "/api/run", {"plane": "all"}, cookie=ck[E.name])))
        time.sleep(STEP_WAIT)
        cmp("state after start", lambda E: resp_view(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)))

        # ---- all devices call in
        cmp("hook all", lambda E: [resp_view(hook(E, [ev_for(i, d)])) for i, d in enumerate(devs)])
        cmp("state after hooks", lambda E: resp_view(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)))

        # ---- web root: every file as owner, as another device, as unknown, as local; odd paths
        owned = [(d, f) for d in devs for f in d.get("files", [])]
        fa, _ = cmp("fetch as owner (all files)", lambda E: [(f, resp_view(E.boot("/" + f, src=d["ip"]))) for d, f in owned])
        print("  fetch as owner: %d files, statuses %s" % (len(fa), dict(collections.Counter(x[1]["status"] for x in fa))))
        time.sleep(STEP_WAIT)
        wa, _ = cmp("fetch as wrong device", lambda E: [(f, resp_view(E.boot("/" + f, src=devs[(k + 1) % len(devs)]["ip"]))) for k, (d, f) in enumerate(owned) if devs[(k + 1) % len(devs)] is not d][:400])
        print("  fetch as wrong device: %d, statuses %s" % (len(wa), dict(collections.Counter(x[1]["status"] for x in wa))))
        cmp("fetch as unknown", lambda E: [resp_view(E.boot("/" + f, src="127.99.0.1")) for d, f in owned[:50]])
        cmp("fetch as local", lambda E: [resp_view(E.boot("/" + f, src="127.0.0.1")) for d, f in owned[:50]])
        cmp("HEAD as owner", lambda E: [resp_view(E.boot("/" + f, src=d["ip"], method="HEAD")) for d, f in owned[:20]])
        odd = ["/", "/bootstrap/", "/bootstrap/../ztp/x", "/bootstrap//x", "/./bootstrap/./x.conf", "/%2e%2e/etc/passwd",
               "/bootstrap/%2e/x", "/a%00b", "/images/none.bin", "/bootstrap/missing.conf", "/README.txt", "/ci/x", "/ci/",
               "//ci/" + devs[0]["name"] + "/zzz", "/licenses/NOPE.lic", "/bootstrap/a%2Fb", "/%41%42"]
        if sn_dev:
            odd += ["/licenses/%s.lic" % sn_dev["id"]["value"]]
        srcs = [devs[0]["ip"], "127.99.0.2"] + ([sn_dev["ip"]] if sn_dev else [])
        time.sleep(STEP_WAIT)
        cmp("odd paths", lambda E: [(p, s, resp_view(E.boot(p, src=s))) for p in odd for s in srcs])
        rep.same("go: malformed escape answers 400", GO.boot("/%zz", src=devs[0]["ip"])[0], 400)
        cmp("POST to boot port", lambda E: E.req(E.http, "POST", "/x", b"")[0])
        if owned:
            d0, f0 = owned[0]
            cmp("fetch path variants as owner", lambda E: [resp_view(E.boot(p, src=d0["ip"])) for p in
                ("/" + f0, "//" + f0, "/./" + f0, "/" + f0.replace("/", "/./"), "/" + f0 + "?x=1", "/" + f0.replace("/", "%2F"))])

        time.sleep(STEP_WAIT)
        # ---- device check-ins
        wave1 = plan["builds"][0]["waves"][0]["devices"]
        byname = {d["name"]: d for d in plan["devices"]}
        def ci(E):
            out = []
            for k, n in enumerate(wave1 + [d["name"] for d in devs[:8]]):
                ip = byname[n].get("ip") or "127.0.0.1"
                for stg in (["ztp", "config", "verify"] if k % 3 == 0 else ["fail?msg=boom"] if k % 3 == 1 else ["config/applied%20by%20script", "image"]):
                    out.append(resp_view(E.boot("/ci/%s/%s" % (n, stg), src=ip)))
            out.append(resp_view(E.boot("/ci/%s/config" % wave1[0], src="127.99.0.3")))
            out.append(resp_view(E.boot("/ci/NOPE/config", src="127.0.0.1")))
            out.append(resp_view(E.boot("/ci/%s/weird" % wave1[0], src="127.0.0.1")))
            for d in plan["devices"]:
                if d.get("aggregate"):
                    for nval in ("3", "x", "999999", "-4", " 7 "):
                        out.append(resp_view(E.boot("/ci/%s/count?n=%s" % (d["name"], nval.replace(" ", "%20")), src="127.0.0.1")))
            return out
        cmp("check-ins", ci)
        time.sleep(STEP_WAIT)
        cmp("state after check-ins", lambda E: resp_view(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)))

        # ---- operator actions
        def first_unclaimed(E, i=0):
            st = json.loads(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)[2])
            keys = sorted(u["key"] for u in st["unclaimed"])
            return keys[i] if len(keys) > i else ""
        noid = [d["name"] for d in devs if not d.get("id") and d.get("dhcp")]
        cmp("bind", lambda E: resp_view(E.con("POST", "/api/bind", {"key": first_unclaimed(E), "device": noid[0] if noid else devs[0]["name"]}, cookie=ck[E.name])))
        cmp("bind again", lambda E: resp_view(E.con("POST", "/api/bind", {"key": first_unclaimed(E), "device": noid[0] if noid else devs[0]["name"]}, cookie=ck[E.name])))
        cmp("bind unknown device", lambda E: resp_view(E.con("POST", "/api/bind", {"key": "x", "device": "NOPE"}, cookie=ck[E.name])))
        cmp("dismiss no reason", lambda E: resp_view(E.con("POST", "/api/dismiss", {"key": first_unclaimed(E, 1)}, cookie=ck[E.name])))
        cmp("dismiss", lambda E: resp_view(E.con("POST", "/api/dismiss", {"key": first_unclaimed(E, 1), "reason": "a laptop"}, cookie=ck[E.name])))
        if len(noid) > 1:
            cmp("bind 2", lambda E: resp_view(E.con("POST", "/api/bind", {"key": first_unclaimed(E, 1), "device": noid[1]}, cookie=ck[E.name])))
            cmp("unbind", lambda E: resp_view(E.con("POST", "/api/unbind", {"device": noid[1]}, cookie=ck[E.name])))
            cmp("unbind again", lambda E: resp_view(E.con("POST", "/api/unbind", {"device": noid[1]}, cookie=ck[E.name])))
        cmp("unbind inventory", lambda E: resp_view(E.con("POST", "/api/unbind", {"device": next(d["name"] for d in devs if d.get("id"))}, cookie=ck[E.name])))
        cmp("retry", lambda E: resp_view(E.con("POST", "/api/retry", {"device": wave1[1 % len(wave1)]}, cookie=ck[E.name])))
        cmp("reserve", lambda E: resp_view(E.con("POST", "/api/reserve", {"device": wave1[0]}, cookie=ck[E.name])))
        cmp("mark configured", lambda E: resp_view(E.con("POST", "/api/mark-configured", {"device": devs[-1]["name"], "note": "by hand"}, cookie=ck[E.name])))
        cmp("gate confirm", lambda E: resp_view(E.con("POST", "/api/gate/confirm", {"build": "oob", "check": "ra", "note": "ok"}, cookie=ck[E.name])))
        cmp("gate confirm bogus", lambda E: resp_view(E.con("POST", "/api/gate/confirm", {"build": "oob", "check": "zz"}, cookie=ck[E.name])))
        cmp("gate override short", lambda E: resp_view(E.con("POST", "/api/gate/override", {"build": "fe", "reason": "short"}, cookie=ck[E.name])))
        cmp("gate override", lambda E: resp_view(E.con("POST", "/api/gate/override", {"build": "be", "reason": "lab without hosts"}, cookie=ck[E.name])))
        cmp("count api", lambda E: resp_view(E.con("POST", "/api/count", {"device": next((d["name"] for d in plan["devices"] if d.get("aggregate")), devs[0]["name"]), "n": 5}, cookie=ck[E.name])))
        cmp("unknown endpoint", lambda E: resp_view(E.con("POST", "/api/nope", {}, cookie=ck[E.name])))
        cmp("phase 3 endpoints with an empty body", lambda E: [resp_view(E.con("POST", p, {}, cookie=ck[E.name])) for p in ("/api/push-credential", "/api/rma/start", "/api/promote")])
        cmp("backup without a device", lambda E: resp_view(E.con("POST", "/api/backup", {}, cookie=ck[E.name]))["status"])
        cmp("recheck unknown build", lambda E: resp_view(E.con("POST", "/api/gate/recheck", {"build": "zz"}, cookie=ck[E.name])))
        cmp("viewer post", lambda E: resp_view(E.con("POST", "/api/pause", {}, cookie=E.login("view1", "x")[1])))
        cmp("pause", lambda E: resp_view(E.con("POST", "/api/pause", {}, cookie=ck[E.name])))
        cmp("local resume", lambda E: resp_view(E.internal_post("/local/resume", b"{}", E.local_token())))
        cmp("local pause", lambda E: resp_view(E.internal_post("/local/pause", b"{}", E.local_token())))
        cmp("local start fe (unpassed)", lambda E: resp_view(E.internal_post("/local/start", json.dumps({"plane": "fe"}).encode(), E.local_token())))
        cmp("cli status", lambda E: (lambda r: (r[0], norm(json.loads(r[1]))))(E.cmd("status")))
        cmp("cli start", lambda E: E.cmd("start", "--plane", "oob")[:2])
        # quarantine the rest of wave 1: wave 2 opens
        cmp("quarantine wave 1", lambda E: [resp_view(E.con("POST", "/api/quarantine", {"device": n, "reason": "lab"}, cookie=ck[E.name])) for n in wave1])
        time.sleep(STEP_WAIT * 2)
        cmp("hook all again", lambda E: [resp_view(hook(E, [ev_for(i, d)])) for i, d in enumerate(devs)])
        time.sleep(STEP_WAIT)
        cmp("logout", lambda E: resp_view(E.con("POST", "/api/logout", {}, cookie=opck[E.name])))
        cmp("after logout", lambda E: resp_view(E.con("GET", "/api/whoami", cookie=opck[E.name], csrf=False)))
        # ---- reports (HS v2.02): while building, and after marking and overriding (the closed build is covered by the P2 scenarios)
        def reports(E, c=None):
            c = c or ck[E.name]
            g = lambda q: resp_view(E.con("GET", q, cookie=c, csrf=False))
            out = {"ready": g("/api/reports")}
            for k in ("progress", "final", "post", "cert"):
                for f in ("json", "csv", "html"):
                    out[k + " " + f] = g("/api/report?kind=%s&format=%s" % (k, f))
            out["bad kind"] = g("/api/report?kind=zz")
            out["bad format"] = g("/api/report?kind=progress&format=pdf")
            return out
        cmp("reports while building", reports)
        cmp("reports as viewer", lambda E: reports(E, E.login("view1", "x")[1]))
        def close_build(E):
            out = []
            for d in plan["devices"]:
                out.append(resp_view(E.con("POST", "/api/mark-configured", {"device": d["name"], "note": "closed for the report test"}, cookie=ck[E.name]))["status"])
            time.sleep(STEP_WAIT)
            for _ in range(len(plan["builds"]) + 1):
                for b in plan["builds"]:
                    E.con("POST", "/api/gate/override", {"build": b["key"], "reason": "closed for the report test"}, cookie=ck[E.name])
                time.sleep(STEP_WAIT)
            return out
        cmp("mark every device configured, override every gate", close_build)
        cmp("reports after overrides", reports)
        cmp("final console reads", lambda E: console_reads(E, ck[E.name], plan))
        py_final = console_reads(PY, ck["python"], plan)
        a, b = both(lambda E: open(E.conf and json.load(open(E.conf))["kea_live"]).read())
        rep.same("kea-live.conf (byte-identical)", a, b.replace("/go/kea.sock", "/py/kea.sock"))
        rep.same("kea reloads issued", PY.kea.n > 0, GO.kea.n > 0)
    finally:
        for E in eng:
            E.stop()
    st = both(lambda E: os.path.join(E.base, "state"))
    ea, eb = events_of(st[0]), events_of(st[1])
    print("  events: python %d, go %d" % (len(ea), len(eb)))
    rep.same("events (multiset)", sorted(map(str, norm([list(x) for x in ea]))), sorted(map(str, norm([list(x) for x in eb]))))
    rep.same("database contents", db_dump(st[0]), db_dump(st[1]))
    rep.same("stored JSON text", raw_json_texts(st[0]), raw_json_texts(st[1]))
    rep.same("state dir files", sorted(os.listdir(st[0])), sorted(os.listdir(st[1])))
    rep.same("backups dir entries", sorted(os.listdir(os.path.join(st[0], "backups"))), sorted(os.listdir(os.path.join(st[1], "backups"))))

    # ---- engine switch: Python's state, opened by Python and by Go
    print("  engine switch")
    for E, sub in ((PY, "sw-py"), (GO, "sw-go")):
        dst = os.path.join(E.base, "state")
        sw = os.path.join(E.base, sub)
        shutil.copytree(os.path.join(PY.base, "state"), sw)
        c = json.load(open(E.conf))
        c["state_dir"] = sw
        json.dump(c, open(E.conf, "w"))
    try:
        for E in eng:
            E.start(args.console if E is PY else None)
        time.sleep(STEP_WAIT)
        cks = {}
        for E in eng:
            cks[E.name] = E.login("admin", NEW_PW)[1]
        rep.same("switch: login with Python-made password hash", bool(cks["python"]), bool(cks["go"]))
        cmp("switch: console reads", lambda E: console_reads(E, cks[E.name], plan))
        a = console_reads(GO, cks["go"], plan)
        # Go on Python's state vs Python before the stop (state/plan/users/device views)
        for k in ("/api/plan", "/api/users", "/api/whoami"):
            rep.same("switch: Go view == Python view before stop: " + k, py_final[k], a[k])
        cmp("switch: fetch as owner", lambda E: [(f, resp_view(E.boot("/" + f, src=d["ip"]))) for d, f in owned[:60]])
    finally:
        for E in eng:
            E.stop()

    # ---- timeouts with short limits (seconds), small set
    print("  timeouts")
    T = {"callin_timeout_min": 0.05, "stage_timeout_min": {"1": 0.05, "2": 0.05, "3": 0.05, "4": 0.05}, "os_timeouts": {"junos": {"callin": 0.06}}, "slow_factor": 2}
    PY2 = Engine("python", [sys.executable, "-I", args.py], os.path.join(W, "py-t"), (18090, 18453, 18091), plan, tmpl0, web, T)
    GO2 = Engine("go", [args.go], os.path.join(W, "go-t"), (28090, 28453, 28091), plan, tmpl0, web, T)
    eng = (PY2, GO2)
    crt, key = os.path.join(PY2.base, "fabric", "console.crt"), os.path.join(PY2.base, "fabric", "console.key")
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30", "-subj", "/CN=127.0.0.1",
                    "-keyout", key, "-out", crt], capture_output=True, check=True)
    shutil.copy(crt, os.path.join(GO2.base, "fabric", "console.crt"))
    shutil.copy(key, os.path.join(GO2.base, "fabric", "console.key"))
    cmp("fingerprint", lambda E: E.cmd("fingerprint")[:2])
    cmp("check --readiness (self-signed cert)", lambda E: norm(list(E.cmd("check", "--readiness")[:2])))
    for E in eng:
        E.cmd("init", "--admin-pass", ADMIN_PW)
    try:
        for E in eng:
            E.start(args.console if E is PY2 else None)
        import ssl
        def https_login(E):
            c = http.client.HTTPSConnection("127.0.0.1", E.console, timeout=10, context=ssl._create_unverified_context())
            c.request("POST", "/api/login", body=json.dumps({"user": "admin", "password": ADMIN_PW}), headers={"Content-Type": "application/json"})
            r = c.getresponse()
            ck = r.getheader("Set-Cookie", "")
            r.read()
            c.close()
            return r.status, re.sub(r"estate_session=[A-Za-z0-9_-]+", "estate_session=X", ck)
        cmp("https login (Secure cookie)", https_login)
        slice_csv = os.path.join(W, "slice.csv")
        # keep devices of the first build's last wave: every wave before it opens, and that wave holds (no gate)
        lastw = plan["builds"][0]["waves"][-1]["devices"]
        byn = {d["name"]: d for d in plan["devices"]}
        keepd = [byn[n] for n in lastw if not byn[n].get("ztp_links")][:3] + devs[:2]
        open(slice_csv, "w").write("node,role\n" + "\n".join(d["name"] for d in keepd) + "\nNOT-IN-PLAN\n")
        macs = os.path.join(W, "macs.txt")
        open(macs, "w").write("node mac\n" + "\n".join("%s 02:dd:00:00:00:%02x" % (d["name"], i) for i, d in enumerate(devs[:5])) + "\n")
        for E in eng:
            E.internal_post("/local/start", json.dumps({"plane": "all", "reason": "timeouts"}).encode(), E.local_token())
        time.sleep(2.5)
        cmp("cli lab-slice usage", lambda E: E.cmd("lab-slice")[:2])
        for i, d in enumerate(devs[:6]):
            for E in eng:
                hook(E, [ev_for(i, d)])
        time.sleep(15)
        cmp("cli lab-slice", lambda E: E.cmd("lab-slice", slice_csv, "--macs", macs)[:2])
        # each tick opens one more wave behind the set-aside devices; Python quarantines them one lock at a time, so its
        # ticks open waves while the slice is still being applied (up to a tick or two sooner than Go): wait until the
        # last wave's call-in limit (x slow_factor) has passed in both
        time.sleep(STEP_WAIT * 7)
        for E in eng:
            E.stop()
        st = (os.path.join(PY2.base, "state"), os.path.join(GO2.base, "state"))
        ea, eb = events_of(st[0]), events_of(st[1])
        rep.same("timeouts: events (multiset)", sorted(map(str, norm([list(x) for x in ea]))), sorted(map(str, norm([list(x) for x in eb]))))
        da, db_ = db_dump(st[0]), db_dump(st[1])
        rep.same("timeouts: device states", da["devices"], db_["devices"])
        rep.same("timeouts: run/bstate", (da["kv"].get("run"), da["kv"].get("bstate")), (db_["kv"].get("run"), db_["kv"].get("bstate")))
    finally:
        for E in eng:
            E.stop()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--py", required=True)
    ap.add_argument("--console", required=True)
    ap.add_argument("--go", required=True)
    ap.add_argument("--work", required=True)
    ap.add_argument("--run", action="append", default=[])
    ap.add_argument("--p2", action="append", default=[], help="Phase 2 scenario on this installer (fake devices over SSH)")
    ap.add_argument("--p2-os", action="append", default=[], help="for the matching --p2 (same position): NAME=OS,... overrides")
    ap.add_argument("--p3", action="append", default=[], help="Phase 3 scenario on this installer (pushes, runners, RMA, hand-over, alerts, peers)")
    ap.add_argument("--p3ha", action="append", default=[], help="Phase 3 standby-host scenario on this installer")
    args = ap.parse_args()
    rep = Report()
    for r in args.run:
        run_plan(args, r, rep)
    if args.p2:
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        import p2diff
        for i, r in enumerate(args.p2):
            ov = [x for x in (args.p2_os[i] if i < len(args.p2_os) else "").split(",") if x]
            p2diff.run_p2(args, r, rep, ov, tag="p2os" if ov else "p2")
    if args.p3 or args.p3ha:
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        import p3diff
        for r in args.p3:
            p3diff.run_p3(args, r, rep)
        for r in args.p3ha:
            p3diff.run_ha(args, r, rep)
    print("\n%d comparisons, %d mismatch(es)%s" % (rep.n, len(rep.bad), (": " + "; ".join(rep.bad)) if rep.bad else ""))
    return 1 if rep.bad else 0


if __name__ == "__main__":
    sys.exit(main())
