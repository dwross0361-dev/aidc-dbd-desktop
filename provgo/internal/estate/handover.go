package estate

// Hand-over (Python: handover_set, handover_final_backup, asbuilt_overlay, handover_zip, handover_received,
// _remove_dwr, release_host_access, release_vendor).

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

const dwrAcct = "dwr-netadmin"

type removeSpec struct {
	cmds []string
	how  string
}

var removeDWR = map[string]removeSpec{
	"cumulus": {[]string{"nv unset system aaa user dwr-netadmin", "nv config apply -y"}, "shell"},
	"eos":     {[]string{"no username dwr-netadmin"}, "save"}, "nxos": {[]string{"no username dwr-netadmin"}, "save"},
	"iosxe": {[]string{"no username dwr-netadmin"}, "save"}, "iosxr": {[]string{"no username dwr-netadmin"}, "commit"},
	"junos": {[]string{"delete system login user dwr-netadmin"}, "commit"},
	"srl":   {[]string{"delete / system aaa authentication user dwr-netadmin"}, "commit"},
	"sros":  {[]string{`delete configure system security user-params local-user user "dwr-netadmin"`}, "commit"},
}

const handoverReadme = `Hand-over export: site %(site)s
Downloaded by %(by)s (customer admin), %(when)s. %(nb)d device backups; %(final)s.

asbuilt.csv       every device: build, wave, identity as bound, state, completed, RMA ticket
sot-status.csv    Active (Done) or Planned per device
sot-asbuilt.csv   the as-built overlay for the source of truth: serial, MAC, status and RMA note per device
tuning.json       the back-end tuning record: each version, its values and lines, who approved it, the version each switch runs
apply_asbuilt.py  applies sot-asbuilt.csv to NetBox or Nautobot over the API, after the Final package's source of truth is
                  loaded (HS-DEP-003 step 6.2):  python3 apply_asbuilt.py netbox https://netbox.example TOKEN --dry-run
backups/          the newest configuration of each device: the baseline for the customer's own backup system
audit.csv, events.csv   the console's audit log and event history

From this download on, the console takes no scheduled backups: configuration backups are the customer's backup
system's. DWR Networking keeps no copy of this export.
`

const applyAsbuilt = `#!/usr/bin/env python3
# Apply the as-built overlay (sot-asbuilt.csv) to NetBox or Nautobot: each device's serial, status and RMA note, matched
# by name. Load the Final package's source of truth first (HS-DEP-003 step 6.2).
USAGE = ("usage:  python3 apply_asbuilt.py netbox|nautobot URL TOKEN [--dry-run] [--csv sot-asbuilt.csv]\n"
         "needs:  pip install pynetbox   (or pynautobot)")
import csv, os, sys
a = [x for x in sys.argv[1:] if not x.startswith("--")]
if len(a) < 3 or a[0] not in ("netbox", "nautobot"):
    sys.exit(USAGE)
dry = "--dry-run" in sys.argv
src = sys.argv[sys.argv.index("--csv") + 1] if "--csv" in sys.argv else os.path.join(os.path.dirname(os.path.abspath(__file__)), "sot-asbuilt.csv")
if a[0] == "netbox":
    import pynetbox
    api = pynetbox.api(a[1], token=a[2])
else:
    import pynautobot
    api = pynautobot.api(a[1], token=a[2])
done = missing = same = 0
for r in csv.DictReader(open(src)):
    d = api.dcim.devices.get(name=r["name"])
    if not d:
        missing += 1
        print("not found:", r["name"])
        continue
    ch = {}
    if r["serial"] and (d.serial or "") != r["serial"]:
        ch["serial"] = r["serial"]
    want = r["status"] if a[0] == "netbox" else r["status"].capitalize()
    cur = d.status.value if a[0] == "netbox" and hasattr(d.status, "value") else str(getattr(d.status, "name", d.status) or "")
    if cur.lower() != r["status"].lower():
        ch["status"] = want
    note = "; ".join(x for x in ("As built: MAC %s" % r["mac"] if r["mac"] else "", r["note"]) if x)
    if note and note not in (d.comments or ""):
        ch["comments"] = ((d.comments or "") + "\n" + note).strip()
    if not ch:
        same += 1
        continue
    print(("would update" if dry else "update"), r["name"], ch)
    if not dry:
        d.update(ch)
    done += 1
print("%d updated%s, %d unchanged, %d not found" % (done, " (dry run)" if dry else "", same, missing))
`

