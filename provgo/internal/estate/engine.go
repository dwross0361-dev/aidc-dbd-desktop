package estate

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

func (e *Estate) stage(n string) int64 { return e.St[n].Int("stage") }
func (e *Estate) flag(n string) string { return e.St[n].Str("flag") }

func tsMap(s *pyjson.Map) *pyjson.Map {
	m := s.M("ts")
	if m == nil {
		m = pyjson.NewMap()
		s.Set("ts", m)
	}
	return m
}

func (e *Estate) setStage(n string, stage int64, detail, level, text string) {
	s := e.St[n]
	if stage <= s.Int("stage") {
		return
	}
	prev := s.Int("stage")
	s.Set("stage", stage)
	switch s.Str("flag") {
	case "failed", "missing", "waiting", "slow":
		s.Set("flag", "")
	}
	s.Set("detail", detail)
	ts := tsMap(s)
	for k := prev + 1; k <= stage; k++ {
		ts.SetDefault(strconv.FormatInt(k, 10), Now())
	}
	ts.Set(strconv.FormatInt(stage, 10), Now())
	e.saveDev(n)
	if text == "" {
		d := ""
		if detail != "" {
			d = " (" + detail + ")"
		}
		text = fmt.Sprintf("%s: %s%s", e.short(n), STG[stage], d)
	}
	if level == "" {
		level = "info"
	}
	e.ev(text, level, n)
	if stage == 6 {
		e.ev("SoT status planned -> active", "info", n)
		if r := s.M("rma"); r != nil && r.Str("state") == "open" {
			e.closeRMA(n)
		}
	}
}

func (e *Estate) fail(n, detail string) {
	s := e.St[n]
	if s.Str("flag") == "failed" {
		return
	}
	s.Set("flag", "failed")
	s.Set("detail", detail)
	e.saveDev(n)
	nx := s.Int("stage") + 1
	if nx > 6 {
		nx = 6
	}
	e.ev(fmt.Sprintf("%s: failed at %s -- %s", e.short(n), STG[nx], detail), "fail", n)
}

func (e *Estate) closeRMA(n string) {
	r := e.St[n].M("rma")
	r.Set("state", "closed")
	e.St[n].Set("flag", "")
	e.DB.MustExec("UPDATE rma SET state='closed', closed=? WHERE device=? AND state='open'", Now(), n)
	e.saveDev(n)
	e.ev(fmt.Sprintf("%s: RMA %s closed; as-built updated with the new serial", e.short(n), orEmpty(r.V("ticket"))), "info", n)
}

func orEmpty(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return PyStr(v)
}

func (e *Estate) waveOpen(n string) bool {
	if r := e.St[n].M("rma"); r != nil && r.Str("state") == "open" {
		return true
	}
	wp, ok := e.WaveOf[n]
	if !ok || wp.build == "" {
		return false
	}
	bs := e.bstate(wp.build)
	switch bs.Str("state") {
	case "building", "gate", "complete", "blocked":
		return int64(wp.wave) < bs.Int("open_waves")
	}
	return false
}

func (e *Estate) settled(n string) bool {
	s := e.St[n]
	return s.Int("stage") == 6 || s.Str("flag") == "quarantined"
}

// ------------------------------------------------------------------------------------- device fetches and check-ins

// DevByIP is the device at a source address (plan addresses, then current leases).
func (e *Estate) DevByIP(ip string) string {
	if n, ok := e.ByIP[ip]; ok {
		return n
	}
	for _, n := range e.DevOrder {
		if e.St[n].Str("lease_ip") == ip {
			return n
		}
	}
	return ""
}

func (e *Estate) onFetch(n, rel string, size int64, ok bool) {
	s := e.St[n]
	f := s.M("fetched")
	if f == nil {
		f = pyjson.NewMap()
		s.Set("fetched", f)
	}
	f.Set(rel, Now())
	if strings.HasPrefix(rel, "images/") {
		if ok && s.Int("stage") < 3 {
			e.setStage(n, 3, fmt.Sprintf("image %s served (%d MB)", rel[7:], size/1048576), "", "")
		}
	} else if e.FileOwner[rel] == n && s.Int("stage") < 2 {
		if s.Int("stage") < 1 {
			e.setStage(n, 1, "lease seen through the fetch", "", "")
		}
		e.setStage(n, 2, rel+" served", "", "")
	}
	e.saveDev(n)
}

