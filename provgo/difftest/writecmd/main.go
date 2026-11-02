// writecmd applies configuration lines (tune) or removes the DWR account (remove) on one device with the Go writer and
// prints {"ok", "note"} / {"cfg"} / {"err"} (used by difftest/writerdiff.py to compare the writer with netmiko).
package main

import (
	"encoding/json"
	"os"
	"strconv"

	"aidc-dbd/provgo/internal/estate"
)

func main() {
	if len(os.Args) < 8 {
		os.Stderr.WriteString("usage: writecmd tune|remove OS HOST PORT USER PASSWORD WHY|HOW LINE...\n")
		os.Exit(2)
	}
	port, _ := strconv.Atoi(os.Args[4])
	r := map[string]any{}
	if os.Args[1] == "tune" {
		out, err := estate.WriteConfig(os.Args[2], os.Args[3], port, os.Args[5], os.Args[6], os.Args[8:], os.Args[7])
		if err != nil {
			r["err"] = err.Error()
		} else {
			r["ok"], r["note"] = out.OK, out.Note
		}
	} else {
		cfg, err := estate.RemoveAccount(os.Args[2], os.Args[3], port, os.Args[5], os.Args[6], os.Args[8:], os.Args[7], estate.BackupCmd[os.Args[2]])
		if err != nil {
			r["err"], r["login"] = err.Error(), estate.IsLoginErr(err)
		} else {
			r["cfg"] = cfg
		}
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
