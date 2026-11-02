#!/usr/bin/env python3
"""Scale check of the Go engine's probes: 16,000 devices, most of them silent; and of its pushes (--push N).

usage: scale.py --go aidc-prov --work DIR [--answer 6000] [--silent 6000] [--early 4000] [--watch 150]
                [--push N [--push-conc C] [--push-s S] [--push-dead D]]

  answer  configured devices that answer on 22 (a TCP listener that accepts and closes): Verified, then Done
  silent  configured devices whose SYNs are dropped (iptables DROP on 127.31.0.0/16): probed every probe_every_s, 3 s timeout
  early   reachable-signal devices before Config, also dropped (127.32.0.0/16): 1 s timeout

  push    Junos devices due a configuration push (Phase 4): they answer on 22 (the push port), check in at "ztp" (stage
          3), and the console enters the push credential; the fake push_configs.py sleeps --push-s seconds and exits 0.
          --push-conc sets push_concurrency (0: left to the engine). --push-dead of them do not answer on 22 (DROP, 127.34/16)
          and stay waiting. Reported: time until every live one is pushed, pushes running at once (sampled), child processes,
          RSS, CPU.

The engine logs every probe cycle (AIDC_PROBE_LOG=1). Reported: cycle durations, the engine's RSS (peak) and CPU.
Needs root (iptables); the rules are removed at the end.
"""
import argparse, asyncio, json, os, re, shutil, subprocess, sys, threading, time, http.client, concurrent.futures

ap = argparse.ArgumentParser()
ap.add_argument("--go", required=True)
ap.add_argument("--work", required=True)
ap.add_argument("--answer", type=int, default=6000)
ap.add_argument("--silent", type=int, default=6000)
ap.add_argument("--early", type=int, default=4000)
ap.add_argument("--watch", type=int, default=150)
ap.add_argument("--push", type=int, default=0)
ap.add_argument("--push-conc", type=int, default=0)
ap.add_argument("--push-s", type=float, default=3.0)
ap.add_argument("--push-dead", type=int, default=0)
a = ap.parse_args()
W = a.work
shutil.rmtree(W, ignore_errors=True)
os.makedirs(W)


