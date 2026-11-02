package estate

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"aidc-dbd/provgo/internal/pyjson"
)

func (e *Estate) digestShort() string { return e.Digest[:8] + "..." + e.Digest[len(e.Digest)-4:] }

func (e *Estate) ready(n string) any {
	s := e.St[n]
	if s.Str("flag") == "quarantined" || e.Devs[n].Truthy("aggregate") {
		return nil
	}
	dz := s.M("disc")
	for _, k := range []string{"model", "port"} {
		if x := dz.M(k); x != nil && x.Len() > 0 {
			if ok, isB := x.V("ok").(bool); isB && !ok && !s.Truthy("disc_ok") {
				return false
			}
		}
	}
	_, has := e.Ident.Get(n)
	return has && (s.Int("stage") >= 1 || s.Truthy("last_seen"))
}

func (e *Estate) identStr(n string) (string, string) {
	i, ok := e.Ident.Get(n)
	if !ok {
		return "", ""
	}
	return idWord(i.Kind) + " " + i.Value, i.Source
}

// StateView is GET /api/state.
func (e *Estate) StateView() *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.stateView()
}

func (e *Estate) stateView() *pyjson.Map {
	builds := []any{}
	for _, b := range e.Builds {
		bs := e.bstate(b.Str("key"))
		waves := []any{}
		for wi, wv := range b.L("waves") {
			w := wv.(*pyjson.Map)
			waves = append(waves, pyjson.O("name", w.V("name"), "devices", w.V("devices"), "warn_only", w.Truthy("warn_only"),
				"open", int64(wi) < bs.Int("open_waves")))
		}
		var gate []any
		if bs.Str("state") != "queued" {
			for _, r := range e.gateEval(b.Str("key")) {
				gate = append(gate, r)
			}
		} else {
			for _, cv := range b.L("gate") {
				c := cv.(*pyjson.Map)
				gate = append(gate, pyjson.O("key", c.V("key"), "label", c.V("label"), "type", c.V("type"), "note", dget(c, "note", ""), "result", "waiting"))
			}
		}
		if gate == nil {
			gate = []any{}
		}
		rd, tot := int64(0), int64(0)
		for _, w := range b.L("waves") {
			for _, n := range pyjson.StrList(w.(*pyjson.Map).V("devices")) {
				r := e.ready(n)
				if r == nil {
					continue
				}
				tot++
				if r.(bool) {
					rd++
				}
			}
		}
		passed := bs.V("passed")
		if !bs.Has("passed") {
			passed = int64(0)
		}
		held := bs.V("held")
		if !bs.Has("held") {
			held = ""
		}
		builds = append(builds, pyjson.O("key", b.V("key"), "name", b.V("name"), "num", b.V("num"), "state", bs.V("state"), "waves", waves,
			"gate", gate, "override", bs.V("override"), "passed", passed, "held", held, "ready", []any{rd, tot}))
	}
	devs := pyjson.NewMap()
	for _, n := range e.DevOrder {
		s := e.St[n]
		items := pyjson.NewMap()
		if it := s.M("items"); it != nil {
			for _, k := range it.Keys() {
				v, _ := it.V(k).(*pyjson.Map)
				items.Set(k, v.V("state"))
			}
		}
		id, src := e.identStr(n)
		rma := false
		if r := s.M("rma"); r != nil && r.Len() > 0 && r.Str("state") == "open" {
			rma = true
		}
		cnt := s.V("count")
		if !s.Has("count") {
			cnt = int64(0)
		}
		devs.Set(n, pyjson.O("stage", s.V("stage"), "flag", s.V("flag"), "detail", s.V("detail"), "count", cnt, "items", items,
			"id", id, "id_source", src, "rma", rma))
	}
	unc := []any{}
	for _, k := range e.Unclaimed.Keys() {
		u := e.Unclaimed.M(k)
		if u.Str("state") == "open" {
			unc = append(unc, u)
		}
	}
	for _, uv := range unc {
		u := uv.(*pyjson.Map)
		sg := u.L("suggest")
		if len(sg) == 0 {
			continue
		}
		g := sg[0].(*pyjson.Map)
		v := devs.M(g.Str("device"))
		if v != nil && pyjson.Int(v.V("stage")) == 0 && (pyjson.Str(v.V("flag")) == "" || pyjson.Str(v.V("flag")) == "missing") {
			v.Set("flag", "unclaimed")
			kind, val := "MAC", u.V("mac")
			if u.Truthy("serial") {
				kind, val = "serial", u.V("serial")
			}
			v.Set("detail", fmt.Sprintf("likely the unknown %s %s (%s)", kind, PyStr(val), PyStr(g.V("why"))))
		}
	}
	results := pyjson.NewMap()
	for _, k := range e.Results.Keys() {
		v := e.Results.M(k)
		if v.Has("busbw") {
			results.Set(k, pyjson.O("busbw", v.V("busbw"), "target", v.V("target"), "by", v.V("by"), "ts", v.V("ts")))
		} else {
			tests := pyjson.NewMap()
			if tm := v.M("tests"); tm != nil {
				for _, t := range tm.Keys() {
					tv, _ := tm.V(t).(*pyjson.Map)
					tests.Set(t, tv.V("result"))
				}
			}
			results.Set(k, pyjson.O("by", v.V("by"), "ts", v.V("ts"), "tests", tests))
		}
	}
	ho := e.handover().Copy()
	ho.Set("customer_admins", int64(e.customerAdmins()))
	peer := any("")
	if v, ok := e.Conf.Get("peer_url"); ok {
		peer = v
	}
	registry := e.Plan.V("registry")
	if !e.Plan.Has("registry") {
		registry = []any{}
	}
	return pyjson.O("server_time", Now(), "version", Version, "site", e.site(), "digest", e.digestShort(),
		"run", e.Run, "builds", builds, "devices", devs, "unclaimed", unc, "kea", e.KeaStatus,
		"push_cred", e.push.cred != nil, "access", e.coverage(), "pushing", e.pushingNames(), "tuning", e.tuningView(),
		"handover", ho, "runners", e.RunnerState, "results", results,
		"ha", pyjson.O("role", e.Role, "peer", peer, "last_sync", e.LastSync),
		"registry", registry, "foreign_sync", e.ForeignSync, "foreign_n", int64(len(e.Foreign)))
}

