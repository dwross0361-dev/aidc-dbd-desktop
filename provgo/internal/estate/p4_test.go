package estate

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

// take() holds back only an incomplete UTF-8 sequence at the very end: invalid bytes earlier in the output must not
// hold back its last bytes (the prompt) -- DECISIONS P4-9.
func TestTakeUTF8Tail(t *testing.T) {
	cases := []struct {
		in, out, kept string
	}{
		{"abc\xff\xfe x\nro@sw:~$", "abc\xff\xfe x\nro@sw:~$", ""},
		{"caf\xc3", "caf", "\xc3"},
		{"caf\xc3\xa9", "caf\xc3\xa9", ""},
		{"x\xe2\x84", "x", "\xe2\x84"},
		{"x\r", "x", "\r"},
		{"\xff\xff\xff\xff", "\xff\xff\xff\xff", ""},
	}
	for _, c := range cases {
		s := &cliSession{buf: []byte(c.in)}
		got, _ := s.take()
		if string(got) != c.out || string(s.buf) != c.kept {
			t.Errorf("take(%q) = %q, kept %q; want %q, %q", c.in, got, s.buf, c.out, c.kept)
		}
	}
}

// readUntil finds a match that spans reads, and a prompt at the end of a long answer, searching only new text.
func TestReadUntilIncremental(t *testing.T) {
	s := &cliSession{os: "cumulus", notify: make(chan struct{}, 1)}
	body := strings.Repeat("{\"swp1\": {\"lldp\": {}}}\n", 40000)
	go func() {
		for i := 0; i < len(body); i += 1000 {
			j := i + 1000
			if j > len(body) {
				j = len(body)
			}
			s.mu.Lock()
			s.buf = append(s.buf, body[i:j]...)
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
		}
		for _, p := range []string{"ro@SW", "01:mgmt:", "~$"} {
			s.mu.Lock()
			s.buf = append(s.buf, p...)
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	out, err := s.readUntil(regexp.MustCompile(regexp.QuoteMeta("ro@SW01:mgmt:~$")+`[ \t]*$`), 20*time.Second)
	if err != nil || !strings.HasSuffix(out, "ro@SW01:mgmt:~$") || len(out) != len(body)+15 {
		t.Fatalf("readUntil: err %v, %d bytes", err, len(out))
	}
}

// probeInOrder settles the first `want` answering candidates in index order without waiting for later slow probes.
func TestProbeInOrder(t *testing.T) {
	slow := func(i int) bool {
		if i >= 6 {
			time.Sleep(2 * time.Second) // silent devices further down
			return false
		}
		return i%2 == 0
	}
	t0 := time.Now()
	up := probeInOrder(10, 2, slow)
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("waited %v for probes after the choice was settled", d)
	}
	want := []bool{true, false, true, false, false, false, false, false, false, false}
	for i := range want {
		if up[i] != want[i] {
			t.Fatalf("up = %v, want %v", up, want)
		}
	}
	// a silent device before the chosen ones is waited for (the choice depends on it)
	up = probeInOrder(4, 1, func(i int) bool {
		if i == 0 {
			time.Sleep(300 * time.Millisecond)
			return true
		}
		return true
	})
	if !up[0] || up[1] {
		t.Fatalf("up = %v, want the first candidate only", up)
	}
	if got := probeInOrder(3, 5, func(i int) bool { return i != 1 }); !got[0] || got[1] || !got[2] {
		t.Fatalf("all probes: %v", got)
	}
}

// emptyEstate is p2Estate's fixture with an empty build ("edge", no waves) placed before the fixture's back end,
// as the B-BE-* plans have (DECISIONS P4-10).
func emptyEstate(t *testing.T) *Estate {
	t.Helper()
	W := t.TempDir()
	fx, _ := filepath.Abs(filepath.Join(p2dir, "fixtures"))
	pb, err := os.ReadFile(filepath.Join(fx, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pyjson.LoadsMap(pb)
	if err != nil {
		t.Fatal(err)
	}
	plan.Set("builds", append([]any{pyjson.O("key", "edge", "num", int64(0), "name", "Edge-PoP", "waves", []any{}, "gate", []any{})}, plan.L("builds")...))
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(W, "plan.json"), []byte(pyjson.Dumps(plan)), 0o644))
	must(os.WriteFile(filepath.Join(W, "kea.json"), []byte(`{"Dhcp4": {"subnet4": [{"id": 1, "subnet": "127.0.0.0/8"}]}}`), 0o644))
	must(os.WriteFile(filepath.Join(W, "secrets.env"), []byte("TARGET_BUSBW=380\n"), 0o644))
	conf := pyjson.O("state_dir", filepath.Join(W, "st"), "secrets", filepath.Join(W, "secrets.env"), "web_root", filepath.Join(W, "www"),
		"bundle_dir", filepath.Join(fx, "bundle"), "reader_sim_dir", filepath.Join(W, "sim"), "kea_live", filepath.Join(W, "kea-live.conf"))
	must(os.WriteFile(filepath.Join(W, "conf.json"), []byte(pyjson.Dumps(conf)), 0o644))
	must(exec.Command("cp", "-r", filepath.Join(fx, "sim"), filepath.Join(W, "sim")).Run())
	e, err := New(filepath.Join(W, "plan.json"), filepath.Join(W, "conf.json"), filepath.Join(W, "kea.json"))
	must(err)
	e.Out = nil
	return e
}

func eventTexts(e *Estate) []string {
	var out []string
	for _, r := range e.DB.Query("SELECT text FROM events ORDER BY id") {
		out = append(out, r.Str("text"))
	}
	return out
}

func countPrefix(l []string, p string) int {
	n := 0
	for _, x := range l {
		if strings.HasPrefix(x, p) {
			n++
		}
	}
	return n
}

// P4-10 part 1: a run marks a build with no devices complete when it starts (one event), the empty build does not hold
// the builds after it, and the next tick starts the back end with no engine error.
func TestEmptyBuildCompleteAtStart(t *testing.T) {
	e := emptyEstate(t)
	if !e.emptyBuild("edge") || e.emptyBuild("be") {
		t.Fatalf("emptyBuild: edge %v, be %v", e.emptyBuild("edge"), e.emptyBuild("be"))
	}
	// the back end alone, no override: the empty edge build before it does not hold it, and is completed too
	if ok, msg := e.StartRun("be", "test", ""); !ok {
		t.Fatalf("start be: %s", msg)
	}
	if st := e.bstate("edge").Str("state"); st != "complete" || pyjson.Float(e.bstate("edge").V("passed")) == 0 {
		t.Fatalf("edge after start be: %s, passed %v", st, e.bstate("edge").V("passed"))
	}
	ev := eventTexts(e)
	if countPrefix(ev, "No devices in plane edge: build marked complete") != 1 {
		t.Fatalf("events: %q", ev)
	}
	// a run of all: already complete, no second event; the tick starts the back end
	if ok, msg := e.StartRun("all", "test", ""); !ok {
		t.Fatalf("start all: %s", msg)
	}
	e.tickLocked()
	ev = eventTexts(e)
	if countPrefix(ev, "No devices in plane") != 1 || countPrefix(ev, "Build started: Test") != 1 || countPrefix(ev, "engine error") != 0 {
		t.Fatalf("events: %q", ev)
	}
	if st := e.bstate("be").Str("state"); st != "building" {
		t.Fatalf("be: %s", st)
	}
	// saved: the run record holds edge complete (what a restart, or the other engine, reads)
	saved := e.DB.QueryOne("SELECT v FROM kv WHERE k='bstate'")
	if saved == nil || !strings.Contains(saved.Str("v"), `"edge": {"state": "complete"`) {
		t.Fatalf("saved bstate: %v", saved)
	}
}

// P4-10 part 1, a fresh plan run as all: the empty build is completed by start_run, before the first tick.
func TestEmptyBuildRunAll(t *testing.T) {
	e := emptyEstate(t)
	if ok, msg := e.StartRun("all", "test", ""); !ok {
		t.Fatalf("start all: %s", msg)
	}
	ev := eventTexts(e)
	if len(ev) < 2 || ev[len(ev)-2] != "Run started: all" || ev[len(ev)-1] != "No devices in plane edge: build marked complete" {
		t.Fatalf("events: %q", ev)
	}
	e.tickLocked()
	if st := e.bstate("be").Str("state"); st != "building" {
		t.Fatalf("be: %s (events %q)", st, eventTexts(e))
	}
}

// P4-10 part 2: a record left "building" with no waves (as HS v1.56 before the fix saved it) never raises in the tick:
// the timeouts pass skips it, and the run completes it instead of starting it.
func TestEmptyBuildTickGuard(t *testing.T) {
	e := emptyEstate(t)
	bs := e.bstate("edge")
	bs.Set("state", "building")
	bs.Set("open_waves", int64(1))
	waveTS(bs).Set("0", Now())
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("timeouts raised: %v", r)
			}
		}()
		e.timeouts(Now())
		// open_waves past the end of a build's waves is clamped, not raised on
		e.bstate("be").Set("state", "building")
		e.bstate("be").Set("open_waves", int64(5))
		e.timeouts(Now())
		e.bstate("be").Set("state", "queued")
		e.bstate("be").Set("open_waves", int64(0))
	}()
	// a run with the malformed build in it: the tick completes it and goes on to the back end
	e.Run = pyjson.O("state", "running", "builds", []any{"edge", "be"}, "started", Now(), "plane", "all")
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("tick raised: %v", r)
			}
		}()
		e.tickLocked()
	}()
	ev := eventTexts(e)
	if e.bstate("edge").Str("state") != "complete" || countPrefix(ev, "No devices in plane edge") != 1 || countPrefix(ev, "engine error") != 0 {
		t.Fatalf("edge %s, events %q", e.bstate("edge").Str("state"), ev)
	}
	if st := e.bstate("be").Str("state"); st != "building" {
		t.Fatalf("be: %s", st)
	}
}
