package main

// Phase 3 commands: probe-dhcp, record-vendor, release-vendor, handover-export (Python: probe_dhcp and main()).

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"aidc-dbd/provgo/internal/estate"
	"aidc-dbd/provgo/internal/pyjson"
	"aidc-dbd/provgo/internal/server"
)

// probeDHCP broadcasts one DISCOVER on the provisioning NIC; any OFFER means another DHCP server is on the segment.
func probeDHCP(nic string, wait time.Duration) int {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
	if err != nil {
		fmt.Printf("[warn] rogue-DHCP check skipped (%s)\n", pyOSErr(err))
		return 0
	}
	defer syscall.Close(fd)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
	if nic != "" {
		if err := syscall.SetsockoptString(fd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, nic+"\x00"); err != nil {
			fmt.Printf("[warn] rogue-DHCP check skipped (%s)\n", pyOSErr(err))
			return 0
		}
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: 68}); err != nil {
		fmt.Printf("[warn] rogue-DHCP check skipped (%s)\n", pyOSErr(err))
		return 0
	}
	mac := make([]byte, 6)
	xid := make([]byte, 4)
	_, _ = rand.Read(mac[1:])
	mac[0] = 0x02
	_, _ = rand.Read(xid)
	pkt := []byte{1, 1, 6, 0}
	pkt = append(pkt, xid...)
	pkt = append(pkt, 0, 0, 0x80, 0)
	pkt = append(pkt, make([]byte, 16)...)
	pkt = append(pkt, mac...)
	pkt = append(pkt, make([]byte, 10+192)...)
	pkt = append(pkt, 0x63, 0x82, 0x53, 0x63, 0x35, 0x01, 0x01, 0x37, 0x03, 0x01, 0x03, 0x06, 0xff)
	if err := syscall.Sendto(fd, pkt, 0, &syscall.SockaddrInet4{Port: 67, Addr: [4]byte{255, 255, 255, 255}}); err != nil {
		fmt.Printf("[warn] rogue-DHCP check skipped (%s)\n", pyOSErr(err))
		return 0
	}
	tv := syscall.Timeval{Usec: 500000}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	end := time.Now().Add(wait)
	type offer struct{ sid, yi string }
	var seen []offer
	idx := map[string]int{}
	buf := make([]byte, 4096)
	for time.Now().Before(end) {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			continue
		}
		d := buf[:n]
		if len(d) < 240 || string(d[4:8]) != string(xid) || d[0] != 2 {
			continue
		}
		sid := ""
		if a, ok := from.(*syscall.SockaddrInet4); ok {
			sid = net.IP(a.Addr[:]).String()
		}
		for i := 240; i < len(d) && d[i] != 255; {
			if d[i] == 0 {
				i++
				continue
			}
			if i+1 >= len(d) {
				break
			}
			if d[i] == 54 && d[i+1] == 4 && i+6 <= len(d) {
				sid = net.IP(d[i+2 : i+6]).String()
			}
			i += 2 + int(d[i+1])
		}
		yi := net.IP(d[16:20]).String()
		if k, ok := idx[sid]; ok {
			seen[k].yi = yi
		} else {
			idx[sid] = len(seen)
			seen = append(seen, offer{sid, yi})
		}
	}
	where := nic
	if where == "" {
		where = "the segment"
	}
	for _, o := range seen {
		fmt.Printf("[!] DHCP server %s offered %s on %s\n", o.sid, o.yi, where)
	}
	if len(seen) == 0 {
		fmt.Printf("[ok] no other DHCP server answered on %s\n", where)
		return 0
	}
	return 1
}

func pyOSErr(err error) string {
	if en, ok := err.(syscall.Errno); ok {
		m := en.Error()
		return fmt.Sprintf("[Errno %d] %s", int(en), strings.ToUpper(m[:1])+m[1:])
	}
	return err.Error()
}

// recordVendor records what Release DWR access removes from this host (/etc/fabric/vendor-access.json).
func recordVendor(account string) int {
	accts := []any{}
	var names []string
	for _, x := range strings.Split(account, ",") {
		x = strings.TrimSpace(x)
		if x != "" && x != "none" && x != "root" {
			accts = append(accts, x)
			names = append(names, x)
		}
	}
	sudo := []any{}
	keys := []any{}
	files, _ := filepath.Glob("/etc/sudoers.d/*")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, x := range names {
			if regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(x) + `\b`).Match(b) {
				sudo = append(sudo, f)
				break
			}
		}
	}
	for _, x := range names {
		u, err := user.Lookup(x)
		if err != nil {
			continue
		}
		ak := filepath.Join(u.HomeDir, ".ssh", "authorized_keys")
		if _, err := os.Stat(ak); err != nil {
			continue
		}
		out, _ := exec.Command("ssh-keygen", "-lf", ak).Output()
		for _, ln := range strings.Split(string(out), "\n") {
			if f := strings.Fields(ln); len(f) > 1 {
				keys = append(keys, pyjson.O("account", x, "fingerprint", f[1]))
			}
		}
	}
	rec := pyjson.O("accounts", accts, "sudoers", sudo, "ssh_keys", keys, "recorded", estate.ISO(estate.Now()))
	p := "/etc/fabric/vendor-access.json"
	if err := os.WriteFile(p, []byte(pyjson.DumpsIndent(rec, 1)), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = os.Chmod(p, 0o600)
	who := strings.Join(names, ", ")
	if who == "" {
		who = "no OS account"
	}
	fmt.Printf("[ok] DWR host access recorded for Release DWR access: %s\n", who)
	return 0
}

