package estate

// Tuning actions (Python: tuning_propose, tuning_apply, _tune_write, tuning_approve, tuning_rollout, tuning_profile).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

// TuningPropose records a new tuning version (values checked against the plan's ranges, steps and rules).
func (e *Estate) TuningPropose(values *pyjson.Map, note, user string) (bool, string) {
	tp := e.tuningPlan()
	if !tp.Truthy("params") {
		return false, "this design has no tunable back-end values"
	}
	P := pyjson.NewMap()
	for _, pv := range tp.L("params") {
		p := pv.(*pyjson.Map)
		P.Set(PyStr(p.V("key")), p)
	}
	vals := pyjson.NewMap()
	for _, k := range values.Keys() {
		v := values.V(k)
		if !P.Has(k) || v == nil || v == "" {
			continue
		}
		p := P.M(k)
		f, ok := pyFloatAny(v)
		if !ok {
			return false, PyStr(p.V("label")) + ": a number"
		}
		if !(pyFloatAnyZ(p.V("min")) <= f && f <= pyFloatAnyZ(p.V("max"))) {
			return false, fmt.Sprintf("%s: %s to %s %s", PyStr(p.V("label")), PyStr(p.V("min")), PyStr(p.V("max")), PyStr(p.V("unit")))
		}
		st := 0.0
		if p.Truthy("step") {
			st = pyFloatAnyZ(p.V("step"))
		}
		if st != 0 && math.Abs(f/st-pyRoundHalfEven(f/st)) > 1e-6 {
			return false, fmt.Sprintf("%s: in steps of %s %s", PyStr(p.V("label")), PyStr(p.V("step")), PyStr(p.V("unit")))
		}
		vals.Set(k, f)
	}
	full := pyjson.NewMap()
	for _, k := range P.Keys() {
		full.Set(k, P.M(k).V("base"))
	}
	full.Update(vals)
	for _, rv := range tp.L("rules") {
		rl, _ := rv.([]any)
		if len(rl) < 3 {
			continue
		}
		a, b := PyStr(rl[0]), PyStr(rl[2])
		if !(pyFloatAnyZ(full.V(a)) < pyFloatAnyZ(full.V(b))) {
			return false, fmt.Sprintf("%s must sit below %s (the ordering the procedure requires)", PyStr(P.M(a).V("label")), PyStr(P.M(b).V("label")))
		}
	}
	if vals.Len() == 0 {
		return false, "no value changed"
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	t := e.tuningState()
	v := int64(1)
	for _, x := range t.L("versions") {
		if xv := pyIntAny(x.(*pyjson.Map).V("v")); xv+1 > v {
			v = xv + 1
		}
	}
	t.Set("versions", append(t.L("versions"), pyjson.O("v", v, "values", vals, "note", note, "by", user, "ts", Now())))
	e.KVSet("tuning", t)
	det := pyjson.Dumps(vals)
	if note != "" {
		det += " -- " + note
	}
	e.audit(user, "tuning-propose", fmt.Sprintf("v%d", v), det)
	var parts []string
	for _, k := range vals.Keys() {
		parts = append(parts, fmt.Sprintf("%s %s", PyStr(P.M(k).V("label")), tfmt(vals.Float(k))))
	}
	e.event(fmt.Sprintf("Tuning v%d proposed by %s: %s", v, user, strings.Join(parts, ", ")), "info", "", nil, nil, user)
	return true, fmt.Sprintf("v%d", v)
}

// pyRoundHalfEven is Python's round(x) (banker's rounding).
func pyRoundHalfEven(x float64) float64 { return math.RoundToEven(x) }

// tuneWrite applies configuration lines to one switch with the push credential (or WRITE_USER in secrets.env).
func (e *Estate) tuneWrite(n string, lines []string, why string) (bool, string) {
	d := e.Devs[n]
	os_ := d.Str("os")
	if e.Conf.Truthy("reader_sim_dir") {
		p := filepath.Join(e.Conf.Str("reader_sim_dir"), n, "applied.txt")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			panic(pyErrno(err))
		}
		f.WriteString(strings.Join(lines, "\n") + "\n")
		f.Close()
		return true, "applied (simulated)"
	}
	e.Mu.Lock()
	var u, pw string
	if e.push.cred != nil {
		u, pw = e.push.cred[0], e.push.cred[1]
	}
	e.Mu.Unlock()
	if u == "" && pw == "" {
		v := e.Secrets()
		u, pw = v["WRITE_USER"], v["WRITE_PASSWORD"]
	}
	if u == "" || pw == "" {
		return false, "no write credential: enter the push credential (Run > Push credential), or WRITE_USER / WRITE_PASSWORD in secrets.env"
	}
	if _, ok := DT[os_]; !ok || !d.Truthy("ip") {
		return false, "no SSH writer for " + os_
	}
	// HS v1.56 r3 (DECISIONS P5-2, P5-3): inside the device's session limit; a transient login failure is retried
	s, done, err := e.dialFor(n, os_, d.Str("ip"), 22, u, pw)
	if err != nil {
		return false, "write failed: " + err.Error()
	}
	defer done()
	out, err := writeConfigOn(s, os_, lines, why)
	if err != nil {
		return false, "write failed: " + err.Error()
	}
	return out.OK, out.Note
}