// handoverSet updates kv handover (lock held).
func (e *Estate) handoverSet(kv ...any) *pyjson.Map {
	h := e.handover()
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i].(string), kv[i+1])
	}
	e.KVSet("handover", h)
	return h
}

// HandoverFinalBackup takes the final as-built backup of every Done device (in the background).
func (e *Estate) HandoverFinalBackup(user string) (bool, string) {
	e.Mu.Lock()
	if e.handover().M("final").Str("state") == "running" {
		e.Mu.Unlock()
		return false, "the final backup is already running"
	}
	var devs []string
	for _, n := range e.DevOrder {
		d := e.Devs[n]
		if e.stage(n) == 6 && BackupCmd[d.Str("os")] != "" && !d.Truthy("aggregate") {
			devs = append(devs, n)
		}
	}
	e.handoverSet("final", pyjson.O("state", "running", "by", user, "ts", Now(), "n", int64(len(devs))))
	site := e.siteCode()
	e.Mu.Unlock()
	go func() {
		ok := make([]bool, len(devs))
		e.poolMap(len(devs), func(i int) { ok[i] = e.TakeBackup(devs[i], "final as-built (hand-over)") != "" })
		var bad []any
		var badN []string
		for i, n := range devs {
			if !ok[i] {
				bad = append(bad, n)
				badN = append(badN, n)
			}
		}
		e.Mu.Lock()
		defer e.Mu.Unlock()
		fl := bad
		if len(fl) > 200 {
			fl = fl[:200]
		}
		if fl == nil {
			fl = []any{}
		}
		e.handoverSet("final", pyjson.O("state", "done", "by", user, "ts", Now(), "n", int64(len(devs)), "ok", int64(len(devs)-len(bad)), "failed", fl))
		e.audit(user, "handover-final-backup", site, fmt.Sprintf("%d of %d devices", len(devs)-len(bad), len(devs)))
		extra, lvl := "", "info"
		if len(badN) > 0 {
			var sh []string
			for i, n := range badN {
				if i >= 5 {
					break
				}
				sh = append(sh, e.short(n))
			}
			extra, lvl = "; not possible on "+strings.Join(sh, ", "), "warn"
		}
		e.ev(fmt.Sprintf("Hand-over: final as-built backup of %d of %d device(s)%s", len(devs)-len(bad), len(devs), extra), lvl, "")
	}()
	e.Audit(user, "handover-final-backup-start", site, fmt.Sprintf("%d devices", len(devs)))
	return true, fmt.Sprintf("final backup of %d device(s) started", len(devs))
}

