// readcmd runs show commands on one device with the Go reader and prints {"out": [...]} or {"err": "..."} (used by
// difftest/readerdiff.py to compare the reader with netmiko).
package main

import (
	"encoding/json"
	"os"
	"strconv"

	"aidc-dbd/provgo/internal/estate"
)

func main() {
	if len(os.Args) < 6 {
		os.Stderr.WriteString("usage: readcmd OS HOST PORT USER PASSWORD CMD...\n")
		os.Exit(2)
	}
	port, _ := strconv.Atoi(os.Args[3])
	out, err := estate.ReadDirect(os.Args[1], os.Args[2], port, os.Args[4], os.Args[5], os.Args[6:])
	r := map[string]any{"out": out}
	if err != nil {
		r = map[string]any{"err": err.Error()}
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
