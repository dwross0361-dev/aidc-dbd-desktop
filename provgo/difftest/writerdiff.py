#!/usr/bin/env python3
"""The Go configuration writer against the Python service's (netmiko) on fake devices in configuration mode: tuning
writes (applied, rejected) and the DWR account removal (removed, still present, commit refused, wrong password), on
Junos, EOS, NX-OS, Cumulus, SR Linux, SR OS (MD-CLI), IOS XR and IOS XE. The fake devices keep a candidate and a running
configuration: what is read back after the removal is what was committed (or, on EOS/NX-OS/IOS XE, entered).

usage: writerdiff.py --py estate_build.py --writecmd WRITECMD --work DIR
"""
import argparse, json, os, shutil, subprocess, sys, time, importlib.util, re

ap = argparse.ArgumentParser()
ap.add_argument("--py", required=True)
ap.add_argument("--writecmd", required=True)
ap.add_argument("--work", required=True)
a = ap.parse_args()
spec = importlib.util.spec_from_file_location("eb", a.py)
eb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(eb)
W = a.work
shutil.rmtree(W, ignore_errors=True)
os.makedirs(W)
sim = os.path.join(W, "sim")
slug = lambda c: re.sub(r"[^A-Za-z0-9]+", "_", c).strip("_")[:90]
devs = []
for i, os_ in enumerate(("junos", "eos", "nxos", "cumulus", "srl", "sros", "iosxr", "iosxe")):
    for k in range(3):
        devs.append({"name": "W-%s-%d" % (os_.upper(), k), "os": os_, "ip": "127.61.%d.%d" % (i, k + 1), "ports": [22]})
        if os_ == "eos":
            devs[-1]["prompt"] = "{h}#"   # a privileged (TACACS priv-15) login lands in enable mode
for d in devs:
    os.makedirs(os.path.join(sim, d["name"]))
    if d["name"].endswith("-1"):   # the account is still there after the removal
        open(os.path.join(sim, d["name"], "_keep"), "w").close()
    open(os.path.join(sim, d["name"], slug(eb.BACKUP_CMD[d["os"]]) + ".txt"), "w").write(
        "hostname %s\nuser dwr-netadmin class super-user\n" % d["name"])
json.dump(devs, open(os.path.join(W, "devs.json"), "w"))
auth = lambda u, p: json.dump({"user": u, "passwords": {"*": p}}, open(os.path.join(sim, "_auth.json"), "w"))
auth("wr", "write-pass")
fake = subprocess.Popen([sys.executable, "-I", os.path.join(os.path.dirname(os.path.abspath(__file__)), "fakedev.py"), os.path.join(W, "devs.json"), sim],
                        stderr=open(os.path.join(W, "fakedev.log"), "w"))
