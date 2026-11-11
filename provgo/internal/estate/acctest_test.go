package estate

// The acceptance-script release check (provgo/acctest): scores the output of the acceptance scripts, run on a simulated
// site, with the Go engine. Runs only when ACCTEST_JOBS names a jobs file (acctest.py writes it, and the same jobs are
// scored by estate_build.py in acctest/pyjob.py); the results go to ACCTEST_OUT. A job is one estate on a plan from a
// .run: {plan, kea, conf, state, ops}; each op is a gate check on the simulated devices, or a result submitted and evaluated.

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"aidc-dbd/provgo/internal/pyjson"
)

func TestAccJob(t *testing.T) {
	jf := os.Getenv("ACCTEST_JOBS")
	if jf == "" {
		t.Skip("ACCTEST_JOBS not set (run by provgo/acctest/acctest.py)")
	}
	b, err := os.ReadFile(jf)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := pyjson.Loads(b)
	if err != nil {
		t.Fatal(err)
	}
	out := []any{}
	for _, jv := range jobs.([]any) {
		j := jv.(*pyjson.Map)
		e, err := New(j.Str("plan"), j.Str("conf"), j.Str("kea"))
		if err != nil {
			t.Fatal(err)
		}
		e.Out = nil
		if st := j.M("state"); st != nil {
			for _, n := range st.Keys() {
				if e.St[n] == nil {
					continue
				}
				m := st.M(n)
				for _, k := range m.Keys() {
					e.St[n].Set(k, m.V(k))
				}
			}
		}
		res := []any{}
		for _, ov := range j.L("ops") {
			res = append(res, accOp(e, ov.(*pyjson.Map)))
		}
		out = append(out, res)
	}
	if err := os.WriteFile(os.Getenv("ACCTEST_OUT"), []byte(pyjson.Dumps(out)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func accOp(e *Estate, o *pyjson.Map) (r *pyjson.Map) {
	defer func() {
		if x := recover(); x != nil {
			r = pyjson.O("exc", fmt.Sprintf("%v", x))
		}
	}()
	user := "acctest"
	switch o.Str("op") {
	case "check":
		ok, note, _, err := e.EvalKind(o.Str("kind"), o.Str("dev"), pyjson.NewMap())
		if err != nil {
			if ru, isU := isUnavailable(err); isU {
				return pyjson.O("unavailable", ru.Msg)
			}
			return pyjson.O("exc", err.Error())
		}
		return pyjson.O("ok", ok, "note", note)
	case "accept":
		e.Results = pyjson.NewMap()
		ok, msg := e.SubmitAcceptText(o.Str("text"), user, o.M("crit"))
		return pyjson.O("submit", []any{ok, msg}, "eval", e.acceptEval())
	case "isolation":
		e.Results = pyjson.NewMap()
		ok, msg := e.SubmitIsolationText(o.Str("text"), user)
		return pyjson.O("submit", []any{ok, msg}, "eval", e.isolationEval())
	case "inference":
		e.Results = pyjson.NewMap()
		ok, msg := e.SubmitInferenceText(o.Str("text"), user)
		return pyjson.O("submit", []any{ok, msg}, "eval", e.inferenceEval())
	case "rz":
		kind := o.Str("kind")
		if ov := o.M("plan"); ov != nil {
			rz := e.Plan.M("resilience")
			if rz == nil {
				rz = pyjson.NewMap()
				e.Plan.Set("resilience", rz)
			}
			p := rz.M(kind)
			if p == nil {
				p = pyjson.NewMap()
				rz.Set(kind, p)
			}
			for _, k := range ov.Keys() {
				p.Set(k, ov.V(k))
			}
		}
		e.Results.Delete(kind)
		if o.Has("target") {
			e.Conf.Set("TARGET_BUSBW", o.V("target"))
		}
		var ok bool
		var msg string
		switch kind {
		case "failover":
			ok, msg = e.SubmitFailoverText(o.Str("text"), user)
		case "soak":
			sw, _ := o.V("switch").(*pyjson.Map)
			ok, msg = e.SubmitSoakText(o.Str("text"), user, sw)
		default:
			ok, msg = e.SubmitAlertsText(o.Str("text"), user)
		}
		evs := []any{}
		planes := o.L("planes")
		if len(planes) == 0 {
			planes = []any{nil}
		}
		for _, pv := range planes {
			var pl []string
			if pv != nil {
				pl = pyjson.StrList(pv)
			}
			e.Mu.Lock()
			evs = append(evs, e.gateKindEval(kind, pl))
			e.Mu.Unlock()
		}
		return pyjson.O("submit", []any{ok, msg}, "eval", evs)
	}
	b, _ := json.Marshal(o.Str("op"))
	return pyjson.O("exc", "unknown op "+string(b))
}
