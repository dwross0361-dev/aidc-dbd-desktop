// Command aidc-prov is the estate provisioning service (drop-in for estate_build.py).
package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/csv"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aidc-dbd/provgo/console"
	"aidc-dbd/provgo/internal/estate"
	"aidc-dbd/provgo/internal/pyjson"
	"aidc-dbd/provgo/internal/server"
)

var commands = []string{"serve", "init", "status", "start", "pause", "resume", "check", "user", "probe-dhcp", "promote", "result",
	"fingerprint", "lab-slice", "release-vendor", "handover-export", "record-vendor", "rma-restore"}

type opts struct {
	cmd                                            string
	args                                           []string
	plan, conf, keaTemplate, console               string
	plane, reason, role, planes, org, out, account string
	device, adminPass, nic, macs                   string
	readiness                                      bool
}

const usage = "usage: aidc-prov [-h] [--plan PLAN] [--conf CONF] [--kea-template KEA_TEMPLATE] [--console CONSOLE]\n" +
	"                 [--plane {oob,edge,fe,be,all}] [--reason REASON] [--role {admin,operator,viewer}] [--planes PLANES]\n" +
	"                 [--readiness] [--org {customer,dwr}] [--out OUT] [--account ACCOUNT] [--device DEVICE]\n" +
	"                 [--admin-pass ADMIN_PASS] [--nic NIC] [--macs MACS]\n" +
	"                 [{" + "serve,init,status,start,pause,resume,check,user,probe-dhcp,promote,result,fingerprint,lab-slice,release-vendor,handover-export,record-vendor,rma-restore" + "}] [args ...]\n"

func die(msg string) {
	fmt.Fprint(os.Stderr, usage)
	fmt.Fprintf(os.Stderr, "aidc-prov: error: %s\n", msg)
	os.Exit(2)
}

func choice(flag, v string, allowed ...string) string {
	for _, a := range allowed {
		if a == v {
			return v
		}
	}
	q := make([]string, len(allowed))
	for i, a := range allowed {
		q[i] = "'" + a + "'"
	}
	die(fmt.Sprintf("argument %s: invalid choice: '%s' (choose from %s)", flag, v, strings.Join(q, ", ")))
	return ""
}

func parseArgs(argv []string) *opts {
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	here := filepath.Dir(exe)
	o := &opts{plan: filepath.Join(here, "estate.json"), conf: "/etc/fabric/estate.conf", keaTemplate: filepath.Join(here, "kea-dhcp4.json"),
		plane: "all", role: "admin", org: "customer", adminPass: os.Getenv("ESTATE_ADMIN_PASS")}
	str := map[string]*string{"--plan": &o.plan, "--conf": &o.conf, "--kea-template": &o.keaTemplate, "--console": &o.console,
		"--plane": &o.plane, "--reason": &o.reason, "--role": &o.role, "--planes": &o.planes, "--org": &o.org, "--out": &o.out,
		"--account": &o.account, "--device": &o.device, "--admin-pass": &o.adminPass, "--nic": &o.nic, "--macs": &o.macs}
	var pos []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "-h" || a == "--help" {
			fmt.Print(usage)
			os.Exit(0)
		}
		if a == "--readiness" {
			o.readiness = true
			continue
		}
		if strings.HasPrefix(a, "--") {
			k, v, has := strings.Cut(a, "=")
			p, ok := str[k]
			if !ok {
				die("unrecognized arguments: " + a)
			}
			if !has {
				if i+1 >= len(argv) {
					die(fmt.Sprintf("argument %s: expected one argument", k))
				}
				i++
				v = argv[i]
			}
			*p = v
			continue
		}
		pos = append(pos, a)
	}
	choice("--plane", o.plane, "oob", "edge", "fe", "be", "all")
	choice("--role", o.role, "admin", "operator", "viewer")
	choice("--org", o.org, "customer", "dwr")
	o.cmd = "serve"
	if len(pos) > 0 {
		o.cmd = choice("cmd", pos[0], commands...)
		o.args = pos[1:]
	}
	return o
}

func main() { os.Exit(run(os.Args[1:])) }

