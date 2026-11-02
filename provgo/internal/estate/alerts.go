package estate

// Alerts (Python: the alerts queue and alert_worker): warn/fail events, "Gate passed" and "Build complete" go to the
// webhook (JSON POST) and to e-mail (SMTP), one alert at a time, in order. A receiver that fails is logged to stderr
// and the next alert is tried; nothing is retried.

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aidc-dbd/provgo/internal/pyjson"
)

// alertCap bounds the queue (Python's is unbounded): DECISIONS P3-9.
const alertCap = 10000

type alertQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	q       []*pyjson.Map
	dropped int
}

func (a *alertQueue) put(m *pyjson.Map) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cond == nil {
		a.cond = sync.NewCond(&a.mu)
	}
	if len(a.q) >= alertCap {
		a.dropped++
		if a.dropped == 1 || a.dropped%1000 == 0 {
			fmt.Fprintf(os.Stderr, "alerts: queue full (%d waiting): %d alert(s) dropped\n", len(a.q), a.dropped)
		}
		return
	}
	a.q = append(a.q, m)
	a.cond.Signal()
}

func (a *alertQueue) get() *pyjson.Map {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cond == nil {
		a.cond = sync.NewCond(&a.mu)
	}
	for len(a.q) == 0 {
		a.cond.Wait()
	}
	m := a.q[0]
	a.q[0] = nil
	a.q = a.q[1:]
	return m
}

// AlertWorker delivers queued alerts (runs for the life of the service).
func (e *Estate) AlertWorker() {
	c := e.Conf
	for {
		a := e.alerts.get()
		dev := ""
		if a.Str("device") != "" {
			dev = a.Str("device") + ": "
		}
		text := fmt.Sprintf("[%s] %s %s%s", PyStr(a.V("site")), strings.ToUpper(a.Str("level")), dev, a.Str("text"))
		if c.Truthy("alert_webhook") {
			body := a.Copy()
			body.Set("text", text)
			if err := postWebhook(c.Str("alert_webhook"), []byte(pyjson.Dumps(body))); err != nil {
				fmt.Fprintf(os.Stderr, "webhook: %s\n", err)
			}
		}
		if c.Truthy("smtp_host") && c.Truthy("smtp_to") {
			if err := e.sendMail(text, a); err != nil {
				fmt.Fprintf(os.Stderr, "smtp: %s\n", err)
			}
		}
	}
}

