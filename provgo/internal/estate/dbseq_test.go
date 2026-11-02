package estate

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDBLargeValuesOneGoroutine(t *testing.T) {
	d, err := OpenDB(filepath.Join(t.TempDir(), "estate.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		for g := 0; g < 4; g++ {
			v := strings.Repeat(string(rune('a'+g)), 50000*(1+(i*7+g)%30))
			d.MustExec("INSERT OR REPLACE INTO kv(k,v) VALUES(?,?)", "k"+string(rune('a'+g)), v)
		}
	}
	if got := d.Query("PRAGMA integrity_check")[0].Str("integrity_check"); got != "ok" {
		t.Fatalf("integrity: %.200s", got)
	}
}
