#!/usr/bin/env python3
"""Phase 2 differential test: the Python service and aidc-prov read the same fake devices over real SSH and are driven
through check-in, every wave, probes and every gate check of every build, with failures injected and then fixed.

Called from difftest.py (--p2 INSTALLER.run [--p2-os NAME=OS,...]). Fake devices: fakedev.py on 127.0.0.0/8, one per
plan device (SSH on 22, TCP on its other verify ports) and one per aggregate member (DPUs). Each device's answers are
files written here from its planned configuration and release (and rewritten to inject and fix faults).
"""
import collections, copy, json, os, re, shutil, subprocess, sys, threading, time, concurrent.futures

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import difftest as D

HERE = os.path.dirname(os.path.abspath(__file__))
DT = ("junos", "eos", "nxos", "cumulus", "srl", "sros", "iosxr", "iosxe", "ftd")
USER, PW = "ro", "readonly"


def slug(c):
    return re.sub(r"[^A-Za-z0-9]+", "_", c).strip("_")[:90]


# ------------------------------------------------------------------------------------------------ device answers

class Answers:
    """Per-device command outputs derived from the plan (what a correctly built device would print)."""

    CMDS = {
        "bgp": {"junos": "show bgp summary | display json", "eos": "show ip bgp summary vrf all | json", "nxos": "show ip bgp summary vrf all | json",
                "srl": "info from state network-instance * protocols bgp neighbor * session-state | as json", "sros": "show router bgp summary all",
                "iosxr": "show bgp vrf all summary", "ftd": "show bgp summary"},
        "evpn": {"eos": "show bgp evpn summary | json", "nxos": "show bgp l2vpn evpn summary | json", "sros": "show router bgp summary family evpn",
                 "iosxr": "show bgp l2vpn evpn summary"},
        "version": {"cumulus": "cat /etc/lsb-release", "junos": "show version", "eos": "show version", "nxos": "show version",
                    "srl": "info from state system information version", "sros": "show version", "iosxr": "show version", "iosxe": "show version"},
        "lldp": {"cumulus": "nv show interface lldp -o json", "junos": "show lldp neighbors | display json", "eos": "show lldp neighbors | json",
                 "nxos": "show lldp neighbors detail | json", "srl": "info from state system lldp interface * neighbor * | as json"},
        "backup": {"junos": "show configuration | display set", "eos": "show running-config", "nxos": "show running-config",
                   "cumulus": "nv config show -o commands", "srl": "info flat /", "sros": "admin show configuration /configure flat",
                   "iosxr": "show running-config", "iosxe": "show running-config"},
        "roce": {"cumulus": "nv show qos roce -o json", "junos": "show configuration class-of-service | display set | match \"pfc|explicit-congestion|ecn\"",
                 "eos": "show priority-flow-control", "nxos": "show interface priority-flow-control", "srl": "info flat qos"},
    }

    def __init__(self, plan, web, sim):
        self.plan, self.web, self.sim = plan, web, sim
        self.devs = {d["name"]: d for d in plan["devices"]}
        self.short = {d.get("short", d["name"]): d["name"] for d in plan["devices"]}

    def planned(self, n):
        d = self.devs[n]
        p = d.get("planned_config")
        if not p:
            return None
        try:
            t = open(os.path.join(self.web, p)).read()
        except OSError:
            return None
        if d.get("os") == "cumulus":
            t = "\n".join(l for l in t.splitlines() if l.startswith("nv "))
        return t

    def links(self, n):
        return {m.group(1): (m.group(2), m.group(3)) for m in re.finditer(r"interface (\S+) link mtu \d+\s+#\s*->\s*(\S+)\s+(\S+)", self.planned(n) or "")}

    def peers(self, n):
        return {m.group(1): m.group(2) for m in re.finditer(r"neighbor (\S+) remote-as \S+\s+#\s*([A-Za-z0-9_.-]+)", self.planned(n) or "")}

    def write(self, n, cmd, text):
        os.makedirs(os.path.join(self.sim, n), exist_ok=True)
        open(os.path.join(self.sim, n, slug(cmd) + ".txt"), "w").write(text)

    def read(self, n, cmd):
        return open(os.path.join(self.sim, n, slug(cmd) + ".txt")).read()

    # one device -----------------------------------------------------------------------------
    def bgp_text(self, n, os_, sess, evpn=False):
        if os_ == "junos":
            return json.dumps({"bgp-information": [{"bgp-peer": [{"peer-address": [{"data": a}], "peer-as": [{"data": "4200002000"}],
                                                                  "peer-state": [{"data": st}], "description": [{"data": "to " + a}]} for a, st in sess]}]}, indent=2)
        if os_ == "eos":
            return json.dumps({"vrfs": {"default": {"routerId": "10.0.0.1", "asn": "4200001001", "peers": {
                a: {"peerState": st, "prefixReceived": 4, "asn": "4200002000", "underMaintenance": False} for a, st in sess}}}}, indent=3)
        if os_ == "nxos":
            rows = [{"neighborid": a, "state": st, "neighboras": "4200002000", "time": "1w2d"} for a, st in sess]
            return json.dumps({"TABLE_vrf": {"ROW_vrf": [{"vrf-name-out": "default", "TABLE_af": {"ROW_af": {"TABLE_saf": {"ROW_saf": {
                "af-name": "L2VPN EVPN" if evpn else "IPv4 Unicast", "TABLE_neighbor": {"ROW_neighbor": rows if len(rows) != 1 else rows[0]}}}}}}]}}, indent=2)
        if os_ == "srl":
            return json.dumps({"network-instance": [{"name": "default", "protocols": {"srl_nokia-bgp:bgp": {"neighbor": [
                {"peer-address": a, "session-state": st.lower()} for a, st in sess]}}}]}, indent=2)
        if os_ == "sros":
            body = "".join("%s\n                64496  1834512    0 05d02h11m %s\n                         4521    0\n" % (
                a, "950123/950012/12 (IPv4)" if st == "Established" else st) for a, st in sess)
            return "BGP Summary\n=====\nNeighbor\n-------\n" + body + "-------\n"
        if os_ in ("iosxr", "ftd"):
            hdr = "Neighbor        Spk    AS MsgRcvd MsgSent   TblVer  InQ OutQ  Up/Down  St/PfxRcd\n"
            return "Fri Oct 31 22:01:02.345 UTC\n" + hdr + "".join("%-15s 0 4200002000   43812   43790     1203    0    0 %s\n" % (
                a, "    3w0d         24" if st == "Established" else "00:00:12 " + st) for a, st in sess)
        return ""

    def version_text(self, os_, rel):
        return {"cumulus": "DISTRIB_ID=\"Cumulus Linux\"\nDISTRIB_RELEASE=%s\nDISTRIB_DESCRIPTION=\"Cumulus Linux %s\"\n" % (rel, rel),
                "junos": "Hostname: x\nModel: qfx5240-64od\nJunos: %s\nJUNOS OS Kernel 64-bit  [20240910.4f3a1c2_builder_stable_13]\n" % rel,
                "eos": "Arista DCS-7280DR3A-36-F\nSoftware image version: %s\nArchitecture: x86_64\n" % rel,
                "nxos": "Cisco Nexus Operating System (NX-OS) Software\n  NXOS: version %s\n" % rel,
                "srl": "    system {\n        information {\n            version v%s-492-g7a1b2c3\n        }\n    }" % rel,
                "sros": "TiMOS-C-%s cpm/x86_64 Nokia 7750 SR Copyright (c) 2000-2026 Nokia.\n" % rel,
                "iosxr": "Cisco IOS XR Software, Version %s LNT\n" % rel, "iosxe": "Cisco IOS XE Software, Version %s\n" % rel}.get(os_, "")

    def lldp_text(self, os_, links):
        if os_ == "cumulus":
            return json.dumps({lp: {"lldp": {"neighbor": {peer: {"chassis": {"system-name": peer}, "port": {"name": pp, "type": "ifname"}}}}}
                               for lp, (peer, pp) in links.items()}, indent=2)
        if os_ == "junos":
            return json.dumps({"lldp-neighbors-information": [{"lldp-neighbor-information": [{"lldp-local-port-id": [{"data": lp}],
                "lldp-remote-port-id": [{"data": pp}], "lldp-remote-system-name": [{"data": peer}]} for lp, (peer, pp) in links.items()]}]}, indent=2)
        if os_ == "eos":
            return json.dumps({"lldpNeighbors": [{"port": lp, "neighborDevice": peer, "neighborPort": pp, "ttl": 120} for lp, (peer, pp) in links.items()]})
        if os_ == "nxos":
            return json.dumps({"TABLE_nbor_detail": {"ROW_nbor_detail": [{"l_port_id": lp, "sys_name": peer, "port_id": pp} for lp, (peer, pp) in links.items()]}})
        if os_ == "srl":
            return json.dumps({"system": {"lldp": {"interface": [{"name": lp, "neighbor": [{"system-name": peer, "port-id": pp}]} for lp, (peer, pp) in links.items()]}}})
        return ""

    def roce_text(self, os_):
        return {"cumulus": json.dumps({"enable": "on", "mode": "lossless", "pfc": {"pfc-priority": "3"}}),
                "junos": "set class-of-service congestion-notification-profile CNP input ieee-802.1 code-point 011 pfc\nset class-of-service schedulers S explicit-congestion-notification",
                "eos": "Global PFC : Enabled\nEt1/1       E A W   3", "nxos": "Ethernet1/1        Auto On  (8)        1022       331",
                "srl": "set / qos interfaces interface ethernet-1/1 output pfc pfc-enable true"}.get(os_, "")

    def device(self, n):
        d = self.devs[n]
        os_ = d.get("os")
        if os_ not in DT:
            return
        rel = ((d.get("release") or {}).get("target") or "1.0") if isinstance(d.get("release"), dict) else "1.0"
        pl = self.planned(n)
        if os_ in self.CMDS["version"]:
            self.write(n, self.CMDS["version"][os_], self.version_text(os_, rel))
        if os_ in self.CMDS["lldp"]:
            self.write(n, self.CMDS["lldp"][os_], self.lldp_text(os_, self.links(n)))
        peers = self.peers(n)
        if os_ == "cumulus":
            vrfs = collections.OrderedDict()
            for m in re.finditer(r"nv set vrf (\S+) router bgp neighbor (\S+) remote-as", pl or ""):
                vrfs.setdefault(m.group(1), collections.OrderedDict())[m.group(2)] = {"state": "established", "remote-as": "external"}
            vrfs.setdefault("default", collections.OrderedDict())
            self.write(n, "nv show vrf -o json", json.dumps({v: {} for v in list(vrfs) + ["mgmt"]}))
            for v, nb in vrfs.items():
                self.write(n, "nv show vrf %s router bgp neighbor -o json" % v, json.dumps(nb, indent=2) if nb else "{}")
            self.write(n, "nv show vrf mgmt router bgp neighbor -o json", "Error: The requested item does not exist.")
        else:
            addrs = list(peers) or sorted(set(re.findall(r"neighbor (\d+\.\d+\.\d+\.\d+)", pl or "")))[:6] or ["10.%d.0.1" % (len(n) % 200), "10.%d.0.3" % (len(n) % 200)]
            sess = [(a, "Established") for a in addrs]
            if os_ in self.CMDS["bgp"]:
                self.write(n, self.CMDS["bgp"][os_], self.bgp_text(n, os_, sess))
            if os_ in self.CMDS["evpn"]:
                self.write(n, self.CMDS["evpn"][os_], self.bgp_text(n, os_, sess, evpn=True))
        if os_ in self.CMDS["roce"]:
            self.write(n, self.CMDS["roce"][os_], self.roce_text(os_))
        if os_ in self.CMDS["backup"]:
            self.write(n, self.CMDS["backup"][os_], (pl if pl is not None else "hostname %s\n" % n) +
                       ("\nnv set system aaa role nvue-admin class nvapply\nnv set interface swp1 type swp\n" if os_ == "cumulus" else ""))
        role = d.get("role", "")
        if os_ == "junos" and "firewall" in role.lower():
            first = re.search(r"node 1|primary|FW01", role + n) is not None
            self.write(n, "show chassis high-availability information", "Node Status: ONLINE\nServices Redundancy Group: 0\n  Sync Status: IN SYNC\n"
                       "Services Redundancy Group: 1\n  Current State: %s\n" % ("ACTIVE" if first else "BACKUP"))
            self.write(n, "show chassis cluster status", "Redundancy group: 0 , Failover count: 0\nnode0   200   primary\nnode1   100   secondary\n")
        if os_ == "ftd":
            first = re.search(r"primary|FW01", role + n) is not None
            self.write(n, "show failover", "Failover On\n        This host: %s\n        Other host: %s\n" % (
                ("Primary - Active", "Secondary - Standby Ready") if first else ("Secondary - Standby Ready", "Primary - Active")))


