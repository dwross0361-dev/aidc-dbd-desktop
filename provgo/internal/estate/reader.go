package estate

// The device reader: show commands over SSH, the way the Python service reads them with netmiko (an interactive
// shell with a PTY, the platform's session preparation, the command echo and the trailing prompt stripped), or from
// recorded outputs when reader_sim_dir is set.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// ReaderUnavailable means the check cannot read this device (no reader, no credential, no recorded output): the
// gate check falls back to a confirmation by hand.
type ReaderUnavailable struct{ Msg string }

func (r *ReaderUnavailable) Error() string { return r.Msg }

func unavailable(f string, a ...any) error { return &ReaderUnavailable{fmt.Sprintf(f, a...)} }

func isUnavailable(err error) (*ReaderUnavailable, bool) {
	var ru *ReaderUnavailable
	ok := errors.As(err, &ru)
	return ru, ok
}

// Reader timeouts (Python: netmiko conn_timeout=15, banner_timeout=60, auth_timeout=90 for the TACACS fall-back,
// read_timeout=120 per command).
var (
	ReaderConnTimeout   = 15 * time.Second
	ReaderAuthTimeout   = 90 * time.Second
	ReaderBannerTimeout = 60 * time.Second
	ReaderCmdTimeout    = 120 * time.Second
	ReaderPrepTimeout   = 20 * time.Second
)

