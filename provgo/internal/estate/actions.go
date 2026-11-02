package estate

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

func idWord(kind string) string {
	if kind == "sn" {
		return "serial"
	}
	return "MAC"
}

func (e *Estate) bind(key, n, user string) (bool, string) {
	u := e.Unclaimed.M(key)
	d := e.Devs[n]
	if u == nil || d == nil {
		return false, "unknown device or unclaimed entry"
	}
	if u.Truthy("foreign") && !u.Truthy("override") {
		return false, "serial belongs to site " + PyStr(u.V("foreign"))
	}
	if cur, ok := e.Ident.Get(n); ok {
		return false, fmt.Sprintf("%s is already bound to %s %s: unbind it first", e.short(n), idWord(cur.Kind), cur.Value)
	}
	dh := d.M("dhcp")
	forms := pyjson.StrList(dh.V("sn_forms"))
	vcp := dh.Str("vc_prefix")
	planned := ""
	if strings.HasPrefix(vcp, "Juniper-") {
		planned = strings.ToLower(strings.TrimRight(vcp[len("Juniper-"):], "-"))
	}
	mismatch := containsStr(forms, "vendor-class") && u.Truthy("model") && planned != "" && strings.ToLower(u.Str("model")) != planned
	if mismatch && !u.Truthy("mac") {
		return false, fmt.Sprintf("reports model %s, planned %s, and no MAC was seen to bind by", u.Str("model"), planned)
	}
	var kind, val string
	switch {
	case u.Truthy("serial") && len(forms) > 0 && !mismatch:
		kind, val = "sn", PyStr(u.V("serial"))
	case u.Truthy("mac"):
		kind, val = "mac", PyStr(u.V("mac"))
	default:
		return false, "this platform matches on MAC and none was seen"
	}
	s := e.St[n]
	src := "bound"
	if r := s.M("rma"); r != nil && r.Str("state") == "open" {
		src = "rma"
	}
	e.Ident.Set(n, Identity{kind, val, src})
	e.DB.MustExec("INSERT OR REPLACE INTO bindings(name,kind,value,source,ts,user) VALUES(?,?,?,?,?,?)", n, kind, val, src, Now(), user)
	u.Set("state", "bound")
	u.Set("bound_to", n)
	e.DB.MustExec("INSERT OR REPLACE INTO unclaimed(key,data) VALUES(?,?)", key, pyjson.Dumps(u))
	if f := s.Str("flag"); f == "missing" || f == "unclaimed" {
		s.Set("flag", "")
	}
	if src == "rma" {
		r := s.M("rma")
		r.Set("new", pyjson.O("kind", kind, "value", val))
		e.DB.MustExec("UPDATE rma SET new_kind=?, new_value=? WHERE device=? AND state='open'", kind, val, n)
		e.knownHosts(n)
		for _, iv := range r.L("checklist") {
			if i, ok := iv.(*pyjson.Map); ok && i.Str("key") == "keys" {
				i.Set("note", "known-hosts entry replaced by the service; reissue the gNMI/TLS certificate from certs/")
			}
		}
	}
	e.saveDev(n)
	e.audit(user, "bind", n, fmt.Sprintf("%s %s (was none)", kind, val))
	if mismatch {
		e.event(fmt.Sprintf("%s reports model %s, planned %s: bound by MAC %s instead of serial %s", e.short(n), u.Str("model"), planned,
			PyStr(u.V("mac")), PyStr(dget(u, "serial", ""))), "warn", n, nil, nil, user)
	}
	e.event(fmt.Sprintf("%s bound to %s %s (%s, logged)", e.short(n), idWord(kind), val, user), "warn", n, nil, nil, user)
	e.KeaDirty = true
	return true, "bound"
}

// Bind binds an unclaimed entry to a device.
func (e *Estate) Bind(key, n, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.bind(key, n, user)
}