// OnFetch records a served file.
func (e *Estate) OnFetch(n, rel string, size int64, ok bool) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.onFetch(n, rel, size, ok)
}

func (e *Estate) checkin(n, what, msg, ip string) (int, string) {
	d := e.Devs[n]
	if d == nil {
		return 404, "unknown device"
	}
	if pyjson.Truthy(e.Conf.V("strict_fetch")) {
		okIPs := []any{d.V("ip"), e.St[n].V("lease_ip"), "127.0.0.1", e.site().V("prov_ip")}
		match := false
		for _, x := range okIPs {
			if s, isS := x.(string); isS && s == ip {
				match = true
			}
		}
		if !match {
			e.ev(fmt.Sprintf("check-in for %s refused: came from %s, not its address %s", n, ip, PyStr(d.V("ip"))), "warn", n)
			return 403, "address mismatch"
		}
	}
	s := e.St[n]
	switch {
	case what == "fail":
		if msg == "" {
			msg = "reported failure"
		}
		e.fail(n, msg)
	case what == "ztp" || what == "image":
		if msg == "" {
			msg = "ZTP image step complete"
		}
		e.setStage(n, 3, msg, "", "")
	case what == "config":
		if s.Int("stage") < 3 {
			tsMap(s).SetDefault("3", Now())
		}
		if msg == "" {
			msg = "configuration applied (device check-in)"
		}
		e.setStage(n, 4, msg, "", "")
	case what == "verify":
		if msg == "" {
			msg = "verified (device check-in)"
		}
		e.setStage(n, 5, msg, "", "")
	case what == "count" && d.Truthy("aggregate"):
		agg := d.Int("aggregate")
		ci, err := pyInt(msg)
		if err != nil {
			return 400, "count?n="
		}
		c := ci
		if c > agg {
			c = agg
		}
		if c < 0 {
			c = 0
		}
		prev, st0 := s.V("count"), s.Int("stage")
		if prev == nil {
			prev = int64(0)
		}
		s.Set("count", c)
		e.saveDev(n)
		if c >= agg {
			e.setStage(n, 6, fmt.Sprintf("%d of %d", c, agg), "", "")
		} else if s.Int("stage") < 4 {
			e.setStage(n, 4, fmt.Sprintf("%d of %d", c, agg), "", "")
		}
		if !(pyjson.IsNum(prev) && pyjson.Float(prev) == float64(c)) && s.Int("stage") == st0 {
			e.ev(fmt.Sprintf("%s: %d of %d checked in", PyStr(dget(d, "short", n)), c, agg), "info", n)
		}
	default:
		return 400, "unknown check-in"
	}
	return 200, "ok"
}

func dget(d *pyjson.Map, k string, def any) any {
	if v, ok := d.Get(k); ok {
		return v
	}
	return def
}

// Checkin is a device check-in (GET /ci/<host>/<stage>).
func (e *Estate) Checkin(n, what, msg, ip string) (int, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.checkin(n, what, msg, ip)
}

// ------------------------------------------------------------------------------------- engine

func (e *Estate) startRun(plane, user, force string) (bool, string) {
	order := e.buildKeys()
	keys := order
	if plane != "all" {
		keys = []string{plane}
		if !containsStr(order, plane) {
			return false, "unknown plane " + plane
		}
	}
	if len(keys) == 0 {
		panic("no builds in the plan")
	}
	first := keys[0]
	var unpassed, before []string
	for _, k := range order {
		if k == first {
			break
		}
		before = append(before, k)
		if e.bstate(k).Str("state") != "complete" && !e.emptyBuild(k) { // an empty build has nothing to pass (P4-10)
			unpassed = append(unpassed, k)
		}
	}
	if len(unpassed) > 0 && force == "" {
		return false, fmt.Sprintf("builds before %s have not passed their gates: %s (start them first, or give a reason to override)", first, strings.Join(unpassed, ", "))
	}
	kl := make([]any, len(keys))
	for i, k := range keys {
		kl[i] = k
	}
	e.Run = pyjson.O("state", "running", "builds", kl, "started", Now(), "plane", plane)
	e.saveRun()
	e.audit(user, "start", plane, force)
	ov := ""
	if force != "" {
		ov = " (override: " + force + ")"
	}
	e.event("Run started: "+plane+ov, "info", "", nil, nil, user)
	for _, k := range append(append([]string{}, before...), keys...) { // a build with no devices is complete at once (P4-10)
		if e.bstate(k).Str("state") != "complete" && e.emptyBuild(k) {
			e.completeEmpty(k)
		}
	}
	e.KeaDirty = true
	return true, "started"
}