// Read runs the commands on a device and returns their outputs (Python: Phase2Mixin.read).
func (e *Estate) Read(name string, cmds []string) ([]string, error) {
	d := e.Devs[name]
	os_ := d.Str("os")
	if sim := e.Conf.Str("reader_sim_dir"); e.Conf.Truthy("reader_sim_dir") {
		out := make([]string, 0, len(cmds))
		for _, c := range cmds {
			p := filepath.Join(sim, name, Slug(c)+".txt")
			if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
				return nil, unavailable("no recorded output for %s: %s", name, c)
			}
			t, err := readTextPy(p)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		return out, nil
	}
	if _, ok := DT[os_]; !ok || !d.Truthy("ip") {
		return nil, unavailable("no SSH reader for %s", os_)
	}
	v := e.Secrets()
	u := v["READ_USER_"+strings.ToUpper(os_)]
	if u == "" {
		u = v["READ_USER"]
	}
	p := v["READ_PASSWORD_"+strings.ToUpper(os_)]
	if p == "" {
		p = v["READ_PASSWORD"]
	}
	if u == "" || p == "" {
		return nil, unavailable("READ_USER / READ_PASSWORD not set in secrets.env")
	}
	// HS v1.56 r3 (DECISIONS P5-2, P5-3): inside the device's session limit, a session of this pass reused, transient
	// failures retried
	var out []string
	err := e.sshRetry(name, func() error {
		var err error
		out, err = e.readOnce(name, os_, d.Str("ip"), 22, u, p, cmds)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Estate) readOnce(name, os_, host string, port int, u, p string, cmds []string) ([]string, error) {
	g, key := e.gate(), "read:"+u
	var s *cliSession
	if c := g.take(name, key, e.sshPerDevice()); c != nil && c.(*cliSession).alive() {
		s = c.(*cliSession)
	} else {
		if c != nil { // held for reuse but closed by the device since: its slot serves a new session
			c.close()
		}
		x, err := dialSession(os_, host, port, u, p)
		if err != nil {
			g.release(name)
			return nil, err
		}
		s = x
	}
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		t, err := s.sendCommand(c, ReaderCmdTimeout)
		if err != nil {
			g.give(name, key, s, false)
			return nil, err
		}
		out = append(out, t)
	}
	g.give(name, key, s, true)
	return out, nil
}

// phaseConn notes when the server's first bytes (its banner) arrive and then gives key exchange and authentication
// their own deadline (paramiko: banner_timeout, then auth_timeout).
// The banner counts once a line starting "SSH-" has arrived: lines before it (OpenSSH's "Exceeded MaxStartups" before it
// drops a connection) are not a banner, as paramiko sees it.
type phaseConn struct {
	net.Conn
	mu   sync.Mutex
	got  bool
	head []byte // what arrived before the version line (bounded)
}

func (c *phaseConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mu.Lock()
		if !c.got {
			c.head = append(c.head, b[:n]...)
			if bytes.HasPrefix(c.head, []byte("SSH-")) || bytes.Contains(c.head, []byte("\nSSH-")) {
				c.got = true
				c.head = nil
				_ = c.Conn.SetDeadline(time.Now().Add(ReaderAuthTimeout))
			} else if len(c.head) > 8192 {
				c.head = c.head[len(c.head)-8192:]
			}
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *phaseConn) banner() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got
}

// ------------------------------------------------------------------------------------- interactive session

type cliSession struct {
	os         string
	devType    string
	host       string
	port       int
	client     *ssh.Client
	sess       *ssh.Session
	stdin      interface{ Write([]byte) (int, error) }
	mu         sync.Mutex
	buf        []byte // raw bytes not yet consumed
	notify     chan struct{}
	closed     bool
	basePrompt string
}

func tcpFailMsg(devType, host string, port int) string {
	return fmt.Sprintf("TCP connection to device failed.\n\nCommon causes of this problem are:\n1. Incorrect hostname or IP address.\n"+
		"2. Wrong TCP port.\n3. Intermediate firewall blocking access.\n\nDevice settings: %s %s:%d\n\n", devType, host, port)
}

func authFailMsg(devType, host string, port int) string {
	return fmt.Sprintf("Authentication to device failed.\n\nCommon causes of this problem are:\n1. Invalid username and password\n"+
		"2. Incorrect SSH-key file\n3. Connecting to the wrong device\n\nDevice settings: %s %s:%d\n\n\nAuthentication failed.", devType, host, port)
}

func dialSession(os_, host string, port int, user, pw string) (*cliSession, error) {
	devType := DT[os_]
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, ReaderConnTimeout)
	if err != nil {
		return nil, errors.New(tcpFailMsg(devType, host, port))
	}
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pw),
			// TACACS-backed logins often offer keyboard-interactive only; paramiko falls back to it with the password
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				a := make([]string, len(qs))
				for i := range a {
					a[i] = pw
				}
				return a, nil
			}),
		},
		// netmiko connects with paramiko's AutoAddPolicy: any host key is accepted
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         ReaderConnTimeout,
	}
	// HS v1.56 r3 (DECISIONS P5-3): paramiko's phases -- the server's banner within banner_timeout (60 s), then key
	// exchange and authentication within auth_timeout (90 s; a TACACS fall-back takes time) -- and its wording for each
	pc := &phaseConn{Conn: conn}
	_ = conn.SetDeadline(time.Now().Add(ReaderBannerTimeout))
	c, chans, reqs, err := ssh.NewClientConn(pc, addr, cfg)
	if err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, errors.New(authFailMsg(devType, host, port))
		}
		var ne net.Error
		timeout := errors.As(err, &ne) && ne.Timeout()
		if !pc.banner() {
			why := ""
			if strings.Contains(err.Error(), "connection reset") {
				why = "[Errno 104] Connection reset by peer"
			}
			if timeout || why != "" || strings.Contains(err.Error(), "EOF") {
				return nil, errors.New("\nA paramiko SSHException occurred during connection creation:\n\nError reading SSH protocol banner" + why + "\n\n")
			}
		} else if timeout {
			return nil, errors.New(strings.TrimSuffix(authFailMsg(devType, host, port), "Authentication failed.") + "Authentication timeout.")
		}
		return nil, fmt.Errorf("\nA paramiko SSHException occurred during connection creation:\n\n%v\n\n", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	w, h := 511, 1000
	if os_ == "iosxr" {
		h = 511
	}
	if err := sess.RequestPty("vt100", h, w, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		sess.Close()
		client.Close()
		return nil, err
	}
	s := &cliSession{os: os_, devType: devType, host: host, port: port, client: client, sess: sess, stdin: stdin, notify: make(chan struct{}, 1)}
	go func() {
		b := make([]byte, 32768)
		for {
			n, err := stdout.Read(b)
			s.mu.Lock()
			if n > 0 {
				s.buf = append(s.buf, b[:n]...)
			}
			if err != nil {
				s.closed = true
			}
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	if err := s.prepare(); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// alive: the device has not closed the session (one held for reuse is checked before it is used again).
func (s *cliSession) alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

func (s *cliSession) close() {
	s.write(cleanupCmd(s.os) + "\n")
	time.Sleep(20 * time.Millisecond)
	s.sess.Close()
	s.client.Close()
}

func cleanupCmd(os_ string) string {
	if os_ == "sros" {
		return "logout"
	}
	return "exit"
}

func (s *cliSession) write(t string) { _, _ = s.stdin.Write([]byte(t)) }

var (
	crlfBase = regexp.MustCompile("\r\r\r\n|\r\r\n|\r\n|\n\r")
	crlfNXOS = regexp.MustCompile("\r\r\n\r|\r\r\n|\r\n")
	ansiRE   = regexp.MustCompile("\x1b\\[[0-9;?]*[A-Za-z]|\x1b[EDM78]")
)

// normalize is netmiko's read_channel post-processing: line feeds to \n and, where the platform asks for it, ANSI
// escape codes removed.
func (s *cliSession) normalize(b []byte) string {
	t := string(bytes.ToValidUTF8(b, []byte{}))
	if s.os == "nxos" {
		t = strings.ReplaceAll(crlfNXOS.ReplaceAllString(t, "\n"), "\r", "\n")
	} else {
		t = strings.ReplaceAll(crlfBase.ReplaceAllString(t, "\n"), "\r", "\n")
	}
	switch s.os {
	case "eos", "cumulus", "nxos", "srl":
		t = ansiRE.ReplaceAllString(t, "")
	}
	return t
}

// take removes the buffered bytes (a trailing \r is kept back until more data arrives, as netmiko does).
func (s *cliSession) take() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buf
	keep := 0
	// a trailing \r may be the first half of \r\n: keep it until the next byte (prompts never end in \r)
	if len(b) > 0 && b[len(b)-1] == '\r' && !s.closed {
		keep = 1
	}
	// do not split a UTF-8 sequence: an incomplete one at the end waits for its last bytes. Only the tail is looked at;
	// invalid bytes earlier in the output must not hold back its end (the prompt) -- DECISIONS P4-9
	end := len(b) - keep
	for i := end - 1; i >= 0 && i >= end-3; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:end]) {
				keep = len(b) - i
			}
			break
		}
	}
	out := append([]byte(nil), b[:len(b)-keep]...)
	s.buf = append([]byte(nil), b[len(b)-keep:]...)
	return out, s.closed
}