func (e *Estate) handover() *pyjson.Map {
	m, _ := e.KVGet("handover", nil).(*pyjson.Map)
	if m == nil {
		return pyjson.NewMap()
	}
	return m
}

// Handover is GET /local/handover.
func (e *Estate) Handover() *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.handover()
}

// LocalStatus is GET /local/status.
func (e *Estate) LocalStatus() *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	v := e.stateView()
	bl := []any{}
	for _, b := range v.L("builds") {
		bm := b.(*pyjson.Map)
		bl = append(bl, []any{bm.V("name"), bm.V("state")})
	}
	return pyjson.O("run", v.V("run"), "builds", bl, "unclaimed", int64(len(v.L("unclaimed"))), "kea", v.V("kea"))
}

// ------------------------------------------------------------------------------------- tuning (read only here)

var (
	podRE      = regexp.MustCompile(`-Pod(\d+)-`)
	podScopeRE = regexp.MustCompile(`^pod:(\d+)$`)
)

func (e *Estate) tuningPlan() *pyjson.Map {
	m := e.Plan.M("tuning")
	if m == nil {
		return pyjson.NewMap()
	}
	return m
}

func (e *Estate) tuningState() *pyjson.Map {
	t, _ := e.KVGet("tuning", nil).(*pyjson.Map)
	if t == nil {
		t = pyjson.NewMap()
	}
	t.SetDefault("versions", []any{})
	t.SetDefault("dev", pyjson.NewMap())
	return t
}

func (e *Estate) losslessDevices() []string {
	for _, b := range e.Builds {
		if b.Str("key") != "be" {
			continue
		}
		for _, cv := range b.L("gate") {
			c := cv.(*pyjson.Map)
			if c.Str("kind") == "lossless" {
				return pyjson.StrList(c.V("devices"))
			}
		}
	}
	return nil
}

