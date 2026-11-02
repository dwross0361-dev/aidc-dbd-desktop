package estate

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Large kv values written over and over from several goroutines (as the checker, probes and backups do) keep the
// database intact.
func TestDBLargeValues(t *testing.T) {
	p := filepath.Join(t.TempDir(), "estate.db")
	d, err := OpenDB(p)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				v := strings.Repeat(string(rune('a'+g)), 50000*(1+(i*7+g)%30))
				mu.Lock()
				d.MustExec("INSERT OR REPLACE INTO kv(k,v) VALUES(?,?)", "k"+string(rune('a'+g)), v)
				d.MustExec("INSERT INTO events(ts,level,text) VALUES(?,?,?)", 1.0, "info", v[:100])
				mu.Unlock()
			}
		}(g)
	}
	wg.Wait()
	r := d.Query("PRAGMA integrity_check")
	if got := r[0].Str("integrity_check"); got != "ok" {
		t.Fatalf("integrity: %.300s", got)
	}
}