var errTimeout = errors.New("timeout")

// readUntil reads (normalized) until re matches the text read so far.
func (s *cliSession) readUntil(re *regexp.Regexp, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var acc strings.Builder
	scanned := 0 // what earlier reads already searched; only the new text and a tail before it are searched again
	for {
		b, closed := s.take()
		if len(b) > 0 {
			acc.WriteString(s.normalize(b))
			t := acc.String()
			// none of the patterns read for is anchored at the start of the text or longer than readOverlap, so a match
			// not found before ends in the new text: searching from readOverlap bytes back is enough (DECISIONS P4-9;
			// searching the whole output after every read is quadratic in its size)
			from := scanned - readOverlap
			if from < 0 {
				from = 0
			}
			if re.MatchString(t[from:]) {
				return t, nil
			}
			scanned = len(t)
		}
		if closed {
			return acc.String(), errors.New("Socket is closed")
		}
		left := time.Until(deadline)
		if left <= 0 {
			return acc.String(), errTimeout
		}
		select {
		case <-s.notify:
		case <-time.After(minDur(left, 200*time.Millisecond)):
		}
	}
}

// readOverlap is how far back readUntil searches into text it has already searched (longer than any prompt or command echo).
const readOverlap = 8192

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// drain reads what arrives until the channel is quiet (clear_buffer).
func (s *cliSession) drain() string {
	var acc strings.Builder
	quiet := 100 * time.Millisecond
	for i := 0; i < 10; i++ {
		select {
		case <-s.notify:
		case <-time.After(quiet):
		}
		b, _ := s.take()
		if len(b) == 0 {
			break
		}
		acc.WriteString(s.normalize(b))
		quiet *= 2
		if quiet > 3*time.Second {
			quiet = 3 * time.Second
		}
	}
	return acc.String()
}

func (s *cliSession) promptPattern() *regexp.Regexp {
	switch s.os {
	case "junos":
		return regexp.MustCompile(`[%>$#]`)
	case "eos":
		return regexp.MustCompile(`[$>#]`)
	case "cumulus":
		return regexp.MustCompile(`[$#]`)
	case "srl":
		return regexp.MustCompile(`#`)
	}
	return regexp.MustCompile(`[>#]`)
}

func (s *cliSession) basePattern() *regexp.Regexp {
	switch s.os {
	case "cumulus":
		return regexp.MustCompile(`[$#]`)
	case "srl":
		return regexp.MustCompile(`#`)
	case "eos":
		return regexp.MustCompile(`[$>#]`)
	}
	return regexp.MustCompile(`(?:#|>)`)
}

// prepCmd sends one session-preparation command and reads until want (a timeout is tolerated where netmiko
// tolerates it; otherwise it fails the read).
func (s *cliSession) prepCmd(cmd string, want *regexp.Regexp, tolerate bool) error {
	s.write(cmd + "\n")
	if want == nil {
		want = regexp.MustCompile(regexp.QuoteMeta(cmd))
	}
	if _, err := s.readUntil(want, 10*time.Second); err != nil && !tolerate {
		return fmt.Errorf("Pattern not detected: %s in output.", pyStrRepr(want.String()))
	}
	return nil
}