// asbuiltOverlay is sot-asbuilt.csv (lock held).
func (e *Estate) asbuiltOverlay() string {
	w := &CSVWriter{}
	w.Row("name", "serial", "mac", "identity", "status", "completed", "rma_ticket", "note")
	for _, n := range e.DevOrder {
		d := e.Devs[n]
		if d.Truthy("aggregate") {
			continue
		}
		s := e.St[n]
		i, has := e.Ident.Get(n)
		pid := d.M("id")
		var ser, mac any
		switch {
		case has && i.Kind == "sn":
			ser = i.Value
		case s.Truthy("serial"):
			ser = s.V("serial")
		case pid.Truthy("serial"):
			ser = pid.V("serial")
		default:
			ser = ""
		}
		switch {
		case has && i.Kind == "mac":
			mac = i.Value
		case s.Truthy("mac"):
			mac = s.V("mac")
		case pid.Truthy("mac"):
			mac = pid.V("mac")
		default:
			mac = ""
		}
		rma := s.M("rma")
		note := ""
		if pyjson.Truthy(rma) {
			nw := rma.M("new")
			note = pyStripU(fmt.Sprintf("RMA %s: replaced, new %s %s", orStr(rma.V("ticket")), PyStr(dget(nw, "kind", "")), PyStr(dget(nw, "value", ""))))
		}
		ident := ""
		if has {
			ident = i.Kind + ":" + i.Value
		}
		status := "planned"
		if s.Int("stage") == 6 {
			status = "active"
		}
		w.Row(n, ser, mac, ident, status, isoAny(tsMap(s).V("6")), dget(rma, "ticket", ""), note)
	}
	return w.String()
}

type zipEntry struct {
	name string
	data []byte
	mod  time.Time
}

// HandoverZip builds the hand-over export: (file name, body).
func (e *Estate) HandoverZip(user string) (string, []byte) {
	site := e.siteCode()
	top := fmt.Sprintf("%s-handover-%s/", site, time.Now().UTC().Format("20060102T150405Z"))
	now := time.Now()
	var ents []zipEntry
	add := func(name, data string) { ents = append(ents, zipEntry{top + name, []byte(data), now}) }
	for _, f := range []string{"asbuilt.csv", "sot-status.csv", "audit.csv", "events.csv"} {
		t, _ := e.Export(f)
		add(f, t)
	}
	t, _ := e.Export("readiness.csv")
	add("readiness.csv", t)
	add("readiness.json", pyjson.DumpsIndent(e.Readiness(), 1))
	e.Mu.Lock()
	if tp := e.tuningPlan(); tp.Truthy("params") {
		tt := e.tuningState()
		vers := []any{}
		for _, x := range tt.L("versions") {
			xm := x.(*pyjson.Map).Copy()
			l, err := e.tuningLines(e.tuningValues(pyIntAny(xm.V("v"))))
			if err != nil {
				e.Mu.Unlock()
				panic(err)
			}
			xm.Set("lines", strList(l))
			vers = append(vers, xm)
		}
		pl, err := e.tuningLines(e.tuningValues(0))
		if err != nil {
			e.Mu.Unlock()
			panic(err)
		}
		add("tuning.json", pyjson.DumpsIndent(pyjson.O("os", tp.V("os"), "params", tp.V("params"), "versions", vers,
			"package_lines", strList(pl), "approved", tt.V("approved"), "rollout", tt.V("rollout"), "switches", tt.V("dev")), 1))
	}
	add("sot-asbuilt.csv", e.asbuiltOverlay())
	add("apply_asbuilt.py", applyAsbuilt)
	nb := 0
	for _, n := range e.DevOrder {
		if b := e.backups(n); len(b) > 0 {
			data, err := os.ReadFile(b[0])
			if err != nil {
				e.Mu.Unlock()
				panic(err)
			}
			mod := now
			if st, err := os.Stat(b[0]); err == nil {
				mod = st.ModTime()
			}
			ents = append(ents, zipEntry{top + "backups/" + safeName(n) + ".cfg", data, mod})
			nb++
		}
	}
	fin := e.handover().M("final")
	final := "no final as-built backup was taken: the newest backup of each device is included"
	if fin.Str("state") == "done" {
		final = fmt.Sprintf("final as-built backup %s by %s, %s of %s devices", isoAny(fin.V("ts")), PyStr(fin.V("by")), PyStr(fin.V("ok")), PyStr(fin.V("n")))
	}
	e.Mu.Unlock()
	rd := strings.NewReplacer("%(site)s", site, "%(by)s", user, "%(when)s", ISO(Now()), "%(nb)d", fmt.Sprint(nb), "%(final)s", final)
	add("README.txt", rd.Replace(handoverReadme))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, en := range ents {
		h := &zip.FileHeader{Name: en.name, Method: zip.Deflate, Modified: en.mod}
		h.SetMode(0o600)
		w, err := zw.CreateHeader(h)
		if err != nil {
			panic(err)
		}
		w.Write(en.data)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	name := strings.TrimSuffix(top, "/") + ".zip"
	e.Mu.Lock()
	e.handoverSet("built", pyjson.O("by", user, "ts", Now(), "backups", int64(nb), "sha256", sha, "name", name))
	e.audit(user, "handover-export-built", site, fmt.Sprintf("%d device backups; sha256 %s", nb, sha))
	e.event(fmt.Sprintf("Hand-over export built for %s: %d device backups (sha256 %s...); it counts once its receipt is confirmed", user, nb, sha[:12]), "info", "", nil, nil, user)
	e.Mu.Unlock()
	return name, body
}

func strList(l []string) []any {
	out := []any{}
	for _, x := range l {
		out = append(out, x)
	}
	return out
}

var nonHexRE = regexp.MustCompile(`[^0-9a-f]`)

// HandoverReceived: the customer confirms it holds the export (its SHA-256 or a prefix of 12 or more digits).
func (e *Estate) HandoverReceived(user string, sha any) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	b := e.handover().M("built")
	s := ""
	if pyjson.Truthy(sha) {
		s = PyStr(sha)
	}
	s = nonHexRE.ReplaceAllString(strings.ToLower(s), "")
	if !b.Truthy("sha256") {
		return false, "download the hand-over export first"
	}
	full := b.Str("sha256")
	if len(s) < 12 || !strings.HasPrefix(full, s) {
		return false, fmt.Sprintf("that does not match the export's SHA-256 (%s...): check the file you saved, or download it again", pyHead(full, 6))
	}
	e.handoverSet("exported", pyjson.O("by", user, "ts", Now(), "backups", dget(b, "backups", int64(0)), "sha256", full, "name", b.V("name")))
	e.audit(user, "handover-export", e.siteCode(), fmt.Sprintf("receipt confirmed (sha256 %s); scheduled backups off from now", full))
	e.event(fmt.Sprintf("Hand-over export received by %s (sha256 confirmed): scheduled console backups are off from now", user), "info", "", nil, nil, user)
	return true, "receipt confirmed: the export is handed over; scheduled console backups are off"
}