func run(argv []string) int {
	a := parseArgs(argv)
	switch a.cmd {
	case "fingerprint":
		conf := estate.LoadConf(a.conf)
		b, err := os.ReadFile(conf.Str("tls_cert"))
		var der []byte
		if err == nil {
			if blk, _ := pem.Decode(b); blk != nil && blk.Type == "CERTIFICATE" {
				der = blk.Bytes
			} else {
				err = errors.New("Failed to read PEM certificate")
			}
		}
		if err != nil {
			fmt.Printf("cannot read %s: %s\n", conf.Str("tls_cert"), err)
			return 1
		}
		s := sha256.Sum256(der)
		fmt.Println(hex.EncodeToString(s[:]))
		return 0
	case "probe-dhcp":
		return probeDHCP(a.nic, 4*time.Second)
	case "record-vendor":
		return recordVendor(a.account)
	case "release-vendor", "handover-export":
		return cliHandover(a)
	case "status", "start", "pause", "resume", "lab-slice", "result", "promote":
		return cliLocal(a)
	}
	est, err := estate.New(a.plan, a.conf, a.keaTemplate)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	switch a.cmd {
	case "rma-restore":
		if len(a.args) == 0 || a.device == "" {
			fmt.Println("usage: estate_build.py rma-restore <hand-over export .zip> --device NAME")
			return 2
		}
		ok, msg := est.RestoreAsbuilt(a.args[0], a.device, "cli:root")
		if ok {
			fmt.Println("[ok] " + msg)
			return 0
		}
		fmt.Println("[!] " + msg)
		return 1
	case "check":
		if a.readiness || (len(a.args) > 0 && a.args[0] == "readiness") {
			r := est.Readiness()
			for _, iv := range r.L("items") {
				i := iv.(*pyjson.Map)
				note := ""
				if i.Str("note") != "" {
					note = "  -- " + i.Str("note")
				}
				fmt.Printf("%-10s %s%s\n", strings.ToUpper(i.Str("result")), i.Str("label"), note)
			}
			if r.Truthy("ok") {
				fmt.Println("Ready: Start build is open.")
				return 0
			}
			fmt.Printf("Start build is held: %d item(s) fail or wait for confirmation (console: Readiness)\n", r.Int("fails"))
			return 1
		}
		return est.Preflight(os.Stdout)
	case "init":
		if est.UserCount() > 0 {
			fmt.Println("logins already exist; use 'user add NAME' or 'user reset NAME'")
			return 0
		}
		if a.adminPass != "" {
			if why := estate.PWRule("admin", a.adminPass, nil); why != "" {
				fmt.Printf("initial admin password: %s\n", why)
				return 2
			}
			if _, err := est.UserAdd("admin", "admin", a.adminPass, "", true, "dwr"); err != nil {
				fmt.Println(err)
				return 1
			}
			fmt.Println("Console login  user: admin   password: as given   (must be changed at first login)")
		} else {
			pw, err := est.UserAdd("admin", "admin", "", "", true, "dwr")
			if err != nil {
				fmt.Println(err)
				return 1
			}
			fmt.Printf("Console login  user: admin   password: %s   (temporary: must be changed at first login)\n", pw)
		}
		fmt.Println("Add a second admin and personal logins per plane: estate_build.py user add NAME --role operator --planes fe,edge")
		return 0
	case "user":
		return cliUser(est, a)
	}
	return serve(est, a)
}