func (s *cliSession) prepare() error {
	s.write("\n")
	time.Sleep(100 * time.Millisecond)
	if s.os != "iosxe" {
		if _, err := s.readUntil(s.promptPattern(), ReaderPrepTimeout); err != nil {
			if err == errTimeout {
				return fmt.Errorf("Pattern not detected: %s in output.", pyStrRepr(s.promptPattern().String()))
			}
			return err
		}
	}
	switch s.os {
	case "junos":
		if p, err := s.findPrompt(regexp.MustCompile(`[%>$#]`)); err == nil && strings.HasSuffix(p, "%") {
			s.write("cli\n")
			if _, err := s.readUntil(regexp.MustCompile(`>\s*$`), ReaderPrepTimeout); err != nil {
				return errors.New("Failed to enter CLI mode")
			}
		}
		if err := s.prepCmd("set cli screen-width 511", regexp.MustCompile(`Screen width set to`), false); err != nil {
			return err
		}
		if err := s.prepCmd("set cli complete-on-space off", nil, false); err != nil {
			return err
		}
		if err := s.prepCmd("set cli screen-length 0", nil, false); err != nil {
			return err
		}
	case "eos":
		_ = s.prepCmd("terminal width 511", regexp.MustCompile(`Width set to`), true)
		if err := s.prepCmd("terminal length 0", regexp.MustCompile(`Pagination disabled`), false); err != nil {
			return err
		}
	case "nxos", "iosxe":
		if err := s.prepCmd("terminal width 511", nil, false); err != nil {
			return err
		}
		if err := s.prepCmd("terminal length 0", nil, false); err != nil {
			return err
		}
	case "iosxr":
		if err := s.prepCmd("terminal width 511", nil, false); err != nil {
			return err
		}
		if err := s.prepCmd("terminal length 0", nil, false); err != nil {
			return err
		}
		// netmiko reads again until a prompt character; what is left of the last answer is drained here
		s.drain()
	case "srl":
		for _, c := range []string{"environment complete-on-space false", "environment cli-engine type basic"} {
			if err := s.prepCmd(c, nil, false); err != nil {
				return err
			}
		}
	}
	p, err := s.findPrompt(s.basePattern())
	if err != nil {
		return err
	}
	if s.os == "cumulus" || s.os == "srl" {
		if !strings.HasSuffix(p, "$") && !strings.HasSuffix(p, "#") {
			return fmt.Errorf("Router prompt not found: %s", pyStrRepr(p))
		}
	} else if !strings.HasSuffix(p, "#") && !strings.HasSuffix(p, ">") && !(s.os == "eos" && strings.HasSuffix(p, "$")) {
		return fmt.Errorf("Router prompt not found: %s", pyStrRepr(p))
	}
	bp := p
	if runeLen(p) > 1 {
		bp = string([]rune(p)[:runeLen(p)-1])
	}
	switch s.os {
	case "iosxr":
		bp = pyHead(bp, 31)
	case "iosxe":
		bp = pyHead(bp, 16)
	case "sros":
		if m := regexp.MustCompile(`\*?(.*?)(>.*)*#`).FindStringSubmatch(bp); m != nil {
			bp = m[1]
		}
	}
	s.basePrompt = bp
	if s.os == "sros" {
		if strings.Contains(bp, "@") {
			s.write("environment command-completion space false\n")
			time.Sleep(100 * time.Millisecond)
			s.drain()
			_ = s.prepCmd("environment console width 512", regexp.MustCompile(`environment`), true)
			_ = s.prepCmd("environment more false", nil, true)
			_ = s.prepCmd("//environment no more", nil, true)
		} else {
			_ = s.prepCmd("//environment more false", nil, true)
			_ = s.prepCmd("environment no more", regexp.MustCompile(`environment`), true)
		}
		time.Sleep(300 * time.Millisecond)
		s.drain()
	}
	return nil
}

// findPrompt is netmiko's find_prompt: clear the buffer, send a return, read until the terminator, last line.
func (s *cliSession) findPrompt(pat *regexp.Regexp) (string, error) {
	s.drain()
	s.write("\n")
	t, err := s.readUntil(pat, 10*time.Second)
	if err != nil {
		if err == errTimeout {
			return "", fmt.Errorf("Pattern not detected: %s in output.", pyStrRepr(pat.String()))
		}
		return "", err
	}
	lines := strings.Split(t, "\n")
	p := pyStripU(lines[len(lines)-1])
	if p == "" {
		return "", errors.New("Unable to find prompt: ")
	}
	return p, nil
}

