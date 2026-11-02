package estate

// Phase 3 unit tests: expected values come from Python's own functions (testdata/p3/genexp.py writes expected.json:
// email.message.EmailMessage flattened as smtplib sends it, shlex.quote, json.dumps canonical form).

import (
	"os"
	"testing"

	"aidc-dbd/provgo/internal/pyjson"
)

func loadP3(t *testing.T) *pyjson.Map {
	b, err := os.ReadFile("testdata/p3/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := pyjson.LoadsMap(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAlertMessage(t *testing.T) {
	ex := loadP3(t)
	bad := 0
	for _, x := range ex.L("messages") {
		m := x.(*pyjson.Map)
		got := string(AlertMessage(m.Str("subject"), "estate@example.net", "noc@example.net, ops@example.net", m.M("alert")))
		if got != m.Str("msg") {
			bad++
			if bad <= 3 {
				t.Errorf("subject %q:\n got: %q\nwant: %q", m.Str("subject"), got, m.Str("msg"))
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d messages differ", bad, len(ex.L("messages")))
	}
}

func TestShQuote(t *testing.T) {
	ex := loadP3(t)
	q := ex.M("quote")
	for _, k := range q.Keys() {
		if got := shQuote(k); got != q.Str(k) {
			t.Errorf("shQuote(%q) = %q, want %q", k, got, q.Str(k))
		}
	}
}

func TestCanonJSON(t *testing.T) {
	ex := loadP3(t)
	for _, x := range ex.L("canon") {
		p := x.([]any)
		if got := canonJSON(p[0]); got != p[1].(string) {
			t.Errorf("canonJSON = %q, want %q", got, p[1])
		}
	}
}

func TestMailAddrs(t *testing.T) {
	got := mailAddrs("NOC <noc@example.net>, ops@example.net,,")
	if len(got) != 2 || got[0] != "noc@example.net" || got[1] != "ops@example.net" {
		t.Errorf("mailAddrs = %q", got)
	}
}