// emptyBuild: a build with no devices in any wave (a plane the design leaves empty; DECISIONS P4-10).
func (e *Estate) emptyBuild(k string) bool {
	for _, w := range e.BuildByKey[k].L("waves") {
		if len(w.(*pyjson.Map).L("devices")) > 0 {
			return false
		}
	}
	return true
}

// completeEmpty marks an empty build complete (nothing to sequence or gate; no gate backups: it has no devices).
func (e *Estate) completeEmpty(k string) {
	bs := e.bstate(k)
	bs.Set("state", "complete")
	bs.Set("passed", Now())
	e.saveRun()
	e.ev(fmt.Sprintf("No devices in plane %s: build marked complete", k), "info", "")
}

// StartRun starts a run of one plane or all.
func (e *Estate) StartRun(plane, user, force string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	return e.startRun(plane, user, force)
}

// Tick is one engine pass (every 2 s): the run, timeouts and Kea, then the push pass (a primary only).
func (e *Estate) Tick() {
	if e.tickLocked() {
		e.Pushes()
	}
}

func (e *Estate) tickLocked() bool {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if e.Role == "standby" {
		if e.KeaDirty {
			e.KeaDirty = false
			if _, err := e.keaApply(); err != nil {
				e.ev("engine error: "+err.Error(), "fail", "")
			}
		}
		return false
	}
	t := Now()
	if e.Run.Str("state") == "running" {
		blist := pyjson.StrList(e.Run.V("builds"))
		advanced := false
		for _, k := range blist {
			bs := e.bstate(k)
			if bs == nil {
				continue
			}
			if bs.Str("state") == "complete" {
				continue
			}
			if e.emptyBuild(k) { // no devices (a record from before P4-10): complete, never started (waves[0] would raise)
				e.completeEmpty(k)
				continue
			}
			if bs.Str("state") == "queued" {
				b := e.BuildByKey[k]
				bs.Set("state", "building")
				bs.Set("open_waves", int64(1))
				waveTS(bs).Set("0", t)
				w0 := pyIndex(b.L("waves"), 0)
				e.ev(fmt.Sprintf("Build started: %s; wave 1 open (%s)", b.Str("name"), w0.Str("name")), "info", "")
				e.KeaDirty = true
				e.saveRun()
			}
			e.advanceBuild(k, t)
			advanced = true
			break
		}
		if !advanced {
			all := true
			for _, k := range blist {
				if bs := e.bstate(k); bs == nil || bs.Str("state") != "complete" {
					all = false
				}
			}
			if all {
				e.Run.Set("state", "complete")
				e.saveRun()
				e.ev("Run complete: "+PyStr(e.Run.V("plane")), "info", "")
			}
		}
	}
	e.timeouts(t)
	if e.KeaDirty {
		e.KeaDirty = false
		if _, err := e.keaApply(); err != nil {
			e.ev("engine error: "+err.Error(), "fail", "")
		}
	}
	return true
}

func waveTS(bs *pyjson.Map) *pyjson.Map {
	m := bs.M("wave_ts")
	if m == nil {
		m = pyjson.NewMap()
		bs.Set("wave_ts", m)
	}
	return m
}

func (e *Estate) waveDevices(b *pyjson.Map, wi int) []string {
	return pyjson.StrList(b.L("waves")[wi].(*pyjson.Map).V("devices"))
}

