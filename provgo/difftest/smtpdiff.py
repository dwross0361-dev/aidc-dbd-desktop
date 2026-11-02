#!/usr/bin/env python3
"""Alert mail with STARTTLS and AUTH: the Python service (smtplib) and aidc-prov send the same alerts to a fake SMTP
server that offers STARTTLS (certificate from a test CA, trusted through SSL_CERT_FILE) and AUTH CRAM-MD5 / PLAIN / LOGIN.

usage: smtpdiff.py --py estate_build.py --console console.html --go aidc-prov --work DIR

Scenarios (one alert each, the server reconfigured between them): STARTTLS + AUTH with all three mechanisms offered
(CRAM-MD5 chosen), PLAIN and LOGIN offered, LOGIN only, a wrong password, an untrusted certificate, no STARTTLS offered
while smtp_starttls is set, AUTH PLAIN without TLS. Compared per scenario: TLS in force at AUTH, the mechanism, the
login and whether the password checked out, the envelope and the message bytes (or no delivery).
"""
import argparse, base64, hashlib, hmac, json, os, re, socket, ssl, subprocess, sys, threading, time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import difftest as D

USER, PW = "alerts@example.net", "smtp-test-pass"


class FakeSMTP(threading.Thread):
    daemon = True

    def __init__(self, port, ctx_good, ctx_bad):
        threading.Thread.__init__(self)
        self.port, self.ctx_good, self.ctx_bad = port, ctx_good, ctx_bad
        self.mode = {}
        self.sessions = []
        self.s = socket.socket()
        self.s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.s.bind(("127.0.0.1", port))
        self.s.listen(8)

    def run(self):
        while True:
            c, _ = self.s.accept()
            threading.Thread(target=self.session, args=(c,), daemon=True).start()

    def session(self, c):
        m = dict(self.mode)
        rec = {"tls_at_auth": None, "mech": None, "user": None, "pw_ok": None, "from": None, "rcpt": [], "data": None, "cmds": []}
        self.sessions.append(rec)
        f = c.makefile("rb")
        tls = False

        def send(t):
            c.sendall((t + "\r\n").encode())

        def line():
            ln = f.readline()
            if not ln:
                raise EOFError
            return ln.decode().rstrip("\r\n")

        try:
            c.settimeout(20)
            send("220 fake.example.net ESMTP")
            while True:
                ln = line()
                verb = ln.split(" ", 1)[0].upper()
                rec["cmds"].append(verb if verb not in ("AUTH",) else "AUTH " + ln.split(" ")[1].upper())
                if verb in ("EHLO", "HELO"):
                    ext = ["SIZE 10240000", "8BITMIME"]
                    if m.get("starttls") and not tls:
                        ext.append("STARTTLS")
                    if m.get("auth") and (tls or m.get("auth_plain_text")):
                        ext.append("AUTH " + m["auth"])
                    send("250-fake.example.net\r\n" + "\r\n".join("250-" + x for x in ext[:-1]) + "\r\n250 " + ext[-1])
                elif verb == "STARTTLS":
                    send("220 2.0.0 Ready to start TLS")
                    ctx = self.ctx_bad if m.get("bad_cert") else self.ctx_good
                    try:
                        c = ctx.wrap_socket(c, server_side=True)
                    except (ssl.SSLError, OSError) as e:
                        rec["tls_error"] = "handshake failed"
                        return
                    f = c.makefile("rb")
                    tls = True
                elif verb == "AUTH":
                    parts = ln.split(" ")
                    mech = parts[1].upper()
                    rec["tls_at_auth"], rec["mech"] = tls, mech
                    if mech == "CRAM-MD5":
                        ch = b"<12345.67890@fake.example.net>"
                        send("334 " + base64.b64encode(ch).decode())
                        u, _, dig = base64.b64decode(line()).decode().rpartition(" ")
                        rec["user"], rec["pw_ok"] = u, hmac.new(m.get("pw", PW).encode(), ch, hashlib.md5).hexdigest() == dig
                    elif mech == "PLAIN":
                        resp = parts[2] if len(parts) > 2 else (send("334 ") or line())
                        _, u, p = base64.b64decode(resp).decode().split("\0")
                        rec["user"], rec["pw_ok"] = u, p == m.get("pw", PW)
                    elif mech == "LOGIN":
                        if len(parts) > 2:
                            u = base64.b64decode(parts[2]).decode()
                        else:
                            send("334 VXNlcm5hbWU6")
                            u = base64.b64decode(line()).decode()
                        send("334 UGFzc3dvcmQ6")
                        p = base64.b64decode(line()).decode()
                        rec["user"], rec["pw_ok"] = u, p == m.get("pw", PW)
                    send("235 2.7.0 Authentication successful" if rec["pw_ok"] else "535 5.7.8 Authentication credentials invalid")
                elif verb == "MAIL":
                    rec["from"] = re.sub(r"(?i)^from:", "FROM:", ln[5:])   # smtplib writes "mail from:"; SMTP keywords are case-blind
                    send("250 2.1.0 Ok")
                elif verb == "RCPT":
                    rec["rcpt"].append(re.sub(r"(?i)^to:", "TO:", ln[5:]))
                    send("250 2.1.5 Ok")
                elif verb == "DATA":
                    send("354 End data with <CR><LF>.<CR><LF>")
                    buf = []
                    while True:
                        x = f.readline()
                        if x in (b".\r\n", b".\n", b""):
                            break
                        buf.append(x)
                    rec["data"] = b"".join(buf).decode(errors="replace")
                    send("250 2.0.0 Ok: queued")
                elif verb == "QUIT":
                    send("221 2.0.0 Bye")
                    return
                elif verb == "RSET":
                    send("250 Ok")
                else:
                    send("502 5.5.2 Error: command not recognized")
        except (EOFError, OSError, ValueError, UnicodeDecodeError):
            pass
        finally:
            try:
                c.close()
            except OSError:
                pass


