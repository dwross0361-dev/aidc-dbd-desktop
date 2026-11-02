package estate

// Configuration backups and the difference view (Python: take_backup, backup_worker, gate_backups, diff).

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

// TakeBackup reads a device's configuration and keeps it when it changed. The result is "" when no backup was
// possible, else "saved F" or "unchanged since F".
func (e *Estate) TakeBackup(n, why string) string {
	d := e.Devs[n]
	cmd := BackupCmd[d.Str("os")]
	if cmd == "" {
		return ""
	}
	tt, err := e.Read(n, []string{cmd})
	if err != nil {
		if _, ok := isUnavailable(err); ok {
			return ""
		}
		e.Mu.Lock()
		e.ev(fmt.Sprintf("%s: backup failed: %s", e.short(n), err.Error()), "warn", n)
		e.Mu.Unlock()
		return ""
	}
	text := tt[0]
	if pyStripU(text) == "" {
		return ""
	}
	e.Mu.Lock()
	defer e.Mu.Unlock()
	prev := e.backups(n)
	if len(prev) > 0 {
		if old, err := readTextPy(prev[0]); err == nil && old == text {
			now := time.Now()
			_ = os.Chtimes(prev[0], now, now)
			e.ev(fmt.Sprintf("%s: configuration backed up (%s): unchanged since %s", e.short(n), why, filepath.Base(prev[0])), "info", n)
			return "unchanged since " + filepath.Base(prev[0])
		}
	}
	p := filepath.Join(e.bdir(n), time.Now().UTC().Format("20060102T150405Z")+".cfg")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		panic(err)
	}
	keep := int(pyIntAny(dget(e.Conf, "backup_keep", int64(20))))
	all := e.backups(n)
	if keep >= 0 && keep < len(all) {
		for _, old := range all[keep:] {
			_ = os.Remove(old)
		}
	}
	e.ev(fmt.Sprintf("%s: configuration backed up (%s): %s", e.short(n), why, filepath.Base(p)), "info", n)
	return "saved " + filepath.Base(p)
}

// backupPass is one pass of the backup worker.
func (e *Estate) backupPass() {
	c := e.p2s()
	e.Mu.Lock()
	ivl := e.confFloat("backup_every_h", 6) * 3600
	if e.handover().Truthy("exported") {
		ivl = 0
	}
	var due []string
	t := Now()
	for _, n := range e.DevOrder {
		if e.St[n].Int("stage") == 6 && BackupCmd[e.Devs[n].Str("os")] != "" && t >= c.backupDue[n] {
			due = append(due, n)
		}
	}
	zero := func(n string) bool { v, ok := c.backupDue[n]; return ok && v == 0 }
	if ivl <= 0 {
		var keep []string
		for _, n := range due {
			if zero(n) {
				keep = append(keep, n)
			}
		}
		due = keep
	}
	lim := 8 * e.readerWorkers()
	if lim < 20 {
		lim = 20
	}
	if len(due) > lim {
		due = due[:lim]
	}
	whys := make([]string, len(due))
	for i, n := range due {
		whys[i] = "after build"
		if len(e.backups(n)) > 0 {
			whys[i] = "scheduled"
		}
	}
	e.Mu.Unlock()
	res := make([]string, len(due))
	e.poolMap(len(due), func(i int) { res[i] = e.TakeBackup(due[i], whys[i]) })
	e.Mu.Lock()
	defer e.Mu.Unlock()
	for i, n := range due {
		if res[i] != "" {
			if ivl > 0 {
				c.backupDue[n] = Now() + ivl
			} else {
				c.backupDue[n] = Now() + 1e9
			}
		} else {
			c.backupDue[n] = Now() + 900
		}
	}
}

// BackupWorker takes scheduled, after-build and gate backups (every 30 s; a standby host takes none).
func (e *Estate) BackupWorker() {
	for {
		if e.roleNow() != "primary" {
			time.Sleep(10 * time.Second)
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "backups: %v\n", r)
				}
			}()
			e.backupPass()
		}()
		time.Sleep(30 * time.Second)
	}
}