// Dismiss takes an unclaimed entry off the list (audited).
func (e *Estate) Dismiss(key, user string, reason any) (bool, string) {
	rs := ""
	if pyjson.Truthy(reason) {
		rs = PyStr(reason)
	}
	rs = pyStrip(rs)
	if r := []rune(rs); len(r) > 200 {
		rs = string(r[:200])
	}
	if rs == "" {
		return false, "a reason is required"
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	u := e.Unclaimed.M(key)
	if u == nil || u.Str("state") != "open" {
		return false, "no open unclaimed entry " + key
	}
	u.Set("state", "dismissed")
	u.Set("dismissed", pyjson.O("by", user, "ts", Now(), "reason", rs))
	e.DB.MustExec("INSERT OR REPLACE INTO unclaimed(key,data) VALUES(?,?)", key, pyjson.Dumps(u))
	e.audit(user, "dismiss", key, rs)
	e.event(fmt.Sprintf("Unclaimed %s dismissed by %s: %s", key, user, rs), "info", "", nil, nil, user)
	return true, "dismissed"
}

// Unbind undoes a console binding before Config.
func (e *Estate) Unbind(n, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.Devs[n] == nil {
		return false, "unknown device"
	}
	cur, ok := e.Ident.Get(n)
	if !ok {
		return false, "not bound"
	}
	if cur.Source != "bound" {
		return false, "its identity comes from the design inventory: change it there, or start an RMA"
	}
	if e.stage(n) >= 4 {
		return false, "already at Config or beyond: use Start RMA to replace its identity"
	}
	e.Ident.Pop(n)
	e.DB.MustExec("DELETE FROM bindings WHERE name=?", n)
	for _, k := range e.Unclaimed.Keys() {
		u := e.Unclaimed.M(k)
		if u.Str("state") == "bound" && u.Str("bound_to") == n {
			u.Set("state", "open")
			u.Delete("bound_to")
			e.DB.MustExec("INSERT OR REPLACE INTO unclaimed(key,data) VALUES(?,?)", k, pyjson.Dumps(u))
		}
	}
	s := e.St[n]
	s.Set("stage", int64(0))
	s.Set("flag", "")
	s.Set("detail", "unbound by "+user)
	e.saveDev(n)
	e.audit(user, "unbind", n, cur.Kind+" "+cur.Value)
	e.event(fmt.Sprintf("%s unbound from %s %s (%s, logged)", e.short(n), idWord(cur.Kind), cur.Value, user), "warn", n, nil, nil, user)
	e.KeaDirty = true
	return true, "unbound"
}

// Retry restarts a device (reserve: re-serve from DHCP on).
func (e *Estate) Retry(n, user string, reserve bool) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	s := e.St[n]
	if strings.HasPrefix(PyStr(dget(s, "detail", "")), "discovery:") {
		s.Set("disc_ok", true)
		e.audit(user, "discovery-accept", n, PyStr(dget(s, "detail", "")))
	}
	s.Set("flag", "")
	s.Set("attempts", s.Int("attempts")+1)
	if reserve && s.Int("stage") >= 1 {
		s.Set("stage", int64(1))
	} else {
		s.Set("stage", int64(0))
	}
	if reserve {
		s.Set("detail", "re-served by "+user)
	} else {
		s.Set("detail", fmt.Sprintf("retry %d by %s", s.Int("attempts"), user))
	}
	s.Set("fetched", pyjson.NewMap())
	nts := pyjson.NewMap()
	ts := tsMap(s)
	for _, k := range ts.Keys() {
		if ki, err := strconv.Atoi(k); err == nil && int64(ki) <= s.Int("stage") {
			nts.Set(k, ts.V(k))
		}
	}
	s.Set("ts", nts)
	s.Set("clock", Now())
	e.saveDev(n)
	act, word := "retry", "retry"
	if reserve {
		act, word = "re-serve", "re-serve"
	}
	e.audit(user, act, n, "")
	e.event(fmt.Sprintf("%s: %s requested (%s, logged)", e.short(n), word, user), "warn", n, nil, nil, user)
	e.KeaDirty = true
	return true, "ok"
}

func (e *Estate) quarantine(n, user, reason string) (bool, string) {
	s := e.St[n]
	s.Set("flag", "quarantined")
	if reason != "" {
		s.Set("detail", reason)
	} else {
		s.Set("detail", "quarantined")
	}
	e.saveDev(n)
	e.audit(user, "quarantine", n, reason)
	e.event(fmt.Sprintf("%s quarantined: %s (%s)", e.short(n), reason, user), "warn", n, nil, nil, user)
	e.KeaDirty = true
	return true, "ok"
}

// Quarantine sets a device aside.
func (e *Estate) Quarantine(n, user, reason string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.quarantine(n, user, reason)
}