// HandoverExportLocal is POST /local/handover-export: the export written for the customer by root.
func (e *Estate) HandoverExportLocal(out string) (string, error) {
	fn, body := e.HandoverZip("cli:root")
	if out == "" {
		out = "/root"
	}
	p := pyJoin(out, fn)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(p, 0o600)
	s := sha256.Sum256(body)
	e.HandoverReceived("cli:root", hex.EncodeToString(s[:]))
	return p, nil
}

func trunc(s string, n int) string { return pyHead(s, n) }

func (e *Estate) removeDWRDev(n, pw string) (string, string) {
	d := e.Devs[n]
	os_ := d.Str("os")
	spec, ok := removeDWR[os_]
	if !ok || !d.Truthy("ip") {
		w := os_
		if w == "" {
			w = "this device"
		}
		return "skipped", "no removal command for " + w
	}
	if e.Conf.Truthy("reader_sim_dir") {
		return "removed", "simulated"
	}
	bg := "netadmin"
	if v, ok := e.Conf.Get("break_glass_user"); ok {
		bg = PyStr(v)
	}
	// HS v1.56 r3 (DECISIONS P5-2, P5-3): inside the device's session limit; a transient login failure is retried
	s, done, err := e.dialFor(n, os_, d.Str("ip"), 22, bg, pw)
	if err != nil {
		return "failed", "netadmin login: " + trunc(err.Error(), 160)
	}
	defer done()
	cfg, err := removeAccountOn(s, spec.cmds, spec.how, BackupCmd[os_])
	if err != nil {
		if le, isL := err.(*loginErr); isL {
			return "failed", "netadmin login: " + trunc(le.msg, 160)
		}
		return "failed", trunc(err.Error(), 160)
	}
	if strings.Contains(cfg, dwrAcct) {
		return "failed", "dwr-netadmin is still in the configuration"
	}
	return "removed", ""
}