func postWebhook(url string, body []byte) error {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Python-urllib/3")
	cl := &http.Client{Timeout: 10 * time.Second}
	r, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	_, _ = bufio.NewReader(r.Body).WriteTo(discard{})
	if r.StatusCode >= 400 {
		return fmt.Errorf("HTTP Error %d: %s", r.StatusCode, strings.TrimPrefix(r.Status, strconv.Itoa(r.StatusCode)+" "))
	}
	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// ------------------------------------------------------------------------------------- the message (email.message)

// AlertMessage is the e-mail Python's EmailMessage + smtplib.send_message writes for one alert (CRLF line ends).
func AlertMessage(subject, from, to string, a *pyjson.Map) []byte {
	var b strings.Builder
	b.WriteString(foldHeader("Subject", pyHead(subject, 120)))
	b.WriteString(foldHeader("From", from))
	b.WriteString(foldHeader("To", to))
	cte, body := encodeText(pyjson.DumpsIndent(a, 2))
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: " + cte + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// encodeText is email.contentmanager._encode_text for an ASCII-safe text, policy default (max 78, linesep "\n").
func encodeText(s string) (string, string) {
	const maxlen = 78
	lines := pySplitlinesBytes(s)
	long := false
	for _, l := range lines {
		if len(l) > maxlen {
			long = true
		}
	}
	normal := strings.Join(lines, "\n") + "\n"
	if !long && isASCII(normal) {
		return "7bit", normal
	}
	head := lines
	if len(head) > 10 {
		head = head[:10]
	}
	sniff := strings.Join(head, "\n") + "\n"
	sniffQP := qpBodyEncode(sniff, maxlen)
	sniffB64 := base64.StdEncoding.EncodedLen(len(sniff)) + 1
	if len(sniffQP) > sniffB64 {
		var out strings.Builder
		per := maxlen / 4 * 3
		emb := []byte(strings.Join(lines, "\n") + "\n")
		for i := 0; i < len(emb); i += per {
			j := i + per
			if j > len(emb) {
				j = len(emb)
			}
			out.WriteString(base64.StdEncoding.EncodeToString(emb[i:j]) + "\n")
		}
		return "base64", out.String()
	}
	if len(lines) <= 10 {
		return "quoted-printable", sniffQP
	}
	return "quoted-printable", qpBodyEncode(normal, maxlen)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// pySplitlinesBytes is bytes.splitlines() (\n, \r\n, \r).
func pySplitlinesBytes(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			out = append(out, s[start:i])
			if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func qpSafe(c byte) bool {
	return c == '\t' || (c >= ' ' && c <= '~' && c != '=')
}

// qpBodyEncode is email.quoprimime.body_encode(body, maxlinelen) with eol "\n" (body as latin-1 text).
func qpBodyEncode(body string, maxlinelen int) string {
	if body == "" {
		return body
	}
	var t strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if qpSafe(c) || c == '\r' || c == '\n' {
			t.WriteByte(c)
		} else {
			fmt.Fprintf(&t, "=%02X", c)
		}
	}
	enc := t.String()
	soft := "=\n"
	max1 := maxlinelen - 1
	var out []string
	for _, line := range pySplitlinesBytes(enc) {
		start := 0
		last := len(line) - 1 - maxlinelen
		for start <= last {
			stop := start + max1
			if line[stop-2] == '=' {
				out = append(out, line[start:stop-1])
				start = stop - 2
			} else if line[stop-1] == '=' {
				out = append(out, line[start:stop])
				start = stop - 1
			} else {
				out = append(out, line[start:stop]+"=")
				start = stop
			}
		}
		if line != "" && (line[len(line)-1] == ' ' || line[len(line)-1] == '\t') {
			room := start - last
			var q string
			ws := line[len(line)-1]
			switch {
			case room >= 3:
				q = fmt.Sprintf("=%02X", ws)
			case room == 2:
				q = string(ws) + soft
			default:
				q = soft + fmt.Sprintf("=%02X", ws)
			}
			out = append(out, line[start:len(line)-1]+q)
		} else {
			out = append(out, line[start:])
		}
	}
	if c := body[len(body)-1]; c == '\r' || c == '\n' {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// ------------------------------------------------------------------------------------- header folding

// foldHeader is an unstructured header folded as email.policy.default does (_refold_parse_tree, max 78, CRLF).
func foldHeader(name, value string) string {
	const maxlen = 78
	f := &folder{lines: []string{name + ":"}, maxlen: maxlen, lastEW: -1}
	if value != "" {
		f.lines[0] += " "
	}
	toks := unstructuredTokens(value)
	if value != "" {
		whole := value
		if !isASCII(whole) {
			f.words(toks)
		} else if len(whole) <= maxlen-runeLen(f.lines[len(f.lines)-1]) {
			f.lines[len(f.lines)-1] += whole
		} else if runeLen(whole)+1 <= maxlen && f.stealWSP() != "" {
			f.lines = append(f.lines, " "+whole)
		} else {
			f.words(toks)
		}
	}
	return strings.Join(f.lines, "\r\n") + "\r\n"
}

type hdrTok struct {
	s   string
	fws bool
}

func unstructuredTokens(v string) []hdrTok {
	var out []hdrTok
	for v != "" {
		i := 0
		ws := v[0] == ' ' || v[0] == '\t'
		for i < len(v) && (v[i] == ' ' || v[i] == '\t') == ws {
			i++
		}
		out = append(out, hdrTok{v[:i], ws})
		v = v[i:]
	}
	return out
}

type folder struct {
	lines      []string
	maxlen     int
	lastEW     int    // start of the last encoded word on the last line, -1 for none
	lastEWLen  int    // its length
	lastEWText string // its decoded text
	lastIsEW   bool
}

func (f *folder) cur() string { return f.lines[len(f.lines)-1] }

func (f *folder) stealWSP() string {
	l := f.cur()
	if l != "" && (l[len(l)-1] == ' ' || l[len(l)-1] == '\t') {
		w := l[len(l)-1:]
		f.lines[len(f.lines)-1] = l[:len(l)-1]
		if f.cur() == "" {
			f.lines = f.lines[:len(f.lines)-1]
		}
		return w
	}
	return ""
}

func (f *folder) words(toks []hdrTok) {
	for _, t := range toks {
		if !t.fws && !isASCII(t.s) {
			f.foldEW(t.s, "utf-8")
			continue
		}
		if runeLen(t.s) <= f.maxlen-runeLen(f.cur()) {
			f.lines[len(f.lines)-1] += t.s
			f.lastIsEW = f.lastIsEW && strings.Trim(t.s, " \t") == ""
			continue
		}
		if runeLen(t.s)+1 <= f.maxlen {
			nl := f.stealWSP()
			if nl != "" || t.fws {
				f.lines = append(f.lines, nl+t.s)
				f.lastIsEW = f.lastIsEW && strings.Trim(f.cur(), " \t") == ""
				f.lastEW = -1
				continue
			}
		}
		f.foldEW(t.s, "us-ascii")
	}
}

// foldEW is _fold_as_ew for an unstructured word (ew_combine_allowed).
func (f *folder) foldEW(toEncode, charset string) {
	if f.lastEW >= 0 {
		l := f.cur()
		toEncode = f.lastEWText + l[f.lastEW+f.lastEWLen:] + toEncode
		f.lines[len(f.lines)-1] = l[:f.lastEW]
	} else if f.lastIsEW {
		l := f.cur()
		trimmed := strings.TrimRight(l, " \t")
		lead := l[len(trimmed):]
		f.lines[len(f.lines)-1] = trimmed
		if lead != "" {
			f.lines[len(f.lines)-1] += " "
		}
		toEncode = lead + toEncode
	} else if toEncode[0] == ' ' || toEncode[0] == '\t' {
		lead := toEncode[:1]
		toEncode = toEncode[1:]
		if runeLen(f.cur()) == f.maxlen {
			f.lines = append(f.lines, f.stealWSP())
		}
		f.lines[len(f.lines)-1] += lead
	}
	trailing := ""
	if c := toEncode[len(toEncode)-1]; c == ' ' || c == '\t' {
		trailing = toEncode[len(toEncode)-1:]
		toEncode = toEncode[:len(toEncode)-1]
	}
	newLast := runeLen(f.cur())
	if f.lastEW >= 0 {
		newLast = f.lastEW
	}
	chrome := len("utf-8") + 7
	segText, segLen := "", 0
	for toEncode != "" {
		remaining := f.maxlen - runeLen(f.cur())
		space := remaining - chrome
		if space <= 0 {
			nl := f.stealWSP()
			if nl == "" {
				nl = " "
			}
			f.lines = append(f.lines, nl)
			newLast = runeLen(f.cur())
			continue
		}
		word := runeHead(toEncode, space)
		ew := encodeWord(word)
		for runeLen(ew)-remaining > 0 {
			word = runeHead(word, runeLen(word)-1)
			ew = encodeWord(word)
		}
		f.lines[len(f.lines)-1] += ew
		segText, segLen = word, len(ew)
		toEncode = toEncode[len(word):]
		if toEncode != "" {
			f.lines = append(f.lines, " ")
			newLast = runeLen(f.cur())
		}
	}
	f.lines[len(f.lines)-1] += trailing
	f.lastEW, f.lastEWLen, f.lastEWText = newLast, segLen, segText
	f.lastIsEW = true
}

func runeHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for k := range s {
		if i == n {
			return s[:k]
		}
		i++
	}
	return s
}

func qByteSafe(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.IndexByte("-!*+/", c) >= 0
}

// encodeWord is email._encoded_words.encode(s, "utf-8"): q unless b is shorter by 5 or more.
func encodeWord(s string) string {
	b := []byte(s)
	ql := 0
	for _, c := range b {
		if qByteSafe(c) || c == ' ' {
			ql++
		} else {
			ql += 3
		}
	}
	bl := (len(b) + 2) / 3 * 4
	if ql-bl < 5 {
		var q strings.Builder
		for _, c := range b {
			switch {
			case c == ' ':
				q.WriteByte('_')
			case qByteSafe(c):
				q.WriteByte(c)
			default:
				fmt.Fprintf(&q, "=%02X", c)
			}
		}
		return "=?utf-8?q?" + q.String() + "?="
	}
	return "=?utf-8?b?" + base64.StdEncoding.EncodeToString(b) + "?="
}

var _ = utf8.RuneLen

// ------------------------------------------------------------------------------------- SMTP (smtplib)

// mailAddrs is email.utils.getaddresses of a header value: the bare addresses.
func mailAddrs(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if i := strings.LastIndex(p, "<"); i >= 0 {
			if j := strings.Index(p[i:], ">"); j > 0 {
				p = p[i+1 : i+j]
			}
		}
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func (e *Estate) sendMail(text string, a *pyjson.Map) error {
	c := e.Conf
	from := c.Str("smtp_from")
	if from == "" {
		h, _ := os.Hostname()
		from = "estate-build@" + h
	}
	msg := AlertMessage(text, from, c.Str("smtp_to"), a)
	port := int64(25)
	if c.Has("smtp_port") {
		port = pyIntAny(c.V("smtp_port"))
	}
	addr := net.JoinHostPort(c.Str("smtp_host"), strconv.FormatInt(port, 10))
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	tp := textproto.NewConn(conn)
	cmd := func(expect int, f string, args ...any) (int, string, error) {
		if f != "" {
			if err := tp.PrintfLine(f, args...); err != nil {
				return 0, "", err
			}
		}
		code, m, err := tp.ReadResponse(expect)
		return code, m, err
	}
	if _, _, err := cmd(220, ""); err != nil {
		return err
	}
	host, _ := os.Hostname()
	ehlo := func() (map[string]string, error) {
		_, m, err := cmd(250, "EHLO %s", host)
		if err != nil {
			return nil, err
		}
		ext := map[string]string{}
		for _, l := range strings.Split(m, "\n")[1:] {
			k, v, _ := strings.Cut(l, " ")
			ext[strings.ToUpper(k)] = v
		}
		return ext, nil
	}
	ext, err := ehlo()
	if err != nil {
		return err
	}
	if c.Truthy("smtp_starttls") {
		if _, ok := ext["STARTTLS"]; !ok {
			return fmt.Errorf("STARTTLS extension not supported by server.")
		}
		if _, _, err := cmd(220, "STARTTLS"); err != nil {
			return err
		}
		tc := tls.Client(conn, &tls.Config{ServerName: c.Str("smtp_host"), MinVersion: tls.VersionTLS12})
		if err := tc.Handshake(); err != nil {
			return err
		}
		tp = textproto.NewConn(tc)
		if ext, err = ehlo(); err != nil {
			return err
		}
	}
	if c.Truthy("smtp_user") {
		// smtplib login(): each offered mechanism in its preference order (CRAM-MD5, PLAIN, LOGIN); a refused one moves on
		// to the next; 235 or 503 (already authenticated) is success; the last refusal is the error
		user, pw := c.Str("smtp_user"), c.Str("smtp_password")
		mechs := strings.Fields(strings.ToUpper(ext["AUTH"]))
		done := func(code int, m string, err error) error {
			if err != nil {
				return err
			}
			if code == 235 || code == 503 {
				return nil
			}
			return &textproto.Error{Code: code, Msg: m}
		}
		auth := func(mech string) error {
			switch mech {
			case "CRAM-MD5":
				code, ch, err := cmd(0, "AUTH CRAM-MD5")
				if err != nil || code != 334 {
					return done(code, ch, err)
				}
				raw, _ := base64.StdEncoding.DecodeString(ch)
				h := hmac.New(md5.New, []byte(pw))
				h.Write(raw)
				return done(cmd(0, "%s", base64.StdEncoding.EncodeToString([]byte(user+" "+hex.EncodeToString(h.Sum(nil))))))
			case "PLAIN":
				ir := base64.StdEncoding.EncodeToString([]byte("\x00" + user + "\x00" + pw))
				code, m, err := cmd(0, "AUTH PLAIN %s", ir)
				if err == nil && code == 334 {
					return done(cmd(0, "%s", ir))
				}
				return done(code, m, err)
			default: // LOGIN
				code, m, err := cmd(0, "AUTH LOGIN %s", base64.StdEncoding.EncodeToString([]byte(user)))
				if err != nil || code != 334 {
					return done(code, m, err)
				}
				return done(cmd(0, "%s", base64.StdEncoding.EncodeToString([]byte(pw))))
			}
		}
		var last error
		tried := false
		for _, m := range []string{"CRAM-MD5", "PLAIN", "LOGIN"} {
			if !containsStr(mechs, m) {
				continue
			}
			tried = true
			if last = auth(m); last == nil {
				break
			}
			var pe *textproto.Error
			if !errors.As(last, &pe) {
				return last
			}
		}
		if !tried {
			return fmt.Errorf("No suitable authentication method found.")
		}
		if last != nil {
			return last
		}
	}
	opts := ""
	if _, ok := ext["SIZE"]; ok {
		opts = fmt.Sprintf(" size=%d", len(msg))
	}
	if _, _, err := cmd(250, "MAIL FROM:<%s>%s", mailAddrs(from)[0], opts); err != nil {
		return err
	}
	rcpts := mailAddrs(c.Str("smtp_to"))
	ok := 0
	for _, r := range rcpts {
		if _, _, err := cmd(250, "RCPT TO:<%s>", r); err == nil {
			ok++
		}
	}
	if ok == 0 {
		return fmt.Errorf("all recipients were refused")
	}
	if _, _, err := cmd(354, "DATA"); err != nil {
		return err
	}
	w := tp.DotWriter()
	if _, err := w.Write(bytes.ReplaceAll(msg, []byte("\r\n"), []byte("\n"))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if _, _, err := cmd(250, ""); err != nil {
		return err
	}
	_, _, _ = cmd(221, "QUIT")
	return nil
}