var (
	junosCtx = []*regexp.Regexp{regexp.MustCompile(`(?i)\[edit.*\]`), regexp.MustCompile(`(?i)\{master:?.*\}`), regexp.MustCompile(`(?i)\{backup:?.*\}`),
		regexp.MustCompile(`(?i)\{line.*\}`), regexp.MustCompile(`(?i)\{primary.*\}`), regexp.MustCompile(`(?i)\{secondary.*\}`)}
	srlCtx   = regexp.MustCompile(`(?i)--\{.*\B`)
	nokiaCtx = regexp.MustCompile(`(?m)^!?\*?(\((ex|gl|pr|ro)\))?\[.*\]`)
)

// sendCommand is netmiko's send_command (auto_find_prompt, cmd_verify, strip_command, strip_prompt).
func (s *cliSession) sendCommand(cmd string, timeout time.Duration) (string, error) {
	prompt, err := s.findPrompt(s.basePattern())
	if err != nil {
		prompt = s.basePrompt
	}
	// netmiko stops at the prompt anywhere in what it has read; a short prompt (FTD ">") also matches inside the
	// output ("->"), and netmiko then returns whatever its last read held. Here the prompt must end the text read.
	search := regexp.MustCompile(regexp.QuoteMeta(pyStripU(prompt)) + `[ \t]*$`)
	c := pyStripU(cmd)
	s.write(c + "\n")
	start := time.Now()
	echo, err := s.readUntil(regexp.MustCompile(regexp.QuoteMeta(c)), 10*time.Second)
	if err != nil {
		return "", errors.New("\n\nThe command echo was not detected within the read timeout.\n\nCommand echo verification can potentially be disabled by setting either cmd_verify=False or by setting global_cmd_verify=False (as an argument to ConnectHandler).\n\n")
	}
	if parts := strings.Split(echo, c); len(parts) == 2 {
		echo = c + parts[1]
	}
	out := echo
	// the first line may be repainted (backspaces): what follows the search pattern on it is dropped
	if lines := strings.Split(out, "\n"); strings.Contains(lines[0], "\x08") {
		lines[0] = regexp.MustCompile(search.String()+`.*$`).ReplaceAllString(lines[0], "")
		out = strings.Join(lines, "\n")
	}
	if !search.MatchString(out) {
		rest, err := s.readUntil(search, timeout-time.Since(start))
		out += rest
		if err != nil {
			if err == errTimeout {
				return "", fmt.Errorf("\nPattern not detected: %s in output.\n\nThings you might try to fix this:\n1. Explicitly set your pattern using the expect_string argument.\n2. Increase the read_timeout to a larger value.\n\nYou can also look at the Netmiko session_log or debug log for more information.\n\n", pyStrRepr(search.String()))
			}
			return "", err
		}
	}
	// strip_command
	out = strings.ReplaceAll(out, "\x08", "")
	if strings.HasPrefix(out, c) {
		lines := strings.Split(out, "\n")
		out = strings.Join(lines[1:], "\n")
	}
	return s.stripPrompt(out), nil
}

func (s *cliSession) stripPrompt(a string) string {
	lines := strings.Split(a, "\n")
	if strings.Contains(lines[len(lines)-1], s.basePrompt) {
		a = strings.Join(lines[:len(lines)-1], "\n")
	}
	switch s.os {
	case "junos":
		lines = strings.Split(a, "\n")
		last := lines[len(lines)-1]
		for _, p := range junosCtx {
			if p.MatchString(last) {
				return strings.Join(lines[:len(lines)-1], "\n")
			}
		}
	case "srl":
		lines = strings.Split(a, "\n")
		if srlCtx.MatchString(lines[len(lines)-1]) {
			return strings.Join(lines[:len(lines)-1], "\n")
		}
	case "sros":
		if strings.Contains(s.basePrompt, "@") {
			ol := pySplitlines(strings.TrimRightFunc(a, pyIsSpace))
			if len(ol) == 0 {
				ol = []string{""}
			}
			ol[len(ol)-1] = nokiaCtx.ReplaceAllString(ol[len(ol)-1], "")
			return strings.TrimRightFunc(strings.Join(ol, "\n"), pyIsSpace)
		}
	}
	return a
}

// ReadDirect runs commands on one device over SSH with the reader's session handling (for tests and tools).
func ReadDirect(os_, host string, port int, user, pw string, cmds []string) ([]string, error) {
	s, err := dialSession(os_, host, port, user, pw)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var out []string
	for _, c := range cmds {
		t, err := s.sendCommand(c, ReaderCmdTimeout)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
