#!/usr/bin/env python3
"""Fake network devices for the Phase 2 differential test: real SSH servers (paramiko) on 127.0.0.0/8 addresses, each
with its OS's prompt and session behavior, answering show commands from files, plus plain TCP listeners on the other
management ports (830, 443) and on aggregate members (DPUs).

usage: fakedev.py DEVICES.json SIMROOT

DEVICES.json: [{"name", "os", "ip", "ports": [22, 830, ...]}, ...]
SIMROOT/<name>/<slug(command)>.txt   the output of a command (read at command time: the test edits them live)
SIMROOT/<name>/_down                 present: the device does not answer at all (its listeners are closed)
SIMROOT/_auth.json                   {"user": U, "passwords": {"<os>|*": P}}: the login each OS accepts
"""
import json, os, re, socket, sys, threading, time
import logging, paramiko
logging.getLogger("paramiko").setLevel(logging.CRITICAL + 10)

DEVS = json.load(open(sys.argv[1]))
SIM = sys.argv[2]
KEY = paramiko.ECDSAKey.generate(bits=256)
PROMPT = {
    "junos": "ro@{h}> ", "eos": "{h}>", "nxos": "{h}# ", "cumulus": "ro@{h}:mgmt:~$ ", "iosxr": "RP/0/RP0/CPU0:{h}#",
    "sros": "\n[/]\nA:ro@{h}# ", "srl": "\n--{{ running }}--[  ]--\nA:{h}# ", "ftd": "> ", "iosxe": "{h}#",
}
PREP = {"set cli screen-width 511": "Screen width set to 511", "set cli screen-length 0": "Screen length set to 0",
        "set cli complete-on-space off": "Disabling complete-on-space"}
STATS = {"sessions": 0, "commands": 0}
LOCK = threading.Lock()


def slug(c):
    return re.sub(r"[^A-Za-z0-9]+", "_", c).strip("_")[:90]


def auth():
    try:
        return json.load(open(os.path.join(SIM, "_auth.json")))
    except (OSError, ValueError):
        return {"user": "ro", "passwords": {"*": "readonly"}}


class Srv(paramiko.ServerInterface):
    def __init__(self, os_):
        self.os = os_

    def check_auth_password(self, u, p):
        a = auth()
        want = a["passwords"].get(self.os, a["passwords"].get("*"))
        return paramiko.AUTH_SUCCESSFUL if (u, p) == (a["user"], want) else paramiko.AUTH_FAILED

    def get_allowed_auths(self, u):
        return "password"

    def check_channel_request(self, kind, cid):
        return paramiko.OPEN_SUCCEEDED

    def check_channel_shell_request(self, ch):
        return True

    def check_channel_pty_request(self, *a):
        return True


def answer(d, line):
    os_ = d["os"]
    if line in PREP and os_ == "junos":
        return PREP[line]
    if os_ == "eos" and line == "terminal width 511":
        return "Width set to 511 columns."
    if os_ == "eos" and line == "terminal length 0":
        return "Pagination disabled."
    if not line or re.match(r"^(terminal|set cli|environment|//environment|screen-length|no page|cli|enable|show clock)", line):
        return ""
    p = os.path.join(SIM, d["name"], slug(line) + ".txt")
    try:
        with open(p) as f:
            t = f.read()
        # Phase 4 writer test: a committed removal of the DWR account shows in the configuration read back, unless the
        # device keeps it (_keep)
        with LOCK:
            gone = any("dwr-netadmin" in x for x in COMMITTED.get(d["name"], []))
        if gone and not os.path.exists(os.path.join(SIM, d["name"], "_keep")):
            t = "".join(l for l in t.splitlines(True) if "dwr-netadmin" not in l)
        return t
    except OSError:
        return {"junos": "                  ^\nsyntax error, expecting <command>.", "eos": "% Invalid input", "nxos": "% Invalid command at '^' marker.",
                "cumulus": "-bash: %s: command not found" % line.split()[0], "srl": "Error: Unknown token",
                "sros": "MINOR: CLI Command not allowed for this user.", "iosxr": "% Invalid input detected at '^' marker.",
                "iosxe": "% Invalid input detected at '^' marker.", "ftd": "ERROR: % Invalid input detected at '^' marker."}.get(os_, "error")