func (e *Estate) advanceBuild(k string, t float64) {
	b, bs := e.BuildByKey[k], e.bstate(k)
	waves := b.L("waves")
	if bs.Str("state") == "building" {
		wi := int(bs.Int("open_waves")) - 1
		w := pyIndex(waves, wi)
		opened := t
		if v, ok := waveTS(bs).Get(strconv.Itoa(wi)); ok {
			opened = pyjson.Float(v)
		}
		allOK := true
		for _, n := range e.waveDevices(b, wi) {
			if !(e.settled(n) || (w.Truthy("warn_only") && t-opened > 60)) {
				allOK = false
				break
			}
		}
		if allOK {
			bs.Set("held", "")
			if wi+1 < len(waves) {
				bs.Set("open_waves", bs.Int("open_waves")+1)
				waveTS(bs).Set(strconv.Itoa(wi+1), t)
				e.ev(fmt.Sprintf("%s: wave %d open (%s)", b.Str("name"), wi+2, waves[wi+1].(*pyjson.Map).Str("name")), "info", "")
				e.KeaDirty = true
			} else {
				bs.Set("state", "gate")
				bs.Set("gate_ts", t)
				e.ev(fmt.Sprintf("%s: all waves sequenced; gate checks running", b.Str("name")), "info", "")
			}
			e.saveRun()
		} else {
			var stuck []string
			for _, n := range e.waveDevices(b, wi) {
				if f := e.flag(n); f == "failed" || f == "missing" {
					stuck = append(stuck, n)
				}
			}
			names := make([]string, len(stuck))
			for i, n := range stuck {
				names[i] = e.short(n)
			}
			held := strings.Join(names, ", ")
			if held != bs.Str("held") {
				bs.Set("held", held)
				e.saveRun()
				if held != "" {
					var what, them string
					if len(stuck) > 1 {
						what, them = "failed or missing", "them"
					} else {
						them = "it"
						if e.flag(stuck[0]) == "failed" {
							what = "failed"
						} else {
							what = "missing"
						}
					}
					e.ev(fmt.Sprintf("%s: wave %d held -- %s %s; Retry, or Quarantine to go on without %s", b.Str("name"), wi+1, held, what, them), "warn", "")
				}
			}
		}
	}
	if st := bs.Str("state"); st == "gate" || st == "blocked" {
		res := e.gateEval(k)
		var states []string
		for _, r := range res {
			states = append(states, r.Str("result"))
		}
		allPass := true
		hasBlocked := false
		for _, x := range states {
			if x != "pass" && x != "warn" && x != "confirmed" {
				allPass = false
			}
			if x == "blocked" {
				hasBlocked = true
			}
		}
		if ov := bs.M("override"); ov != nil && ov.Len() > 0 {
			e.pass(k, "override: "+PyStr(ov.V("reason")))
		} else if allPass {
			e.pass(k, "")
		} else if hasBlocked && bs.Str("state") != "blocked" {
			bs.Set("state", "blocked")
			e.saveRun()
			var parts []string
			for _, r := range res {
				if r.Str("result") == "blocked" {
					parts = append(parts, r.Str("label")+" -- "+r.Str("note"))
				}
			}
			e.ev(fmt.Sprintf("%s gate blocked: %s", b.Str("name"), strings.Join(parts, "; ")), "fail", "")
		} else if !hasBlocked && bs.Str("state") == "blocked" {
			bs.Set("state", "gate")
			e.saveRun()
		}
	}
}

func (e *Estate) pass(k, note string) {
	bs := e.bstate(k)
	bs.Set("state", "complete")
	bs.Set("passed", Now())
	e.saveRun()
	if note != "" {
		note = " (" + note + ")"
	}
	e.ev(fmt.Sprintf("Gate passed: %s%s", e.BuildByKey[k].Str("name"), note), "info", "")
	e.gateBackups(k)
}