var acctRE = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

func (e *Estate) releaseHostAccess() []any {
	p := "/etc/fabric/vendor-access.json"
	if v, ok := e.Conf.Get("vendor_access"); ok {
		p = PyStr(v)
	}
	out := []any{}
	b, err := os.ReadFile(p)
	var va *pyjson.Map
	if err == nil {
		va, err = pyjson.LoadsMap(b)
	}
	if err != nil {
		return []any{fmt.Sprintf("no DWR host access recorded (%s): check the host's accounts by hand", p)}
	}
	day := time.Now().UTC().Format("20060102")
	for _, acct := range pyjson.StrList(va.V("accounts")) {
		if !acctRE.MatchString(acct) || acct == "root" {
			continue
		}
		cmd := exec.Command("usermod", "-L", "-e", "1", acct)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			out = append(out, fmt.Sprintf("account %s locked and expired", acct))
		} else {
			out = append(out, fmt.Sprintf("account %s NOT locked: %s", acct, pyHead(pyStripU(stderr.String()), 80)))
		}
		if u, err := user.Lookup(acct); err == nil && u.HomeDir != "" {
			keys, _ := filepath.Glob(filepath.Join(u.HomeDir, ".ssh", "authorized_keys*"))
			for _, ak := range keys {
				dst := ak + ".released-" + day
				if err := os.Rename(ak, dst); err != nil {
					out = append(out, fmt.Sprintf("SSH keys of %s NOT removed: %s", acct, pyErrno(err)))
					continue
				}
				if err := os.Chmod(dst, 0); err != nil {
					out = append(out, fmt.Sprintf("SSH keys of %s NOT removed: %s", acct, pyErrno(err)))
					continue
				}
				out = append(out, fmt.Sprintf("SSH keys of %s removed (%s)", acct, filepath.Base(ak)))
			}
		}
	}
	for _, f := range pyjson.StrList(va.V("sudoers")) {
		if strings.HasPrefix(f, "/etc/sudoers.d/") && isFile(f) {
			if err := os.Remove(f); err != nil {
				out = append(out, fmt.Sprintf("sudo rule %s NOT removed: %s", f, pyErrno(err)))
			} else {
				out = append(out, fmt.Sprintf("sudo rule %s removed", f))
			}
		}
	}
	return out
}