// gateBackups queues a backup of every device of a build (its gate passed). The lock is held.
func (e *Estate) gateBackups(k string) {
	c := e.p2s()
	for _, w := range e.BuildByKey[k].L("waves") {
		for _, n := range pyjson.StrList(w.(*pyjson.Map).V("devices")) {
			c.backupDue[n] = 0
		}
	}
}

// Diff is GET /api/device/<n>/diff[?vs=prev].
func (e *Estate) Diff(n, vs string) *pyjson.Map {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	b := e.backups(n)
	base := func(i int) any {
		if i < len(b) {
			return filepath.Base(b[i])
		}
		return nil
	}
	if vs == "prev" {
		if len(b) < 2 {
			return pyjson.O("backup", base(0), "planned", nil, "vs", "prev", "diff", "", "added", int64(0), "removed", int64(0))
		}
		t1, _ := readTextPy(b[1])
		t2, _ := readTextPy(b[0])
		dl := unifiedDiff(pySplitlines(t1), pySplitlines(t2), filepath.Base(b[1]), filepath.Base(b[0]), 1)
		add, rem := 0, 0
		for _, l := range dl {
			if len(l) > 0 && l[0] == '+' && !(len(l) >= 3 && l[:3] == "+++") {
				add++
			}
			if len(l) > 0 && l[0] == '-' && !(len(l) >= 3 && l[:3] == "---") {
				rem++
			}
		}
		sh := dl
		if len(sh) > 400 {
			sh = sh[:400]
		}
		return pyjson.O("backup", base(0), "previous", base(1), "vs", "prev", "diff", joinStr(sh, "\n"), "added", int64(add), "removed", int64(rem))
	}
	pl, havePl := e.plannedText(n)
	if len(b) == 0 {
		return pyjson.O("backup", nil, "planned", havePl && pl != "", "diff", "", "added", int64(0), "removed", int64(0))
	}
	bt, _ := readTextPy(b[0])
	if !havePl {
		return pyjson.O("backup", base(0), "planned", false, "diff", "", "added", int64(0), "removed", int64(0))
	}
	os_ := e.Devs[n].Str("os")
	P, D := CfgNorm(pl, os_), CfgNorm(bt, os_)
	ign := diffDefaults[os_]
	var onlyDev, onlyPlan []string
	hidden, same := 0, 0
	for _, x := range D.Sorted() {
		if _, in := P[x]; in {
			same++
			continue
		}
		if ign != nil && ign.MatchString(x) {
			hidden++
			continue
		}
		onlyDev = append(onlyDev, x)
	}
	for _, x := range P.Sorted() {
		if _, in := D[x]; !in {
			onlyPlan = append(onlyPlan, x)
		}
	}
	lines := []string{fmt.Sprintf("--- only in the plan (%d)", len(onlyPlan))}
	for i, x := range onlyPlan {
		if i >= 300 {
			break
		}
		lines = append(lines, "- "+x)
	}
	lines = append(lines, "", fmt.Sprintf("+++ only on the device (%d)", len(onlyDev)))
	for i, x := range onlyDev {
		if i >= 300 {
			break
		}
		lines = append(lines, "+ "+x)
	}
	txt := ""
	if len(onlyPlan) > 0 || len(onlyDev) > 0 {
		txt = joinStr(lines, "\n")
	}
	bl := []any{}
	for i, x := range b {
		if i >= 10 {
			break
		}
		bl = append(bl, filepath.Base(x))
	}
	return pyjson.O("backup", base(0), "planned", true, "diff", txt, "added", int64(len(onlyDev)), "removed", int64(len(onlyPlan)),
		"same", int64(same), "defaults_hidden", int64(hidden), "backups", bl)
}

// BackupFile returns a stored backup's text (ok=false when there is none of that name).
func (e *Estate) BackupFile(n, name string) (string, bool) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	if name == "" {
		return "", false
	}
	p := filepath.Join(e.bdir(n), name)
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	t, err := readTextPy(p)
	if err != nil {
		return "", false
	}
	return t, true
}