CFG_PROMPT = {"junos": "\n[edit]\nro@{h}# ", "eos": "{h}(config)#", "nxos": "{h}(config)# ", "iosxe": "{h}(config)#",
              "iosxr": "RP/0/RP0/CPU0:{h}(config)#"}
COMMITTED = {}   # device -> configuration lines in effect (the writer test's account removal reads it back)
XR_FAIL = ("% Failed to commit one or more configuration items during a pseudo-atomic operation. All changes made have been "
           "reverted. Please issue 'show configuration failed [inheritance]' from this session to view the errors")


def applied(d, line):
    with LOCK:
        open(os.path.join(SIM, d["name"], "_written.txt"), "a").write(line + "\n")


def commit(d, st):
    with LOCK:
        COMMITTED.setdefault(d["name"], []).extend(st.get("pending", []))
    st["pending"] = []


def pend(d, st, line):
    applied(d, line)
    st.setdefault("pending", []).append(line)


def flag(d, f):
    return os.path.exists(os.path.join(SIM, d["name"], f))


def config_answer(d, line, st):
    """Configuration mode (Phase 3 writer test): (output, prompt) or None for an ordinary command."""
    os_, h = d["os"], d["name"]
    base = d.get("prompt", PROMPT[os_]).format(h=h)
    bad = "BADLINE" in line
    if os_ == "cumulus" and (line.startswith("nv set ") or line.startswith("nv unset ") or line == "nv config apply -y"):
        if line == "nv config apply -y":
            if os.path.exists(os.path.join(SIM, h, "_badapply")):
                return "Error: invalid configuration (BADLINE)", base
            commit(d, st)
            return "applied [rev_id: 7]", base
        if bad:
            return "Error: invalid argument", base
        pend(d, st, line)
        return "", base
    if os_ == "junos" and st["mode"] == "confirm-exit":
        st["mode"], st["pending"] = "", []
        return ("Exiting configuration mode" if line in ("yes", "") else "Exiting configuration mode"), base
    if os_ == "junos":
        cp = CFG_PROMPT["junos"].format(h=h)
        if line == "configure":
            st["mode"] = "cfg"
            return "Entering configuration mode", cp
        if st["mode"] != "cfg":
            return None
        if line == "":
            return "", cp
        if line.startswith("show "):
            return answer(d, line), cp
        if line.startswith("commit") and flag(d, "_commitfail"):
            return "[edit system login]\n  'user dwr-netadmin'\n    error: commit blocked by a configuration check\nerror: configuration check-out failed", cp
        if line.startswith("commit confirmed"):
            commit(d, st)
            return "commit confirmed will be automatically rolled back in 5 minutes unless confirmed\ncommit complete", cp
        if line == "commit and-quit":
            commit(d, st)
            st["mode"] = ""
            return "commit complete\nExiting configuration mode", base
        if line == "commit":
            commit(d, st)
            return "commit complete", cp
        if line == "exit configuration-mode":
            if st.get("pending"):
                st["mode"] = "confirm-exit"
                return "The configuration has been changed but not committed\nExit with uncommitted changes? [yes,no] (yes) ", ""
            st["mode"] = ""
            return "Exiting configuration mode", base
        if bad:
            return "                  ^\nsyntax error.", cp
        pend(d, st, line)
        return "", cp
    if os_ in ("eos", "nxos", "iosxe"):
        cp = CFG_PROMPT[os_].format(h=h)
        if line == "configure terminal":
            st["mode"] = "cfg"
            return ("Enter configuration commands, one per line.  End with CNTL/Z." if os_ == "iosxe" else ""), cp
        if line == "copy running-config startup-config":
            return "Copy completed successfully.", base.replace(">", "#")
        if os_ == "iosxe" and line == "write mem":
            return "Building configuration...\n[OK]", base
        if st["mode"] != "cfg":
            return None
        if line == "":
            return "", cp
        if line == "end":
            st["mode"] = ""
            return "", base.replace(">", "#")
        if bad:
            return ("% Invalid input detected at '^' marker." if os_ == "iosxe" else "% Invalid input"), cp
        pend(d, st, line)
        commit(d, st)   # live at once
        return "", cp
    if os_ == "iosxr":
        cp = CFG_PROMPT["iosxr"].format(h=h)
        if st["mode"] == "confirm-end":
            st["mode"] = ""
            if line.startswith("y"):
                commit(d, st)
            st["pending"] = []
            return "", base
        if line == "configure terminal":
            st["mode"] = "cfg"
            return "", cp
        if st["mode"] != "cfg":
            return None
        if line == "":
            return "", cp
        if line.startswith("show "):
            return answer(d, line), cp
        if line == "commit":
            if flag(d, "_commitfail"):
                st["pending"] = []
                return XR_FAIL, cp
            commit(d, st)
            return "", cp
        if line == "end":
            if st.get("pending"):
                st["mode"] = "confirm-end"
                return "Uncommitted changes found, commit them before exiting(yes/no/cancel)? [cancel]:", ""
            st["mode"] = ""
            return "", base
        if bad:
            return "                    ^\n% Invalid input detected at '^' marker.", cp
        pend(d, st, line)
        return "", cp
    if os_ == "srl":
        mark = lambda: ("* " if st.get("pending") else "") + ("+ " if st.get("unsaved") else "")
        cand = lambda: "\n--{ %scandidate private private-admin }--[  ]--\nA:%s# " % (mark(), h)
        run = lambda: "\n--{ %srunning }--[  ]--\nA:%s# " % ("+ " if st.get("unsaved") else "", h)
        if line == "enter candidate private":
            st["mode"] = "cfg"
            return "", cand()
        if st["mode"] != "cfg":
            return None
        if line == "":
            return "", cand()
        if line == "enter running":
            st["mode"] = ""
            return "", run()
        if line in ("discard stay", "discard now"):
            st["pending"] = []
            return "", cand()
        if line in ("commit stay", "commit now"):
            if flag(d, "_commitfail"):
                return "Error: Failed to commit: validation of the candidate configuration failed", cand()
            if st.get("pending"):
                st["unsaved"] = True
            commit(d, st)
            return "All changes have been committed. Starting new transaction.", cand()
        if line == "save startup":
            st["unsaved"] = False
            return "/system:\n    Saved current running configuration as initial (startup) configuration '/etc/opt/srlinux/config.json'", cand()
        if line.startswith("info "):
            return answer(d, line), cand()
        if bad:
            return "Error: Parsing error: Unknown token 'BADLINE'. Options are ['/', 'acl', 'interface', 'network-instance', 'qos', 'system']", cand()
        pend(d, st, line)
        return "", cand()
    if os_ == "sros":
        ctx = lambda: "\n%s(ex)[/]\nA:ro@%s# " % ("*" if st.get("pending") else "", h)
        if line == "edit-config exclusive":
            st["mode"] = "cfg"
            return "INFO: CLI #2060: Entering exclusive configuration mode\nINFO: CLI #2061: Uncommitted changes are discarded on configuration mode exit", ctx()
        if line == "/admin save":
            return "Writing configuration to cf3:/config.cfg\nSaving configuration ... OK\nCompleted.", ctx() if st["mode"] == "cfg" else base
        if st["mode"] != "cfg":
            return None
        if line in ("", "exit all"):
            return "", ctx()
        if line == "commit":
            if flag(d, "_commitfail"):
                return "MINOR: MGMT_CORE #224: Commit failed - configuration validation", ctx()
            commit(d, st)
            return "", ctx()
        if line == "discard":
            st["pending"] = []
            return "", ctx()
        if line == "quit-config":
            st["mode"], st["pending"] = "", []
            return "INFO: CLI #2064: Exiting exclusive configuration mode", base
        if line.startswith("admin show") or line.startswith("info"):
            return answer(d, line), ctx()
        if bad:
            return "MINOR: CLI #2069: Operation failed because of a parsing error", ctx()
        pend(d, st, line)
        return "", ctx()
    return None