func (e *Estate) gateEval(k string) []*pyjson.Map {
	b := e.BuildByKey[k]
	var devs, warnDevs []string
	for _, w := range b.L("waves") {
		wm := w.(*pyjson.Map)
		for _, n := range pyjson.StrList(wm.V("devices")) {
			devs = append(devs, n)
			if wm.Truthy("warn_only") {
				warnDevs = append(warnDevs, n)
			}
		}
	}
	site := e.siteCode()
	var out []*pyjson.Map
	for _, cv := range b.L("gate") {
		c := cv.(*pyjson.Map)
		r := pyjson.O("key", c.V("key"), "label", c.V("label"), "type", c.V("type"), "note", dget(c, "note", ""),
			"result", "waiting", "kind", dget(c, "kind", ""))
		var aside []string
		for _, n := range devs {
			if e.flag(n) == "quarantined" {
				aside = append(aside, n)
			}
		}
		asn := ""
		if len(aside) > 0 {
			asn = fmt.Sprintf(" (%d set aside)", len(aside))
		}
		typ := c.Str("type")
		gr := e.GateRes[[2]string{k, c.Str("key")}]
		switch {
		case typ == "devices_done":
			var hard, bad, soft []string
			for _, n := range devs {
				if !containsStr(warnDevs, n) && !containsStr(aside, n) {
					hard = append(hard, n)
				}
			}
			for _, n := range hard {
				if e.stage(n) < 6 {
					bad = append(bad, n)
				}
			}
			for _, n := range warnDevs {
				if e.stage(n) < 6 {
					soft = append(soft, n)
				}
			}
			if len(bad) > 0 {
				flagged := false
				for _, n := range bad {
					if f := e.flag(n); f == "failed" || f == "missing" || f == "down" {
						flagged = true
					}
				}
				if flagged {
					r.Set("result", "blocked")
				} else {
					r.Set("result", "running")
				}
				var parts []string
				for i, x := range bad {
					if i >= 4 {
						break
					}
					f := e.flag(x)
					if f == "" {
						f = STG[e.stage(x)]
					}
					parts = append(parts, x+" "+f)
				}
				r.Set("note", fmt.Sprintf("%d of %d done%s; %s", len(hard)-len(bad), len(hard), asn, strings.Join(parts, ", ")))
			} else if len(soft) > 0 {
				r.Set("result", "warn")
				r.Set("note", fmt.Sprintf("%d of %d done; %s not done (warn only)", len(devs)-len(soft), len(devs), strings.Join(soft, ", ")))
			} else if len(aside) > 0 {
				r.Set("result", "warn")
				r.Set("note", fmt.Sprintf("%d of %d done%s, every identity matched to %s", len(hard), len(hard), asn, site))
			} else {
				r.Set("result", "pass")
				r.Set("note", fmt.Sprintf("%d of %d done, every identity matched to %s", len(devs), len(devs), site))
			}
		case typ == "reachable":
			var live, ok []string
			for _, n := range devs {
				if !containsStr(aside, n) {
					live = append(live, n)
				}
			}
			for _, n := range live {
				if (e.stage(n) >= 5 && e.flag(n) != "down") || containsStr(warnDevs, n) {
					ok = append(ok, n)
				}
			}
			if len(ok) == len(live) {
				if len(aside) > 0 {
					r.Set("result", "warn")
				} else {
					r.Set("result", "pass")
				}
			} else {
				r.Set("result", "running")
			}
			r.Set("note", fmt.Sprintf("%d of %d answer on their management ports%s", len(ok), len(live), asn))
		case typ == "auto" && gr == nil:
			e.autoEval(k, c, r)
		case typ == "confirm" || typ == "auto":
			if gr != nil {
				r.Set("result", "confirmed")
				note := gr.V("note")
				if !pyjson.Truthy(note) {
					note = "confirmed"
				}
				r.Set("note", fmt.Sprintf("%s: %s", PyStr(gr.V("user")), PyStr(note)))
			} else if c.Truthy("warn_only") {
				r.Set("result", "warn")
			} else {
				r.Set("result", "needs confirmation")
			}
		}
		out = append(out, r)
	}
	return out
}

// autoEval shows the last automatic check result (written by the checker).
func (e *Estate) autoEval(k string, c, r *pyjson.Map) {
	a := e.autoGet(k, c.Str("key"))
	if a == nil {
		r.Set("result", "running")
		r.Set("note", "automatic check queued")
		return
	}
	res := a.V("result")
	if PyStr(res) == "unavailable" {
		res = "needs confirmation"
	}
	r.Set("result", res)
	r.Set("note", a.V("note"))
	ev := pyjson.NewMap()
	if em := a.M("evidence"); em != nil {
		for _, n := range em.Keys() {
			x := em.M(n)
			ev.Set(n, pyjson.O("ok", x.V("ok"), "note", x.V("note")))
		}
	}
	r.Set("evidence", ev)
	r.Set("kind", c.V("kind"))
}

func (e *Estate) limits(n string) (float64, *pyjson.Map) {
	var o *pyjson.Map
	if ot := e.Conf.M("os_timeouts"); ot != nil {
		o = ot.M(e.Devs[n].Str("os"))
	}
	st := e.Conf.M("stage_timeout_min").Copy()
	if o != nil {
		for _, k := range o.Keys() {
			if k != "callin" {
				st.Set(k, o.V(k))
			}
		}
	}
	callin := e.Conf.Float("callin_timeout_min")
	if o != nil {
		if v, ok := o.Get("callin"); ok {
			callin = pyjson.Float(v)
		}
	}
	return callin, st
}