func (e *Estate) tuningScope(scope string) []string {
	every := e.losslessDevices()
	switch scope {
	case "test":
		for _, r := range e.Plan.L("runners") {
			rm := r.(*pyjson.Map)
			if rm.Str("kind") == "accept" {
				return pyjson.StrList(rm.V("devices"))
			}
		}
		return nil
	case "rest":
		var out []string
		for _, n := range every {
			if !podRE.MatchString(n) {
				out = append(out, n)
			}
		}
		return out
	}
	if m := podScopeRE.FindStringSubmatch(scope); m != nil {
		var out []string
		pre := "-Pod" + m[1] + "-"
		for _, n := range every {
			if strings.Contains(n, pre) {
				out = append(out, n)
			}
		}
		return out
	}
	return every
}

func (e *Estate) tuningPods() []any {
	set := map[int64]bool{}
	for _, n := range e.losslessDevices() {
		if m := podRE.FindStringSubmatch(n); m != nil {
			v, _ := pyInt(m[1])
			set[v] = true
		}
	}
	var l []int64
	for k := range set {
		l = append(l, k)
	}
	sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
	out := []any{}
	for _, x := range l {
		out = append(out, x)
	}
	return out
}

func (e *Estate) tuningView() any {
	tp, t := e.tuningPlan(), e.tuningState()
	if !tp.Truthy("params") {
		return nil
	}
	every := e.tuningScope("all")
	cnt := pyjson.NewMap()
	dev := t.M("dev")
	for _, n := range every {
		var k any = int64(0)
		if dm := dev.M(n); dm != nil {
			if v, ok := dm.Get("v"); ok {
				k = v
			}
		}
		ks := PyStr(k)
		cnt.Set(ks, pyjson.Int(cnt.V(ks))+1)
	}
	return pyjson.O("os", tp.V("os"), "note", dget(tp, "note", ""), "params", tp.V("params"), "versions", t.V("versions"),
		"approved", t.V("approved"), "at", cnt, "switches", int64(len(every)), "test", int64(len(e.tuningScope("test"))),
		"rest", int64(len(e.tuningScope("rest"))), "pods", e.tuningPods(), "rollout", t.V("rollout"))
}

// ------------------------------------------------------------------------------------- plan and device views

// PlanView is GET /api/plan.
func (e *Estate) PlanView() *pyjson.Map {
	P := e.Plan
	builds := []any{}
	for _, b := range e.Builds {
		waves := []any{}
		for _, wv := range b.L("waves") {
			w := wv.(*pyjson.Map)
			waves = append(waves, pyjson.O("name", w.V("name"), "devices", w.V("devices"), "warn_only", w.Truthy("warn_only")))
		}
		builds = append(builds, pyjson.O("key", b.V("key"), "name", b.V("name"), "num", b.V("num"), "waves", waves))
	}
	devs := pyjson.NewMap()
	for _, n := range e.DevOrder {
		d := e.Devs[n]
		m := pyjson.NewMap()
		for _, k := range []string{"short", "role", "os", "os_label", "ip", "rack", "release", "aggregate", "push", "config_signal", "files", "model"} {
			m.Set(k, d.V(k))
		}
		devs.Set(n, m)
	}
	runners := []any{}
	for _, r := range P.L("runners") {
		rm := r.(*pyjson.Map)
		runners = append(runners, pyjson.O("key", rm.V("key"), "label", rm.V("label")))
	}
	floor := P.V("floor")
	if !P.Has("floor") {
		floor = []any{}
	}
	return pyjson.O("site", e.site(), "builds", builds, "devices", devs, "floor", floor, "runners", runners, "digest", e.digestShort())
}

func (e *Estate) bdir(n string) string {
	p := filepath.Join(e.Conf.Str("state_dir"), "backups", safeName(n))
	_ = os.MkdirAll(p, 0o755)
	return p
}

func (e *Estate) backups(n string) []string {
	m, _ := filepath.Glob(filepath.Join(e.bdir(n), "*.cfg"))
	sort.Sort(sort.Reverse(sort.StringSlice(m)))
	return m
}