# ------------------------------------------------------------------------------------------------ the scenario

def norm2(v):
    """Phase 1 normalization plus the Phase 2 time-dependent texts."""
    v = D.norm(v)
    if isinstance(v, str):
        v = re.sub(r"127\.0\.0\.1:[12](8\d\d\d)", r"127.0.0.1:\1", v)
        v = re.sub(r"\d{8}T\d{6}Z\.cfg", "<BK>.cfg", v)
        v = re.sub(r"since \d\d:\d\d UTC", "since <HM> UTC", v)
        v = re.sub(r"Backed up \d\d:\d\dZ", "Backed up <HM>Z", v)
        return v
    if isinstance(v, dict):
        return {re.sub(r"\d{8}T\d{6}Z\.cfg", "<BK>.cfg", k): norm2(x) for k, x in v.items()}
    if isinstance(v, list):
        return [norm2(x) for x in v]
    return v


def rv(r):
    st, h, b = r
    return {"status": st, "body": norm2(D.jload(b)) if b else ""}


def run_p2(args, run, rep, os_over=None, tag="p2"):
    name = os.path.basename(os.path.dirname(run)) + "-" + tag
    W = os.path.join(args.work, name[-12:])   # short: the fake Kea socket path must stay under the AF_UNIX limit
    shutil.rmtree(W, ignore_errors=True)
    os.makedirs(W)
    print("== %s (phase 2)" % name)
    D.extract(run, W)
    args = D.shipped(args, W)
    web = os.path.join(W, "web")
    plan0 = json.load(open(os.path.join(W, "estate.json")))
    tmpl0 = json.load(open(os.path.join(W, "kea-dhcp4.json")))
    plan = D.test_plan(plan0)
    # runners stay in the plan (Phase 3 runs them; with no runner configuration here they report "not configured")
    k = 40000
    for d in plan["devices"]:
        if d.get("probe_ips"):
            d["probe_ips"] = [D.loop_ip(k + i) for i in range(len(d["probe_ips"]))]
            k += len(d["probe_ips"])
    sv = plan["site"].get("services") or {}
    for i, key in enumerate(sorted(sv)):
        sv[key] = ["127.9.%d.%d" % (i, j + 1) for j in range(len(sv[key]))]   # answer quickly (refused)
    for spec in (os_over or []):
        n, o = spec.split("=")
        for d in plan["devices"]:
            if d["name"].endswith(n):
                d["os"] = o
                d["os_label"] = o
    for b in plan["builds"]:
        for c in b["gate"]:
            if c["type"] == "auto":
                c["grace_s"] = 600 if c["key"] == "ul" else -1   # failing: blocked at once (ul: running, never late)
    # a TFTP file for the first device of the first wave
    first = plan["builds"][0]["waves"][0]["devices"][0]
    tftp_root = os.path.join(W, "tftp")
    os.makedirs(os.path.join(tftp_root, "poap"), exist_ok=True)
    open(os.path.join(tftp_root, "poap", "boot.py"), "w").write("#!/usr/bin/env python\n# POAP script for %s\n" % first + "x" * 1500 + "\n")
    os.makedirs(os.path.join(tftp_root, "images"), exist_ok=True)
    open(os.path.join(tftp_root, "images", "nxos.bin"), "wb").write(b"\0" * 3000)
    for d in plan["devices"]:
        if d["name"] == first:
            d["tftp_files"] = ["poap/boot.py"]
    byn = {d["name"]: d for d in plan["devices"]}

    # fake devices
    sim = os.path.join(W, "sim")
    os.makedirs(sim)
    A = Answers(plan, web, sim)
    fdevs = []
    for d in plan["devices"]:
        A.device(d["name"])
        if d.get("ip"):
            ports = list(d.get("verify_ports") or [22])
            if d.get("os") in DT and 22 not in ports:
                ports = [22] + ports
            fdevs.append({"name": d["name"], "os": d.get("os"), "ip": d["ip"], "ports": ports})
        for i, ip in enumerate(d.get("probe_ips") or []):
            fdevs.append({"name": "%s#%d" % (d["name"], i), "os": "member", "ip": ip, "ports": [d.get("probe_port", 22)]})
    json.dump(fdevs, open(os.path.join(W, "fakedevs.json"), "w"))
    json.dump({"user": USER, "passwords": {"*": PW}}, open(os.path.join(sim, "_auth.json"), "w"))

    def down(n, flag=True):
        p = os.path.join(sim, n, "_down")
        os.makedirs(os.path.dirname(p), exist_ok=True)
        if flag:
            open(p, "w").close()
        elif os.path.exists(p):
            os.remove(p)

    # ---- faults, chosen from the plan
    def gate_devs(bk, ck):
        for b in plan["builds"]:
            if b["key"] == bk:
                for c in b["gate"]:
                    if c["key"] == ck:
                        return [n for n in c.get("devices", []) if byn[n].get("os") in DT]
        return []

    faults = []   # (build, description, apply(), fix())

    def fault(bk, what, apply, fix):
        apply()
        faults.append((bk, what, fix))

    def wrong_release(bk):
        c = [n for n in gate_devs(bk, "software") if byn[n]["os"] in A.CMDS["version"]]
        if not c:
            return
        n = c[min(3, len(c) - 1)]
        os_ = byn[n]["os"]
        cmd = A.CMDS["version"][os_]
        good = A.read(n, cmd)
        fault(bk, "wrong release on " + n, lambda: A.write(n, cmd, A.version_text(os_, "1.2.3")), lambda: A.write(n, cmd, good))

    def wrong_lldp(bk):
        for n in gate_devs(bk, "cabling"):
            L = A.links(n)
            if byn[n]["os"] == "cumulus" and len(L) >= 2:
                cmd = A.CMDS["lldp"]["cumulus"]
                good = A.read(n, cmd)
                bad = dict(L)
                ks = sorted(bad)
                bad[ks[0]] = (bad[ks[1]][0], "swp99")
                del bad[ks[1]]
                fault(bk, "wrong LLDP neighbor on " + n, lambda: A.write(n, cmd, A.lldp_text("cumulus", bad)), lambda: A.write(n, cmd, good))
                return

    def bgp_down(bk, ck):
        for n in gate_devs(bk, ck):
            os_ = byn[n]["os"]
            if os_ == "cumulus":
                cmd = "nv show vrf default router bgp neighbor -o json"
                try:
                    good = A.read(n, cmd)
                    nb = json.loads(good, object_pairs_hook=collections.OrderedDict)
                except (OSError, ValueError):
                    continue
                if len(nb) < 2:
                    continue
                k0 = list(nb)[1]
                badj = copy.deepcopy(nb)
                badj[k0]["state"] = "idle"
                fault(bk, "BGP %s down on %s" % (k0, n), lambda: A.write(n, cmd, json.dumps(badj, indent=2)), lambda: A.write(n, cmd, good))
                return
            if os_ in A.CMDS["bgp"]:
                cmd = A.CMDS["bgp"][os_]
                good = A.read(n, cmd)
                fault(bk, "BGP down on %s (%s)" % (n, os_), lambda: A.write(n, cmd, A.bgp_text(n, os_, [("10.66.0.1", "Established"), ("10.66.0.3", "Active")])),
                      lambda: A.write(n, cmd, good))
                return

    def lossless_missing(bk):
        for n in gate_devs(bk, "lossless"):
            os_ = byn[n]["os"]
            cmd = A.CMDS["backup"].get(os_)
            pl = A.planned(n) or ""
            lines = [l for l in pl.splitlines() if re.search(r"pfc|ecn|explicit-congestion|watchdog|qos roce|lossless", l, re.I)]
            if cmd and lines:
                good = A.read(n, cmd)
                fault(bk, "lossless line missing on %s: %s" % (n, lines[-1].strip()[:60]), lambda: A.write(n, cmd, good.replace(lines[-1] + "\n", "")),
                      lambda: A.write(n, cmd, good))
                return

    def roce_off(bk):
        for n in gate_devs(bk, "roce"):
            os_ = byn[n]["os"]
            if os_ in A.CMDS["roce"]:
                cmd = A.CMDS["roce"][os_]
                good = A.read(n, cmd)
                fault(bk, "RoCE off on " + n, lambda: A.write(n, cmd, "{\"mode\": \"lossy\"}" if os_ == "cumulus" else "PFC Disabled"), lambda: A.write(n, cmd, good))
                return

    def mnha_bad(bk):
        c = gate_devs(bk, "fwpair")
        if len(c) >= 2:
            n = c[1]
            cmd = "show failover" if byn[n]["os"] == "ftd" else "show chassis high-availability information"
            good = A.read(n, cmd)
            fault(bk, "HA pair not formed (%s)" % n, lambda: A.write(n, cmd, "This host: Secondary - Failed\nServices Redundancy Group: 1\n  Current State: INELIGIBLE\n"),
                  lambda: A.write(n, cmd, good))

    keys = [b["key"] for b in plan["builds"] if b["waves"]]
    for bk in keys:
        wrong_release(bk)
        wrong_lldp(bk)
    if "edge" in keys:
        bgp_down("edge", "carrier")
        mnha_bad("edge")
    if "be" in keys:
        bgp_down("be", "ul")
        lossless_missing("be")
        roce_off("be")
    # every SR OS login refused until the secret is fixed (an authentication failure in the gate notes)
    sros_auth = any(byn[n].get("os") == "sros" for b in plan["builds"] for w in b["waves"] for n in w["devices"])
    # a device that never answers (set aside by the operator), aggregate members partly up, a Done device that goes away
    silent = None
    for bk in ("fe", "be"):
        if bk in keys:
            cand = [n for b in plan["builds"] if b["key"] == bk for w in b["waves"][1:2] for n in w["devices"] if byn[n].get("ip")]
            if cand:
                silent = cand[-1]
                break
    if silent:
        down(silent)
    agg = next((d for d in plan["devices"] if d.get("probe_ips")), None)
    if agg:
        for i in range(len(agg["probe_ips"]) * 2 // 3, len(agg["probe_ips"])):
            down("%s#%d" % (agg["name"], i))
    for f in faults:
        print("  fault: %s (%s)" % (f[1], f[0]))
    print("  silent device: %s; aggregate %s partly up; SR OS logins refused at first: %s" % (silent, agg and agg["name"], sros_auth))

    xc = {"reader_sim_dir": None, "probe_every_s": 2, "done_probe_s": 10, "check_every_s": 1e9, "backup_every_h": 1000, "reader_workers": 16,
          "tftp": True, "tftp_bind": "127.0.0.1", "tftp_root": tftp_root}
    PY = D.Engine("python", [sys.executable, "-I", args.py], os.path.join(W, "py"), (18180, 18543, 18181), plan, tmpl0, web, dict(xc, tftp_port=16969))
    GO = D.Engine("go", [args.go], os.path.join(W, "go"), (28180, 28543, 28181), plan, tmpl0, web, dict(xc, tftp_port=26969))
    eng = (PY, GO)

    def secrets(sros_ok):
        for E in eng:
            open(os.path.join(E.base, "fabric", "secrets.env"), "w").write(
                "READ_USER=%s\nREAD_PASSWORD=%s\n%sTARGET_BUSBW=380\n" % (USER, PW, "" if sros_ok else "READ_PASSWORD_SROS=not-the-password\n"))
    secrets(not sros_auth)

    def both(f):
        with concurrent.futures.ThreadPoolExecutor(2) as ex:
            return list(ex.map(f, eng))

    def cmp(what, f, view=lambda r: r):
        a, b = both(f)
        rep.same(what, view(a), view(b))
        return a, b

    for E in eng:
        E.cmd("init", "--admin-pass", D.ADMIN_PW)
    ck = {}
    fake = None
    try:
        fake = subprocess.Popen([sys.executable, "-I", os.path.join(HERE, "fakedev.py"), os.path.join(W, "fakedevs.json"), sim],
                                stderr=open(os.path.join(W, "fakedev.log"), "w"))
        time.sleep(3)
        for E in eng:
            E.start(args.console if E is PY else None)
        for E in eng:
            _, c, _ = E.login("admin", D.ADMIN_PW)
            E.con("POST", "/api/password", {"current": D.ADMIN_PW, "new": D.NEW_PW}, cookie=c)
            _, ck[E.name], _ = E.login("admin", D.NEW_PW)

        def st(E):
            return json.loads(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)[2])

        def post(E, path, data):
            return E.con("POST", path, data, cookie=ck[E.name], timeout=150)

        def bstate(E, bk):
            return next(b for b in st(E)["builds"] if b["key"] == bk)

        def wait(pred, what, timeout=600, step=1.0):
            t0 = time.time()
            while time.time() - t0 < timeout:
                if all(pred(E) for E in eng):
                    return time.time() - t0
                time.sleep(step)
            print("  TIMEOUT waiting for %s: %s" % (what, [(E.name, [(b["key"], b["state"], b["held"]) for b in st(E)["builds"]]) for E in eng]))
            rep.same("timeout: " + what, True, False)
            return None

        # ---- before the run: readiness, confirmations, the run
        cmp("p2 readiness", lambda E: rv(E.con("GET", "/api/readiness", cookie=ck[E.name], csrf=False)))
        for E in eng:
            post(E, "/api/readiness/confirm", {"key": "_accept", "note": "lab"})
        confirms = [(b["key"], c["key"]) for b in plan["builds"] for c in b["gate"] if c["type"] == "confirm"]
        cmp("p2 gate confirmations", lambda E: [rv(post(E, "/api/gate/confirm", {"build": bk, "check": c, "note": "by hand"})) for bk, c in confirms])
        cmp("p2 run all", lambda E: rv(post(E, "/api/run", {"plane": "all"})))   # P4-10: also on a plan with an empty build
        time.sleep(3)

        # ---- every device calls in, fetches its files and reports configured
        devs = [d for d in plan["devices"]]

        def callins(E):
            out = []
            for i, d in enumerate(devs):
                if d.get("ip"):
                    out.append(D.resp_view(D.hookpost(E, d, i)))
                for f in d.get("files", [])[:1]:
                    if d.get("ip"):
                        out.append(D.resp_view(E.boot("/" + f, src=d["ip"])))
                if d.get("aggregate"):
                    if not d.get("probe_ips"):
                        out.append(D.resp_view(E.boot("/ci/%s/count?n=%d" % (d["name"], d["aggregate"]), src="127.0.0.1")))
                elif d.get("config_signal") == "manual" or not d.get("ip"):
                    out.append(rv(post(E, "/api/mark-configured", {"device": d["name"], "note": "lab"})))
                elif d.get("config_signal") != "reachable":
                    out.append(D.resp_view(E.boot("/ci/%s/config" % d["name"], src="127.0.0.1")))
            return out
        cmp("p2 call-ins and check-ins", callins)
        idx = {d["name"]: i for i, d in enumerate(devs)}
        last_retry = {}

        def retry_waiting(E):
            """Phase 4: a device that called in before its wave opened asks again, as a real one does (DHCP retries): once its
            wave is open in that engine, its lease event and its first file come again (NX-OS and IOS XE devices signal
            "reachable", so nothing else moves them). Both engines get the same retry when their own wave opens."""
            if time.time() - last_retry.get(E.name, 0) < 4:
                return True
            last_retry[E.name] = time.time()
            s_ = st(E)
            opn = {n for b in s_["builds"] for w in b["waves"] if w.get("open") for n in w["devices"]}
            for n, x in s_["devices"].items():
                if x.get("flag") == "waiting" and n in opn and n in idx and byn[n].get("ip") and n != silent:
                    d = byn[n]
                    D.hookpost(E, d, idx[n])
                    for f in d.get("files", [])[:1]:
                        E.boot("/" + f, src=d["ip"])
            return True
        # TFTP: the owner, another device, an unknown address, a missing file, a bad path. The events carry the owner's
        # stage: wait until its probes have settled it in both (Go's probe cycle is faster, DECISIONS P2-1)
        wait(lambda E: st(E)["devices"][first]["stage"] == 6 or st(E)["devices"][first]["flag"], "tftp owner settled", timeout=120)
        cmp("p2 tftp", lambda E: tftp_cases(E, plan, byn, first))

        # ---- build by build
        def gate_view(E, bk):
            b = bstate(E, bk)
            ev = {}
            for c in b["gate"]:
                if c["type"] == "auto":
                    ev[c["key"]] = rv(E.con("GET", "/api/gate-evidence?build=%s&check=%s" % (bk, c["key"]), cookie=ck[E.name], csrf=False))
            return {"state": b["state"], "gate": norm2(b["gate"]), "evidence": ev}

        def evaluated(E, bk):
            b = bstate(E, bk)
            if b["state"] not in ("gate", "blocked", "complete"):
                return False
            if b["state"] == "complete":
                return True
            return all(c.get("note") != "automatic check queued" for c in b["gate"] if c["type"] == "auto")

        dpu_done = False
        for bk in keys:
            t = wait(lambda E: [retry_waiting(X) for X in eng] and bstate(E, bk)["state"] in ("gate", "blocked", "complete") or (silent and bk == D_build(plan, silent) and held_on(st(E), silent, byn[silent].get("config_signal") == "reachable")) or
                     (agg and bk == D_build(plan, agg["name"]) and not dpu_done and st(E)["devices"][agg["name"]]["stage"] == 4), bk + " waves", timeout=900)
            if silent and bk == D_build(plan, silent):
                cmp("p2 %s: wave waits on the silent device" % bk, lambda E: norm2(st(E)["devices"][silent]))
                cmp("p2 %s: quarantine the silent device" % bk, lambda E: rv(post(E, "/api/quarantine", {"device": silent, "reason": "not answering: set aside"})))
            if agg and bk == D_build(plan, agg["name"]):
                wait(lambda E: st(E)["devices"][agg["name"]]["stage"] >= 4, "aggregate partly counted", timeout=600)
                cmp("p2 %s: aggregate counted in part" % bk, lambda E: norm2(st(E)["devices"][agg["name"]]))
                for i in range(len(agg["probe_ips"])):
                    down("%s#%d" % (agg["name"], i), False)
                dpu_done = True
            t = wait(lambda E: [retry_waiting(X) for X in eng] and bstate(E, bk)["state"] in ("gate", "blocked", "complete"), bk + " at its gate", timeout=900)
            wait(lambda E: evaluated(E, bk), bk + " checks evaluated", timeout=1500)
            time.sleep(3)   # one engine tick after the last result: blocked/gate settled in both
            cmp("p2 %s: gate, first evaluation" % bk, lambda E: gate_view(E, bk))
            # results the gate reads
            if bk == "fe":
                cmp("p2 fe: isolation result", lambda E: rv(post(E, "/api/result", {"kind": "isolation", "text": iso_text(plan)})))
            if bk == "be":
                cmp("p2 be: NCCL result below target (CLI)", lambda E: E.cmd("result", "300", "380")[:2])
                cmp("p2 be: accept result (pasted)", lambda E: rv(post(E, "/api/result", {"kind": "accept", "text": D.ACCEPT_TEXT})))
                cmp("p2 be: bad result", lambda E: rv(post(E, "/api/result", {"kind": "nccl", "busbw": "x", "target": 1})))
            for f in [f for f in faults if f[0] == bk]:
                f[2]()
            if bk == "edge" and sros_auth:
                # Phase 5 (DECISIONS P5-5): sessions are one per device and backups start staggered, so how soon each engine's
                # backup pass reaches the edge devices depends on its reading speed. Both engines must try every Done edge
                # device with the wrong SR OS login before it is fixed (up to 5 minutes), or one engine's after-build backup
                # fails while the other's, later, succeeds
                bdev_e = [n for w in next(b for b in plan["builds"] if b["key"] == bk)["waves"] for n in w["devices"]
                          if byn[n].get("os") in A.CMDS["backup"] and byn[n].get("ip")]
                def tried(E):
                    s_ = st(E)["devices"]
                    seen = {x[1] for x in D.events_of(os.path.join(E.base, "state")) if ("backup failed" in x[4] or "backed up" in x[4]) and x[1]}
                    return all(n in seen for n in bdev_e if s_[n]["stage"] == 6 and s_[n].get("flag") != "quarantined")
                if wait(tried, "edge backups tried before the SR OS login is fixed", timeout=300, step=5) is None:
                    for E in eng:
                        seen = {x[1] for x in D.events_of(os.path.join(E.base, "state")) if ("backup failed" in x[4] or "backed up" in x[4]) and x[1]}
                        print("    %s: not tried: %s" % (E.name, [n for n in bdev_e if n not in seen]))
                secrets(True)
            cmp("p2 %s: recheck after the fixes" % bk, lambda E: rv(post(E, "/api/gate/recheck", {"build": bk})))
            time.sleep(3)   # one engine tick: a gate that now passes is complete in both
            # Phase 5 (P5-5): a re-check still reading after 60 s (C64: 3,073 lossless lines per spine, sessions one per device,
            # queued behind backups) lands later; compared once both engines have settled (up to 90 s more)
            t0 = time.time()
            while time.time() - t0 < 90 and norm2(gate_view(PY, bk)) != norm2(gate_view(GO, bk)):
                time.sleep(3)
            cmp("p2 %s: gate after the fixes" % bk, lambda E: gate_view(E, bk))
            if bk == "be":
                cmp("p2 be: NCCL result at target (CLI)", lambda E: E.cmd("result", "401.5", "380")[:2])
                time.sleep(14)   # the checker evaluates the popped NCCL result, then a tick
                # Phase 4: on the larger plans (C64: 3,073 lossless lines per spine) Python's reader can still be re-reading a
                # check the fixes dropped after 14 s; compared once both engines have settled (up to 90 s more)
                t0 = time.time()
                while time.time() - t0 < 90 and norm2(gate_view(PY, bk)) != norm2(gate_view(GO, bk)):
                    time.sleep(3)
                cmp("p2 be: gate with the NCCL result", lambda E: gate_view(E, bk))
            # what still needs a person
            need = [c["key"] for c in bstate(PY, bk)["gate"] if c["result"] == "needs confirmation"]
            cmp("p2 %s: confirm what the reader cannot check" % bk, lambda E: [rv(post(E, "/api/gate/confirm", {"build": bk, "check": c, "note": "checked by hand"})) for c in need])
            t0 = time.time()
            while time.time() - t0 < 150:
                try:
                    if all(bstate(E, bk)["state"] == "complete" for E in eng):
                        break
                except (KeyError, ValueError, StopIteration):
                    pass
                time.sleep(1)
            else:
                # Phase 4: a check the fault fixes and recorded outputs cannot satisfy on this plan (both engines held alike):
                # compared, then overridden, so the rest of the run is still compared
                cmp("p2 %s: gate held after the fixes" % bk, lambda E: gate_view(E, bk))
                cmp("p2 %s: gate override" % bk, lambda E: rv(post(E, "/api/gate/override", {"build": bk, "reason": "lab: recorded outputs do not cover this check"})))
            wait(lambda E: bstate(E, bk)["state"] == "complete", bk + " gate passed", timeout=300)
            cmp("p2 %s: passed" % bk, lambda E: norm2(bstate(E, bk)))
            if bk == keys[0]:
                # a Done device stops answering: down after two missed probes, then back
                gone = next(n for w in plan["builds"][0]["waves"] for n in w["devices"] if byn[n].get("verify_ports") and byn[n].get("ip"))
                down(gone)
                wait(lambda E: st(E)["devices"][gone]["flag"] == "down", "device down", timeout=120)
                cmp("p2 Done device not answering", lambda E: norm2(st(E)["devices"][gone]))
                down(gone, False)
                wait(lambda E: st(E)["devices"][gone]["flag"] == "", "device back", timeout=120)
                cmp("p2 Done device answering again", lambda E: norm2(st(E)["devices"][gone]))
        wait(lambda E: st(E)["run"]["state"] == "complete" or all(b["state"] == "complete" for b in st(E)["builds"] if b["waves"]), "run complete", timeout=300)
        # ---- backups (the worker runs every 30 s), the difference view, a backup on request, the file
        time.sleep(35)
        bdev = [n for n in (A.devs) if byn[n].get("os") in DT and byn[n].get("ip") and n != silent][:6]
        cmp("p2 backup on request", lambda E: [rv(post(E, "/api/backup", {"device": n})) for n in bdev[:3]])
        cmp("p2 backup of a device without a reader", lambda E: rv(post(E, "/api/backup", {"device": next(n for n in A.devs if byn[n].get("os") not in DT)})))
        for n in bdev[:2]:
            cmd = A.CMDS["backup"][byn[n]["os"]]
            A.write(n, cmd, A.read(n, cmd) + "\nchanged on the device 1\n")
        cmp("p2 backup after a change", lambda E: [rv(post(E, "/api/backup", {"device": n})) for n in bdev[:2]])
        cmp("p2 difference views", lambda E: {n: [rv(E.con("GET", "/api/device/%s/diff" % n, cookie=ck[E.name], csrf=False)),
                                                   rv(E.con("GET", "/api/device/%s/diff?vs=prev" % n, cookie=ck[E.name], csrf=False))] for n in bdev})

        def bfile(E, n):
            names = json.loads(E.con("GET", "/api/device/" + n, cookie=ck[E.name], csrf=False)[2])["backups"]
            r = E.con("GET", "/api/device/%s/backup/%s" % (n, names[0]), cookie=ck[E.name], csrf=False) if names else (0, {}, b"")
            return {"n": len(names), "status": r[0], "cd": norm2(r[1].get("content-disposition")), "body": r[2].decode(), "missing":
                    E.con("GET", "/api/device/%s/backup/19990101T000000Z.cfg" % n, cookie=ck[E.name], csrf=False)[0]}
        cmp("p2 backup files", lambda E: [bfile(E, n) for n in bdev])
        cmp("p2 final state", lambda E: rv(E.con("GET", "/api/state", cookie=ck[E.name], csrf=False)))
        cmp("p2 final gate evidence", lambda E: {b["key"]: gate_view(E, b["key"])["evidence"] for b in plan["builds"] if b["waves"]})
        empty = [b["key"] for b in plan["builds"] if not any(w["devices"] for w in b["waves"])]
        if empty:
            # P4-10 (fixed in both engines): the run of all marks each empty build complete when it starts (one event each)
            # and no tick raises
            def empty_view(E):
                ev = D.events_of(os.path.join(E.base, "state"))
                return {"states": {k: bstate(E, k)["state"] for k in empty},
                        "events": [x[4] for x in ev if x[4].startswith("No devices in plane ")],
                        "engine errors": sorted(set(x[4] for x in ev if "engine error" in x[4]))}
            a, _ = cmp("p2 empty builds: complete at the run's start, no engine error", empty_view)
            rep.same("p2 empty builds: as P4-10 requires (python)", a, {"states": {k: "complete" for k in empty}, "events":
                     ["No devices in plane %s: build marked complete" % k for k in empty], "engine errors": []})
        st_py = st(PY)
        # Phase 4: gate backups are queued for every device of a build and taken at most 8 x reader_workers per 30 s pass; on
        # the larger plans (C64-3p: 450 back-end devices) the last passes are still running here. Both engines are let finish
        # (no new backup event for 70 s, up to 6 minutes) before the state is compared.
        nbk = lambda E: sum(1 for x in D.events_of(os.path.join(E.base, "state")) if "configuration backed up" in x[4])
        last, since, t0 = None, time.time(), time.time()
        while time.time() - t0 < 360:
            cur = tuple(nbk(E) for E in eng)
            if cur != last:
                last, since = cur, time.time()
            elif time.time() - since >= 70:
                break
            time.sleep(10)
    finally:
        for E in eng:
            E.stop()
        if fake:
            fake.terminate()
            fake.wait()
    S = [os.path.join(E.base, "state") for E in eng]
    isbk = lambda x: "configuration backed up" in x[4]
    ea, eb = D.events_of(S[0]), D.events_of(S[1])
    print("  events: python %d, go %d" % (len(ea), len(eb)))
    # "<build> gate blocked: ..." lists the checks blocked at that tick; which ones depends on when each finished reading
    gb = lambda x: list(x[:4]) + [re.sub(r"^(.* gate blocked: [^;]*?) -- .*$", r"\1 -- ...", x[4], flags=re.S)] + list(x[5:])
    # an engine error repeated every tick (before P4-10, an empty build): how many depends on how long it ran
    def dedup(L):
        seen, out = set(), []
        for x in L:
            if "engine error" in x[4]:
                if x[4] in seen:
                    continue
                seen.add(x[4])
            out.append(x)
        return out
    nb = lambda L: sorted(map(str, norm2([gb(x) for x in dedup(L) if not isbk(x)])))
    rep.same("p2 events (multiset, backups aside)", nb(ea), nb(eb))
    bset = lambda L: sorted(set(map(str, norm2([[x[1], re.sub(r"\((after build|scheduled)\): (unchanged since )?", "(KIND): ", x[4])] for x in L if isbk(x)]))))
    rep.same("p2 backup events (per device)", bset(ea), bset(eb))
    da, db_ = D.db_dump(S[0]), D.db_dump(S[1])
    rep.same("p2 database contents", norm2(da), norm2(db_))
    ra, rb = D.raw_json_texts(S[0]), D.raw_json_texts(S[1])
    rep.same("p2 stored JSON text: devices", ra["devices"], rb["devices"])
    for key in sorted(set(ra["kv"]) | set(rb["kv"])):
        if key == "auto_res":
            continue
        if key == "runners":
            # which runner a worker pass reaches first depends on the 10 s worker phase in each engine (DECISIONS,
            # Phase 3 verification): the entries are compared exactly, their order is not
            srt = lambda t: None if t is None else sorted(re.findall(r'"[\w-]+": \{[^{}]*\}', t))   # flat entries, time stamps masked
            rep.same("p2 stored JSON text: kv runners (entries)", srt(ra["kv"].get(key)), srt(rb["kv"].get(key)))
            continue
        rep.same("p2 stored JSON text: kv " + key, ra["kv"].get(key), rb["kv"].get(key))
    def rawkv(s, k):
        import sqlite3
        db = sqlite3.connect(os.path.join(s, "estate.db"))
        r = db.execute("SELECT v FROM kv WHERE k=?", (k,)).fetchone()
        db.close()
        return r[0] if r else "{}"
    mask = lambda t: re.sub(r"1[5-9]\d{8}\.\d+|2[0-4]\d{8}\.\d+", "T", t)
    ar = lambda t: {k: mask(json.dumps(v)) for k, v in json.loads(t, object_pairs_hook=collections.OrderedDict).items()}
    ta, tb = rawkv(S[0], "auto_res"), rawkv(S[1], "auto_res")
    rep.same("p2 stored JSON text: auto_res entries", ar(ta), ar(tb))
    rep.same("p2 go auto_res text is json.dumps form", tb, json.dumps(json.loads(tb, object_pairs_hook=collections.OrderedDict)))
    # Phase 4: the newest backup of each device (what a restore uses). Which earlier versions exist depends on which 30 s
    # backup pass reached a device first after it was Done -- before or after a fault was fixed (C64-3p: Go's pass caught a
    # spine before its lossless line was restored, Python's after; both newest backups are the fixed configuration)
    def bk(s):
        out = {}
        for d in sorted(os.listdir(os.path.join(s, "backups"))):
            fs = sorted(os.listdir(os.path.join(s, "backups", d)))
            out[d] = open(os.path.join(s, "backups", d, fs[-1])).read() if fs else None
        return out
    rep.same("p2 backup contents (newest per device)", bk(S[0]), bk(S[1]))
    rep.same("p2 state dir files", sorted(os.listdir(S[0])), sorted(os.listdir(S[1])))
    n_ok = sum(1 for d in st_py["devices"].values() if d["stage"] == 6)
    print("  devices Done: %d of %d; fake devices: %s" % (n_ok, len(st_py["devices"]), open(os.path.join(sim, "_stats.json")).read()))