def mk_ca(d):
    os.makedirs(d, exist_ok=True)
    run = lambda *a: subprocess.run(a, capture_output=True, check=True, cwd=d)
    # extensions as a real CA issues them (Python 3.13's default context verifies with X509 strict: the leaf needs its
    # authority key identifier, the CA its key usage)
    run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30", "-subj", "/CN=Test Alert CA", "-keyout", "ca.key", "-out", "ca.pem",
        "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign")
    open(os.path.join(d, "san.cnf"), "w").write("subjectAltName=IP:127.0.0.1,DNS:localhost\nbasicConstraints=CA:FALSE\n"
                                                 "keyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n"
                                                 "authorityKeyIdentifier=keyid\nsubjectKeyIdentifier=hash\n")
    for n in ("good", "bad"):
        run("openssl", "req", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=127.0.0.1", "-keyout", n + ".key", "-out", n + ".csr")
        if n == "good":
            run("openssl", "x509", "-req", "-in", "good.csr", "-CA", "ca.pem", "-CAkey", "ca.key", "-CAcreateserial", "-days", "30", "-extfile", "san.cnf", "-out", "good.pem")
        else:   # self-signed: not trusted
            run("openssl", "x509", "-req", "-in", "bad.csr", "-signkey", "bad.key", "-days", "30", "-extfile", "san.cnf", "-out", "bad.pem")
    ctx = {}
    for n in ("good", "bad"):
        x = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        x.load_cert_chain(os.path.join(d, n + ".pem"), os.path.join(d, n + ".key"))
        ctx[n] = x
    return os.path.join(d, "ca.pem"), ctx


SCENARIOS = [
    ("STARTTLS, AUTH CRAM-MD5 PLAIN LOGIN offered", {"starttls": True, "auth": "CRAM-MD5 PLAIN LOGIN"}),
    ("STARTTLS, AUTH PLAIN LOGIN offered", {"starttls": True, "auth": "PLAIN LOGIN"}),
    ("STARTTLS, AUTH LOGIN only", {"starttls": True, "auth": "LOGIN"}),
    ("STARTTLS, wrong password", {"starttls": True, "auth": "PLAIN LOGIN", "pw": "not-it"}),
    ("STARTTLS, untrusted certificate", {"starttls": True, "auth": "PLAIN", "bad_cert": True}),
    ("no STARTTLS offered (smtp_starttls set)", {"starttls": False, "auth": "PLAIN", "auth_plain_text": True}),
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--py", required=True)
    ap.add_argument("--console", required=True)
    ap.add_argument("--go", required=True)
    ap.add_argument("--work", required=True)
    a = ap.parse_args()
    W = a.work
    import shutil
    shutil.rmtree(W, ignore_errors=True)
    os.makedirs(os.path.join(W, "web"))
    ca, ctx = mk_ca(os.path.join(W, "ca"))
    os.environ["SSL_CERT_FILE"] = ca   # both engines: the test CA is the only trusted root
    plan = {"schema": 1, "site": {"code": "SMTPT", "prov_ip": "127.0.0.1", "services": {}}, "dhcp": {"subnet_id": 50},
            "builds": [{"key": "oob", "num": 1, "name": "OOB", "waves": [{"name": "w1", "devices": ["T-SW01"]}], "gate": []}],
            "devices": [{"name": "T-SW01", "short": "SW01", "os": "cumulus", "ip": "127.30.0.1", "role": "leaf", "dhcp": {}}]}
    tmpl = {"Dhcp4": {"control-socket": {"socket-type": "unix", "socket-name": "x"}, "subnet4": []}}
    rep = D.Report()
    srv, eng = {}, []
    for tag, base, sport in (("python", 18780, 18799), ("go", 28780, 28799)):
        srv[tag] = FakeSMTP(sport, ctx["good"], ctx["bad"])
        srv[tag].start()
        argv = [sys.executable, "-I", a.py] if tag == "python" else [a.go]
        E = D.Engine(tag, argv, os.path.join(W, tag[:2]), (base, base + 3, base + 1), plan, tmpl, os.path.join(W, "web"),
                     {"smtp_host": "127.0.0.1", "smtp_port": sport, "smtp_to": "noc@example.net, Ops Team <ops@example.net>",
                      "smtp_from": "estate@example.net", "smtp_starttls": True, "smtp_user": USER, "smtp_password": PW,
                      "probe_every_s": 1e6, "check_every_s": 1e9, "backup_every_h": 1000})
        eng.append(E)
    try:
        for E in eng:
            E.start(a.console if E.name == "python" else None)
        time.sleep(3)
        for i, (what, mode) in enumerate(SCENARIOS):
            for E in eng:
                srv[E.name].mode = mode
                srv[E.name].sessions = []
                # an unknown MAC calls in: a warn event, queued as an alert
                body = ("LEASES4_SIZE=1\nLEASES4_AT0_ADDRESS=10.99.0.%d\nLEASES4_AT0_HWADDR=02:5a:00:00:00:%02x\nLEASES4_AT0_CLIENT_ID=\n"
                        "QUERY4_GIADDR=0.0.0.0\n" % (i + 1, i + 1)).encode()
                E.internal_post("/hook/lease?event=leases4_committed", body, D.TOKEN)
            t0 = time.time()
            while time.time() - t0 < 25 and not all(srv[E.name].sessions and (srv[E.name].sessions[-1]["data"] is not None or
                                                                                 srv[E.name].sessions[-1].get("tls_error") or
                                                                                 srv[E.name].sessions[-1]["pw_ok"] is False or
                                                                                 "QUIT" in srv[E.name].sessions[-1]["cmds"]) for E in eng):
                time.sleep(0.3)
            time.sleep(1.5)
            view = {}
            for E in eng:
                ss = srv[E.name].sessions
                view[E.name] = [{k: v for k, v in x.items() if k != "cmds" or True} for x in ss]
            # Python ends a refused session with QUIT (smtplib's context manager); the commands up to the failure must match
            strip = lambda L: [dict(x, cmds=[c for c in x["cmds"] if c != "QUIT"]) for x in L]
            rep.same("smtp: " + what, strip(view["python"]), strip(view["go"]))
            print("  %-45s python: %s | go: %s" % (what, summary(view["python"]), summary(view["go"])))
    finally:
        for E in eng:
            E.stop()
    print("\nsmtp: %d comparisons, %d mismatch(es)%s" % (rep.n, len(rep.bad), (": " + "; ".join(rep.bad)) if rep.bad else ""))
    return 1 if rep.bad else 0


def summary(L):
    if not L:
        return "no connection"
    x = L[-1]
    if x.get("tls_error"):
        return "TLS refused by the client"
    return "tls=%s mech=%s pw_ok=%s delivered=%s" % (x["tls_at_auth"], x["mech"], x["pw_ok"], x["data"] is not None)


if __name__ == "__main__":
    sys.exit(main())