time.sleep(2)
n = bad = 0
try:
    P = {"site": {"code": "T", "prov_ip": "127.0.0.1"}, "dhcp": {"subnet_id": 1},
         "devices": [{"name": d["name"], "os": d["os"], "ip": d["ip"]} for d in devs] + [{"name": "W-DOWN", "os": "eos", "ip": "127.61.9.9"}],
         "builds": [{"key": "b", "num": 1, "name": "B", "waves": [{"name": "w", "devices": [d["name"] for d in devs]}], "gate": []}]}
    json.dump(P, open(os.path.join(W, "plan.json"), "w"))
    json.dump({"Dhcp4": {"subnet4": [{"id": 1, "subnet": "127.0.0.0/8"}]}}, open(os.path.join(W, "kea.json"), "w"))
    open(os.path.join(W, "secrets.env"), "w").write("")
    json.dump({"state_dir": os.path.join(W, "st"), "secrets": os.path.join(W, "secrets.env"), "web_root": W, "break_glass_user": "netadmin"},
              open(os.path.join(W, "conf.json"), "w"))
    est = eb.Estate(os.path.join(W, "plan.json"), os.path.join(W, "conf.json"), os.path.join(W, "kea.json"))
    est.push_cred = ("wr", "write-pass")
    LINES = {"cumulus": ["nv set qos congestion-control default-global traffic-class 3 min-threshold 204800",
                         "nv set qos congestion-control default-global traffic-class 3 max-threshold 1843200"],
             "junos": ["set class-of-service schedulers S explicit-congestion-notification", "set class-of-service drop-profiles DP fill-level 20 drop-probability 0"],
             "eos": ["qos profile LOSSLESS", "random-detect ecn minimum-threshold 200 kbytes maximum-threshold 1800 kbytes"],
             "nxos": ["policy-map type network-qos RoCE", "class type network-qos c-8q-nq3", "pause pfc-cos 3"],
             "srl": ["set / qos output-queues queue 3 wred-profile ROCE", "set / qos explicit-congestion-notification ecn-enable true"],
             "sros": ["/configure qos slope-policy \"ROCE\" high-slope admin-state enable", "/configure qos slope-policy \"ROCE\" high-slope max-avg 80"],
             "iosxr": ["hw-module profile qos ecn-marking", "class-map match-any ROCE"],
             "iosxe": ["ip ssh version 2", "service timestamps log datetime msec"]}

    def py_tune(d, lines):
        try:
            ok, note = est._tune_write(d["name"], lines, "tuning v1 (test) by admin")
            return {"ok": ok, "note": note}
        except Exception as e:
            return {"note": "write failed: %s" % e, "ok": False}

    def go_tune(d, lines):
        r = json.loads(subprocess.run([a.writecmd, "tune", d["os"], d["ip"], "22", "wr", "write-pass", "tuning v1 (test) by admin"] + lines,
                                      capture_output=True, text=True, timeout=300).stdout)
        return {"note": "write failed: %s" % r["err"], "ok": False} if "err" in r else {"ok": r["ok"], "note": r["note"]}

    def py_remove(d, pw):
        _, st, why = est._remove_dwr(d["name"], pw)
        return {"st": st, "why": why}

    def go_remove(d, pw):
        cmds, how = eb.REMOVE_DWR[d["os"]]
        r = json.loads(subprocess.run([a.writecmd, "remove", d["os"], d["ip"], "22", "netadmin", pw, how] + cmds, capture_output=True, text=True, timeout=300).stdout)
        if "err" in r:
            return {"st": "failed", "why": ("netadmin login: " + r["err"][:160]) if r["login"] else r["err"][:160]}
        return {"st": "failed", "why": "dwr-netadmin is still in the configuration"} if "dwr-netadmin" in r["cfg"] else {"st": "removed", "why": ""}

    def same(what, p, g):
        global n, bad
        n += 1
        if p != g:
            bad += 1
            print("MISMATCH %s\n  py: %r\n  go: %r" % (what, p, g))

    for d in devs:
        k = int(d["name"][-1])
        lines = LINES[d["os"]] + (["BADLINE here"] if k == 2 else [])
        if d["os"] == "cumulus" and k == 1:
            open(os.path.join(sim, d["name"], "_badapply"), "w").close()
        same("tune %s" % d["name"], py_tune(d, lines), go_tune(d, lines))
    auth("netadmin", "nadmin-pass")
    for d in devs:
        if d["name"].endswith("-2"):   # the device refuses the commit (commit platforms)
            open(os.path.join(sim, d["name"], "_commitfail"), "w").close()
    expected = 0
    for d in devs:
        p, g = py_remove(d, "nadmin-pass"), go_remove(d, "nadmin-pass")
        same("remove %s" % d["name"], p, g)
    same("remove wrong password", py_remove(devs[1], "nope"), go_remove(devs[1], "nope"))
    print("writer: %d comparisons, %d mismatch(es)" % (n, bad))
    sys.exit(1 if bad else 0)
finally:
    fake.terminate()
    fake.wait()