func cliUser(est *estate.Estate, a *opts) int {
	sub := ""
	if len(a.args) > 0 {
		sub = a.args[0]
	}
	if sub == "list" {
		v := est.UsersView()
		for _, uv := range v.L("users") {
			u := uv.(*pyjson.Map)
			pl := strings.Join(pyjson.StrList(u.V("planes")), ",")
			if pl == "" {
				if u.Str("role") == "admin" {
					pl = "all"
				} else {
					pl = "-"
				}
			}
			dis, mc := "", ""
			if u.Truthy("disabled") {
				dis = "disabled "
			}
			if u.Truthy("must_change") {
				mc = "change-required"
			}
			fmt.Printf("%-20s %-8s %-16s %s%s\n", u.Str("name"), u.Str("role"), pl, dis, mc)
		}
		cv := v.M("coverage")
		pp := cv.M("per_plane")
		var parts []string
		for _, k := range pp.Keys() {
			parts = append(parts, fmt.Sprintf("%s %d", k, pp.Int(k)))
		}
		warn := ""
		if sh := pyjson.StrList(cv.V("short")); len(sh) > 0 {
			warn = "   WARNING: fewer than two on " + strings.Join(sh, ", ")
		}
		fmt.Println("people per plane: " + strings.Join(parts, ", ") + warn)
		return 0
	}
	if (sub == "add" || sub == "reset" || sub == "disable" || sub == "enable") && len(a.args) >= 2 {
		name := a.args[1]
		switch sub {
		case "add":
			pw, err := est.UserAdd(name, a.role, "", a.planes, true, a.org)
			if err != nil {
				fmt.Println(err)
				return 2
			}
			est.Audit("cli:root", "user-add", name, fmt.Sprintf("role=%s planes=%s", a.role, a.planes))
			fmt.Printf("temporary password for %s: %s   (must be changed at first login)\n", name, pw)
			return 0
		case "reset":
			ok, res := est.UserReset("cli:root", name)
			if ok {
				fmt.Printf("temporary password for %s: %s   (must be changed at first login; their sessions ended)\n", name, res)
				return 0
			}
			fmt.Println(res)
			return 1
		}
		ok, msg := est.UserUpdate("cli:root", name, nil, nil, sub == "disable", nil)
		fmt.Println(msg)
		if ok {
			return 0
		}
		return 1
	}
	fmt.Println("usage: user add NAME [--role viewer|operator|admin] [--planes oob,edge,fe,be] | user reset NAME | user disable|enable NAME | user list")
	return 2
}