// TuningApply applies a version to a scope of back-end switches (in the background unless wait).
func (e *Estate) TuningApply(v int64, scope, user string, wait bool) (bool, string) {
	e.Mu.Lock()
	vals := e.tuningValues(v)
	if vals == nil {
		e.Mu.Unlock()
		return false, fmt.Sprintf("no version v%d", v)
	}
	var devs []string
	for _, n := range e.tuningScope(scope) {
		if e.Devs[n] != nil && e.flag(n) != "quarantined" {
			devs = append(devs, n)
		}
	}
	if len(devs) == 0 {
		e.Mu.Unlock()
		return false, "no back-end switch in scope " + scope
	}
	lines, err := e.tuningLines(vals)
	if err != nil {
		e.Mu.Unlock()
		panic(err)
	}
	if len(lines) == 0 {
		e.Mu.Unlock()
		return false, "this platform has no tuning lines"
	}
	why := fmt.Sprintf("tuning v%d (%s) by %s", v, scope, user)
	e.audit(user, "tuning-apply", fmt.Sprintf("v%d", v), fmt.Sprintf("%s: %d switches", scope, len(devs)))
	e.event(fmt.Sprintf("Tuning v%d: applying to %s (%d switches) -- %s", v, scope, len(devs), user), "warn", "", nil, nil, user)
	e.Mu.Unlock()
	goFn := func() bool {
		type res struct {
			ok   bool
			note string
		}
		out := make([]res, len(devs))
		e.poolMap(len(devs), func(i int) {
			func() {
				defer func() {
					if x := recover(); x != nil {
						out[i] = res{false, fmt.Sprintf("write failed: %v", x)}
					}
				}()
				ok, note := e.tuneWrite(devs[i], lines, why)
				out[i] = res{ok, note}
			}()
		})
		e.Mu.Lock()
		defer e.Mu.Unlock()
		t := e.tuningState()
		dev := t.M("dev")
		var bad []string
		for i, n := range devs {
			var prev any = int64(0)
			if dm := dev.M(n); dm != nil {
				if pv, ok := dm.Get("v"); ok {
					prev = pv
				}
			}
			var nv any = v
			if !out[i].ok {
				nv = prev
				bad = append(bad, n)
			}
			dev.Set(n, pyjson.O("v", nv, "ts", Now(), "ok", out[i].ok, "note", out[i].note, "tried", v))
		}
		e.KVSet("tuning", t)
		failed, lvl := "", "info"
		if len(bad) > 0 {
			var sh []string
			for i, n := range bad {
				if i >= 4 {
					break
				}
				sh = append(sh, e.short(n))
			}
			failed, lvl = "; failed: "+strings.Join(sh, ", "), "fail"
		}
		e.ev(fmt.Sprintf("Tuning v%d on %s: %d of %d applied%s", v, scope, len(devs)-len(bad), len(devs), failed), lvl, "")
		e.autoPopKind("tuned", "lossless")
		return len(bad) == 0
	}
	if wait {
		return goFn(), "done"
	}
	go goFn()
	return true, fmt.Sprintf("applying v%d to %d switches (%s)", v, len(devs), scope)
}

// TuningApprove makes a version the platform profile.
func (e *Estate) TuningApprove(v int64, user string) (bool, string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if v != 0 && e.tuningValues(v) == nil {
		return false, fmt.Sprintf("no version v%d", v)
	}
	t := e.tuningState()
	t.Set("approved", pyjson.O("v", v, "by", user, "ts", Now()))
	e.KVSet("tuning", t)
	e.audit(user, "tuning-approve", fmt.Sprintf("v%d", v), "")
	e.event(fmt.Sprintf("Tuning v%d approved as the platform profile by %s", v, user), "info", "", nil, nil, user)
	e.autoPopKind("tuned")
	return true, fmt.Sprintf("approved v%d", v)
}

