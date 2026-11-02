#!/usr/bin/env python3
"""The Go reader against the Python service's reader (netmiko) on fake devices of every OS: same commands, same
outputs, same error texts.

usage: readerdiff.py --py estate_build.py --readcmd READCMD --fixtures internal/estate/testdata/p2/fixtures --work DIR
"""
import argparse, json, os, shutil, subprocess, sys, tempfile, time, importlib.util, concurrent.futures

ap = argparse.ArgumentParser()
ap.add_argument("--py", required=True)
ap.add_argument("--readcmd", required=True)
ap.add_argument("--fixtures", required=True)
ap.add_argument("--work", required=True)
a = ap.parse_args()
spec = importlib.util.spec_from_file_location("eb", a.py)
eb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(eb)

W = a.work
shutil.rmtree(W, ignore_errors=True)
os.makedirs(W)
sim = os.path.join(W, "sim")
shutil.copytree(os.path.join(a.fixtures, "sim"), sim)
plan = json.load(open(os.path.join(a.fixtures, "plan.json")))
cmds = json.load(open(os.path.join(a.fixtures, "commands.json")))
devs = []
for i, d in enumerate(plan["devices"]):
    if d["os"] not in eb.DT or d["name"] not in cmds:
        continue
    devs.append({"name": d["name"], "os": d["os"], "ip": "127.60.0.%d" % (i + 1), "ports": [22]})
json.dump(devs, open(os.path.join(W, "devs.json"), "w"))
BIG = "".join("set interfaces et-0/0/%d:%d mtu 9216\n# to PEER-%04d:h%d/gpu1:3 (L2 access, rail VLAN; no prompt character here: FTD prompts with a bare greater-than sign)\n" % (i // 4, i % 4, i, i % 9) for i in range(1500))
for d in devs:   # a long answer: many reads, chunk boundaries inside \r\n
    open(os.path.join(sim, d["name"], "show_tech_support.txt"), "w").write(BIG)
json.dump({"user": "ro", "passwords": {"*": "readonly", "sros": "sros-pass"}}, open(os.path.join(sim, "_auth.json"), "w"))
fake = subprocess.Popen([sys.executable, "-I", os.path.join(os.path.dirname(os.path.abspath(__file__)), "fakedev.py"), os.path.join(W, "devs.json"), sim],
                        stderr=open(os.path.join(W, "fakedev.log"), "w"))
time.sleep(2)
try:
    # the Python reader: estate_build's own read() on a plan of these devices
    P = {"site": {"code": "T", "prov_ip": "127.0.0.1"}, "dhcp": {"subnet_id": 1},
         "devices": [{"name": d["name"], "os": d["os"], "ip": d["ip"]} for d in devs] + [{"name": "T-DOWN", "os": "eos", "ip": "127.60.1.1"}],
         "builds": [{"key": "b", "num": 1, "name": "B", "waves": [{"name": "w", "devices": [d["name"] for d in devs]}], "gate": []}]}
    json.dump(P, open(os.path.join(W, "plan.json"), "w"))
    json.dump({"Dhcp4": {"subnet4": [{"id": 1, "subnet": "127.0.0.0/8"}]}}, open(os.path.join(W, "kea.json"), "w"))
    open(os.path.join(W, "secrets.env"), "w").write("READ_USER=ro\nREAD_PASSWORD=readonly\nREAD_PASSWORD_SROS=sros-pass\n")
    json.dump({"state_dir": os.path.join(W, "st"), "secrets": os.path.join(W, "secrets.env"), "web_root": W}, open(os.path.join(W, "conf.json"), "w"))
    est = eb.Estate(os.path.join(W, "plan.json"), os.path.join(W, "conf.json"), os.path.join(W, "kea.json"))

    def py_read(name, cl):
        try:
            return {"out": est.read(name, cl)}
        except Exception as e:
            return {"err": str(e)}

    def go_read(os_, ip, cl, pw=None):
        r = subprocess.run([a.readcmd, os_, ip, "22", "ro", pw or ("sros-pass" if os_ == "sros" else "readonly")] + cl, capture_output=True, text=True, timeout=300)
        return json.loads(r.stdout)

    jobs = []
    for d in devs:
        cl = [c for c in cmds[d["name"]] if not c.startswith("@")] + ["show tech-support", "show nothing-such"]
        jobs.append((d, cl))
    n = bad = 0
    with concurrent.futures.ThreadPoolExecutor(8) as ex:
        res = list(ex.map(lambda j: (j, py_read(j[0]["name"], j[1]), go_read(j[0]["os"], j[0]["ip"], j[1])), jobs))
    per_os = {}
    for (d, cl), p, g in res:
        n += 1
        same = p == g
        per_os.setdefault(d["os"], [0, 0])
        per_os[d["os"]][0] += len(cl)
        if not same:
            bad += 1
            per_os[d["os"]][1] += 1
            print("MISMATCH %s (%s)" % (d["name"], d["os"]))
            for i, c in enumerate(cl):
                po = (p.get("out") or [None] * len(cl))[i] if "out" in p else p.get("err")
                go = (g.get("out") or [None] * len(cl))[i] if "out" in g else g.get("err")
                if po != go:
                    po, go = po or "", go or ""
                    k = next((i for i in range(min(len(po), len(go))) if po[i] != go[i]), min(len(po), len(go)))
                    print("  cmd %r (lengths py %d, go %d, first difference at %d)\n    py: %r\n    go: %r" % (c, len(po), len(go), k, po[max(0, k - 120):k + 120], go[max(0, k - 120):k + 120]))
                    break
    # error paths: wrong password, nothing listening
    e1 = py_read(devs[0]["name"], ["show version"])
    est.conf["secrets"] = os.path.join(W, "bad.env")
    open(os.path.join(W, "bad.env"), "w").write("READ_USER=ro\nREAD_PASSWORD=wrong\n")
    pe = py_read(devs[0]["name"], ["show version"])
    ge = go_read(devs[0]["os"], devs[0]["ip"], ["show version"], "wrong")
    n += 1
    if pe != ge:
        bad += 1
        print("MISMATCH auth failure\n  py: %r\n  go: %r" % (pe, ge))
    pd = py_read("T-DOWN", ["show version"])
    gd = go_read("eos", "127.60.1.1", ["show version"], "wrong")
    n += 1
    if pd != gd:
        bad += 1
        print("MISMATCH refused\n  py: %r\n  go: %r" % (pd, gd))
    st = json.load(open(os.path.join(sim, "_stats.json")))
    print("reader: %d device sessions compared (%s), %d mismatch(es); fake devices served %d SSH sessions, %d commands" % (
        n, ", ".join("%s %d cmds" % (k, v[0]) for k, v in sorted(per_os.items())), bad, st["sessions"], st["commands"]))
    sys.exit(1 if bad else 0)
finally:
    fake.terminate()
    fake.wait()