def session(conn, d):
    # a TCP probe connects and closes: answer it without starting SSH
    try:
        conn.settimeout(15)
        if not conn.recv(1, socket.MSG_PEEK):
            conn.close()
            return
        conn.settimeout(None)
    except OSError:
        conn.close()
        return
    t = paramiko.Transport(conn)
    t.add_server_key(KEY)
    try:
        t.start_server(server=Srv(d["os"]))
    except Exception:
        return
    ch = t.accept(30)
    if ch is None:
        t.close()
        return
    with LOCK:
        STATS["sessions"] += 1
    pr = d.get("prompt", PROMPT[d["os"]]).format(h=d["name"])
    st = {"mode": "", "user": None}
    try:
        st["user"] = t.get_username()
    except Exception:
        pass
    try:
        ch.sendall(("Last login: Fri Oct 31 21:00:00 2026 from 10.0.0.1\r\n").encode() + pr.encode())
        buf = b""
        while True:
            data = ch.recv(4096)
            if not data:
                break
            buf += data
            while b"\n" in buf or b"\r" in buf:
                i = min([x for x in (buf.find(b"\n"), buf.find(b"\r")) if x >= 0])
                line, buf = buf[:i].decode(errors="replace").strip(), buf[i + 1:].lstrip(b"\r\n")
                if line in ("exit", "quit", "logout"):
                    ch.close()
                    return
                cfg = config_answer(d, line, st)
                if cfg is not None:
                    out, pr = cfg
                else:
                    out = answer(d, line)
                with LOCK:
                    STATS["commands"] += 1
                body = out.replace("\r\n", "\n").replace("\n", "\r\n")
                ch.sendall((line + "\r\n" + (body + "\r\n" if out else "") + pr).encode())
    except Exception:
        pass
    finally:
        try:
            t.close()
        except Exception:
            pass