def D_build(plan, n):
    for b in plan["builds"]:
        for w in b["waves"]:
            if n in w["devices"]:
                return b["key"]
    return None


def held_on(state, n, reachable=False):
    """The run waits on the silent device: checked in (stage 4), or for a device signalling "reachable" (it never checks in;
    the harness does not retry its DHCP), its wave open and the device short of Verified."""
    if reachable:
        return state["devices"][n]["stage"] < 5 and any(w.get("open") and n in w["devices"] for b in state["builds"] for w in b["waves"]) and \
            any(b["state"] == "building" for b in state["builds"])
    return state["devices"][n]["stage"] == 4 and any(b["state"] == "building" for b in state["builds"])


def iso_text(plan):
    ts = [t["tenant"] for t in (plan.get("isolation") or {}).get("tenants", [])] or ["TENANT-01"]
    out = []
    for t in ts:
        out += ["ISOLATION %s bind PASS vip bound" % t, "ISOLATION %s snat PASS own pool" % t, "ISOLATION %s cross PASS no reach" % t]
    return "\n".join(out) + "\n"


def tftp_cases(E, plan, byn, owner):
    import socket, struct
    port = json.load(open(E.conf))["tftp_port"]
    other = next(d["name"] for d in plan["devices"] if d.get("ip") and d["name"] != owner)
    out = []
    for src, fname, opts in ((byn[owner]["ip"], "poap/boot.py", {"blksize": "512", "tsize": "0"}), (byn[other]["ip"], "poap/boot.py", {}),
                             ("127.99.0.9", "poap/boot.py", {}), (byn[owner]["ip"], "nope.cfg", {}), (byn[owner]["ip"], "../etc/passwd", {}),
                             (byn[owner]["ip"], "images/nxos.bin", {"blksize": "1468"}), (byn[owner]["ip"], "images/none.bin", {})):
        s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        s.bind((src, 0))
        s.settimeout(5)
        pkt = struct.pack("!H", 1) + fname.encode() + b"\0octet\0" + b"".join(k.encode() + b"\0" + v.encode() + b"\0" for k, v in opts.items())
        s.sendto(pkt, ("127.0.0.1", port))
        got, oack, err = b"", None, None
        try:
            while True:
                d, a = s.recvfrom(70000)
                op = struct.unpack("!H", d[:2])[0]
                if op == 6:
                    oack = d[2:].split(b"\0")[:-1]
                    s.sendto(struct.pack("!HH", 4, 0), a)
                elif op == 3:
                    num = struct.unpack("!H", d[2:4])[0]
                    got += d[4:]
                    s.sendto(struct.pack("!HH", 4, num), a)
                    if len(d) - 4 < int(opts.get("blksize", 512)):
                        break
                elif op == 5:
                    err = (struct.unpack("!H", d[2:4])[0], d[4:-1].decode())
                    break
        except socket.timeout:
            err = "timeout"
        s.close()
        out.append({"src": src, "file": fname, "len": len(got), "oack": [x.decode() for x in oack] if oack else None, "err": err})
    time.sleep(0.5)
    return out