// tail is the last 3000 characters of a text file, "" when unreadable.
func tail(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	s := string(b)
	if utf8.RuneCountInString(s) <= 3000 {
		return s
	}
	r := []rune(s)
	return string(r[len(r)-3000:])
}

// Tail exposes tail for the runner log endpoint.
func (e *Estate) Tail(name string) string { return tail(filepath.Join(e.Conf.Str("state_dir"), name)) }

// DeviceView is GET /api/device/<n>.
func (e *Estate) DeviceView(n string) *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	d, s := e.Devs[n], e.St[n]
	evs := []any{}
	for _, r := range e.DB.Query("SELECT * FROM events WHERE device=? ORDER BY id DESC LIMIT 40", n) {
		evs = append(evs, r)
	}
	var ident any
	if i, ok := e.Ident.Get(n); ok {
		ident = i.toMap()
	}
	bl := e.backups(n)
	if len(bl) > 10 {
		bl = bl[:10]
	}
	names := []any{}
	seen := pyjson.NewMap()
	for _, x := range bl {
		names = append(names, filepath.Base(x))
	}
	for _, x := range e.backups(n) {
		if len(seen.Keys()) >= 10 {
			break
		}
		if st, err := os.Stat(x); err == nil {
			seen.Set(filepath.Base(x), st.ModTime().Unix())
		}
	}
	return pyjson.O("plan", d, "state", s, "identity", ident, "events", evs, "backups", names, "backup_seen", seen,
		"push_log", tail(filepath.Join(e.Conf.Str("state_dir"), "push-"+safeName(n)+".log")))
}

// Events is GET /api/events.
func (e *Estate) Events(since, limit int64) []any {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	out := []any{}
	for _, r := range e.DB.Query("SELECT * FROM events WHERE id>? ORDER BY id LIMIT ?", since, limit) {
		out = append(out, r)
	}
	return out
}

// GateEvidence is GET /api/gate-evidence.
func (e *Estate) GateEvidence(k, c string) *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	a := e.autoGet(k, c)
	ev := any(pyjson.NewMap())
	if a.Has("evidence") {
		ev = a.V("evidence")
	}
	return pyjson.O("result", a.V("result"), "note", a.V("note"), "evidence", ev)
}

// ------------------------------------------------------------------------------------- exports

