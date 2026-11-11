#!/usr/bin/env python3
"""Expected values for the Go Phase 2 unit tests, computed by the Python service's own functions.

usage: python3 -I genexp.py estate_build.py FIXTURES_DIR OUT.json

Loads estate_build.py as a module (nothing runs at import), then: every parser on every fixture output; every check
kind on every fixture device (reader_sim_dir = FIXTURES_DIR/sim); a fixed list of scenarios for the counters, the
acceptance scoring, isolation, tuning and the difference view. The Go test replays the same list (cases.json).
"""
import copy, importlib.util, json, os, shutil, sys, tempfile, time

PY, FX, OUT = sys.argv[1], sys.argv[2], sys.argv[3]
spec = importlib.util.spec_from_file_location("eb", PY)
eb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(eb)
CASES = json.load(open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "cases.json")))


def flat_pairs(f):
    return [[k, v] for k, v in f.items()]


def counts(c):
    return {"pfc_tx": c["pfc_tx"], "pfc_rx": c["pfc_rx"], "ecn": c["ecn"], "drops": c["drops"], "wd": c["wd"], "seen": sorted(c["seen"])}


def lldp(t):
    r = eb.lldp_pairs(t)
    return None if r is None else [[k, list(v)] for k, v in r.items()]


out = {"parsers": {}, "checks": [], "scenarios": {}}
# ---- parsers on every fixture output
for dev in sorted(os.listdir(os.path.join(FX, "sim"))):
    for fn in sorted(os.listdir(os.path.join(FX, "sim", dev))):
        t = open(os.path.join(FX, "sim", dev, fn)).read()
        key = dev + "/" + fn
        out["parsers"][key] = {
            "bgp_sessions": [list(x) for x in eb.bgp_sessions([t])],
            "text_bgp": [list(x) for x in eb.text_bgp(t)],
            "parse_bgp": list(eb.parse_bgp([t])),
            "lldp_pairs": lldp(t),
            "acc_flat_text": flat_pairs(eb.acc_flat_text(t)),
            "acc_counts": counts(eb.acc_counts(eb.acc_flat_text(t))),
            "cfg_norm_cumulus": sorted(eb.cfg_norm(t, "cumulus")),
            "cfg_norm_other": sorted(eb.cfg_norm(t, "junos")),
            "vnum": eb.vnum(t[:200]),
        }
# ---- standalone parser cases
P = CASES["parsers"]
out["scenarios"]["nv_expand"] = [eb.nv_expand(x) for x in P["nv_expand"]]
out["scenarios"]["cfg_norm"] = [sorted(eb.cfg_norm(t, o)) for t, o in P["cfg_norm"]]
out["scenarios"]["same_dev"] = [eb.same_dev(*x) for x in P["same_dev"]]
out["scenarios"]["vnum"] = [eb.vnum(x) for x in P["vnum"]]
out["scenarios"]["lldp_text"] = [lldp(x) for x in P["lldp_text"]]
out["scenarios"]["bgp_text"] = [[list(y) for y in eb.bgp_sessions(x)] for x in P["bgp_texts"]]
out["scenarios"]["acc_flat_text"] = [flat_pairs(eb.acc_flat_text(x)) for x in P["acc_flat_text"]]
out["scenarios"]["acc_parse"] = [[eb.acc_parse(t, test)[0], eb.acc_parse(t, test)[1]] for t, test in P["acc_parse"]]
out["scenarios"]["slug"] = [eb.slug(x) for x in P["slug"]]

# ---- an estate on the fixture plan
W = tempfile.mkdtemp()
plan = json.load(open(os.path.join(FX, "plan.json")))
json.dump(plan, open(os.path.join(W, "plan.json"), "w"))
json.dump({"Dhcp4": {"subnet4": [{"id": 1, "subnet": "127.0.0.0/8"}]}}, open(os.path.join(W, "kea.json"), "w"))
open(os.path.join(W, "secrets.env"), "w").write("TARGET_BUSBW=380\n")
json.dump({"state_dir": os.path.join(W, "st"), "secrets": os.path.join(W, "secrets.env"), "web_root": os.path.join(W, "www"),
           "bundle_dir": os.path.join(FX, "bundle"), "reader_sim_dir": os.path.join(W, "sim")}, open(os.path.join(W, "conf.json"), "w"))
shutil.copytree(os.path.join(FX, "sim"), os.path.join(W, "sim"))
est = eb.Estate(os.path.join(W, "plan.json"), os.path.join(W, "conf.json"), os.path.join(W, "kea.json"))
for d in plan["devices"]:
    est.st[d["name"]].update(d["_state"])


def ev(kind, n, c):
    try:
        ok, note, text = est.eval_kind(kind, n, c)
        return {"ok": ok, "note": note, "text": text}
    except eb.ReaderUnavailable as e:
        return {"unavailable": str(e)}
    except Exception as e:
        return {"exc": str(e)}


for step in CASES["checks"]:
    if step.get("write"):
        for rel, text in step["write"].items():
            p = os.path.join(W, "sim", rel)
            os.makedirs(os.path.dirname(p), exist_ok=True)
            open(p, "w").write(text)
        out["checks"].append(None)
        continue
    c = step.get("c", {})
    if step["kind"] == "mnha" and "devices" not in c:
        c = dict(c, devices=[step["dev"]])
    out["checks"].append(ev(step["kind"], step["dev"], c))