// TuningRollout applies the approved version pod by pod, each followed by a T2/T3 smoke test on that pod.
func (e *Estate) TuningRollout(user string) (bool, string) {
	e.Mu.Lock()
	t := e.tuningState()
	apv, ok := t.M("approved").Get("v")
	if !ok || apv == nil {
		e.Mu.Unlock()
		return false, "approve a version first"
	}
	ap := pyIntAny(apv)
	var pods []any
	for _, p := range e.tuningPods() {
		pods = append(pods, p)
	}
	if len(pods) == 0 {
		pods = []any{nil}
	}
	if pods[0] != nil && len(e.tuningScope("rest")) > 0 {
		pods = append(pods, "rest")
	}
	var acc *pyjson.Map
	for _, r := range e.Plan.L("runners") {
		if rm := r.(*pyjson.Map); rm.Str("kind") == "accept" {
			acc = rm
			break
		}
	}
	e.audit(user, "tuning-rollout", fmt.Sprintf("v%d", ap), fmt.Sprintf("%d pods", len(pods)))
	e.Mu.Unlock()
	go func() {
		st := pyjson.O("v", apv, "state", "running", "pods", pyjson.NewMap(), "by", user, "ts", Now())
		save := func() {
			t2 := e.tuningState()
			t2.Set("rollout", st)
			e.KVSet("tuning", t2)
		}
		complete := true
		for _, p := range pods {
			scope := "all"
			switch x := p.(type) {
			case string:
				scope = "rest"
			case int64:
				scope = fmt.Sprintf("pod:%d", x)
			}
			ok, _ := e.TuningApply(ap, scope, user, true)
			pk := PyStr(p)
			pm := pyjson.O("applied", ok)
			st.M("pods").Set(pk, pm)
			acmd := e.Conf.Str("accept_cmd")
			switch {
			case ok && p == "rest":
				pm.Update(pyjson.O("smoke", "not run", "note", "shared spines: covered by the next acceptance run"))
			case ok && acc != nil && e.Conf.Truthy("accept_cmd") && strings.Contains(acmd, "{POD}"):
				r := acc.Copy()
				devs := []any{}
				e.Mu.Lock()
				for _, n := range e.tuningScope(scope) {
					devs = append(devs, n)
				}
				e.Mu.Unlock()
				r.Update(pyjson.O("tests", []any{"T2", "T3"}, "devices", devs, "pod", p, "smoke", true))
				logf := filepath.Join(e.Conf.Str("state_dir"), fmt.Sprintf("runner-accept-smoke-pod%s.log", pk))
				if f, err := os.Create(logf); err == nil {
					f.Close()
				}
				sok, note, err := e.runAccept(r, e.Secrets(), logf)
				if err != nil {
					panic(err)
				}
				sm := "fail"
				if sok {
					sm = "pass"
				}
				pm.Update(pyjson.O("smoke", sm, "note", note))
				ok = ok && sok
			case ok:
				pm.Update(pyjson.O("smoke", "not run", "note", "accept_cmd has no {POD}: smoke-test this pod by hand"))
			}
			e.Mu.Lock()
			save()
			if !ok {
				st.Set("state", fmt.Sprintf("stopped at pod %s", pk))
				e.ev(fmt.Sprintf("Tuning rollout of v%d stopped at pod %s", ap, pk), "fail", "")
			}
			e.Mu.Unlock()
			if !ok {
				complete = false
				break
			}
		}
		e.Mu.Lock()
		defer e.Mu.Unlock()
		if complete {
			st.Set("state", "complete")
			e.ev(fmt.Sprintf("Tuning rollout of v%d complete (%d pods)", ap, len(pods)), "info", "")
		}
		save()
		e.autoPopKind("tuned")
	}()
	return true, fmt.Sprintf("rolling out v%d across %d pod(s)", ap, len(pods))
}

var (
	tprofTRE   = regexp.MustCompile(`^T[1-6]$`)
	tprofDrop  = regexp.MustCompile("[<>`]")
	tprofCtrl  = regexp.MustCompile(`[^\x20-\x7e]`)
	tprofIDRE  = regexp.MustCompile(`[^A-Za-z0-9._-]`)
	tprofDigit = pyRe(`\d+`)
)

func tprofClean(v any) string {
	s := ""
	if pyjson.Truthy(v) {
		s = PyStr(v)
	}
	return pyHead(tprofCtrl.ReplaceAllString(tprofDrop.ReplaceAllString(s, ""), ""), 200)
}