// cliHandover is release-vendor (interactive) and handover-export, through the running service.
func cliHandover(a *opts) int {
	conf := estate.LoadConf(a.conf)
	base := fmt.Sprintf("http://127.0.0.1:%d", conf.Int("internal_port"))
	tok := server.LocalToken(conf, false)
	post := func(path string, body *pyjson.Map, t time.Duration) (*pyjson.Map, error) {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(pyjson.Dumps(body)))
		req.Header.Set("X-Estate-Token", tok)
		req.Header.Set("Content-Type", "application/json")
		r, err := (&http.Client{Timeout: t}).Do(req)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		if len(b) == 0 {
			b = []byte("{}")
		}
		return pyjson.LoadsMap(b)
	}
	if a.cmd == "handover-export" {
		out := a.out
		if out == "" {
			out = "/root"
		}
		r, err := post("/local/handover-export", pyjson.O("out", out), 600*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if r.Truthy("ok") {
			fmt.Printf("[ok] hand-over export written: %s (give it to the customer; keep no copy)\n", estate.PyStr(r.V("msg")))
			return 0
		}
		fmt.Printf("[!] %s\n", estate.PyStr(r.V("msg")))
		return 1
	}
	in := bufio.NewReader(os.Stdin)
	ended := false // standard input ended before every answer was given (Python: EOFError): nothing is sent
	ask := func(q string) string {
		if ended {
			return ""
		}
		fmt.Print(q)
		l, err := in.ReadString('\n')
		if err != nil && l == "" {
			ended = true
		}
		return strings.TrimSpace(l)
	}
	yes := func(q string) string {
		if strings.ToLower(ask(q)) == "y" {
			return "yes"
		}
		return "no"
	}
	fmt.Println("Release DWR access removes dwr-netadmin from every device (it signs in as netadmin to do it), disables the DWR")
	fmt.Println("console logins, locks DWR's OS accounts on this host and removes their SSH keys and sudo rules, and clears the push credential.")
	tv := yes("DWR's TACACS and remote-access accounts are disabled? [y/N] ")
	bs := yes("The customer's backup system has a first backup of every device? [y/N] ")
	at := ask("Reference of DWR Networking's written attestation that it keeps no copies: ")
	pw := ""
	if !ended {
		pw, ended = readPassword(in, "netadmin password (held for this action only): ")
	}
	ty := ask("Type RELEASE to go ahead: ")
	if ended {
		fmt.Println("\n[!] input ended before every answer was given: nothing was sent")
		return 1
	}
	body := pyjson.O("tacacs_vpn", tv, "backup_system", bs, "dwr_attest", at, "password", pw, "typed", ty)
	r, err := post("/local/release-vendor", body, 30*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(estate.PyStr(r.V("msg")))
	if !r.Truthy("ok") {
		return 1
	}
	for {
		time.Sleep(5 * time.Second)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/local/handover")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		h, err := pyjson.LoadsMap(b)
		if err != nil {
			continue
		}
		rel := h.M("release")
		if rel.Str("state") == "running" {
			continue
		}
		fmt.Printf("Release DWR access: %s; dwr-netadmin removed from %s of %s device(s)\n", estate.PyStr(rel.V("state")), estate.PyStr(rel.V("removed")), estate.PyStr(rel.V("n")))
		for i, x := range rel.L("failed") {
			if i >= 20 {
				break
			}
			xm := x.(*pyjson.Map)
			fmt.Printf("  FAILED %s: %s\n", estate.PyStr(xm.V("device")), estate.PyStr(xm.V("why")))
		}
		for _, x := range rel.L("host") {
			fmt.Println("  host: " + estate.PyStr(x))
		}
		if rel.Str("state") == "done" {
			return 0
		}
		return 1
	}
}

// readPassword is getpass: from the terminal without echo, else a line of standard input. ended: no input at all (EOF).
func readPassword(in *bufio.Reader, prompt string) (string, bool) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err == nil {
		defer tty.Close()
		fmt.Fprint(tty, prompt)
		if err := exec.Command("stty", "-F", "/dev/tty", "-echo").Run(); err == nil {
			l, rerr := bufio.NewReader(tty).ReadString('\n')
			_ = exec.Command("stty", "-F", "/dev/tty", "echo").Run()
			fmt.Fprintln(tty)
			return strings.TrimRight(l, "\r\n"), rerr != nil && l == ""
		}
	}
	fmt.Fprint(os.Stderr, "Warning: Password input may be echoed.\n"+prompt)
	l, rerr := in.ReadString('\n')
	return strings.TrimRight(l, "\r\n"), rerr != nil && l == ""
}

var _ = json.Valid
