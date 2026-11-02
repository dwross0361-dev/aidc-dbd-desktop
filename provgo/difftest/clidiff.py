#!/usr/bin/env python3
"""The host-side CLI commands, Python and Go: probe-dhcp (on a veth pair, against fake DHCP servers), record-vendor (real
local accounts with SSH keys and sudo rules), release-vendor (scripted answers on a pipe and on a pseudo-terminal,
against a fake local service that records what it is sent and plays the release to its end).

usage: clidiff.py --py estate_build.py --go aidc-prov --work DIR    (root; creates and removes veth p4va/p4vb and users)
"""
import argparse, json, os, pty, pwd, select, shutil, socket, struct, subprocess, sys, threading, time
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import difftest as D

ap = argparse.ArgumentParser()
ap.add_argument("--py", required=True)
ap.add_argument("--go", required=True)
ap.add_argument("--work", required=True)
a = ap.parse_args()
W = os.path.abspath(a.work)   # absolute: Go refuses a program found through a relative PATH entry
shutil.rmtree(W, ignore_errors=True)
os.makedirs(W)
rep = D.Report()
ENG = {"python": [sys.executable, "-I", a.py], "go": [a.go]}


def run(tag, args, stdin=None, timeout=120):
    r = subprocess.run(ENG[tag] + args, input=stdin, capture_output=True, text=True, timeout=timeout)
    return r.returncode, r.stdout


# ------------------------------------------------------------------------------------------------ probe-dhcp
FAKE_DHCP = r"""
import socket, sys, json
offers = json.loads(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.setsockopt(socket.SOL_SOCKET, socket.SO_BROADCAST, 1)
s.setsockopt(socket.SOL_SOCKET, 25, b"p4vb\0")
s.bind(("", 67))
print("ready", flush=True)
while True:
    data, _ = s.recvfrom(4096)
    if len(data) < 240 or data[0] != 1:
        continue
    xid, mac = data[4:8], data[28:44]
    for sid, yi in offers:
        pkt = b"\x02\x01\x06\x00" + xid + b"\x00\x00\x80\x00" + b"\0" * 4 + socket.inet_aton(yi) + socket.inet_aton(sid) + b"\0" * 4 + mac
        pkt += b"\0" * 192 + b"\x63\x82\x53\x63" + b"\x35\x01\x02" + b"\x00" + b"\x36\x04" + socket.inet_aton(sid) + b"\x01\x04\xff\xff\xff\x00\xff"
        s.sendto(pkt, ("255.255.255.255", 68))
"""
open(os.path.join(W, "fakedhcp.py"), "w").write(FAKE_DHCP)


def fake_dhcp(offers):
    """A DHCP server in its own network namespace on the far end of the veth pair, answering each DISCOVER with an OFFER
    per (server id, offered address)."""
    p = subprocess.Popen(["ip", "netns", "exec", "p4ns", sys.executable, "-I", os.path.join(W, "fakedhcp.py"), json.dumps(offers)],
                         stdout=subprocess.PIPE, text=True)
    p.stdout.readline()
    return p


for c in (["ip", "link", "del", "p4va"], ["ip", "netns", "del", "p4ns"]):
    subprocess.run(c, capture_output=True)
for c in (["ip", "netns", "add", "p4ns"], ["ip", "link", "add", "p4va", "type", "veth", "peer", "name", "p4vb"], ["ip", "link", "set", "p4vb", "netns", "p4ns"],
          ["ip", "link", "set", "p4va", "up"], ["ip", "netns", "exec", "p4ns", "ip", "link", "set", "p4vb", "up"],
          ["ip", "netns", "exec", "p4ns", "ip", "addr", "add", "10.250.0.1/24", "dev", "p4vb"]):
    subprocess.run(c, check=True)
time.sleep(1.5)
try:
    for what, offers, nic in (("no DHCP server", [], "p4va"), ("one rogue server", [("10.250.0.1", "10.250.0.50")], "p4va"),
                              ("two servers", [("10.250.0.1", "10.250.0.51"), ("10.250.0.9", "10.250.0.77")], "p4va"),
                              ("no such NIC", [], "p4nosuch")):
        out = {}
        for tag in ENG:
            f = fake_dhcp(offers) if offers else None
            out[tag] = run(tag, ["probe-dhcp", "--nic", nic])
            if f:
                f.terminate()
                f.wait()
        rep.same("probe-dhcp: " + what, out["python"], out["go"])
        print("  probe-dhcp %-18s rc %s/%s  %s" % (what, out["python"][0], out["go"][0], out["go"][1].strip().replace("\n", " | ")))
