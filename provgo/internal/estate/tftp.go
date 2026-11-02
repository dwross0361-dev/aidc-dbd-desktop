package estate

// TFTP server that serves tftp_root and observes POAP and autoinstall fetches (Python: tftp_server, _tftp_send).

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aidc-dbd/provgo/internal/pyjson"
)

func (e *Estate) tftpBind() string {
	if b := e.Conf.Str("tftp_bind"); b != "" {
		return b
	}
	if p := e.provIP(); p != "" {
		return p
	}
	return "0.0.0.0"
}

// TFTPServer listens on tftp_port and answers read requests (one goroutine per transfer).
func (e *Estate) TFTPServer() {
	root := e.Conf.Str("tftp_root")
	if !e.Conf.Has("tftp_root") {
		root = "/srv/tftp"
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	} else if a, err := filepath.Abs(root); err == nil {
		root = a
	}
	owner := map[string]string{}
	for _, dv := range e.Plan.L("devices") {
		d := dv.(*pyjson.Map)
		for _, f := range pyjson.StrList(d.V("tftp_files")) {
			owner[f] = d.Str("name")
		}
	}
	port := int64(69)
	if v, ok := e.Conf.Get("tftp_port"); ok {
		port = pyIntAny(v)
	}
	pc, err := net.ListenPacket("udp4", net.JoinHostPort(e.tftpBind(), strconv.FormatInt(port, 10)))
	if err != nil {
		e.Event(fmt.Sprintf("TFTP off (%s): POAP and autoinstall fetches are not observed", pyErrno(err)), "warn")
		return
	}
	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tftp: %v\n", err)
			continue
		}
		data := append([]byte(nil), buf[:n]...)
		if len(data) < 4 || binary.BigEndian.Uint16(data[:2]) != 1 {
			continue
		}
		parts := bytes.Split(data[2:], []byte{0})
		fname := strings.TrimLeft(strings.ToValidUTF8(string(parts[0]), "�"), "/")
		opts := map[string]string{}
		for i := 2; i < len(parts)-1; i += 2 {
			opts[strings.ToLower(strings.ToValidUTF8(string(parts[i]), "�"))] = strings.ToValidUTF8(string(parts[i+1]), "�")
		}
		// Python passes options in its dict order; the OACK below lists them in a fixed order either way
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "tftp: %v\n", r)
				}
			}()
			e.tftpSend(root, owner, fname, opts, addr.(*net.UDPAddr))
		}()
	}
}

