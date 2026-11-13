#!/usr/bin/env python3
"""Acceptance-script release check: every acceptance test of a .run, run on a simulated site and scored by both engines.

usage: acctest.py --work DIR [--go-dir provgo] [--only NAME,...] --run INSTALLER.run [--run ...]

For each installer: the payload is unpacked; a fake site is made from its plan and acceptance files (hosts with rail, storage
and tenant NICs, RoCE v2 GIDs at differing indexes, sysfs counters, switch LLDP tables from cabling-expected.csv); the mock
tools in bin/ stand in for ssh, rdma, ip, ethtool, perftest, mpirun, iperf3, fio, hey, kubectl, curl, lldpctl and mlnx_qos.
Every test runs on a correct site, then with each fault planted. A test scored by the console is scored by estate_build.py
(pyjob.py) and by aidc-prov (internal/estate acctest_test.go): the two must agree, and the verdict must be the expected one
(pass; fail for the planted cause; or a setup failure naming why). Standalone tests are judged on their own output.
Exit 1 on any unexpected verdict or engine difference.
"""
import argparse, base64, collections, csv, io, json, os, re, shutil, subprocess, sys, time, zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
BIN = os.path.join(HERE, "bin")
LLDP_OS = ("cumulus", "junos", "eos", "nxos", "srl")
NOLLDP_ROLE = re.compile(r"firewall|edge|router|gw|load balancer|storage|DPU", re.I)


def sh(cmd, env, cwd, timeout=900, stdin=None):
    try:
        r = subprocess.run(["bash", "-c", cmd], cwd=cwd, env=env, capture_output=True, text=True, timeout=timeout, input=stdin)
        return r.returncode, r.stdout + r.stderr
    except subprocess.TimeoutExpired as e:
        o = e.stdout or ""
        o = o.decode(errors="replace") if isinstance(o, bytes) else o
        return 124, o + "\n[acctest: timed out after %d s]" % timeout


# ------------------------------------------------------------------------------------------------ the installer
class Pkg:
    def __init__(self, run, work):
        self.name = os.path.basename(os.path.dirname(os.path.abspath(run))) or os.path.basename(run)
        self.W = os.path.join(work, re.sub(r"[^A-Za-z0-9_.-]", "-", self.name))
        shutil.rmtree(self.W, ignore_errors=True)
        os.makedirs(self.W)
        t = open(run, "rb").read()
        i = t.index(b"\n__PAYLOAD_BELOW__\n")
        z = zipfile.ZipFile(io.BytesIO(base64.b64decode(b"".join(t[i + 19:].split()))))
        z.extractall(os.path.join(self.W, "pkg"))
        top = os.listdir(os.path.join(self.W, "pkg"))
        self.root = os.path.join(self.W, "pkg", top[0])
        self.acc = os.path.join(self.root, "acceptance")
        self.plan_path = os.path.join(self.root, "provision", "estate.json")
        self.kea = os.path.join(self.root, "provision", "kea-dhcp4.json")
        self.py = os.path.join(self.root, "provision", "estate_build.py")
        self.plan = json.load(open(self.plan_path))
        self.devs = {d["name"]: d for d in self.plan["devices"]}
        import importlib.util
        spec = importlib.util.spec_from_file_location("eb_" + str(abs(hash(self.W))), self.py)
        self.eb = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.eb)

    def has(self, f):
        return os.path.exists(os.path.join(self.acc, f))

    def text(self, f):
        return open(os.path.join(self.acc, f)).read()

    def default(self, f, var, env=None):
        m = re.search(r"\b%s=\$\{%s:-([^}]*)\}" % (re.escape(var), re.escape(env or var)), self.text(f))
        return m.group(1) if m else None

    def cable_rows(self):
        p = os.path.join(self.acc, "cabling-expected.csv")
        return list(csv.DictReader(open(p))) if os.path.exists(p) else []

    def lldp_dev(self, n):
        d = self.devs.get(n)
        return bool(d) and d.get("os") in LLDP_OS and not NOLLDP_ROLE.search(d.get("role", ""))


# ------------------------------------------------------------------------------------------------ the fake site
def ip2n(a):
    p = [int(x) for x in a.split(".")]
    return (p[0] << 24) + (p[1] << 16) + (p[2] << 8) + p[3]


def n2ip(n):
    return "%d.%d.%d.%d" % (n >> 24 & 255, n >> 16 & 255, n >> 8 & 255, n & 255)