finally:
    subprocess.run(["ip", "link", "del", "p4va"], capture_output=True)
    subprocess.run(["ip", "netns", "del", "p4ns"], capture_output=True)

# ------------------------------------------------------------------------------------------------ record-vendor
VA = "/etc/fabric/vendor-access.json"
saved = open(VA, "rb").read() if os.path.exists(VA) else None
os.makedirs("/etc/fabric", exist_ok=True)
users = ("p4dwr1", "p4dwr2")
made = []
try:
    for u in users:
        try:
            pwd.getpwnam(u)
        except KeyError:
            subprocess.run(["useradd", "-m", u], check=True)
            made.append(u)
    if not shutil.which("ssh-keygen"):
        # this host has no OpenSSH client: a stand-in for "ssh-keygen -lf FILE" (OpenSSH's output form, real SHA-256 fingerprints)
        fb = os.path.join(W, "fakebin")
        os.makedirs(fb)
        open(os.path.join(fb, "ssh-keygen"), "w").write("""#!/usr/bin/env python3
import base64, hashlib, sys
assert sys.argv[1] == "-lf"
for ln in open(sys.argv[2]):
    p = ln.split()
    if len(p) < 2:
        continue
    fp = base64.b64encode(hashlib.sha256(base64.b64decode(p[1])).digest()).decode().rstrip("=")
    print("256 SHA256:%s %s (%s)" % (fp, p[2] if len(p) > 2 else "no comment", p[0].split("-", 1)[1].upper()))
""")
        os.chmod(os.path.join(fb, "ssh-keygen"), 0o755)
        os.environ["PATH"] = fb + os.pathsep + os.environ["PATH"]
        print("  (no ssh-keygen on this host: a stand-in computes the fingerprints)")
    for u, n in (("p4dwr1", 2), ("p4dwr2", 0)):
        home = pwd.getpwnam(u).pw_dir
        os.makedirs(os.path.join(home, ".ssh"), exist_ok=True)
        lines = []
        for i in range(n):
            blob = b"\0\0\0\x0bssh-ed25519\0\0\0\x20" + os.urandom(32)
            lines.append("ssh-ed25519 %s %s-key%d" % (__import__("base64").b64encode(blob).decode(), u, i))
        if lines:
            open(os.path.join(home, ".ssh", "authorized_keys"), "w").write("\n".join(lines) + "\n")
    open("/etc/sudoers.d/p4-dwr1", "w").write("p4dwr1 ALL=(ALL) NOPASSWD:ALL\n")
    open("/etc/sudoers.d/p4-other", "w").write("# p4dwr2 is mentioned only in a comment\nsomeone ALL=(ALL) ALL\n")
    for acct in ("p4dwr1,p4dwr2", "none", "root, p4dwr2", "p4nosuchuser"):
        out = {}
        for tag in ENG:
            if os.path.exists(VA):
                os.remove(VA)
            rc, so = run(tag, ["record-vendor", "--account", acct])
            rec = json.load(open(VA)) if os.path.exists(VA) else None
            mode = oct(os.stat(VA).st_mode & 0o777) if rec else None
            if rec:
                rec["recorded"] = "<ISO>" if rec.get("recorded") else rec.get("recorded")
            out[tag] = (rc, so, rec, mode, open(VA).read().count("\n") if rec else 0)
        rep.same("record-vendor --account %s" % acct, out["python"], out["go"])
        print("  record-vendor %-15s rc %s/%s  %s" % (acct, out["python"][0], out["go"][0], json.dumps(out["go"][2])[:150]))
finally:
    for f in ("/etc/sudoers.d/p4-dwr1", "/etc/sudoers.d/p4-other"):
        if os.path.exists(f):
            os.remove(f)
    for u in made:
        subprocess.run(["userdel", "-r", u], capture_output=True)
    if saved is not None:
        open(VA, "wb").write(saved)
    elif os.path.exists(VA):
        os.remove(VA)


# ------------------------------------------------------------------------------------------------ release-vendor
class Local(BaseHTTPRequestHandler):
    script = {}
    got = []

    def log_message(self, *x):
        pass

    def reply(self, obj, code=200):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n).decode() or "{}")
        Local.got.append({"path": self.path, "token": self.headers.get("X-Estate-Token"), "body": body})
        r = Local.script["post"]
        self.reply(r[0], r[1])

    def do_GET(self):
        Local.got.append({"path": self.path})
        seq = Local.script["polls"]
        self.reply({"release": seq.pop(0) if len(seq) > 1 else seq[0]})