class Listener:
    def __init__(self, d, port):
        self.d, self.port, self.sock = d, port, None

    def is_down(self):
        return os.path.exists(os.path.join(SIM, self.d["name"], "_down"))

    def up(self):
        if self.sock:
            return
        s = socket.socket()
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            s.bind((self.d["ip"], self.port))
        except OSError as e:
            sys.stderr.write("bind %s:%d: %s\n" % (self.d["ip"], self.port, e))
            return
        s.listen(64)
        self.sock = s
        threading.Thread(target=self.loop, args=(s,), daemon=True).start()

    def down(self):
        if self.sock:
            s, self.sock = self.sock, None
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            s.close()

    def loop(self, s):
        while True:
            try:
                c, _ = s.accept()
            except OSError:
                return
            if self.port == 22 and self.d.get("os") in PROMPT:
                threading.Thread(target=session, args=(c, self.d), daemon=True).start()
            else:
                c.close()


L = []
for d in DEVS:
    os.makedirs(os.path.join(SIM, d["name"]), exist_ok=True)
    for p in d.get("ports", [22]):
        L.append(Listener(d, p))
while True:
    for x in L:
        if x.is_down():
            x.down()
        else:
            x.up()
    try:
        json.dump(STATS, open(os.path.join(SIM, "_stats.json"), "w"))
    except OSError:
        pass
    time.sleep(0.3)