func (e *Estate) tftpSend(root string, owner map[string]string, fname string, opts map[string]string, addr *net.UDPAddr) {
	ip := addr.IP.String()
	t, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(e.tftpBind())})
	if err != nil {
		return
	}
	defer t.Close()
	errPkt := func(code uint16, msg string) {
		b := make([]byte, 4, 5+len(msg))
		binary.BigEndian.PutUint16(b[0:], 5)
		binary.BigEndian.PutUint16(b[2:], code)
		b = append(append(b, msg...), 0)
		_, _ = t.WriteToUDP(b, addr)
	}
	rel, ok := CanonRel(fname)
	if !ok {
		errPkt(2, "bad path")
		e.Event(fmt.Sprintf("refused tftp:%s to %s: not a canonical file path", pyHead(fname, 120), ip), "warn")
		return
	}
	fname = rel
	full := filepath.Join(root, fname)
	if r, err := filepath.EvalSymlinks(full); err == nil {
		full = r
	}
	e.Mu.Lock()
	n := e.DevByIP(ip)
	own := owner[fname]
	st, serr := os.Stat(full)
	if !strings.HasPrefix(full, root+string(os.PathSeparator)) || serr != nil || !st.Mode().IsRegular() {
		errPkt(1, "file not found")
		if n != "" {
			img := strings.HasPrefix(fname, "images/")
			w, lvl := "", "fail"
			if img {
				w, lvl = " (the device decides by its baseline)", "warn"
			}
			e.ev(fmt.Sprintf("%s asked for tftp:%s: not on this host%s", n, fname, w), lvl, n)
		}
		e.Mu.Unlock()
		return
	}
	strict := pyjson.Truthy(e.Conf.V("strict_fetch"))
	if strict && own != "" && own != n {
		errPkt(2, "this file belongs to another device")
		who := n
		if who == "" {
			who = ip
		}
		e.ev(fmt.Sprintf("refused tftp:%s to %s: it belongs to %s", fname, who, own), "warn", own)
		e.Mu.Unlock()
		return
	}
	if strict && n == "" {
		errPkt(2, "unknown requester")
		e.ev(fmt.Sprintf("refused tftp:%s to %s: not a device of %s", fname, ip, e.siteCode()), "warn", "")
		e.Mu.Unlock()
		return
	}
	e.Mu.Unlock()
	size := st.Size()
	blk := 512
	type kv struct{ k, v string }
	var oack []kv
	if v, ok := opts["blksize"]; ok {
		b := int64(512)
		if v != "" {
			x, err := pyInt(v)
			if err != nil {
				return // Python: ValueError ends the transfer thread
			}
			b = x
		}
		if b > 65464 {
			b = 65464
		}
		if b > 1468 {
			b = 1468
		}
		if b < 8 {
			b = 8
		}
		blk = int(b)
		oack = append(oack, kv{"blksize", strconv.Itoa(blk)})
	}
	if _, ok := opts["tsize"]; ok {
		oack = append(oack, kv{"tsize", strconv.FormatInt(size, 10)})
	}
	if v, ok := opts["timeout"]; ok {
		oack = append(oack, kv{"timeout", v})
	}
	xfer := func(pkt []byte, want uint16) bool {
		ack := make([]byte, 1024)
		for i := 0; i < 6; i++ {
			_, _ = t.WriteToUDP(pkt, addr)
			_ = t.SetReadDeadline(time.Now().Add(3 * time.Second))
			for {
				nr, _, err := t.ReadFromUDP(ack)
				if err != nil {
					break // timeout: send again
				}
				if nr < 4 {
					continue
				}
				op, num := binary.BigEndian.Uint16(ack[:2]), binary.BigEndian.Uint16(ack[2:4])
				if op == 4 && num == want {
					return true
				}
				if op == 5 {
					return false
				}
			}
		}
		return false
	}
	okx := true
	if len(oack) > 0 {
		p := []byte{0, 6}
		for _, o := range oack {
			p = append(append(append(append(p, o.k...), 0), o.v...), 0)
		}
		okx = xfer(p, 0)
	}
	f, err := os.Open(full)
	if err != nil {
		return
	}
	num := 1
	chunk := make([]byte, blk)
	for okx {
		nr, _ := ioReadFull(f, chunk)
		p := make([]byte, 4, 4+nr)
		binary.BigEndian.PutUint16(p[0:], 3)
		binary.BigEndian.PutUint16(p[2:], uint16(num&0xFFFF))
		p = append(p, chunk[:nr]...)
		okx = xfer(p, uint16(num&0xFFFF))
		if nr < blk {
			break
		}
		num++
	}
	f.Close()
	if n != "" {
		r := fname
		if strings.HasPrefix(fname, "images/") {
			r = "images/" + filepath.Base(fname)
		}
		e.Mu.Lock()
		defer e.Mu.Unlock()
		if strings.HasPrefix(r, "images/") {
			if okx && e.stage(n) < 3 {
				e.setStage(n, 3, "image "+filepath.Base(fname)+" served over TFTP", "", "")
			}
		} else if own == n && e.stage(n) < 2 {
			if e.stage(n) < 1 {
				e.setStage(n, 1, "lease seen through the TFTP fetch", "", "")
			}
			e.setStage(n, 2, "tftp:"+fname+" served", "", "")
		}
	}
}

func ioReadFull(f *os.File, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		k, err := f.Read(b[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