srv = HTTPServer(("127.0.0.1", 18981), Local)
threading.Thread(target=srv.serve_forever, daemon=True).start()
tokf = os.path.join(W, "local.token")
open(tokf, "w").write("cli-test-token\n")
conf = os.path.join(W, "estate.conf")
json.dump({"internal_port": 18981, "local_token_file": tokf}, open(conf, "w"))
ANS_OK = "y\ny\nDWR-ATT-2026-11\nnetadmin-secret\nRELEASE\n"
CASES = [
    ("refused by the service", ANS_OK, ({"ok": False, "msg": "two customer admins must exist before DWR access is released"}, 409), [{}]),
    ("done", ANS_OK, ({"ok": True, "msg": "Release DWR access started on 12 device(s)"}, 200),
     [{"state": "running"}, {"state": "done", "removed": 12, "n": 12, "failed": [], "host": ["locked p4dwr1", "removed 2 SSH key(s) of p4dwr1"]}]),
    ("partly failed", "n\ny\n  ref 7  \npw\nRELEASE\n", ({"ok": True, "msg": "started"}, 200),
     [{"state": "failed", "removed": 10, "n": 12, "failed": [{"device": "ACME-OOB-SW%02d" % i, "why": "netadmin login: Authentication failed."} for i in range(23)], "host": []}]),
    ("answers cut short", "y\n", ({"ok": False, "msg": "type RELEASE to confirm"}, 409), [{}]),
]


def run_pty(argv, answers, timeout=60):
    """argv on a pseudo-terminal (getpass reads the terminal); the answers typed one line at a time."""
    pid, fd = pty.fork()
    if pid == 0:
        os.execvp(argv[0], argv)
    out, lines, t0 = b"", answers.splitlines(True), time.time()
    while time.time() - t0 < timeout:
        r, _, _ = select.select([fd], [], [], 0.2)
        if r:
            try:
                ch = os.read(fd, 4096)
            except OSError:
                break
            if not ch:
                break
            out += ch
            if lines and (out.rstrip().endswith(b"]") or out.rstrip().endswith(b":")):
                time.sleep(0.1)
                os.write(fd, lines.pop(0).encode())
        else:
            if os.waitpid(pid, os.WNOHANG)[0]:
                break
    try:
        _, st = os.waitpid(pid, 0)
    except ChildProcessError:
        st = 0
    return os.waitstatus_to_exitcode(st), out.decode(errors="replace").replace("\r\n", "\n")


for what, ans, post, polls in CASES:
    for mode in ("pipe", "pty"):
        if mode == "pty" and what == "answers cut short":
            continue
        out = {}
        for tag in ENG:
            Local.script = {"post": post, "polls": [dict(x) for x in polls]}
            Local.got = []
            if mode == "pipe":
                rc, so = run(tag, ["release-vendor", "--conf", conf], stdin=ans)
            else:
                rc, so = run_pty(ENG[tag] + ["release-vendor", "--conf", conf], ans)
            posts = [g for g in Local.got if g["path"] != "/local/handover"]
            out[tag] = (rc, so, posts, len(Local.got) - len(posts) > 0)
        if what == "answers cut short":
            # Python stops with EOFError (a traceback on stderr); Go says so on stdout (DECISIONS P4-6): nothing is sent by either
            gm = "\n[!] input ended before every answer was given: nothing was sent\n"
            g = out["go"]
            rep.same("release-vendor (%s, %s): Go's message" % (what, mode), g[1].endswith(gm), True)
            out["go"] = (g[0], g[1][:-len(gm)] if g[1].endswith(gm) else g[1], g[2], g[3])
        rep.same("release-vendor (%s, %s)" % (what, mode), out["python"], out["go"])
        print("  release-vendor %-24s %-4s rc %s/%s  body %s" % (what, mode, out["python"][0], out["go"][0],
                                                                json.dumps(out["go"][2][0]["body"] if out["go"][2] else None)[:110]))
srv.shutdown()
print("\ncli: %d comparisons, %d mismatch(es)%s" % (rep.n, len(rep.bad), (": " + "; ".join(rep.bad)) if rep.bad else ""))
sys.exit(1 if rep.bad else 0)
