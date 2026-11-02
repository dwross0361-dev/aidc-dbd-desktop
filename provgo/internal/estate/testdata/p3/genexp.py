#!/usr/bin/env python3
"""Expected values for p3_test.go, computed by Python's own functions (email.message + smtplib's flattening, shlex.quote,
json.dumps canon form). usage: genexp.py > expected.json"""
import json, shlex, sys
from email.message import EmailMessage
from email.generator import BytesGenerator
import io

subjects = [
    "[ACME] WARN ACME-ACME-OOB-FW01: OOB-FW01: not called in after 45 min -- waiting (slow)",
    "[ACME] FAIL ACME-ACME-OOB-MCS02: OOB-MCS02: failed at Config -- push exited 3: ERROR: commit failed on ACME-ACME-OOB-MCS02: x",
    "[ACME] FAIL DPU image install (bfb-install over each host’s rshim) failed: 287 of 288 DPU images installed (each DPU counts once it answers on its OOB port)",
    "[ACME] INFO Gate passed: Out-of-band",
    "[ACME] WARN " + "x" * 100,
    "[ACME] WARN café crème brûlée naïve — “quoted” words here and there to fold the header across lines",
    "[ACME] FAIL Refused the standby at https://10.0.0.2:8443: its certificate (SHA-256 " + "ab" * 32 + ") is not the pinned one",
    "short",
    "[ACME] WARN a b",
    "[ACME] WARN exactly-long-word-" + "y" * 60 + " tail",
]
alerts = [
    {"ts": "2026-11-01T00:00:00Z", "level": "warn", "device": "ACME-ACME-OOB-FW01", "text": "OOB-FW01: not called in after 45 min -- waiting (slow)", "site": "ACME"},
    {"ts": "2026-11-01T00:00:00Z", "level": "fail", "device": "", "text": "x" * 200, "site": "ACME"},
    {"ts": "2026-11-01T00:00:00Z", "level": "info", "device": "", "text": "Gate passed: Out-of-band", "site": "ACME"},
    {"ts": "2026-11-01T00:00:00Z", "level": "warn", "device": "", "text": "has = signs == and trailing space ", "site": "ACME"},
    {"ts": "2026-11-01T00:00:00Z", "level": "warn", "device": "", "text": "é’ non-ascii " + "z" * 80, "site": "ACME"},
]
msgs = []
for s in subjects:
    for a in alerts:
        m = EmailMessage()
        m["Subject"] = s[:120]
        m["From"] = "estate@example.net"
        m["To"] = "noc@example.net, ops@example.net"
        m.set_content(json.dumps(a, indent=2))
        b = io.BytesIO()
        BytesGenerator(b, policy=m.policy.clone(linesep="\r\n")).flatten(m)
        msgs.append({"subject": s, "alert": a, "msg": b.getvalue().decode()})
quotes = ["", "abc", "a b", "it's", "100TB (Acme's 'tier 1')", "/dev/nvme0n1 /dev/nvme1n1", "x=y,z:1.2/3-4@%+", "$HOME", "café"]
canon = [{"b": 1, "a": [1.5, "xé\n\"q\""], "c": {"z": None, "y": True}}]
json.dump({"messages": msgs, "quote": {q: shlex.quote(q) for q in quotes},
           "canon": [[c, json.dumps(c, sort_keys=True, separators=(",", ":"), ensure_ascii=False)] for c in canon]}, sys.stdout, indent=1)
