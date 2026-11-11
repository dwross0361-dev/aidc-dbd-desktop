#!/usr/bin/env python3
"""Scores acctest jobs with the Python engine (the Go engine scores the same jobs in internal/estate/acctest_test.go).

usage: python3 -I pyjob.py estate_build.py JOBS.json OUT.json
"""
import importlib.util, json, sys

PY, JOBS, OUT = sys.argv[1:4]
spec = importlib.util.spec_from_file_location("eb", PY)
eb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(eb)
USER = "acctest"


def op(est, o):
    try:
        k = o["op"]
        if k == "check":
            try:
                ok, note, _ = est.eval_kind(o["kind"], o["dev"], {})
                return {"ok": ok, "note": note}
            except eb.ReaderUnavailable as e:
                return {"unavailable": str(e)}
        if k in ("accept", "isolation", "inference"):
            est.results = {}
            if k == "accept":
                r = est.submit_accept_text(o["text"], USER, o.get("crit"))
                return {"submit": list(r), "eval": est.accept_eval()}
            if k == "isolation":
                r = est.submit_isolation_text(o["text"], USER)
                return {"submit": list(r), "eval": est.isolation_eval()}
            r = est.submit_inference_text(o["text"], USER)
            return {"submit": list(r), "eval": est.inference_eval()}
        if k == "rz":
            kind = o["kind"]
            if o.get("plan"):
                est.plan.setdefault("resilience", {}).setdefault(kind, {}).update(o["plan"])
            est.results.pop(kind, None)
            if "target" in o:
                est.conf["TARGET_BUSBW"] = o["target"]
            if kind == "failover":
                r = est.submit_failover_text(o["text"], USER)
            elif kind == "soak":
                r = est.submit_soak_text(o["text"], USER, o.get("switch"))
            else:
                r = est.submit_alerts_text(o["text"], USER)
            return {"submit": list(r), "eval": [est.gate_kind_eval(kind, p) for p in (o.get("planes") or [None])]}
        return {"exc": "unknown op " + json.dumps(k)}
    except Exception as e:   # recorded, so the two engines are compared on it too
        return {"exc": str(e)}


out = []
for j in json.load(open(JOBS)):
    est = eb.Estate(j["plan"], j["conf"], j["kea"])
    for n, st in (j.get("state") or {}).items():
        if n in est.st:
            est.st[n].update(st)
    out.append([op(est, o) for o in j["ops"]])
json.dump(out, open(OUT, "w"), indent=1, sort_keys=True)
