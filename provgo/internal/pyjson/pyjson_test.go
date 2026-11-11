package pyjson

import "testing"

func TestDumpsMatchesPython(t *testing.T) {
	v := []any{1730412345.123456, 1e16, 1e-5, 0.0001, 123456789012345678.0, 1.5, -0.0 * -1 * -1, float64(1 << 60), pt1() + pt2(), "é \U0001F600\x7f<&>"}
	v[6] = negZero()
	want := "[1730412345.123456, 1e+16, 1e-05, 0.0001, 1.2345678901234568e+17, 1.5, -0.0, 1.152921504606847e+18, 0.30000000000000004, \"\\u00e9\\u2028\\ud83d\\ude00\\u007f<&>\"]"
	if got := Dumps(v); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func negZero() float64 { z := 0.0; return -z }

func TestRoundTripKeepsOrderAndKinds(t *testing.T) {
	in := `{"z": 1, "a": 1.0, "m": {"b": [true, null, "x"], "a": {}}, "e": []}`
	v, err := Loads([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := Dumps(v); got != in {
		t.Fatalf("got %s", got)
	}
	if got := DumpsIndent(v, 2); got != "{\n  \"z\": 1,\n  \"a\": 1.0,\n  \"m\": {\n    \"b\": [\n      true,\n      null,\n      \"x\"\n    ],\n    \"a\": {}\n  },\n  \"e\": []\n}" {
		t.Fatalf("indent: %s", got)
	}
}

func pt1() float64 { return 0.1 }
func pt2() float64 { return 0.2 }

func TestCanon(t *testing.T) {
	v := O("b", 1.5, "a", []any{int64(2), "é", nil, true}, "A", O("z", 400.0, "y", ""))
	got, want := Canon(v), `{"A":{"y":"","z":400.0},"a":[2,"\u00e9",null,true],"b":1.5}`
	if got != want {
		t.Fatalf("Canon = %s, want %s", got, want)
	}
}