func cliLocal(a *opts) int {
	conf := estate.LoadConf(a.conf)
	base := fmt.Sprintf("http://127.0.0.1:%d", conf.Int("internal_port"))
	tok := server.LocalToken(conf, false)
	cl := &http.Client{Timeout: 10 * time.Second}
	unreachable := func(err error) int {
		fmt.Printf("service not reachable on %s: %s\n", base, err)
		return 1
	}
	if a.cmd == "status" {
		cl.Timeout = 5 * time.Second
		r, err := cl.Get(base + "/local/status")
		if err != nil {
			return unreachable(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		fmt.Println(string(b))
		if r.StatusCode >= 400 {
			return 1
		}
		return 0
	}
	body := pyjson.O("plane", a.plane, "reason", a.reason)
	if a.cmd == "result" {
		// Python: float(a.args[0]), float(a.args[1]); a missing or bad value ends with a traceback (exit 1)
		var vals [2]float64
		for i := 0; i < 2; i++ {
			if i >= len(a.args) {
				fmt.Fprintln(os.Stderr, "Traceback (most recent call last):\nIndexError: list index out of range")
				return 1
			}
			f, ok := estate.PyFloat(a.args[i])
			if !ok {
				fmt.Fprintf(os.Stderr, "Traceback (most recent call last):\nValueError: could not convert string to float: %s\n", estate.PyRepr(a.args[i]))
				return 1
			}
			vals[i] = f
		}
		body = pyjson.O("kind", "nccl", "busbw", vals[0], "target", vals[1])
	}
	if a.cmd == "lab-slice" {
		if len(a.args) == 0 {
			fmt.Println("usage: estate_build.py lab-slice air/slice.csv [--reason TEXT]")
			return 2
		}
		f, err := os.Open(a.args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		rd := csv.NewReader(f)
		rd.FieldsPerRecord = -1
		rows, _ := rd.ReadAll()
		f.Close()
		keep := []any{}
		if len(rows) > 0 {
			col := -1
			for i, h := range rows[0] {
				if h == "node" {
					col = i
				}
			}
			for _, r := range rows[1:] {
				if col >= 0 && col < len(r) && r[col] != "" {
					keep = append(keep, r[col])
				}
			}
		}
		macs := pyjson.NewMap()
		if a.macs != "" {
			if b, err := os.ReadFile(a.macs); err == nil {
				for _, ln := range strings.Split(string(b), "\n") {
					p := strings.Fields(strings.ReplaceAll(ln, ",", " "))
					if len(p) >= 2 && p[0] != "node" {
						macs.Set(p[0], p[1])
					}
				}
			}
		}
		reason := a.reason
		if reason == "" {
			reason = fmt.Sprintf("lab: outside the test slice (%s)", filepath.Base(a.args[0]))
		}
		body = pyjson.O("keep", keep, "macs", macs, "reason", reason)
	}
	req, _ := http.NewRequest("POST", base+"/local/"+a.cmd, strings.NewReader(pyjson.Dumps(body)))
	req.Header.Set("X-Estate-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	r, err := cl.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	fmt.Println(string(b))
	if r.StatusCode >= 400 {
		return 1
	}
	return 0
}

func serve(est *estate.Estate, a *opts) int {
	html := console.HTML
	if a.console == "" { // as Python: console.html next to the program when it is there (setup.sh installs it); else the embedded copy
		exe, _ := os.Executable()
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		if p := filepath.Join(filepath.Dir(exe), "console.html"); isFile(p) {
			a.console = p
		}
	}
	if a.console != "" {
		b, err := os.ReadFile(a.console)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		html = []byte(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n"))
	}
	c := est.Conf
	bl, baddr, err := server.Listen(est, "http_bind", c.Int("http_port"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	il, err := net4Listen("127.0.0.1:" + strconv.FormatInt(c.Int("internal_port"), 10))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	internal := server.InternalHandler(est)
	cl, caddr, err := server.Listen(est, "console_bind", c.Int("console_port"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	scheme := "http"
	var tlsConf *tls.Config
	if isFile(c.Str("tls_cert")) && isFile(c.Str("tls_key")) {
		cert, err := tls.LoadX509KeyPair(c.Str("tls_cert"), c.Str("tls_key"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		tlsConf = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		scheme = "https"
	} else {
		est.Event(fmt.Sprintf("console TLS certificate missing (%s): serving plain HTTP", c.Str("tls_cert")), "warn")
	}
	go server.NewServer(server.BootHandler(est), nil).Serve(bl)
	go server.NewServer(internal, nil).Serve(il)
	cs := server.NewServer(server.ConsoleHandler(est, html, tlsConf != nil), tlsConf)
	if tlsConf != nil {
		go cs.Serve(tls.NewListener(cl, tlsConf))
	} else {
		go cs.Serve(cl)
	}
	go est.AlertWorker()
	go est.Checker()
	go est.BackupWorker()
	go est.RunnerWorker()
	go est.SyncWorker()
	if c.Truthy("sites") {
		go est.ForeignWorker()
	}
	if n, auto := est.PushConcurrency(); auto {
		fmt.Fprintf(os.Stderr, "push concurrency: %d at once (sized by this engine: no push_concurrency in estate.conf)\n", n)
	} else {
		fmt.Fprintf(os.Stderr, "push concurrency: %d at once (push_concurrency)\n", n)
	}
	tftpOn := true
	if v, ok := c.Get("tftp"); ok {
		tftpOn = pyjson.Truthy(v)
	}
	if (tftpOn && est.Role == "primary") || pyjson.Truthy(c.V("tftp_standby")) {
		go est.TFTPServer()
	}
	go est.Prober()
	if pyjson.Truthy(c.V("sniff")) {
		go estate.Sniffer(est, c.Str("prov_nic"))
	}
	// SIGTERM (systemctl stop) or SIGINT: finish the statement in progress, close the database, exit (DECISIONS P4-3)
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigc
		est.Mu.Lock()
		est.DB.Close()
		os.Exit(0)
	}()
	est.Event(fmt.Sprintf("Service %s up: bootstraps on http://%s:%d, console on %s://%s:%d, plan digest %s",
		estate.Version, baddr, c.Int("http_port"), scheme, caddr, c.Int("console_port"), est.Digest[:12]), "info")
	for {
		func() {
			defer func() {
				if x := recover(); x != nil {
					fmt.Fprintf(os.Stderr, "engine: %v\n", x)
					est.Event(fmt.Sprintf("engine error: %v", x), "fail")
				}
			}()
			est.Tick()
		}()
		time.Sleep(2 * time.Second)
	}
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func net4Listen(addr string) (net.Listener, error) { return net.Listen("tcp4", addr) }
