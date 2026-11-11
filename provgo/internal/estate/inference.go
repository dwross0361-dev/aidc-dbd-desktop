package estate

// The inference load gate (builder HS-v1.98; Python: submit_inference_text, inference_eval, _run_inference). An inference
// site's front-end gate also holds until acceptance/inference_load.sh shows every tenant meeting its request rate, and its
// p99 latency where a target is set, through the published path (VIP, ingress node, worker).

import (
	"fmt"
	"sort"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

var infRE = pyRe(`(?m)^INFERENCE\s+(\S+)\s+(rps|p99|setup)\s+(PASS|FAIL)\s*(.*)$`)

// SubmitInferenceText records the inference load script's output (pasted in the console, or the runner's log).
func (e *Estate) SubmitInferenceText(text, user string) (bool, string) {
	T := pyjson.NewMap()
	for _, m := range infRE.FindAllStringSubmatch(text, -1) {
		tm, _ := T.V(m[1]).(*pyjson.Map)
		if tm == nil {
			tm = pyjson.NewMap()
			T.Set(m[1], tm)
		}
		tm.Set(m[2], pyjson.O("ok", m[3] == "PASS", "note", pyHead(pyStripU(m[4]), 300)))
	}
	if T.Len() == 0 {
		return false, "no INFERENCE lines in the text (paste the output of acceptance/inference_load.sh)"
	}
	tests := pyjson.NewMap()
	failing := 0
	for _, t := range T.Keys() {
		v := T.M(t)
		all := true
		for _, k := range v.Keys() {
			if !v.M(k).Truthy("ok") {
				all = false
			}
		}
		r := "blocked"
		if all {
			r = "pass"
		} else {
			failing++
		}
		tests.Set(t, pyjson.O("result", r, "checks", v))
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	e.Results.Set("inference", pyjson.O("tests", tests, "by", user, "ts", Now()))
	e.KVSet("results", e.Results)
	e.audit(user, "result-inference", "", fmt.Sprintf("%d tenant(s), %d failing", tests.Len(), failing))
	e.autoPopKind("inference")
	return true, "recorded"
}

func (e *Estate) inferenceEval() *pyjson.Map {
	var want []string
	p99 := map[string]bool{}
	anyP99 := false
	if P := e.Plan.M("inference"); P != nil {
		for _, x := range P.L("tenants") {
			xm := x.(*pyjson.Map)
			t := PyStr(xm.V("tenant"))
			want = append(want, t)
			p99[t] = pyjson.Truthy(xm.V("p99_ms"))
			if p99[t] {
				anyP99 = true
			}
		}
	}
	r := e.Results.M("inference")
	if !pyjson.Truthy(r) {
		return pyjson.O("result", "waiting", "note", "waiting for the inference load run (runner or uploaded results)", "evidence", pyjson.NewMap())
	}
	T := r.M("tests")
	if T == nil {
		T = pyjson.NewMap()
	}
	ev := pyjson.NewMap()
	var bad, missing []string
	if tm := T.M("-"); tm != nil { // the script stopped before any tenant (no load tool, no request body)
		if g0 := tm.M("checks"); g0 != nil && g0.Has("setup") {
			g := g0.M("setup")
			ev.Set("- setup", pyjson.O("ok", g.V("ok"), "note", g.V("note")))
			if !g.Truthy("ok") {
				bad = append(bad, "- setup")
			}
		}
	}
	var others []string
	for _, k := range T.Keys() {
		if k != "-" {
			others = append(others, k)
		}
	}
	sort.Strings(others)
	ts := want
	if len(ts) == 0 {
		ts = others
	}
	for _, t := range ts {
		var got *pyjson.Map
		if tm := T.M(t); tm != nil {
			got = tm.M("checks")
		}
		if got == nil {
			got = pyjson.NewMap()
		}
		ks := []string{"rps"}
		if p99[t] || (len(want) == 0 && got.Has("p99")) {
			ks = append(ks, "p99")
		}
		if got.Has("setup") {
			ks = append(ks, "setup")
		}
		for _, k := range ks {
			if !got.Has(k) {
				missing = append(missing, t+" "+k)
				continue
			}
			g := got.M(k)
			ev.Set(t+" "+k, pyjson.O("ok", g.V("ok"), "note", g.V("note")))
			if !g.Truthy("ok") {
				bad = append(bad, t+" "+k)
			}
		}
	}
	by := PyStr(dget(r, "by", ""))
	if len(bad) > 0 {
		sh := bad
		if len(sh) > 4 {
			sh = sh[:4]
		}
		more := ""
		if len(bad) > 4 {
			more = fmt.Sprintf(" +%d more", len(bad)-4)
		}
		return pyjson.O("result", "blocked", "note", fmt.Sprintf("%d failing: %s (%s)", len(bad), strings.Join(sh, ", ")+more, by), "evidence", ev)
	}
	if len(missing) > 0 {
		sh := missing
		if len(sh) > 4 {
			sh = sh[:4]
		}
		return pyjson.O("result", "needs confirmation", "note", fmt.Sprintf("%d check(s) not reported: %s (%s)", len(missing), strings.Join(sh, ", "), by), "evidence", ev)
	}
	n := len(want)
	if n == 0 {
		n = len(others)
	}
	p := ""
	if anyP99 {
		p = ", p99 within target"
	}
	return pyjson.O("result", "pass", "note", fmt.Sprintf("%d tenant(s): request rate met%s (%s)", n, p, by), "evidence", ev)
}

func (e *Estate) runInference(r *pyjson.Map, sec map[string]string, logf string) (bool, string, error) {
	rc, err := e.runExec(r, []string{"bash", "-c", e.sub(e.Conf.Str("inference_cmd"), sec)}, nil, logf, "")
	if err != nil {
		return false, "", err
	}
	txt := readAll(logf)
	ok, msg := e.SubmitInferenceText(txt, "runner")
	if !ok {
		return false, fmt.Sprintf("exit %d; %s", rc, msg), nil
	}
	e.Mu.Lock()
	res := e.inferenceEval()
	lvl := "warn"
	if res.Str("result") == "pass" {
		lvl = "info"
	}
	e.ev("Inference load: "+res.Str("note"), lvl, "")
	e.Mu.Unlock()
	return res.Str("result") == "pass", res.Str("note"), nil
}