// ReleaseVendor is Release DWR access: dwr-netadmin removed from every Done device, DWR console logins disabled,
// DWR's host accounts locked, the push credential cleared.
func (e *Estate) ReleaseVendor(user, pw string, confirm *pyjson.Map, background bool) (bool, string) {
	e.Mu.Lock()
	h := e.handover()
	if h.M("release").Str("state") == "running" {
		e.Mu.Unlock()
		return false, "Release DWR access is already running"
	}
	if !h.Truthy("exported") {
		e.Mu.Unlock()
		return false, "refused: a customer admin downloads the hand-over export and confirms its receipt first (HS-DEP-003 step 6.1)"
	}
	ca := e.customerAdmins()
	e.Mu.Unlock()
	if ca < 2 {
		return false, fmt.Sprintf("refused: the customer needs two admin logins of its own (it has %d); add them under Logins", ca)
	}
	if pyStripU(PyStr(dget(confirm, "typed", ""))) != "RELEASE" {
		return false, "type RELEASE to confirm"
	}
	if !pyEq(confirm.V("tacacs_vpn"), "yes") {
		return false, "refused: confirm DWR's TACACS and remote-access accounts are disabled"
	}
	if !pyEq(confirm.V("backup_system"), "yes") {
		return false, "refused: confirm the customer's backup system has taken a first backup of every device"
	}
	if pyStripU(PyStr(dget(confirm, "dwr_attest", ""))) == "" {
		return false, "refused: record the reference of DWR Networking's written attestation that it keeps no copies"
	}
	if pw == "" {
		return false, "the netadmin password (it removes dwr-netadmin; held in memory for this action only)"
	}
	e.Mu.Lock()
	var devs []string
	for _, n := range e.DevOrder {
		d := e.Devs[n]
		if _, ok := removeDWR[d.Str("os")]; ok && e.stage(n) == 6 && !d.Truthy("aggregate") {
			devs = append(devs, n)
		}
	}
	e.handoverSet("release", pyjson.O("state", "running", "by", user, "ts", Now(), "n", int64(len(devs))))
	site := e.siteCode()
	e.Mu.Unlock()
	attest := pyHead(PyStr(confirm.V("dwr_attest")), 200)
	goFn := func() {
		type res struct{ st, why string }
		out := make([]res, len(devs))
		e.poolMap(len(devs), func(i int) {
			st, why := e.removeDWRDev(devs[i], pw)
			out[i] = res{st, why}
		})
		var removed int
		failed := []any{}
		for i, n := range devs {
			switch out[i].st {
			case "removed":
				removed++
			case "failed":
				failed = append(failed, pyjson.O("device", n, "why", out[i].why))
			}
		}
		e.Mu.Lock()
		dw := []any{}
		var dwN []string
		for _, r := range e.DB.Query("SELECT name FROM users WHERE org='dwr' AND COALESCE(disabled,0)=0") {
			dw = append(dw, r.Str("name"))
			dwN = append(dwN, r.Str("name"))
		}
		for _, nm := range dwN {
			e.DB.MustExec("UPDATE users SET disabled=1 WHERE name=?", nm)
			e.DB.MustExec("DELETE FROM sessions WHERE user=?", nm)
		}
		e.push.cred = nil
		e.Mu.Unlock()
		host := e.releaseHostAccess()
		state := "done"
		if len(failed) > 0 {
			state = "partial"
		}
		fl := failed
		if len(fl) > 500 {
			fl = fl[:500]
		}
		rec := pyjson.O("state", state, "by", user, "ts", Now(), "n", int64(len(devs)), "removed", int64(removed),
			"failed", fl, "console_logins_disabled", dw, "host", host,
			"confirm", pyjson.O("tacacs_vpn", true, "backup_system", true, "dwr_attest", attest))
		e.Mu.Lock()
		defer e.Mu.Unlock()
		e.handoverSet("release", rec)
		sub := pyjson.NewMap()
		for _, k := range []string{"removed", "n", "failed", "console_logins_disabled", "host", "confirm"} {
			sub.Set(k, rec.V(k))
		}
		e.audit(user, "release-vendor", site, pyHead(pyjson.Dumps(sub), 4000))
		fx, lvl := "", "info"
		if len(failed) > 0 {
			fx, lvl = fmt.Sprintf("; FAILED on %d: Retry the release for them", len(failed)), "warn"
		}
		dwS := strings.Join(dwN, ", ")
		if dwS == "" {
			dwS = "none"
		}
		var hs []string
		for _, x := range host {
			hs = append(hs, PyStr(x))
		}
		e.event(fmt.Sprintf("Release DWR access by %s: dwr-netadmin removed from %d of %d device(s)%s; DWR console logins disabled (%s); push credential cleared; %s",
			user, removed, len(devs), fx, dwS, strings.Join(hs, "; ")), lvl, "", nil, nil, user)
	}
	if background {
		go goFn()
		return true, fmt.Sprintf("releasing: dwr-netadmin is being removed from %d device(s); the result appears in Hand-over", len(devs))
	}
	goFn()
	return true, "done"
}