// LabSlice keeps a lab's devices and sets the rest aside (TC-202).
func (e *Estate) LabSlice(keepL []string, reason, user string, macs *pyjson.Map) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	keep := map[string]bool{}
	for _, k := range keepL {
		keep[k] = true
	}
	var unknown []string
	for k := range keep {
		if e.Devs[k] == nil {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	if len(keep) == 0 || len(unknown) == len(keep) {
		u := unknown
		if len(u) > 3 {
			u = u[:3]
		}
		return false, fmt.Sprintf("none of the slice's nodes are in this plan (%s)", strings.Join(u, ", "))
	}
	var kept []string
	out := 0
	for _, n := range e.DevOrder {
		inband := e.Devs[n].Truthy("ztp_links")
		if keep[n] && !inband {
			kept = append(kept, n)
			continue
		}
		if e.flag(n) == "quarantined" {
			continue
		}
		why := reason
		if keep[n] {
			why = "lab: in-band ZTP through a switch outside the slice"
		}
		e.quarantine(n, user, why)
		out++
	}
	bound, dism := 0, 0
	var names []string
	if macs != nil {
		names = sortedStrings(macs.Keys())
	}
	for _, n := range names {
		mac := NormMACAny(macs.V(n))
		if mac == "" || e.Devs[n] == nil {
			continue
		}
		_, hasID := e.Ident.Get(n)
		if containsStr(kept, n) && !hasID {
			key := "manual:" + mac
			e.Unclaimed.Set(key, pyjson.O("key", key, "first", Now(), "last", Now(), "state", "open", "why", "lab slice MAC list",
				"mac", mac, "serial", "", "suggest", []any{}))
			if ok, _ := e.bind(key, n, user); ok {
				bound++
			}
		} else if keep[n] && !containsStr(kept, n) {
			e.Unclaimed.Set(mac, pyjson.O("key", mac, "first", Now(), "last", Now(), "state", "dismissed", "mac", mac, "why", "lab: set aside",
				"dismissed", pyjson.O("by", user, "ts", Now(), "reason", "lab: in-band ZTP through a switch outside the slice")))
			e.DB.MustExec("INSERT OR REPLACE INTO unclaimed(key,data) VALUES(?,?)", mac, pyjson.Dumps(e.Unclaimed.V(mac)))
			dism++
		}
	}
	shorts := []string{}
	for i, n := range kept {
		if i >= 6 {
			break
		}
		shorts = append(shorts, e.short(n))
	}
	more := ""
	if len(kept) > 6 {
		more = " ..."
	}
	bnd := ""
	if bound > 0 {
		bnd = fmt.Sprintf("; %d bound by MAC", bound)
	}
	e.event(fmt.Sprintf("Lab slice: %d device(s) kept (%s), %d set aside%s", len(kept), strings.Join(shorts, ", ")+more, out, bnd), "warn", "", nil, nil, user)
	msg := fmt.Sprintf("%d kept, %d set aside", len(kept), out)
	if macs != nil && macs.Len() > 0 {
		msg += fmt.Sprintf("; %d bound by MAC", bound)
	}
	if dism > 0 {
		msg += fmt.Sprintf("; %d set-aside MAC(s) dismissed", dism)
	}
	if len(unknown) > 0 {
		msg += "; not in the plan: " + strings.Join(unknown, ", ")
	}
	return true, msg
}

// MarkConfigured records an operator-applied configuration.
func (e *Estate) MarkConfigured(n, user, note string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.audit(user, "mark-configured", n, note)
	x := ""
	if note != "" {
		x = ": " + note
	}
	e.setStage(n, 4, fmt.Sprintf("marked configured by %s%s", user, x), "", "")
	if !e.Devs[n].Truthy("ip") {
		e.setStage(n, 6, "no management address to probe; operator-confirmed", "", "")
	}
	return true, "ok"
}

// GateConfirm records a manual gate confirmation.
func (e *Estate) GateConfirm(k, chk, user, note string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	b := e.BuildByKey[k]
	found := false
	if b != nil {
		for _, c := range b.L("gate") {
			if c.(*pyjson.Map).Str("key") == chk {
				found = true
			}
		}
	}
	if !found {
		return false, "unknown build/check"
	}
	e.DB.MustExec("INSERT OR REPLACE INTO gates(build,chk,result,user,note,ts) VALUES(?,?,?,?,?,?)", k, chk, "confirmed", user, note, Now())
	e.GateRes[[2]string{k, chk}] = pyjson.O("build", k, "chk", chk, "result", "confirmed", "user", user, "note", note)
	e.audit(user, "gate-confirm", k+"/"+chk, note)
	e.event(fmt.Sprintf("%s gate: '%s' confirmed by %s", b.Str("name"), chk, user), "info", "", nil, nil, user)
	return true, "ok"
}

// GateOverride passes a gate with a reason.
func (e *Estate) GateOverride(k, user, reason string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.BuildByKey[k] == nil || !e.BState.Has(k) {
		return false, "unknown build"
	}
	if reason == "" || len([]rune(pyStrip(reason))) < 8 {
		return false, "an override needs a reason (8 characters or more)"
	}
	e.bstate(k).Set("override", pyjson.O("user", user, "reason", reason, "ts", Now()))
	e.saveRun()
	e.audit(user, "gate-override", k, reason)
	e.event(fmt.Sprintf("%s gate overridden by %s: %s", e.BuildByKey[k].Str("name"), user, reason), "warn", "", nil, nil, user)
	return true, "ok"
}

var readyConfirm = [][2]string{
	{"ra", "Remote access (RA-VPN) and the LTE break-glass path to the OOB network tested"},
	{"carriers", "Carrier hand-offs delivered and labeled (circuit IDs recorded)"},
	{"racking", "Racking, power and cabling signed off by the contractor"},
}

// ReadinessConfirm records a readiness confirmation, or "_accept" to accept the failing items.
func (e *Estate) ReadinessConfirm(user, key, note string) (bool, string) {
	var fails int
	if key == "_accept" {
		fails = int(e.Readiness().Int("fails"))
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	rc, _ := e.KVGet("readiness", nil).(*pyjson.Map)
	if rc == nil {
		rc = pyjson.NewMap()
	}
	switch {
	case key == "_accept":
		rc.Set("_accept", pyjson.O("by", user, "note", note, "ts", Now(), "n", int64(fails)))
	case key == "ra" || key == "carriers" || key == "racking":
		rc.Set(key, pyjson.O("by", user, "note", note, "ts", Now()))
	default:
		return false, "unknown item"
	}
	e.KVSet("readiness", rc)
	act := "readiness-confirm"
	if key == "_accept" {
		act = "readiness-accept"
	}
	e.audit(user, act, key, note)
	return true, "recorded"
}

// Pause toggles a running run (console).
func (e *Estate) Pause(user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	st := e.Run.Str("state")
	if st == "complete" || st == "idle" {
		return false, "nothing to pause: the run is " + st
	}
	if st == "running" {
		e.Run.Set("state", "paused")
	} else {
		e.Run.Set("state", "running")
	}
	e.saveRun()
	e.audit(user, "run-"+e.Run.Str("state"), "", "")
	e.event(fmt.Sprintf("Run %s by %s", e.Run.Str("state"), user), "info", "", nil, nil, user)
	return true, e.Run.Str("state")
}

// LocalPauseResume is /local/pause and /local/resume.
func (e *Estate) LocalPauseResume(pause bool) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if pause {
		e.Run.Set("state", "paused")
	} else if pyjson.Truthy(e.Run.V("builds")) {
		e.Run.Set("state", "running")
	} else {
		e.Run.Set("state", "idle")
	}
	e.saveRun()
	e.ev(fmt.Sprintf("Run %s from the command line", e.Run.Str("state")), "info", "")
}

// HasDevice reports whether n is a plan device.
func (e *Estate) HasDevice(n string) bool { return e.Devs[n] != nil }

// Count is /api/count.
func (e *Estate) Count(n string, v any) (bool, string) {
	c, msg := e.Checkin(n, "count", PyStr(v), "127.0.0.1")
	return c == 200, msg
}

// knownHosts drops the replaced unit's SSH host key (ssh-keygen -R <ip>).
func (e *Estate) knownHosts(n string) {
	ip := e.Devs[n].Str("ip")
	if ip == "" {
		return
	}
	if p, err := exec.LookPath("ssh-keygen"); err == nil {
		_ = exec.Command(p, "-R", ip).Run()
	}
}