// Export returns a CSV export, ok=false for an unknown name.
func (e *Estate) Export(what string) (string, bool) {
	if what == "readiness.csv" {
		e.Mu.Lock()
		defer e.Mu.Unlock()
		return e.readinessCSV(), true
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	w := &CSVWriter{}
	switch what {
	case "asbuilt.csv":
		w.Row("device", "build", "wave", "role", "os", "mgmt_ip", "identity", "identity_source", "mac_seen", "serial_seen", "state", "completed", "rma_ticket")
		for _, n := range e.DevOrder {
			d, s := e.Devs[n], e.St[n]
			i, has := e.Ident.Get(n)
			val, src := any(""), any("")
			if has {
				val, src = i.Value, i.Source
			}
			wp, ok := e.WaveOf[n]
			bk, wi := any(""), int64(1)
			if ok {
				bk, wi = wp.build, int64(wp.wave)+1
			}
			state := STG[s.Int("stage")]
			if f := s.Str("flag"); f != "" {
				state += " / " + f
			}
			var ticket any = ""
			if r := s.M("rma"); r != nil {
				if v, ok := r.Get("ticket"); ok {
					ticket = v
				}
			}
			w.Row(n, bk, wi, d.V("role"), d.V("os"), d.V("ip"), val, src, s.V("mac"), s.V("serial"), state, isoAny(tsMap(s).V("6")), ticket)
		}
	case "sot-status.csv":
		w.Row("device", "status", "changed")
		for _, n := range e.DevOrder {
			s := e.St[n]
			st := "planned"
			if s.Int("stage") == 6 {
				st = "active"
			}
			w.Row(n, st, isoAny(tsMap(s).V("6")))
		}
	case "audit.csv":
		w.Row("time", "user", "action", "target", "detail")
		for _, r := range e.DB.Query("SELECT * FROM audit ORDER BY id") {
			w.Row(isoAny(r.V("ts")), r.V("user"), r.V("action"), r.V("target"), r.V("detail"))
		}
	case "events.csv":
		w.Row("time", "level", "device", "stage", "flag", "text", "user")
		for _, r := range e.DB.Query("SELECT * FROM events ORDER BY id") {
			w.Row(isoAny(r.V("ts")), r.V("level"), r.V("device"), r.V("stage"), r.V("flag"), r.V("text"), r.V("user"))
		}
	default:
		return "", false
	}
	return w.String(), true
}

func (e *Estate) readinessCSV() string {
	w := &CSVWriter{}
	w.Row("device", "build", "wave", "called_in", "identity", "model_planned", "model_seen", "port_planned", "port_seen", "ready", "why")
	for _, n := range e.DevOrder {
		r := e.ready(n)
		if r == nil {
			continue
		}
		d, s := e.Devs[n], e.St[n]
		dz := s.M("disc")
		wp, ok := e.WaveOf[n]
		bk, wi := any(""), int64(1)
		if ok {
			bk, wi = wp.build, int64(wp.wave)+1
		}
		why := ""
		i, has := e.Ident.Get(n)
		if !r.(bool) {
			switch {
			case !(s.Int("stage") >= 1 || s.Truthy("last_seen")):
				why = "not called in"
			case !has:
				why = "no identity"
			default:
				var parts []string
				for _, k := range []string{"model", "port"} {
					if x := dz.M(k); x != nil && x.Len() > 0 {
						if ok, isB := x.V("ok").(bool); isB && !ok {
							parts = append(parts, fmt.Sprintf("%s %s, planned %s", k, PyStr(x.V("seen")), PyStr(x.V("planned"))))
						}
					}
				}
				why = strings.Join(parts, "; ")
			}
		}
		val := ""
		if has {
			val = i.Value
		}
		ml := d.M("mgmt_link")
		mm, mp := dz.M("model"), dz.M("port")
		yes := "no"
		if r.(bool) {
			yes = "yes"
		}
		w.Row(n, bk, wi, isoAny(s.V("last_seen")), val, dget(d, "model", ""), dget(mm, "seen", ""),
			pyStrip(PyStr(dget(ml, "switch", ""))+" "+PyStr(dget(ml, "port", ""))), dget(mp, "seen", ""), yes, why)
	}
	return w.String()
}

// ------------------------------------------------------------------------------------- readiness

// Readiness is what must hold before Start build (GET /api/readiness).
func (e *Estate) Readiness() *pyjson.Map {
	e.Mu.Lock()
	rc, _ := e.KVGet("readiness", nil).(*pyjson.Map)
	e.Mu.Unlock()
	if rc == nil {
		rc = pyjson.NewMap()
	}
	items := []any{}
	add := func(key, label, result, note string) {
		items = append(items, pyjson.O("key", key, "label", label, "result", result, "note", note))
	}
	sv := e.site().M("services")
	sim := pyjson.Truthy(e.Conf.V("reader_sim_dir"))
	type probe struct {
		k  string
		ip string
	}
	var ps []probe
	for _, k := range []string{"ntp", "dns", "tacacs"} {
		for _, ip := range pyjson.StrList(sv.V(k)) {
			ps = append(ps, probe{k, ip})
		}
	}
	res := make([]bool, len(ps))
	Parallel(len(ps), func(i int) {
		p := ps[i]
		if sim {
			res[i] = true
			return
		}
		switch p.k {
		case "ntp":
			res[i] = udpProbe(p.ip, "123", append([]byte{0x1b}, make([]byte, 47)...))
		case "dns":
			res[i] = udpProbe(p.ip, "53", []byte("\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x01"))
		case "tacacs":
			c, err := net.DialTimeout("tcp", net.JoinHostPort(p.ip, "49"), 2*time.Second)
			if err == nil {
				c.Close()
				res[i] = true
			}
		}
	})
	var down, up []string
	for i, p := range ps {
		if res[i] {
			up = append(up, strings.ToUpper(p.k)+" "+p.ip)
		} else {
			down = append(down, strings.ToUpper(p.k)+" "+p.ip)
		}
	}
	switch {
	case len(down) > 0:
		add("services", "Customer services answer at their planned addresses (NTP, DNS, TACACS)", "fail", "not answering: "+strings.Join(down, ", "))
	case len(up) > 0:
		add("services", "Customer services answer at their planned addresses (NTP, DNS, TACACS)", "pass", fmt.Sprintf("%d answer", len(up)))
	default:
		add("services", "Customer services answer at their planned addresses (NTP, DNS, TACACS)", "warn", "no service addresses in the plan")
	}
	if sv.Truthy("syslog") || sv.Truthy("radius") {
		add("udp-services", "Syslog and RADIUS (UDP, no reply to test)", "warn", "check at their servers: "+strings.Join(append(pyjson.StrList(sv.V("syslog")), pyjson.StrList(sv.V("radius"))...), ", "))
	}
	e.Mu.Lock()
	var live, noid, macfirst, dup []string
	for _, n := range e.DevOrder {
		if e.flag(n) != "quarantined" && !e.Devs[n].Truthy("aggregate") {
			live = append(live, n)
		}
	}
	seen := map[string]string{}
	for _, n := range live {
		i, has := e.Ident.Get(n)
		if !has {
			noid = append(noid, n)
		}
		if os := e.Devs[n].Str("os"); (os == "cumulus" || os == "eos" || os == "iosxr") && has && i.Kind == "sn" {
			macfirst = append(macfirst, n)
		}
		if has && i.Value != "" {
			if p, ok := seen[i.Value]; ok {
				dup = append(dup, fmt.Sprintf("%s (%s, %s)", i.Value, e.short(p), e.short(n)))
			}
			seen[i.Value] = n
		}
	}
	var parts []string
	if len(macfirst) > 0 {
		var sh []string
		for k, n := range macfirst {
			if k < 4 {
				sh = append(sh, e.short(n))
			}
		}
		parts = append(parts, fmt.Sprintf("%d MAC-matched device(s) bound by serial: %s", len(macfirst), strings.Join(sh, ", ")))
	}
	if len(dup) > 0 {
		d := dup
		if len(d) > 3 {
			d = d[:3]
		}
		parts = append(parts, "identifier used twice: "+strings.Join(d, ", "))
	}
	if len(noid) > 0 {
		parts = append(parts, fmt.Sprintf("%d device(s) without an identity: they appear under Unclaimed when they call in", len(noid)))
	}
	note := strings.Join(parts, "; ")
	if note == "" {
		note = fmt.Sprintf("%d devices", len(live))
	}
	res2 := "pass"
	if len(macfirst) > 0 || len(dup) > 0 {
		res2 = "fail"
	} else if len(noid) > 0 {
		res2 = "warn"
	}
	add("identities", "Every identity can match its platform", res2, note)
	needsPush := false
	for _, n := range live {
		if e.Devs[n].Truthy("push") {
			needsPush = true
		}
	}
	cv := e.coverage()
	e.Mu.Unlock()
	var miss, badsum []string
	images := e.Plan.L("images")
	for _, imv := range images {
		im := imv.(*pyjson.Map)
		p := filepath.Join(e.Conf.Str("web_root"), "images", PyStr(dget(im, "file", "")))
		if !isFile(p) {
			miss = append(miss, PyStr(im.V("file")))
		} else if im.Truthy("sha256") {
			if fileSHA256(p) != im.Str("sha256") {
				badsum = append(badsum, im.Str("file"))
			}
		}
	}
	parts = nil
	if len(badsum) > 0 {
		parts = append(parts, "checksum mismatch: "+strings.Join(badsum, ", "))
	}
	if len(miss) > 0 {
		m := miss
		if len(m) > 3 {
			m = m[:3]
		}
		x := fmt.Sprintf("missing: %s (a device not on its target release stops at its check before apply)", strings.Join(m, ", "))
		if len(miss) > 3 {
			x += fmt.Sprintf(" +%d", len(miss)-3)
		}
		parts = append(parts, x)
	}
	note = strings.Join(parts, "; ")
	if note == "" {
		note = fmt.Sprintf("%d image(s)", len(images))
	}
	r3 := "pass"
	if len(badsum) > 0 {
		r3 = "fail"
	} else if len(miss) > 0 {
		r3 = "warn"
	}
	add("images", "Target images present, checksums match", r3, note)
	if needsPush {
		pr := "fail"
		e.Mu.Lock()
		if e.push.cred != nil {
			pr = "pass"
		}
		e.Mu.Unlock()
		add("push", "Push credential entered (Junos, SR Linux)", pr, "")
	} else {
		add("push", "Push credential entered (Junos, SR Linux)", "pass", "no pushed platform in this build")
	}
	if sh := pyjson.StrList(cv.V("short")); len(sh) > 0 {
		add("people", "Two people able to act on every plane", "warn", "fewer than two on "+strings.Join(sh, ", "))
	} else {
		add("people", "Two people able to act on every plane", "pass", "")
	}
	if ha := e.Plan.M("ha"); ha != nil && ha.Truthy("enabled") {
		e.Mu.Lock()
		lastSync := e.LastSync
		e.Mu.Unlock()
		age := Now() - truthyF(lastSync)
		r := "warn"
		if age < 300 {
			r = "pass"
		}
		ls := isoAny(lastSync)
		if ls == "" {
			ls = "never"
		}
		add("standby", "Standby provisioning host in sync", r, "last sync "+ls)
	}
	var st syscall.Statfs_t
	free := 0.0
	if err := syscall.Statfs(e.Conf.Str("state_dir"), &st); err == nil {
		free = float64(st.Bavail) * float64(st.Frsize) / 1e9
	}
	dr := "pass"
	if free < 2 {
		dr = "fail"
	} else if free < 10 {
		dr = "warn"
	}
	add("disk", "Disk space for state and backups", dr, fmt.Sprintf("%.1f GB free", free))
	if certSelfSigned(e.Conf.Str("tls_cert")) {
		add("cert", "Console certificate from the customer's CA", "warn", "self-signed: replace it from certs/ once the CA is up")
	} else {
		add("cert", "Console certificate from the customer's CA", "pass", "")
	}
	for _, rcItem := range readyConfirm {
		c := rc.M(rcItem[0])
		if rc.Has(rcItem[0]) && pyjson.Truthy(rc.V(rcItem[0])) {
			add("c-"+rcItem[0], rcItem[1], "confirmed", fmt.Sprintf("%s: %s", PyStr(c.V("by")), PyStr(c.V("note"))))
		} else {
			add("c-"+rcItem[0], rcItem[1], "confirm", "")
		}
	}
	acc := rc.V("_accept")
	fails := 0
	for _, it := range items {
		if r := it.(*pyjson.Map).Str("result"); r == "fail" || r == "confirm" {
			fails++
		}
	}
	ok := fails == 0
	if am, isM := acc.(*pyjson.Map); !ok && isM && am.Len() > 0 && am.Int("n") >= int64(fails) {
		ok = true
	}
	return pyjson.O("items", items, "fails", int64(fails), "accepted", acc, "ok", ok)
}

func udpProbe(ip, port string, payload []byte) bool {
	c, err := net.DialTimeout("udp", net.JoinHostPort(ip, port), 2*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(payload); err != nil {
		return false
	}
	buf := make([]byte, 512)
	_, err = c.Read(buf)
	return err == nil
}

func fileSHA256(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// certSelfSigned mirrors the Python check (openssl x509 -issuer -subject): a PEM certificate whose issuer and
// subject both carry a CN and are the same name. Unreadable file: treated as self-signed.
func certSelfSigned(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	if !strings.Contains(string(b), "BEGIN CERTIFICATE") {
		return false
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return false
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return false
	}
	is, su := c.Issuer.String(), c.Subject.String()
	if strings.Count(is+su, "CN") < 2 {
		return false
	}
	return is == su
}