func (e *Estate) timeouts(t float64) {
	fac := math.Max(1, e.Conf.Float("slow_factor"))
	if !e.Conf.Has("slow_factor") {
		fac = 2
	}
	for _, k := range e.BState.Keys() {
		bs := e.bstate(k)
		if st := bs.Str("state"); st != "building" && st != "gate" && st != "blocked" {
			continue
		}
		b := e.BuildByKey[k]
		if b == nil {
			continue
		}
		waves := b.L("waves")
		if len(waves) == 0 { // a malformed record is skipped, never raised on every tick (P4-10)
			continue
		}
		for wi := 0; wi < min(int(bs.Int("open_waves")), len(waves)); wi++ {
			opened := t
			if v, ok := waveTS(bs).Get(strconv.Itoa(wi)); ok {
				opened = pyjson.Float(v)
			}
			wm := pyIndex(waves, wi)
			for _, n := range pyjson.StrList(wm.V("devices")) {
				s, d := e.St[n], e.Devs[n]
				if (s.Str("flag") != "" && s.Str("flag") != "slow") || s.Int("stage") == 6 || d.Truthy("aggregate") {
					continue
				}
				callin, to := e.limits(n)
				if d.Str("config_signal") == "manual" && s.Int("stage") >= 1 {
					if s.Int("stage") < 4 && !strings.HasPrefix(s.Str("detail"), "waiting for operator") {
						hint := "mark configured once applied"
						if v, ok := d.Get("manual_hint"); ok {
							hint = PyStr(v)
						}
						s.Set("detail", "waiting for operator: "+hint)
						e.saveDev(n)
					}
					continue
				}
				if s.Int("stage") == 0 {
					if wm.Truthy("warn_only") {
						continue
					}
					since := math.Max(opened, truthyF(s.V("clock")))
					if t-since > callin*fac*60 {
						s.Set("flag", "missing")
						s.Set("detail", fmt.Sprintf("not called in after %s min", pyD(callin*fac)))
						e.saveDev(n)
						e.ev(fmt.Sprintf("%s: not called in after %s min", e.short(n), pyD(callin*fac)), "fail", n)
					} else if t-since > callin*60 && s.Str("flag") != "slow" {
						s.Set("flag", "slow")
						s.Set("detail", fmt.Sprintf("not called in yet (%s min); fails at %s", pyD(callin), pyD(callin*fac)))
						e.saveDev(n)
						e.ev(fmt.Sprintf("%s: not called in after %s min -- waiting (slow)", e.short(n), pyD(callin)), "warn", n)
					}
					continue
				}
				stg := s.Int("stage")
				limV := to.V(strconv.FormatInt(stg, 10))
				lim := pyjson.Float(limV)
				since := t
				if v, ok := tsMap(s).Get(strconv.FormatInt(stg, 10)); ok {
					since = pyjson.Float(v)
				}
				since = math.Max(since, truthyF(s.V("last_seen")))
				since = math.Max(since, truthyF(s.V("clock")))
				if f := s.M("fetched"); f != nil {
					for _, fk := range f.Keys() {
						since = math.Max(since, pyjson.Float(f.V(fk)))
					}
				}
				if lim != 0 && t-since > lim*fac*60 {
					e.fail(n, fmt.Sprintf("no progress past %s for %s min", STG[stg], pyD(lim*fac)))
				} else if lim != 0 && t-since > lim*60 && s.Str("flag") != "slow" {
					s.Set("flag", "slow")
					s.Set("detail", fmt.Sprintf("no progress past %s for %s min; fails at %s", STG[stg], pyD(lim), pyD(lim*fac)))
					e.saveDev(n)
					e.ev(fmt.Sprintf("%s: no progress past %s for %s min -- waiting (slow)", e.short(n), STG[stg], pyD(lim)), "warn", n)
				}
			}
		}
	}
}

func truthyF(v any) float64 {
	if !pyjson.Truthy(v) {
		return 0
	}
	return pyjson.Float(v)
}

// pyIndex is l[i] for a list of maps; out of range it fails the tick as Python does (IndexError: the engine logs
// "engine error: list index out of range" and the rest of the tick is skipped). A build with no waves no longer reaches it (P4-10).
func pyIndex(l []any, i int) *pyjson.Map {
	if i < 0 {
		i += len(l)
	}
	if i < 0 || i >= len(l) {
		panic(errIndex)
	}
	return l[i].(*pyjson.Map)
}

var errIndex = errors.New("list index out of range")