// canonJSON is json.dumps(o, sort_keys=True, separators=(",", ":"), ensure_ascii=False).
func canonJSON(v any) string {
	var b strings.Builder
	var enc func(v any)
	str := func(s string) {
		b.WriteByte('"')
		for _, r := range s {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				if r < 0x20 {
					fmt.Fprintf(&b, `\u%04x`, r)
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	enc = func(v any) {
		switch x := v.(type) {
		case *pyjson.Map:
			ks := append([]string(nil), x.Keys()...)
			sort.Strings(ks)
			b.WriteByte('{')
			for i, k := range ks {
				if i > 0 {
					b.WriteByte(',')
				}
				str(k)
				b.WriteByte(':')
				enc(x.V(k))
			}
			b.WriteByte('}')
		case []any:
			b.WriteByte('[')
			for i, y := range x {
				if i > 0 {
					b.WriteByte(',')
				}
				enc(y)
			}
			b.WriteByte(']')
		case string:
			str(x)
		default:
			b.WriteString(pyjson.Dumps(x))
		}
	}
	enc(v)
	return b.String()
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// TuningProfile is the approved tuning as a platform profile for the builder (design values only).
func (e *Estate) TuningProfile(user string, nic, port any) (bool, string, any) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	tp, t := e.tuningPlan(), e.tuningState()
	if !tp.Truthy("params") {
		return false, "this design has no tunable back-end values", nil
	}
	ap := t.M("approved")
	if !pyjson.Truthy(ap) {
		return false, "approve a tuning version first", nil
	}
	acc := e.acceptEval()
	conf := e.GateRes[[2]string{"be", "accept"}] != nil
	if acc.Str("result") != "pass" && !conf {
		return false, "lossless acceptance T1-T4 has not passed: " + PyStr(dget(acc, "note", "")), nil
	}
	every := e.tuningScope("all")
	d := pyjson.NewMap()
	if len(every) > 0 && e.Devs[every[0]] != nil {
		d = e.Devs[every[0]]
	}
	apv := pyIntAny(ap.V("v"))
	vals := pyjson.NewMap()
	if tv := e.tuningValues(apv); tv != nil {
		for _, k := range tv.Keys() {
			x := pyFloatAnyZ(tv.V(k))
			if x == math.Trunc(x) {
				vals.Set(k, int64(x))
			} else {
				vals.Set(k, pyRound(x, 6))
			}
		}
	}
	tests := pyjson.NewMap()
	if at := e.Results.M("accept").M("tests"); at != nil {
		for _, k := range at.Keys() {
			if tprofTRE.MatchString(k) {
				r := ""
				if m, ok := at.V(k).(*pyjson.Map); ok {
					r = PyStr(dget(m, "result", ""))
				}
				tests.Set(k, r)
			}
		}
	}
	if conf {
		for _, k := range []string{"T1", "T2", "T3", "T4"} {
			if tests.Str(k) != "pass" || !tests.Has(k) {
				tests.Set(k, "confirmed")
			}
		}
	}
	for _, k := range []string{"t5", "t6"} {
		if e.GateRes[[2]string{"be", k}] != nil {
			tests.Set(strings.ToUpper(k), "confirmed")
		}
	}
	rl := d.V("release")
	model := ""
	if v, ok := d.Get("model"); ok {
		model = PyStr(v)
	}
	rel := ""
	if rm, isM := rl.(*pyjson.Map); isM {
		rel = PyStr(dget(rm, "target", ""))
	} else if rl != nil {
		rel = PyStr(rl)
	}
	osv := dget(tp, "os", dget(d, "os", ""))
	note := ""
	found := false
	for _, x := range t.L("versions") {
		xm := x.(*pyjson.Map)
		if pyIntAny(xm.V("v")) == apv {
			note, found = PyStr(dget(xm, "note", "")), true
			break
		}
	}
	if !found && apv == 0 {
		note = "package values"
	}
	based := any("package starting values")
	if tp.Truthy("profile") {
		based = tp.V("profile")
	}
	p := pyjson.O("schema", "aidc-tuned-profile/1", "model", model, "os", osv, "release", rel, "values", vals,
		"defaults_verified", apv == 0 && !tp.Truthy("profile"), "approved", isoAny(ap.V("ts")), "approved_by_role", "admin",
		"tests", tests, "note", tprofClean(note), "nic", tprofClean(nic), "port", tprofClean(port), "based_on", based)
	dg := tprofDigit.FindAllString(rel, -1)
	if len(dg) > 2 {
		dg = dg[:2]
	}
	p.Set("id", tprofIDRE.ReplaceAllString(fmt.Sprintf("%s-%s-%s", model, strings.Join(dg, "."), sha256Hex(canonJSON(p))[:8]), "-"))
	p.Set("sha256", sha256Hex(canonJSON(p)))
	e.audit(user, "tuning-profile-export", p.Str("id"), fmt.Sprintf("v%d", apv))
	e.event(fmt.Sprintf("Tuned profile %s exported by %s (v%d)", p.Str("id"), user, apv), "info", "", nil, nil, user)
	return true, p.Str("id"), p
}

var _ = strconv.Itoa