# counters (for the acceptance runner)
S = out["scenarios"]
S["acc_counts_of"] = {}
for n in CASES["counters"]:
    try:
        S["acc_counts_of"][n] = counts(est._acc_counts_of(n))
    except Exception as e:
        S["acc_counts_of"][n] = {"err": str(e)}


def snap(spec):
    if spec is None:
        return None
    o = {}
    for n, v in spec.items():
        if isinstance(v, str):
            o[n] = (None, v)
        else:
            c = dict(v)
            c["seen"] = set(c.get("seen", []))
            for k in ("pfc_tx", "pfc_rx", "ecn", "drops", "wd"):
                c.setdefault(k, 0.0)
            o[n] = (c, None)
    return o


S["acc_score"] = []
for sc in CASES["acc_score"]:
    est.conf.pop("TARGET_BUSBW", None)
    if sc.get("conf_target") is not None:
        est.conf["TARGET_BUSBW"] = sc["conf_target"]
    r = est._acc_score(sc["test"], sc["text"], snap(sc.get("s0")), snap(sc.get("s1")), sc.get("crit", {}), sc.get("rc", 0))
    S["acc_score"].append(r)
est.conf.pop("TARGET_BUSBW", None)

# results: accept / isolation / nccl, and their evaluation
S["results"] = []
for sc in CASES["results"]:
    est.results = {}
    if sc["kind"] == "accept":
        r = est.submit_accept_text(sc["text"], "op1", sc.get("crit"))
        S["results"].append({"submit": list(r), "stored": est.results.get("accept", {}).get("tests"), "eval": est.accept_eval()})
    elif sc["kind"] == "isolation":
        if sc.get("tenants") is not None:
            est.plan["isolation"] = {"tenants": [{"tenant": t} for t in sc["tenants"]]}
        else:
            est.plan.pop("isolation", None)
        r = est.submit_isolation_text(sc["text"], "op1")
        S["results"].append({"submit": list(r), "stored": est.results.get("isolation", {}).get("tests"), "eval": est.isolation_eval()})
    elif sc["kind"] == "inference":   # HS v1.98
        if sc.get("tenants") is not None:
            est.plan["inference"] = {"tool": "hey", "tenants": [{"tenant": t, "rps": 10, "p99_ms": p} for t, p in sc["tenants"]]}
        else:
            est.plan.pop("inference", None)
        r = est.submit_inference_text(sc["text"], "op1")
        S["results"].append({"submit": list(r), "stored": est.results.get("inference", {}).get("tests"), "eval": est.inference_eval()})

# tuned_eval over tuning states, and the lossless check against an active overlay
S["tuned"] = []
for sc in CASES["tuned"]:
    est.kv_set("tuning", sc["state"])
    est.plan["tuning"] = sc.get("plan", plan["tuning"])
    S["tuned"].append(est.tuned_eval())
est.plan["tuning"] = plan["tuning"]
S["swap"] = []
for sc in CASES["swap"]:
    est.kv_set("tuning", sc["state"])
    S["swap"].append(sorted(est.tuning_swap(sc["dev"], set(sc["planned"]), "cumulus")))
    S["swap"].append(ev("lossless", sc["dev"], {}))
est.kv_set("tuning", {})

# the difference view on stored backups
S["diff"] = []
for sc in CASES["diff"]:
    n = sc["dev"]
    d = est.bdir(n)
    for f in os.listdir(d):
        os.remove(os.path.join(d, f))
    for name, text in sc["backups"]:
        open(os.path.join(d, name), "w").write(text)
    S["diff"].append([est.diff(n), est.diff(n, vs="prev")])

# HS v2.02: the reports, phase by phase ("generated" masked; the certificate rests on a post report with it fixed)
def _mask(v):
    if isinstance(v, dict):
        return {k: ("<T>" if k == "generated" else _mask(x)) for k, x in v.items()}
    if isinstance(v, list):
        return [_mask(x) for x in v]
    return v
RC = CASES["reports"]
est.plan["site"].update(RC["site"])
est.plan["standards"] = RC["standards"]
est.plan["isolation"] = {"tenants": RC["isolation"]}
est.build_by_key["be"]["gate"] = RC["gate"]
est.results = copy.deepcopy(RC["results"])
S["reports"] = []
for ph in RC["phases"]:
    for k, v in ph["bstate"].items():
        if v is None:
            est.bstate["be"].pop(k, None)
        else:
            est.bstate["be"][k] = v
    for n, v in ph["st"].items():
        est.st[n].update(copy.deepcopy(v))
    for n, v in ph["ident"].items():
        est.ident[n] = dict(v)
    for k, c, u, note in ph["gate_res"]:
        est.gate_res[(k, c)] = {"build": k, "chk": c, "result": "confirmed", "user": u, "note": note}
    R = {"ready": est.reports_ready()}
    for kind in ("progress", "final"):
        R[kind] = _mask(est.report(kind, "admin"))
        R[kind + " csv"] = est.report_csv(est.report(kind, "admin"))
    P = est.report("post", "admin")
    P["generated"] = P["final_build"]["generated"] = "2026-11-11T00:00:00Z"
    C = est.report_cert_for(P, "admin")
    R["post"], R["post csv"], R["post canon"] = P, est.report_csv(P), est.canon(P)
    R["cert"], R["cert csv"] = _mask(C), est.report_csv(C)
    S["reports"].append(R)

json.dump(out, open(OUT, "w"), indent=1, sort_keys=True)
print("expected values: %d parser outputs, %d check steps" % (len(out["parsers"]), len(out["checks"])))