class Site:
    CNT = ["link_down_events_phy", "rx_symbol_err_phy", "rx_discards_phy", "rx_prio3_pause", "tx_prio3_pause", "np_ecn_marked_roce_packets"]
    HW = ["out_of_sequence", "packet_seq_err", "np_ecn_marked_roce_packets", "np_cnp_sent", "rp_cnp_handled"]

    def __init__(self, P):
        self.P, self.M = P, os.path.join(P.W, "site")
        shutil.rmtree(self.M, ignore_errors=True)
        os.makedirs(os.path.join(self.M, "fake"))
        A = P.has
        self.rail_net = (P.has("lossless.sh") and P.default("lossless.sh", "RAIL_NET")) or None
        self.rails = int(P.default("lossless.sh", "NR", "ACCEPT_RAILS") or 0) if A("lossless.sh") else 0
        rc = os.path.join(P.root, "provision", "rail_check.sh")
        self.rail_mbps = int(re.search(r"RAIL_MBPS=\$\{RAIL_MBPS:-(\d+)\}", open(rc).read()).group(1)) if os.path.exists(rc) else 400000
        self.sto_net = P.default("storage_lossless.sh", "STO_NET") if A("storage_lossless.sh") else None
        hosts = []
        def add(h):
            if h and h not in hosts and not h.startswith("#"):
                hosts.append(h)
        for f in ("soak.hosts", "t7_pairs.txt"):
            if A(f):
                for l in P.text(f).splitlines():
                    if l.strip() and not l.lstrip().startswith("#"):
                        for x in l.split()[:2]:
                            if "=" not in x:
                                add(x)
        if A("failover-cases.csv"):
            for r in csv.DictReader([l for l in P.text("failover-cases.csv").splitlines() if not l.startswith("#")]):
                add(r["src"]); add(r["dst"])
        self.hosts = hosts
        for i, h in enumerate(hosts):
            self.host(h, i, len(hosts))
        self.host("ext-test", 0, 1, rails=0, sto=False)
        self.clients = ["client%02d" % k for k in range(1, 41)]
        for c in self.clients:
            os.makedirs(os.path.join(self.M, "fake", c))

    def host(self, h, i, H, rails=None, sto=True):
        rails = self.rails if rails is None else rails
        d = os.path.join(self.M, "fake", h)
        os.makedirs(d)
        rd, ad = [], []
        gbps = self.rail_mbps // 1000
        nics = [("rail", k) for k in range(rails if self.rail_net else 0)] + ([("sto", 0)] if (sto and self.sto_net) else [])
        for k, (kind, j) in enumerate(nics):
            dev, nd = "mlx5_%d" % k, "eth%d" % k
            rd.append("link %s/1 state ACTIVE physical_state LINK_UP netdev %s" % (dev, nd))
            if kind == "rail":
                ad.append("%s %s/31" % (nd, n2ip(ip2n(self.rail_net.split("/")[0]) + j * (2 * H + 2) + 2 * i + 1)))
            else:
                ad.append("%s %s/31" % (nd, n2ip(ip2n(self.sto_net.split("/")[0]) + 2 * i + 1)))
            ib = os.path.join(d, "ib", dev, "ports", "1")
            os.makedirs(os.path.join(ib, "hw_counters"))
            open(os.path.join(ib, "rate"), "w").write("%d Gb/sec (4X)\n" % gbps)
            for c in self.HW:
                open(os.path.join(ib, "hw_counters", c), "w").write("0\n")
            gi = 5 if (i % 7 == 3) else 3   # some hosts carry the RoCE v2 IPv4 GID at index 5
            os.makedirs(os.path.join(ib, "gid_attrs", "types")); os.makedirs(os.path.join(ib, "gids"))
            for x in range(6):
                open(os.path.join(ib, "gid_attrs", "types", str(x)), "w").write(("RoCE v2" if x in (1, gi) else "IB/RoCE v1") + "\n")
                open(os.path.join(ib, "gids", str(x)), "w").write(("0000:0000:0000:0000:0000:ffff:0a0a:0001" if x == gi else "fe80:0000:0000:0000:0000:0000:0000:0001") + "\n")
            open(os.path.join(ib, "v2gid"), "w").write(str(gi))
            os.makedirs(os.path.join(d, "ib", dev, "device", "net", nd))
            open(os.path.join(d, "eth." + nd), "w").write("".join("%s 0\n" % c for c in self.CNT))
            nn = os.path.join(d, "net", nd)
            os.makedirs(os.path.join(nn, "ecn", "roce_np", "enable"))
            open(os.path.join(nn, "speed"), "w").write("%d\n" % self.rail_mbps)
            open(os.path.join(nn, "ecn", "roce_np", "enable", "3"), "w").write("1\n")
        ad.append("pf0hpf 172.16.%d.%d/16" % ((i + 10) >> 8, (i + 10) & 255))
        if h == "ext-test":
            ad.append("eth9 198.51.100.200/24")
        open(os.path.join(d, "rdma"), "w").write("\n".join(rd) + ("\n" if rd else ""))
        open(os.path.join(d, "addrs"), "w").write("\n".join(ad) + "\n")

    def env(self, **kw):
        e = dict(os.environ)
        e.update({"PATH": BIN + ":" + os.environ.get("PATH", ""), "MOCK": self.M})
        e.update({k: str(v) for k, v in kw.items()})
        return e

    def put(self, rel, text):
        p = os.path.join(self.M, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        open(p, "w").write(text)

    def rm(self, *rels):
        for r in rels:
            p = os.path.join(self.M, r)
            if os.path.isdir(p):
                shutil.rmtree(p)
            elif os.path.exists(p):
                os.remove(p)


# ------------------------------------------------------------------------------------------------ verdicts
def v_lines(out, rx_pass, rx_fail, rx_setup=r"setup FAIL|error="):
    if re.search(rx_setup, out, re.M):
        return "setup", " | ".join(l for l in out.splitlines() if re.search(rx_setup, l))[:400]
    if re.search(rx_fail, out, re.M):
        return "fail", " | ".join(l for l in out.splitlines() if re.search(rx_fail, l))[:400]
    if re.search(rx_pass, out, re.M):
        return "pass", ""
    return "none", out[-300:]


def v_engine(op, r):
    if "exc" in r:
        return "exc", r["exc"]
    if op["op"] == "check":
        if "unavailable" in r:
            return "unavailable", r["unavailable"]
        return ("pass", r["note"]) if r["ok"] else ("fail", r["note"])
    evs = r["eval"] if isinstance(r["eval"], list) else [r["eval"]]
    fails = []
    for ev in evs:
        for k, x in sorted((ev.get("evidence") or {}).items()):
            if "switch counters" in k:   # uploaded by hand: the switch side is confirmed from the switches, not scored here
                continue
            if op["op"] == "accept" and not k.startswith(op["test"] + " "):
                continue
            if x.get("ok") is False:
                fails.append("%s: %s" % (k, x.get("note")))
        if op["op"] != "accept" and ev.get("result") not in ("pass", "confirm") and not fails and not any(
                "switch counters" in k for k, x in (ev.get("evidence") or {}).items() if x.get("ok") is False):
            fails.append(ev.get("note", ""))
    if not r["submit"][0]:
        return "fail", "not accepted: " + r["submit"][1]
    return ("fail", "; ".join(fails)) if fails else ("pass", "")


def expect_ok(exp, got):
    st, txt = got
    if exp == "pass":
        return st == "pass"
    k, sub = exp
    return st == k and re.search(sub, txt or "", re.I) is not None


# ------------------------------------------------------------------------------------------------ the cases
class Suite:
    def __init__(self, P, args):
        self.P, self.args = P, args
        self.S = Site(P)
        self.rows = []          # (case, expected, got, engines) for the report
        self.ops = []           # engine ops of the script runs: (case, expected, op)
        self.jobs = []          # gate-check jobs: (label, sim dir, state, [(case, expected, op)])
        self.wk = os.path.join(P.W, "runs")
        shutil.copytree(P.acc, self.wk)
        self.crit = (P.plan.get("acceptance") or {}).get("criteria") or {}

    def run(self, cmd, timeout=900, **env):
        return sh(cmd, self.S.env(**env), self.wk, timeout)

    def script(self, case, exp, cmd, verdict, **env):
        rc, out = self.run(cmd, **env)
        got = verdict(out, rc)
        self.rows.append((case, exp, got, None))
        return rc, out

    def engine(self, case, exp, op):
        self.ops.append((case, exp, op))

    # ---- back end
    def lossless(self):
        S = self.S
        hf = os.path.join(self.wk, "pod.hosts")
        open(hf, "w").write("".join("%s slots=8\n" % h for h in S.hosts[:8]))
        env = dict(HOSTFILE=hf, ACCEPT_SECONDS=2, ACCEPT_T1_SECONDS=2, ACCEPT_T7_SECONDS=2)
        for T in ("T1", "T2", "T3", "T4", "T7"):
            rc, out = self.run("bash lossless.sh " + T, **env)
            self.engine("lossless %s, correct site (RoCE v2 GIDs at index 3 and 5)" % T, "pass", {"op": "accept", "test": T, "text": out, "crit": self.crit})
        S.put("fake/%s/bw" % S.hosts[1], "mlx5_2 180.00\n")
        rc, out = self.run("bash lossless.sh T1", **env)
        self.engine("lossless T1, one rail at 180 Gb/s", ("fail", "line rate"), {"op": "accept", "test": "T1", "text": out, "crit": self.crit})
        if self.P.has("t7_pairs.txt"):
            h = [l.split()[0] for l in self.P.text("t7_pairs.txt").splitlines() if l.strip() and not l.startswith("#")][0]
            S.put("fake/%s/bw" % h, "* 200.00\n")
            rc, out = self.run("bash lossless.sh T7", **env)
            self.engine("lossless T7, one host's flows at half rate (a hash collision)", ("fail", "line rate|spread"), {"op": "accept", "test": "T7", "text": out, "crit": self.crit})
            S.rm("fake/%s/bw" % h)
        S.rm("fake/%s/bw" % S.hosts[1])
        # one host missing a rail
        rd = os.path.join(S.M, "fake", S.hosts[2], "rdma")
        good = open(rd).read()
        open(rd, "w").write(good.replace("link mlx5_4/1 state ACTIVE", "link mlx5_4/1 state DOWN"))
        rc, out = self.run("bash lossless.sh T1", **env)
        self.engine("lossless T1, one rail down on a host", ("fail", "line rate|rail"), {"op": "accept", "test": "T1", "text": out, "crit": self.crit})
        open(rd, "w").write(good)

    def nccl(self, script="nccl.sh"):
        S = self.S
        hf = os.path.join(self.wk, "nccl.hosts")
        open(hf, "w").write("".join("%s slots=8\n" % h for h in S.hosts[:4]))
        v = lambda o, rc: v_lines(o, r"^PASS busbw", r"^FAIL busbw", r"NCCL setup FAIL")
        env = dict(HOSTFILE=hf, TARGET_BUSBW=380)
        self.script("%s, at target" % script, "pass", "bash " + script, v, **env)
        S.put("busbw", "300\n")
        self.script("%s, below target" % script, ("fail", "300"), "bash " + script, v, **env)
        S.put("mpifail", "")
        self.script("%s, mpirun cannot launch" % script, ("setup", "mpirun exit 213"), "bash " + script, v, **env)
        S.rm("mpifail")
        if script == "nccl.sh":
            self.script("nccl.sh, no rail NIC in the rail block", ("setup", "no ACTIVE RDMA device"), "bash nccl.sh", v, RAIL_NET="192.0.2.0/24", **env)
            log = open(os.path.join(S.M, "mpirun.log")).read().splitlines()[0]
            ok = re.search(r"NCCL_IB_HCA==mlx5_0(,mlx5_\d+){%d}$" % (S.rails - 1), log) is not None
            self.rows.append(("nccl.sh runs on the %d rail NICs only (NCCL_IB_HCA)" % S.rails, "pass", ("pass", "") if ok else ("fail", log[-200:]), None))

    def rails(self):
        S = self.S
        rc_path = os.path.join(self.P.root, "provision", "rail_check.sh")
        body = open(rc_path).read()
        v = lambda o, rc: ("pass", "") if rc == 0 and re.search(r"RAILS ok %d\b" % S.rails, o) else ("fail", " | ".join(l for l in o.splitlines() if l.startswith(("FAIL", "RAILS"))))
        h = S.hosts[3]
        def one(case, exp):
            rc, out = sh("ssh %s 'RAILS=%d bash -s'" % (h, S.rails), S.env(), self.wk, 120, stdin=body)
            self.rows.append((case, exp, v(out, rc), None))
        one("rail_check.sh, correct host", "pass")
        d = os.path.join(S.M, "fake", h)
        rd0 = open(d + "/rdma").read()
        k = S.rails + 3
        open(d + "/rdma", "a").write("link mlx5_%d/1 state ACTIVE physical_state LINK_UP netdev eth%d\n" % (k, k))   # an unaddressed DPU port
        one("rail_check.sh, an unaddressed DPU port on the host", "pass")
        open(d + "/rdma", "w").write(rd0.replace("link mlx5_3/1 state ACTIVE physical_state LINK_UP", "link mlx5_3/1 state DOWN physical_state DISABLED"))
        one("rail_check.sh, one rail down", ("fail", "mlx5_3.*DOWN"))
        open(d + "/rdma", "w").write(rd0)

    def soak(self):
        S = self.S
        env = dict(SOAK_HOURS=0.01)
        sw = None
        planes = None
        # the mock soak takes about 0.5 s a host on a 2-core machine: allow for the largest reference design (576 hosts)
        tmo = max(300, 2 * len(S.hosts))
        rc, out = self.run("bash soak.sh", timeout=tmo, **env)
        self.engine("soak.sh (36 s), steady at target", "pass", {"op": "rz", "kind": "soak", "text": out, "plan": {"hours": 0.01}, "target": "380", "switch": sw})
        S.put("fake/%s/eth.eth1.inc" % S.hosts[0], "link_down_events_phy 2\n")
        rc, out = self.run("bash soak.sh", timeout=tmo, **env)
        self.engine("soak.sh, a host link flapping", ("fail", "flap|link_down"), {"op": "rz", "kind": "soak", "text": out, "plan": {"hours": 0.01}, "target": "380", "switch": sw})
        S.rm("fake/%s/eth.eth1.inc" % S.hosts[0])
        rc, out = self.run("bash soak.sh", timeout=tmo, RAIL_NET="192.0.2.0/24", **env)
        self.engine("soak.sh, no rail NIC in the rail block", ("fail", "soak.sh stopped: no rail RDMA device"), {"op": "rz", "kind": "soak", "text": out, "plan": {"hours": 0.01}, "target": "380", "switch": sw})

    def storage(self):
        S = self.S
        v = lambda o, rc: v_lines(o, r"^STORAGE S\d PASS", r"^STORAGE S\d FAIL(?! setup)", r"FAIL setup|FAIL no hosts|has no active RDMA")
        env = dict(STO_SECONDS=1)
        self.script("storage_lossless.sh S1, correct", "pass", "bash storage_lossless.sh S1", v, **env)
        self.script("storage_lossless.sh S2, correct", "pass", "bash storage_lossless.sh S2", v, **env)
        h = [l.split()[0] for l in self.P.text("t7_pairs.txt").splitlines() if l.strip() and not l.startswith("#")][0]
        S.put("fake/%s/fio" % h, "70\n")
        self.script("storage_lossless.sh S1, one host at 40% of the others (an uplink collision)", ("fail", "min_gbps=70"), "bash storage_lossless.sh S1", v, **env)
        S.rm("fake/%s/fio" % h)
        for x in S.hosts:
            S.put("fake/%s/fio" % x, "0\n")
        self.script("storage_lossless.sh S1, storage not mounted", ("setup", "no host reported throughput"), "bash storage_lossless.sh S1", v, **env)
        for x in S.hosts:
            S.rm("fake/%s/fio" % x)
        sd = "eth%d" % S.rails
        S.put("fake/%s/eth.%s.inc" % (h, sd), "rx_discards_phy 5\n")
        self.script("storage_lossless.sh S2, discards at a host's storage NIC", ("fail", "loss=[1-9]"), "bash storage_lossless.sh S2", v, **env)
        S.rm("fake/%s/eth.%s.inc" % (h, sd))

    # ---- both
    def failover(self):
        S = self.S
        rows = [r for r in csv.DictReader([l for l in self.P.text("failover-cases.csv").splitlines() if not l.startswith("#")])]
        planes = [[p] for p in sorted(set(r["plane"] for r in rows))]
        env = dict(FAILOVER_DOWN="true", FAILOVER_UP="true", FAILOVER_SECONDS=2, FAILOVER_AT=1, FAILOVER_STREAMS=4, FAILOVER_PPS=1000,
                   FAILOVER_TENANT_NET="172.16.0.0/16", FAILOVER_EXT_HOST="ext-test", FAILOVER_EXT_ADDR="198.51.100.200")
        S.put("iperf.lost.down", "300\n120\n0\n0\n"); S.put("iperf.lost.up", "20\n0\n0\n0\n")
        rc, out = self.run("bash failover.sh", **env)
        self.engine("failover.sh, every case within target", "pass", {"op": "rz", "kind": "failover", "text": out, "planes": planes})
        env["FAILOVER_ONLY"] = rows[0]["id"]   # the faults on one case (the correct site covered every case)
        S.put("iperf.lost.down", "1800\n0\n0\n0\n")
        rc, out = self.run("bash failover.sh", **env)
        self.engine("failover.sh, a 1.8 s outage", ("fail", "1800 ms"), {"op": "rz", "kind": "failover", "text": out, "planes": planes})
        S.rm("iperf.lost.down", "iperf.lost.up")
        env["FAILOVER_DOWN"] = "false"
        rc, out = self.run("bash failover.sh", **env)
        self.engine("failover.sh, the down command fails", ("fail", "could.not.take"), {"op": "rz", "kind": "failover", "text": out, "planes": planes})

    def alerts(self):
        S = self.S
        devs = [l.split()[0] for l in self.P.text("telemetry-devices.txt").splitlines() if l.strip() and not l.startswith("#")]
        S.put("telemetry", "\n".join(devs) + "\n")
        env = dict(ALERT_QUERY="cat $MOCK/alert.{dev} 2>/dev/null", TELEMETRY_QUERY="cat $MOCK/telemetry", ALERT_WAIT_S=4, ALERT_POLL_S=1,
                   FAULT_LINK_DOWN="echo up > $MOCK/alert.{dev}", FAULT_LINK_UP="rm -f $MOCK/alert.{dev}",
                   FAULT_BGP_DOWN="echo up > $MOCK/alert.{dev}", FAULT_BGP_UP="rm -f $MOCK/alert.{dev}")
        rc, out = self.run("bash alerts.sh", **env)
        self.engine("alerts.sh, every alert raised and cleared, every device reporting", "pass", {"op": "rz", "kind": "alerts", "text": out})
        e2 = dict(env, FAULT_LINK_DOWN="true")
        rc, out = self.run("bash alerts.sh", **e2)
        self.engine("alerts.sh, a link fault that raises no alert", ("fail", "alert-link|not detected|no alert"), {"op": "rz", "kind": "alerts", "text": out})
        S.put("telemetry", "\n".join(devs[1:]) + "\n")
        rc, out = self.run("bash alerts.sh", **env)
        self.engine("alerts.sh, a device not reporting", ("fail", re.escape(devs[0])), {"op": "rz", "kind": "alerts", "text": out})

    # ---- front end
    def inference(self):
        S = self.S
        t = self.P.text("inference-targets.csv").replace("<HOSTNAME>", "inference.example")
        open(os.path.join(self.wk, "inference-targets.csv"), "w").write(t)
        b = open(os.path.join(self.wk, "inference-request.json")).read().replace("<MODEL>", "test-model")
        open(os.path.join(self.wk, "inference-request.json"), "w").write(b)
        rows = list(csv.DictReader(io.StringIO(t)))
        need = max(-(-int(r.get("concurrency") or 8) // 4000) for r in rows)
        cl = " ".join("u@" + c for c in S.clients[:max(need, 1)])
        rc, out = self.run("bash inference_load.sh", DURATION=2, CLIENTS=cl)
        self.engine("inference_load.sh, %d clients for %s requests a second" % (need, rows[0]["rps_target"]), "pass", {"op": "inference", "text": out})
        if need > 1:
            rc, out = self.run("bash inference_load.sh", DURATION=2, CLIENT_SSH="u@" + S.clients[0])
            self.engine("inference_load.sh, one client where %d are needed" % need, ("fail", "need %d clients" % need), {"op": "inference", "text": out})
        S.put("hey.lat", "0.8\n")
        rc, out = self.run("bash inference_load.sh", DURATION=2, CLIENTS=cl)
        self.engine("inference_load.sh, p99 over target", ("fail", "p99"), {"op": "inference", "text": out})
        S.rm("hey.lat")

    def isolation(self):
        S = self.S
        rows = list(csv.DictReader(io.StringIO(self.P.text("isolation-tenants.csv"))))
        for r in rows:
            S.put("iso/" + r["vip"], "%s %s\n" % (r["namespace"], r["snat"].split(";")[0]))
        env = dict(REQUESTS=3)
        rc, out = self.run("bash ingress_isolation.sh", **env)
        self.engine("ingress_isolation.sh, tenants held apart", "pass", {"op": "isolation", "text": out})
        if len(rows) > 1:
            S.put("iso.leak", "%s %s\n" % (rows[1]["vip"], rows[0]["namespace"]))
            rc, out = self.run("bash ingress_isolation.sh", **env)
            self.engine("ingress_isolation.sh, one VIP lands in another tenant's namespace", ("fail", rows[1]["tenant"] + " bind"), {"op": "isolation", "text": out})
            S.rm("iso.leak")
            S.put("iso.open", "")
            rc, out = self.run("bash ingress_isolation.sh", **env)
            self.engine("ingress_isolation.sh, cluster policy lets tenants reach each other", ("fail", "cross"), {"op": "isolation", "text": out})
            S.rm("iso.open")
            S.put("iso.noself", "")
            rc, out = self.run("bash ingress_isolation.sh", **env)
            self.engine("ingress_isolation.sh, a probe its own namespace cannot reach", ("fail", "cross"), {"op": "isolation", "text": out})
            S.rm("iso.noself")

    # ---- cabling and software (every switch platform)
    def truth(self, mode):
        """{device: [(local port, neighbor, neighbor port)]} as the switches would report it, from cabling-expected.csv"""
        P, L = self.P, collections.defaultdict(list)
        for k, r in enumerate(P.cable_rows()):
            for a, ap, b, bp in ((r["device_a"], r["port_a"], r["device_b"], r["port_b"]), (r["device_b"], r["port_b"], r["device_a"], r["port_a"])):
                if a not in P.devs or P.devs[a].get("os") not in LLDP_OS or r["plane"] == "oob-drop":
                    continue
                if P.lldp_dev(b):
                    port, bos = bp, P.devs[b]["os"]
                    if bos == "nxos":
                        port = re.sub(r"^Ethernet", "Eth", bp)   # NX-OS sends its short names
                    if mode == "ifindex" and bos == "junos":
                        port = str(500 + k)                     # a Junos far end left at the SNMP-index default
                    L[a].append((ap, b, port))
                elif b not in P.devs or P.devs[b].get("os") not in LLDP_OS:
                    L[a].append((ap, "host-%d" % k, "enp%ds0" % (k % 9)))   # a host, storage node or DPU running lldpd
        return L

    @staticmethod
    def lldp_json(os_, nb):
        J = json.dumps
        if os_ == "cumulus":
            return J({l: {"lldp": {"neighbor": {n: {"chassis": {"system-name": n}, "port": {"name": p, "type": "ifname"}}}}} for l, n, p in nb})
        if os_ == "junos":
            return J({"lldp-neighbors-information": [{"lldp-neighbor-information": [{"lldp-local-port-id": [{"data": l}], "lldp-remote-port-id": [{"data": p}], "lldp-remote-system-name": [{"data": n}]} for l, n, p in nb]}]})
        if os_ == "eos":
            return J({"lldpNeighbors": [{"port": l, "neighborDevice": n + ".lab", "neighborPort": p, "ttl": 120} for l, n, p in nb]})
        if os_ == "nxos":
            return J({"TABLE_nbor_detail": {"ROW_nbor_detail": [{"l_port_id": re.sub(r"^Ethernet", "Eth", l), "port_id": p, "sys_name": n} for l, n, p in nb]}})
        return J({"system": {"lldp": {"interface": [{"name": l, "neighbor": [{"system-name": n, "port-id": p}]} for l, n, p in nb]}}})

    def swap_dev(self, L):
        est = self.py_estate()
        for n in sorted(L):
            exp = est.expected_links(n)
            sw = [x for x in L[n] if x[0] in exp]
            if len(sw) >= 2:
                return n
        return None

    def py_estate(self):
        if not hasattr(self, "_est"):
            d = os.path.join(self.P.W, "probe")
            os.makedirs(d, exist_ok=True)
            json.dump({"state_dir": d + "/st", "secrets": d + "/sec.env", "web_root": self.P.root, "bundle_dir": self.P.root,
                       "reader_sim_dir": d + "/sim"}, open(d + "/conf.json", "w"))
            open(d + "/sec.env", "w").write("TARGET_BUSBW=380\n")
            self._est = self.P.eb.Estate(self.P.plan_path, d + "/conf.json", self.P.kea)
        return self._est

    def cabling(self):
        P, eb = self.P, self.P.eb
        lldp = [n for n, d in P.devs.items() if d.get("os") in LLDP_OS and d.get("planned_config")]
        # the console's cable plan against cabling-expected.csv, link for link
        est = self.py_estate()
        want = set()
        for r in P.cable_rows():
            if r["plane"] in ("be", "fe", "oob") and P.lldp_dev(r["device_a"]) and P.lldp_dev(r["device_b"]):
                for a, ap, b, bp in ((r["device_a"], r["port_a"], r["device_b"], r["port_b"]), (r["device_b"], r["port_b"], r["device_a"], r["port_a"])):
                    if P.devs[a].get("planned_config") and not re.match(r"(?i)ae\d|bond|lag|port-channel", ap):
                        want.add((a, ap, b, bp))
        have = set()
        for n in lldp:
            for lp, (pd, pp) in est.expected_links(n).items():
                have.add((n, lp, est.peer_name(pd), pp))
        miss, extra = sorted(want - have), sorted(have - want)
        self.rows.append(("console cable plan = cabling-expected.csv (%d switch links)" % len(want), "pass",
                          ("pass", "") if not miss and not extra else ("fail", "missing %s; extra %s" % (miss[:3], extra[:3])), None))
        for mode in ("correct", "ifindex", "swap"):
            L = self.truth("ifindex" if mode == "ifindex" else "correct")
            bad = None
            if mode == "swap":
                bad = self.swap_dev(L)
                ex = [x for x in L[bad] if x[0] in est.expected_links(bad)]
                i0, i1 = L[bad].index(ex[0]), L[bad].index(ex[1])
                L[bad][i0], L[bad][i1] = (ex[0][0], ex[1][1], ex[1][2]), (ex[1][0], ex[0][1], ex[0][2])
            sim = os.path.join(P.W, "sim-cabling-" + mode)
            shutil.rmtree(sim, ignore_errors=True)
            for n in lldp:
                os_ = P.devs[n]["os"]
                p = os.path.join(sim, n, eb.slug(eb.LLDP_CMD[os_]) + ".txt")
                os.makedirs(os.path.dirname(p), exist_ok=True)
                open(p, "w").write(self.lldp_json(os_, L.get(n, [])))
            ops = []
            for n in sorted(lldp):
                exp = ("fail", "miswired") if n == bad else "pass"
                what = {"correct": "correct cabling (NX-OS short port names, hosts running lldpd)", "ifindex": "Junos far ends sending SNMP indexes",
                        "swap": "two cables swapped on %s" % bad}[mode]
                ops.append(("console cabling check, %s: %s" % (what, n), exp, {"op": "check", "kind": "cabling", "dev": n}))
            self.jobs.append(("cabling-" + mode, sim, ops))
        # check_cabling.py on the same tables
        for mode in ("correct", "ifindex", "swap"):
            L = self.truth("ifindex" if mode == "ifindex" else "correct")
            if mode == "swap":
                bad = sorted(n for n in L if len([x for x in L[n] if not x[1].startswith("host-")]) >= 2)[0]
                sw = [i for i, x in enumerate(L[bad]) if not x[1].startswith("host-")][:2]
                a, b = L[bad][sw[0]], L[bad][sw[1]]
                L[bad][sw[0]], L[bad][sw[1]] = (a[0], b[1], b[2]), (b[0], a[1], a[2])
            d = os.path.join(self.wk, "lldp")
            shutil.rmtree(d, ignore_errors=True); os.makedirs(d)
            for n, nb in L.items():
                with open(os.path.join(d, n + ".csv"), "w") as f:
                    f.write("local_port,neighbor_name,neighbor_port\n" + "".join("%s,%s.lab,%s\n" % (l, x, p) for l, x, p in nb if not x.startswith("host-")))
            v = lambda o, rc: ("pass", o.strip().splitlines()[-1]) if rc == 0 else ("fail", " | ".join(o.strip().splitlines()[-3:]))
            self.script("check_cabling.py, %s" % mode, "pass" if mode != "swap" else ("fail", "MISWIRED"), "python3 check_cabling.py", v)
        self.ztp_cumulus()

    def ztp_cumulus(self):
        P, S = self.P, self.S
        done, todo = 0, []
        L0, L1 = self.truth("correct"), self.truth("ifindex")
        for n in sorted(P.devs):
            d = P.devs[n]
            zp = os.path.join(P.root, "ztp", n + ".sh")
            if d.get("os") != "cumulus" or not os.path.exists(zp) or "cabling check" not in open(zp).read() or done >= 3:
                continue
            if not any(not x[1].startswith("host-") for x in L0.get(n, [])):
                continue
            z = open(zp).read()
            m = re.search(r"^(EXP=.*?\n)(?:.*\n)*?fi\n", z[z.index("cabling check"):], re.M)
            block = z[z.index("cabling check"):][m.start():m.end()]
            cfg = os.path.join(P.root, d["planned_config"])
            done += 1
            for mode, L in (("correct", L0), ("ifindex", L1), ("swap", None)):
                nb = list((L or L0)[n])
                if mode == "swap":
                    sw = [i for i, x in enumerate(nb) if not x[1].startswith("host-")][:2]
                    if len(sw) < 2:
                        continue
                    a, b = nb[sw[0]], nb[sw[1]]
                    nb[sw[0]], nb[sw[1]] = (a[0], b[1], b[2]), (b[0], a[1], a[2])
                kv = []
                for l, x, p in nb:
                    kv.append("lldp.%s.chassis.name=%s" % (l, x))
                    kv += (["lldp.%s.port.local=%s" % (l, p), "lldp.%s.port.descr=to %s" % (l, n)] if re.match(r"^\d+$", p) else ["lldp.%s.port.ifname=%s" % (l, p)])
                mh = "%s--%s" % (n, mode)   # one fake switch per run, so the runs go in parallel
                S.put("fake/%s/lldp" % mh, "\n".join(kv) + "\n")
                script = 'ci_fail(){ echo "CI_FAIL $*"; exit 1; }\nCFG=%s\n%s' % (cfg, block)
                todo.append(("Cumulus ZTP cabling check on %s, %s" % (n, mode), ("fail", "expected") if mode == "swap" else "pass", script, mh))
        import concurrent.futures
        with concurrent.futures.ThreadPoolExecutor(12) as ex:
            res = list(ex.map(lambda t: sh(t[2], S.env(MOCKHOST=t[3]), self.wk, 180), todo))
        for (case, exp, _, _), (rc, out) in zip(todo, res):
            got = ("fail", out.strip()[-300:]) if "CI_FAIL" in out else ("pass", "") if "no miswire seen" in out else ("none", out[-300:])
            self.rows.append((case, exp, got, None))

    def software(self):
        P, eb = self.P, self.P.eb
        VT = {"cumulus": "DISTRIB_ID=\"Cumulus Linux\"\nDISTRIB_RELEASE=%s\n", "junos": "Hostname: x\nModel: qfx\nJunos: %s\n",
              "eos": "Arista DCS-7060X6\nSoftware image version: %s\n", "nxos": "Cisco Nexus Operating System (NX-OS) Software\n  NXOS: version %s\n",
              "srl": "    system {\n        information {\n            version v%s-104-gabc\n        }\n    }", "sros": "TiMOS-C-%s cpm/x86_64 Nokia 7750 SR\n",
              "iosxr": "Cisco IOS XR Software, Version %s LNT\n", "iosxe": "Cisco IOS XE Software, Version %s\n"}
        def shown(os_, rel):
            if os_ == "iosxe":   # IOS XE pads: 17.15.6 prints as 17.15.06
                rel = re.sub(r"\.(\d)$", r".0\1", rel)
            if os_ == "nxos":
                rel = re.sub(r"[A-Z]$", "", rel)
            if os_ == "junos":
                rel += ".4"
            return VT[os_] % rel
        devs = [n for n, d in P.devs.items() if d.get("os") in eb.VERSION_CMD and isinstance(d.get("release"), dict) and d["release"].get("target")]
        below = sorted(n for n in devs if P.devs[n]["os"] == "junos")[:1] or sorted(devs)[:1]
        for mode in ("at-target", "below"):
            sim = os.path.join(P.W, "sim-software-" + mode)
            shutil.rmtree(sim, ignore_errors=True)
            ops = []
            for n in sorted(devs):
                os_, rel = P.devs[n]["os"], P.devs[n]["release"]["target"]
                txt = shown(os_, "1.2.3" if (mode == "below" and n in below) else rel)
                p = os.path.join(sim, n, eb.slug(eb.VERSION_CMD[os_]) + ".txt")
                os.makedirs(os.path.dirname(p), exist_ok=True)
                open(p, "w").write(txt)
                exp = ("fail", "running .*target") if (mode == "below" and n in below) else "pass"
                ops.append(("console software check, %s: %s (%s)" % (mode, n, os_), exp, {"op": "check", "kind": "software", "dev": n}))
            self.jobs.append(("software-" + mode, sim, ops))
        # check_os_release.py --offline
        op = os.path.join(P.root, "os", "check_os_release.py")
        if os.path.exists(op):
            rows = list(csv.DictReader(open(os.path.join(P.root, "os", "os-profiles.csv"))))
            for mode in ("at-target", "below"):
                d = os.path.join(P.W, "osrel-" + mode)
                shutil.rmtree(d, ignore_errors=True); os.makedirs(d)
                bad = None
                for r in rows:
                    if r["os"] in VT and r["target"]:
                        rel = r["target"]
                        if mode == "below" and bad is None:
                            bad, rel = r["device"], "1.0.0" if r["os"] != "junos" else "1.1R1"
                        txt = ("Software Version      : v%s-104-gabc\n" % rel) if r["os"] == "srl" else shown(r["os"], rel)   # SR Linux "show version"
                        open(os.path.join(d, r["device"] + ".txt"), "w").write(txt)
                rc, out = sh("python3 %s --offline %s" % (op, d), S_env(), self.wk, 300)
                got = ("pass", out.strip().splitlines()[-1]) if rc == 0 else ("fail", " | ".join(l for l in out.splitlines() if l.startswith("FAIL"))[:300])
                self.rows.append(("check_os_release.py, %s" % mode, "pass" if mode == "at-target" else ("fail", re.escape(bad or "-")), got, None))

    # ---- scoring
    def score(self):
        P = self.P
        J = os.path.join(P.W, "jobs")
        shutil.rmtree(J, ignore_errors=True); os.makedirs(J)
        open(J + "/sec.env", "w").write("TARGET_BUSBW=380\n")
        state = {n: {"stage": 6, "flag": ""} for n in P.devs}
        jobs = [("scripts", os.path.join(J, "nosim"), [(c, e, o) for c, e, o in self.ops])] + self.jobs
        def jobfile(eng):
            out = []
            for k, (label, sim, ops) in enumerate(jobs):
                st = os.path.join(J, "%s-%d" % (eng, k))
                conf = os.path.join(J, "conf-%s-%d.json" % (eng, k))
                json.dump({"state_dir": st, "secrets": J + "/sec.env", "web_root": P.root, "bundle_dir": P.root, "reader_sim_dir": sim}, open(conf, "w"))
                out.append({"plan": P.plan_path, "kea": P.kea, "conf": conf, "state": state, "ops": [o for _, _, o in ops]})
            p = os.path.join(J, "jobs-%s.json" % eng)
            json.dump(out, open(p, "w"))
            return p
        jp, jg = jobfile("py"), jobfile("go")
        r = subprocess.run([sys.executable, "-I", os.path.join(HERE, "pyjob.py"), P.py, jp, J + "/out-py.json"], capture_output=True, text=True)
        if r.returncode:
            raise RuntimeError("pyjob: " + r.stderr[-2000:])
        env = dict(os.environ, ACCTEST_JOBS=jg, ACCTEST_OUT=J + "/out-go.json", GOFLAGS="-count=1")
        r = subprocess.run(["go", "test", "./internal/estate", "-run", "^TestAccJob$"], cwd=self.args.go_dir, env=env, capture_output=True, text=True)
        if r.returncode:
            raise RuntimeError("go: " + (r.stdout + r.stderr)[-2000:])
        py, go = json.load(open(J + "/out-py.json")), json.load(open(J + "/out-go.json"))
        canon = lambda x: json.dumps(x, sort_keys=True)
        for (label, sim, ops), rp, rg in zip(jobs, py, go):
            for (case, exp, op), a, b in zip(ops, rp, rg):
                self.rows.append((case, exp, v_engine(op, a), canon(a) == canon(b)))


def S_env():
    e = dict(os.environ)
    e["PATH"] = BIN + ":" + e.get("PATH", "")
    return e


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--run", action="append", required=True)
    ap.add_argument("--work", required=True)
    ap.add_argument("--go-dir", default=os.path.dirname(HERE))
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--jobs", type=int, default=3, help="installers checked at once")
    a = ap.parse_args()
    os.makedirs(a.work, exist_ok=True)
    if len(a.run) > 1:   # one process per installer, a few at a time; each prints its own report
        import concurrent.futures
        base = [sys.executable, os.path.abspath(__file__), "--work", a.work, "--go-dir", a.go_dir] + (["--verbose"] if a.verbose else [])
        with concurrent.futures.ThreadPoolExecutor(a.jobs) as ex:
            res = list(ex.map(lambda r: subprocess.run(base + ["--run", r], capture_output=True, text=True), a.run))
        tot = bad = 0
        for r in res:
            sys.stdout.write(r.stdout + r.stderr)
            m = re.search(r"^(\d+) cases, (\d+) unexpected$", r.stdout, re.M)
            if not m or r.returncode not in (0, 1):
                bad += 1
            else:
                tot += int(m.group(1)); bad += int(m.group(2))
        print("\nall installers: %d cases, %d unexpected" % (tot, bad))
        sys.exit(1 if bad else 0)
    total = bad = 0
    for run in a.run:
        t0 = time.time()
        P = Pkg(run, a.work)
        X = Suite(P, a)
        T = {}
        def timed(name, f, *x):
            t1 = time.time(); f(*x); T[name] = T.get(name, 0) + time.time() - t1
        if P.has("lossless.sh"):
            timed('lossless', X.lossless)
        if P.has("nccl.sh"):
            timed("nccl", X.nccl, "nccl.sh")
        if P.has("nccl_nvlink.sh"):
            timed("nccl", X.nccl, "nccl_nvlink.sh")
        if os.path.exists(os.path.join(P.root, "provision", "rail_check.sh")) and X.S.rails:
            timed('rails', X.rails)
        if P.has("soak.sh"):
            timed('soak', X.soak)
        if P.has("storage_lossless.sh"):
            timed('storage', X.storage)
        if P.has("failover.sh"):
            timed('failover', X.failover)
        if P.has("alerts.sh"):
            timed('alerts', X.alerts)
        if P.has("inference_load.sh"):
            timed('inference', X.inference)
        if P.has("ingress_isolation.sh"):
            timed('isolation', X.isolation)
        if P.cable_rows():
            timed('cabling', X.cabling)
        timed('software', X.software)
        timed('score', X.score)
        n = len(X.rows)
        fails = [(c, e, g, s) for c, e, g, s in X.rows if not expect_ok(e, g) or s is False]
        total += n; bad += len(fails)
        print("== %s: %d cases, %d unexpected (%.0f s: %s)" % (P.name, n, len(fails), time.time() - t0, ", ".join("%s %.0f" % kv for kv in T.items())))
        for c, e, g, s in (X.rows if a.verbose else fails):
            print("  %-4s %s\n       expected %s, got %s%s" % ("ok" if (c, e, g, s) not in fails else "FAIL", c, e if e == "pass" else "%s /%s/" % e,
                  g[0], (": " + g[1][:200]) if g[1] else "") + ("" if s is not False else "   ENGINES DIFFER"))
    print("\n%d cases, %d unexpected" % (total, bad))
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
