package estate

// RMA: replacing a device (Python: rma_checklist, rma_start, rma_identity, rma_check, rma_upload, rma_apply,
// mnha-restore, rma_automation/_rma_auto, restore_asbuilt).

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

var (
	serialRE    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._/-]{2,63}$`)
	rmaMNHA     = regexp.MustCompile(`MNHA`)
	rmaLACP     = regexp.MustCompile(`(?i)border leaf|gateway|core|MCS|MLAG|vPC`)
	licErrRE    = pyRe(`(?i)\b(error|invalid|failed|fail)\b`)
	licenseCmds = map[string][]string{
		"junos": {"request system license add {url}"},
		"eos":   {"license import {url}"},
		"nxos":  {"copy {url} bootflash:{file} vrf management", "install license bootflash:{file}"},
		"sros":  {"file copy {url} cf3:/license.txt force", "admin system license activate cf3:/license.txt"},
	}
)

func (e *Estate) rmaChecklist(n string) []any {
	d := e.Devs[n]
	os_, role := d.Str("os"), d.Str("role")
	var L []any
	switch os_ {
	case "sros", "junos", "iosxr", "nxos", "eos":
		L = append(L, pyjson.O("key", "license", "label", "License for the replacement serial: put it in /etc/fabric/licenses/<serial>.lic and the service installs it",
			"blocks_push", true, "done", false))
	}
	if rmaMNHA.MatchString(role) {
		L = append(L, pyjson.O("key", "mnha", "label", "Preemption held until SRG0 session sync completes, then restored (the service does both)",
			"blocks_push", false, "done", false))
	}
	if rmaLACP.MatchString(role) {
		L = append(L, pyjson.O("key", "lacp", "label", "LACP system ID and ESI are set explicitly in the configuration: hosts keep their bonds",
			"blocks_push", false, "done", true))
	}
	L = append(L, pyjson.O("key", "keys", "label", "SSH known-hosts entry replaced; gNMI/TLS certificate reissued from certs/",
		"blocks_push", false, "done", false))
	return L
}

func orStr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return PyStr(v)
}

func (e *Estate) rmaStart(n, user string, ticket any) (bool, string) {
	s := e.St[n]
	old, hasOld := e.Ident.Get(n)
	reason := fmt.Sprintf("%s retired by RMA %s", n, orStr(ticket))
	if hasOld {
		v := strings.ToUpper(old.Value)
		if old.Kind == "mac" {
			v = NormMAC(old.Value)
		}
		e.Blocked[v] = reason
		e.DB.MustExec("INSERT OR REPLACE INTO blocked(value,reason,ts) VALUES(?,?,?)", v, reason, Now())
	}
	// Python blocks the identity seen on the wire in memory only (not in the blocked table): DECISIONS P3-a
	for _, k := range []string{"mac", "serial", "client_id"} {
		if s.Truthy(k) {
			v := s.Str(k)
			if k == "mac" {
				v = NormMAC(v)
			}
			e.Blocked[v] = reason
		}
	}
	e.Ident.Pop(n)
	e.DB.MustExec("DELETE FROM bindings WHERE name=?", n)
	cl := e.rmaChecklist(n)
	ok, ov := "", ""
	if hasOld {
		ok, ov = old.Kind, old.Value
	}
	e.DB.MustExec("INSERT INTO rma(device,ticket,old_kind,old_value,state,opened,user,checklist) VALUES(?,?,?,?,?,?,?,?)",
		n, ticket, ok, ov, "open", Now(), user, pyjson.Dumps(cl))
	var oldV any
	if hasOld {
		oldV = old.toMap()
	}
	var mnha any
	if rmaMNHA.MatchString(e.Devs[n].Str("role")) && e.Devs[n].Truthy("push") {
		mnha = "hold"
	}
	tk := orStr(ticket)
	if tk == "" {
		tk = "-"
	}
	s.Update(pyjson.O("stage", int64(0), "flag", "rma", "detail", fmt.Sprintf("awaiting replacement (RMA %s)", tk), "ts", pyjson.NewMap(),
		"fetched", pyjson.NewMap(), "lease_ip", "", "mac", "", "serial", "", "client_id", "",
		"rma", pyjson.O("state", "open", "ticket", ticket, "old", oldV, "checklist", cl, "opened", Now(), "mnha", mnha,
			"apply", pyjson.O("source", "planned"))))
	e.saveDev(n)
	oldTxt := "none"
	if hasOld {
		oldTxt = old.Kind + " " + old.Value
	}
	e.audit(user, "rma-start", n, fmt.Sprintf("ticket %s, old %s", PyStr(ticket), oldTxt))
	ovTxt := "identity"
	if hasOld {
		ovTxt = old.Value
	}
	e.event(fmt.Sprintf("%s: RMA %s opened; old %s retired", e.short(n), orStr(ticket), ovTxt), "warn", n, nil, nil, user)
	e.KeaDirty = true
	return true, "ok"
}

// RMAStart opens an RMA: the old identity is retired and the device waits for its replacement.
func (e *Estate) RMAStart(n, user string, ticket any) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.rmaStart(n, user, ticket)
}

// RMAIdentity registers the replacement's serial or MAC.
func (e *Estate) RMAIdentity(n, user string, value any) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	s := e.St[n]
	if s.M("rma").Str("state") != "open" {
		return false, "no open RMA on " + n
	}
	mac := NormMACAny(value)
	v := ""
	if pyjson.Truthy(value) {
		v = PyStr(value)
	}
	v = strings.ToUpper(pyStripU(v))
	if mac == "" && !serialRE.MatchString(v) {
		return false, "not a MAC address or a serial number (letters, digits, '.', '/', '-'; 3 to 64 characters)"
	}
	key := "manual:" + v
	if mac != "" {
		key = "manual:" + mac
	}
	ser := v
	if mac != "" {
		ser = ""
	}
	e.Unclaimed.Set(key, pyjson.O("key", key, "first", Now(), "last", Now(), "state", "open", "why", "entered for RMA",
		"mac", mac, "serial", ser, "suggest", []any{}))
	ok, msg := e.bind(key, n, user)
	if ok {
		s.Set("flag", "")
		kind, val := "sn", v
		if mac != "" {
			kind, val = "mac", mac
		}
		s.M("rma").Set("new", pyjson.O("kind", kind, "value", val))
		s.Set("detail", "replacement registered; waiting for it to call in")
		e.saveDev(n)
	}
	return ok, msg
}

// RMACheck confirms one checklist item.
func (e *Estate) RMACheck(n, key, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	r := e.St[n].M("rma")
	for _, iv := range r.L("checklist") {
		if i, ok := iv.(*pyjson.Map); ok && pyEq(i.V("key"), key) {
			i.Set("done", true)
		}
	}
	e.saveDev(n)
	e.audit(user, "rma-check", n, key)
	e.event(fmt.Sprintf("%s: RMA checklist '%s' confirmed by %s", e.short(n), key, user), "info", n, nil, nil, user)
	return true, "ok"
}

// RMAUpload stores a configuration from the customer's backup system as the device's newest backup, then applies it.
func (e *Estate) RMAUpload(n string, text any, user string) (bool, string) {
	e.Mu.Lock()
	if e.St[n].M("rma").Str("state") != "open" {
		e.Mu.Unlock()
		return false, "no open RMA on " + n
	}
	e.Mu.Unlock()
	t := ""
	if pyjson.Truthy(text) {
		t = PyStr(text)
	}
	if pyStripU(t) == "" || runeLen(t) > 4*1024*1024 {
		return false, "an empty file, or larger than 4 MB"
	}
	p := filepath.Join(e.bdir(n), time.Now().UTC().Format("20060102T150405Z")+".cfg")
	if err := os.WriteFile(p, []byte(t), 0o644); err != nil {
		panic(err)
	}
	e.Mu.Lock()
	e.audit(user, "rma-upload", n, fmt.Sprintf("%d bytes from the customer's backup system -> %s", runeLen(t), filepath.Base(p)))
	e.event(fmt.Sprintf("%s: configuration loaded for the RMA (%s, from the customer's backup system)", e.short(n), filepath.Base(p)), "info", n, nil, nil, user)
	e.Mu.Unlock()
	return e.RMAApply(n, "backup", user)
}

// RMAApply chooses what the replacement is given: the planned configuration, or the newest backup.
func (e *Estate) RMAApply(n string, source any, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	r := e.St[n].M("rma")
	if r.Str("state") != "open" {
		return false, "no open RMA"
	}
	if pyEq(source, "backup") {
		b := e.backups(n)
		if len(b) == 0 {
			return false, "no backup of this device"
		}
		if !(e.Devs[n].Truthy("push") || e.Devs[n].Str("os") == "cumulus") {
			return false, "this platform takes the planned configuration only"
		}
		r.Set("apply", pyjson.O("source", "backup", "file", b[0]))
	} else {
		r.Set("apply", pyjson.O("source", "planned"))
	}
	e.saveDev(n)
	e.audit(user, "rma-apply", n, PyStr(source))
	e.event(fmt.Sprintf("%s: RMA will apply the %s configuration (%s)", e.short(n), PyStr(source), user), "info", n, nil, nil, user)
	return true, "ok"
}

// MNHARestore: the operator confirms SRG0 session sync; preemption is restored by the next push.
func (e *Estate) MNHARestore(n, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	r := e.St[n].M("rma")
	ok := r.Str("mnha") == "await-sync"
	if ok {
		r.Set("mnha", "restore")
		e.saveDev(n)
		e.audit(user, "mnha-restore", n, "sync confirmed by operator")
		e.event(fmt.Sprintf("%s: session sync confirmed by %s; restoring preemption", e.short(n), user), "warn", n, nil, nil, user)
		return true, "ok"
	}
	return false, "not waiting for sync"
}

// rmaAutomation is one pass over the open RMAs (checker loop): license install, MNHA sync wait.
func (e *Estate) rmaAutomation() {
	e.Mu.Lock()
	var items []string
	for _, n := range e.DevOrder {
		if e.St[n].M("rma").Str("state") == "open" {
			items = append(items, n)
		}
	}
	e.Mu.Unlock()
	for _, n := range items {
		func() {
			defer func() {
				if x := recover(); x != nil {
					fmt.Fprintf(os.Stderr, "rma %s: %v\n", n, x)
				}
			}()
			e.rmaAuto(n)
		}()
	}
}

func (e *Estate) rmaAuto(n string) {
	e.Mu.Lock()
	s := e.St[n]
	r := s.M("rma")
	d := e.Devs[n]
	nw := r.M("new")
	var lic *pyjson.Map
	for _, iv := range r.L("checklist") {
		if i, ok := iv.(*pyjson.Map); ok && i.Str("key") == "license" && !i.Truthy("done") {
			lic = i
			break
		}
	}
	cmds, hasCmds := licenseCmds[d.Str("os")]
	var licFile string
	doLic := false
	if lic != nil && nw.Str("kind") == "sn" && s.Int("stage") >= 2 && hasCmds {
		licFile = e.LicenseFile(nw.Str("value"))
		retryAt := 0.0
		if v, ok := lic.Get("retry_at"); ok {
			retryAt = pyjson.Float(v)
		}
		doLic = licFile != "" && !lic.Truthy("tried") && Now() >= retryAt
	}
	prov := e.provIP()
	e.Mu.Unlock()
	if doLic {
		base := filepath.Base(licFile)
		url := fmt.Sprintf("http://%s/licenses/%s", prov, base)
		var cl []string
		for _, c := range cmds {
			cl = append(cl, strings.ReplaceAll(strings.ReplaceAll(c, "{url}", url), "{file}", base))
		}
		outs, err := e.Read(n, cl)
		e.Mu.Lock()
		if err == nil {
			ok := true
			for _, o := range outs {
				if licErrRE.MatchString(o) {
					ok = false
				}
			}
			lic.Set("tried", true)
			lic.Set("done", ok)
			if ok {
				lic.Set("note", "installed from "+base)
			} else {
				lic.Set("note", "install reported: "+pyTail(outs[len(outs)-1], 160))
			}
			lvl := "info"
			if !ok {
				lvl = "warn"
			}
			e.ev(fmt.Sprintf("%s: license %s", e.short(n), lic.Str("note")), lvl, n)
		} else if ru, isU := isUnavailable(err); isU {
			lic.Set("tried", true)
			lic.Set("note", fmt.Sprintf("staged %s; install by hand (%s)", base, ru.Msg))
		} else {
			lic.Set("retry_at", Now()+300)
			lic.Set("note", fmt.Sprintf("staged %s; device not answering yet (%s): retrying", base, pyHead(err.Error(), 120)))
		}
		e.saveDev(n)
		e.Mu.Unlock()
	}
	e.Mu.Lock()
	await := r.Str("mnha") == "await-sync"
	e.Mu.Unlock()
	if await {
		outs, err := e.Read(n, CMDS["mnha"]["junos"])
		e.Mu.Lock()
		defer e.Mu.Unlock()
		if err == nil {
			if syncRE.MatchString(outs[0]) {
				r.Set("mnha", "restore")
				e.saveDev(n)
				e.ev(e.short(n)+": session sync complete; restoring preemption", "info", n)
			}
			return
		}
		msg := err.Error()
		if r.Str("mnha_note") != msg || !r.Has("mnha_note") {
			r.Set("mnha_note", msg)
			e.saveDev(n)
			e.ev(fmt.Sprintf("%s: cannot read MNHA sync (%s): confirm sync, then 'Restore preemption'", e.short(n), msg), "warn", n)
		}
	}
}

// RestoreAsbuilt is `rma-restore ZIP --device N` (service stopped): a maintenance install starts from the customer's
// hand-over export, every device Done with its identity as bound, and an RMA opened on the device being replaced.
func (e *Estate) RestoreAsbuilt(path, device, user string) (bool, string) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return false, err.Error()
	}
	defer zr.Close()
	var f *zip.File
	for _, x := range zr.File {
		if strings.HasSuffix(x.Name, "/asbuilt.csv") {
			f = x
			break
		}
	}
	if f == nil {
		return false, "no asbuilt.csv in " + path
	}
	rc, err := f.Open()
	if err != nil {
		return false, err.Error()
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	text := strings.ToValidUTF8(string(raw), "�")
	cr := csv.NewReader(bytes.NewReader([]byte(text)))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	all, _ := cr.ReadAll()
	var rows []map[string]string
	if len(all) > 0 {
		hdr := all[0]
		for _, rec := range all[1:] {
			if len(rec) == 0 || (len(rec) == 1 && rec[0] == "") {
				continue
			}
			m := map[string]string{}
			for i, h := range hdr {
				if i < len(rec) {
					m[h] = rec[i]
				}
			}
			rows = append(rows, m)
		}
	}
	if e.Devs[device] == nil {
		return false, "unknown device " + device
	}
	nID := 0
	e.Mu.Lock()
	for _, r := range rows {
		n := r["device"]
		if e.Devs[n] == nil {
			continue
		}
		v, _ := r["identity"], r["identity_source"]
		v = pyStripU(v)
		if v != "" {
			kind, val := "sn", strings.ToUpper(v)
			if m := NormMAC(v); m != "" {
				kind, val = "mac", m
			}
			e.Ident.Set(n, Identity{kind, val, "as-built"})
			e.DB.MustExec("INSERT OR REPLACE INTO bindings(name,kind,value,source) VALUES(?,?,?,?)", n, kind, val, "as-built")
			nID++
		}
		s := e.St[n]
		if strings.HasPrefix(r["state"], "Done") {
			s.Update(pyjson.O("stage", int64(6), "flag", "", "detail", "restored from the hand-over export"))
			tsMap(s).Set("6", Now())
		}
		e.DB.MustExec("INSERT OR REPLACE INTO devices(name,data) VALUES(?,?)", n, pyjson.Dumps(s))
	}
	for _, b := range e.Builds {
		e.bstate(b.Str("key")).Update(pyjson.O("state", "complete", "open_waves", int64(len(b.L("waves"))), "passed", Now()))
	}
	keys := []any{}
	for _, k := range e.buildKeys() {
		keys = append(keys, k)
	}
	e.Run.Update(pyjson.O("state", "complete", "builds", keys, "started", Now(), "plane", "all"))
	e.saveRun()
	h := e.handover()
	h.Set("exported", pyjson.O("by", "maintenance install (restored)", "ts", Now(), "backups", int64(0)))
	e.KVSet("handover", h)
	e.audit(user, "rma-restore", device, fmt.Sprintf("from %s: %d identities", filepath.Base(path), nID))
	e.rmaStart(device, user, "maintenance")
	e.Mu.Unlock()
	return true, fmt.Sprintf("restored %d identities; RMA opened on %s: register the replacement in the console", nID, device)
}