def ip(net, k):
    return "127.%d.%d.%d" % (net, k // 250, k % 250 + 1)


devs = []
for k in range(a.answer):
    devs.append({"name": "A%05d" % k, "short": "A%05d" % k, "role": "leaf", "os": "cumulus", "ip": ip(30, k), "verify_ports": [22], "config_signal": "checkin"})
for k in range(a.silent):
    devs.append({"name": "S%05d" % k, "short": "S%05d" % k, "role": "leaf", "os": "cumulus", "ip": ip(31, k), "verify_ports": [22], "config_signal": "checkin"})
for k in range(a.early):
    devs.append({"name": "E%05d" % k, "short": "E%05d" % k, "role": "gw", "os": "sros", "ip": ip(32, k), "verify_ports": [22], "config_signal": "reachable"})
for k in range(a.push):
    dead = k >= a.push - a.push_dead
    devs.append({"name": "P%05d" % k, "short": "P%05d" % k, "role": "leaf", "os": "junos", "ip": ip(34 if dead else 33, k), "verify_ports": [22],
                 "config_signal": "manual", "push": {"cmd": ["push_configs.py", "--only", "P%05d" % k], "port": 22}})
plan = {"site": {"code": "SCL", "prov_ip": "127.0.0.1"}, "dhcp": {"subnet_id": 1}, "devices": devs,
        "builds": [{"key": "be", "num": 1, "name": "Scale", "waves": [{"name": "all", "devices": [d["name"] for d in devs]}],
                    "gate": [{"key": "done", "type": "devices_done", "label": "done"}, {"key": "reach", "type": "reachable", "label": "reach"}]}]}
json.dump(plan, open(os.path.join(W, "estate.json"), "w"))
json.dump({"Dhcp4": {"subnet4": [{"id": 1, "subnet": "127.0.0.0/8", "reservations": []}]}}, open(os.path.join(W, "kea.json"), "w"))
os.makedirs(os.path.join(W, "fabric"))
open(os.path.join(W, "fabric", "secrets.env"), "w").write("")
conf = {"http_bind": "127.0.0.1", "http_port": 9180, "console_bind": "127.0.0.1", "console_port": 9443, "internal_port": 9181,
        "web_root": os.path.join(W, "web"), "state_dir": os.path.join(W, "state"), "bundle_dir": W, "kea_live": os.path.join(W, "kea-live.conf"),
        "secrets": os.path.join(W, "fabric", "secrets.env"), "tls_cert": os.path.join(W, "fabric", "c.crt"), "tls_key": os.path.join(W, "fabric", "c.key"),
        "hook_token_file": os.path.join(W, "fabric", "hook.token"), "local_token_file": os.path.join(W, "fabric", "local.token"),
        "sniff": False, "tftp": False, "probe_every_s": 15, "done_probe_s": 120}
if a.push_conc:
    conf["push_concurrency"] = a.push_conc
json.dump(conf, open(os.path.join(W, "estate.conf"), "w"))
open(os.path.join(W, "push_configs.py"), "w").write(
    "import sys, time, os\nprint('push_configs: %%s as %%s' %% (sys.argv[-1], os.environ.get('FABRIC_USER')), flush=True)\ntime.sleep(%r)\nprint('commit complete')\n" % a.push_s)
os.makedirs(conf["web_root"])

# answering devices: one listener per address, accept and close
LIS = r'''
import asyncio, sys
async def main(ips):
    async def h(r, w):
        w.close()
    for ip in ips:
        await asyncio.start_server(h, ip, 22, backlog=64, reuse_address=True)
    print("listening", len(ips), flush=True)
    await asyncio.Event().wait()
asyncio.run(main(sys.argv[1:] or [l.strip() for l in sys.stdin]))
'''
open(os.path.join(W, "lis.py"), "w").write(LIS)
lis = subprocess.Popen([sys.executable, "-I", os.path.join(W, "lis.py")], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
lis.stdin.write("\n".join(d["ip"] for d in devs if d["name"].startswith("A") or (d["name"].startswith("P") and d["ip"].startswith("127.33."))) + "\n")
lis.stdin.close()
print(lis.stdout.readline().strip())
RULES = [["-d", "127.%d.0.0/16" % n, "-p", "tcp", "--dport", "22", "-j", "DROP"] for n in (31, 32, 34)]
for r in RULES:
    subprocess.check_call(["iptables", "-I", "OUTPUT"] + r)
eng = None
try:
    base = [a.go]
    files = ["--plan", os.path.join(W, "estate.json"), "--conf", os.path.join(W, "estate.conf"), "--kea-template", os.path.join(W, "kea.json")]
    subprocess.run(base + ["init", "--admin-pass", "Scale-Admin-Pass-1"] + files, check=True, capture_output=True)
    env = dict(os.environ, AIDC_PROBE_LOG="1")
    log = open(os.path.join(W, "serve.log"), "w")
    t0 = time.time()
    eng = subprocess.Popen(base + ["serve"] + files, stdout=log, stderr=subprocess.STDOUT, env=env)
    for _ in range(300):
        try:
            http.client.HTTPConnection("127.0.0.1", 9181, timeout=1).request("GET", "/local/status")
            break
        except OSError:
            time.sleep(0.1)
    print("serve up in %.1fs" % (time.time() - t0))
    tok = open(conf["local_token_file"]).read().strip()
    c = http.client.HTTPConnection("127.0.0.1", 9181, timeout=30)
    c.request("POST", "/local/start", json.dumps({"plane": "all"}), {"X-Estate-Token": tok, "Content-Type": "application/json"})
    print("start:", c.getresponse().read().decode())
    time.sleep(3)
    t1 = time.time()

    def ci(chunk):
        h = http.client.HTTPConnection("127.0.0.1", 9180, timeout=30)
        for d in chunk:
            what = "ztp" if d["config_signal"] in ("reachable", "manual") else "config"
            h.request("GET", "/ci/%s/%s" % (d["name"], what))
            h.getresponse().read()
        h.close()
    chunks = [devs[i::8] for i in range(8)]
    with concurrent.futures.ThreadPoolExecutor(8) as ex:
        list(ex.map(ci, chunks))
    print("check-ins: %d in %.1fs" % (len(devs), time.time() - t1))

    def proc():
        st = open("/proc/%d/status" % eng.pid).read()
        rss = int(re.search(r"VmRSS:\s+(\d+)", st).group(1)) // 1024
        hwm = int(re.search(r"VmHWM:\s+(\d+)", st).group(1)) // 1024
        f = open("/proc/%d/stat" % eng.pid).read().split(")")[1].split()
        return rss, hwm, (int(f[11]) + int(f[12])) / os.sysconf("SC_CLK_TCK"), int(re.search(r"Threads:\s+(\d+)", st).group(1))
    def children():
        k = 0
        for p in os.listdir("/proc"):
            if p.isdigit():
                try:
                    if open("/proc/%s/stat" % p).read().split(")")[1].split()[1] == str(eng.pid):
                        k += 1
                except OSError:
                    pass
        return k
    pushlog = []
    if a.push:
        def con(method, path, body=None, ck=None):
            h = http.client.HTTPConnection("127.0.0.1", 9443, timeout=60)
            hs = {"X-Requested-With": "estate-console", "Content-Type": "application/json"}
            if ck:
                hs["Cookie"] = "estate_session=" + ck
            h.request(method, path, json.dumps(body) if body is not None else None, hs)
            r = h.getresponse()
            return r.status, r.getheader("Set-Cookie") or "", r.read()
        def login(pw):
            return re.search(r"estate_session=([A-Za-z0-9_-]+)", con("POST", "/api/login", {"user": "admin", "password": pw})[1]).group(1)
        ck = login("Scale-Admin-Pass-1")
        con("POST", "/api/password", {"current": "Scale-Admin-Pass-1", "new": "Scale-Admin-Pass-2x"}, ck)
        ck = login("Scale-Admin-Pass-2x")
        tp = time.time()
        print("push credential:", con("POST", "/api/push-credential", {"user": "pushuser", "password": "push-pass"}, ck)[2].decode())
    samples = []
    tw = time.time()
    cpu0 = proc()[2]
    pushed_at = None
    while time.time() - tw < a.watch:
        samples.append((time.time() - tw,) + proc())
        if a.push:
            stv = json.loads(con("GET", "/api/state", None, ck)[2])
            dv = stv["devices"]
            done = sum(1 for n, x in dv.items() if n.startswith("P") and x["stage"] >= 4)
            pushlog.append((time.time() - tp, len(stv.get("pushing") or []), children(), done))
            if done >= a.push - a.push_dead and pushed_at is None:
                pushed_at = time.time() - tp
                break
        time.sleep(1)
    cpu1 = proc()[2]
    elapsed = time.time() - tw
    c = http.client.HTTPConnection("127.0.0.1", 9181, timeout=30)
    c.request("GET", "/local/status")
    status = json.loads(c.getresponse().read().decode())
finally:
    if eng:
        eng.terminate()
        eng.wait()
    lis.terminate()
    for r in RULES:
        subprocess.call(["iptables", "-D", "OUTPUT"] + r)
cyc = [(float(m.group(3)), int(m.group(1)), int(m.group(2))) for m in re.finditer(r"probes: cycle of (\d+) probes \((\d+) up\) took ([\d.]+)s", open(os.path.join(W, "serve.log")).read())]
big = [x for x in cyc if x[1] > 1000]
import sqlite3
db = sqlite3.connect(os.path.join(W, "state", "estate.db"))
stages = {}
for (d,) in db.execute("SELECT data FROM devices"):
    s = json.loads(d)
    stages[s["stage"]] = stages.get(s["stage"], 0) + 1
print("devices: %d (answering %d, silent configured %d, silent before config %d); stages now %s" % (len(devs), a.answer, a.silent, a.early, dict(sorted(stages.items()))))
print("probe cycles: %d; with >1000 probes: %d" % (len(cyc), len(big)))
for x in cyc[:12]:
    print("  %6d probes  %6d up  %6.2fs" % (x[1], x[2], x[0]))
if big:
    ds = sorted(x[0] for x in big)
    print("cycles with >1000 probes: min %.2fs, median %.2fs, max %.2fs (probe_every_s %d)" % (ds[0], ds[len(ds) // 2], ds[-1], conf["probe_every_s"]))
print("engine RSS: now %d MB, peak %d MB; threads (OS) peak %d" % (samples[-1][1], max(s[2] for s in samples), max(s[4] for s in samples)))
print("engine CPU over %ds: %.1f s (%.1f%% of one core)" % (elapsed, cpu1 - cpu0, 100.0 * (cpu1 - cpu0) / elapsed))
print("status:", json.dumps(status)[:300])
if a.push:
    sl = open(os.path.join(W, "serve.log")).read()
    m = re.search(r"push concurrency: (\d+) at once \(([^)]*)\)", sl)
    print("push: %d devices (%d not answering on 22), push_concurrency %s, fake push %.1f s each" % (a.push, a.push_dead, m and (m.group(1) + ", " + m.group(2)), a.push_s))
    live = a.push - a.push_dead
    print("push: all %d live devices pushed (stage Config) %s" % (live, ("in %.1f s after the credential (%.1f pushes/s)" % (pushed_at, live / pushed_at)) if pushed_at else "NOT within the watch"))
    print("push: running at once (sampled) max %d; engine child processes max %d" % (max(x[1] for x in pushlog), max(x[2] for x in pushlog)))
    for x in pushlog[::max(1, len(pushlog) // 10)]:
        print("  t=%6.1fs  pushing %4d  children %4d  pushed %5d" % x)
