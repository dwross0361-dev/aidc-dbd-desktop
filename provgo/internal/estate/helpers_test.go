package estate

import (
	"reflect"
	"testing"
)

func TestNormMAC(t *testing.T) {
	for in, want := range map[string]string{"AA-BB-CC-DD-EE-FF": "aa:bb:cc:dd:ee:ff", "aabb.ccdd.eeff": "aa:bb:cc:dd:ee:ff",
		"aa:bb:cc:dd:ee": "", "": "", "0xaabbccddeeff": "", "aa:bb:cc:dd:ee:ff:00": "", "zz:aa:bb:cc:dd:ee:ff": "aa:bb:cc:dd:ee:ff"} {
		if got := NormMAC(in); got != want {
			t.Errorf("NormMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonRel(t *testing.T) {
	ok := map[string]string{"/bootstrap/a.conf": "bootstrap/a.conf", "//x": "x", "/./a/./b": "a/b", "a/.": "a", "/a/b/": ""}
	for in, want := range ok {
		got, k := CanonRel(in)
		if want == "" {
			if k {
				t.Errorf("CanonRel(%q) accepted as %q", in, got)
			}
			continue
		}
		if !k || got != want {
			t.Errorf("CanonRel(%q) = %q,%v want %q", in, got, k, want)
		}
	}
	for _, bad := range []string{"", "/", "/..", "/a/../b", "/a//b", "/a\x00b", "/.", "/./."} {
		if got, k := CanonRel(bad); k {
			t.Errorf("CanonRel(%q) = %q, want refused", bad, got)
		}
	}
}

func TestSerials(t *testing.T) {
	if got := SerialFromClientID("00:53:4e:31:32:33"); got != "SN123" {
		t.Errorf("type 0: %q", got)
	}
	if got := SerialFromClientID("01:aa:bb:cc:dd:ee:ff"); got != "" {
		t.Errorf("type 1: %q", got)
	}
	if got := SerialFromClientID("0x46444f3132333435"); got != "FDO12345" {
		t.Errorf("plain: %q", got)
	}
	if got := SerialFromClientID("00:01:02"); got != "" {
		t.Errorf("unprintable: %q", got)
	}
	if got := SerialFromClientID("abc"); got != "" {
		t.Errorf("odd: %q", got)
	}
	s, m := SerialFromVendorClass("Juniper-ex4400-48t-XH3721AB0012")
	if s != "XH3721AB0012" || m != "ex4400-48t" {
		t.Errorf("vc: %q %q", s, m)
	}
	if s, m := SerialFromVendorClass("Juniper-qfx5120"); s != "" || m != "" {
		t.Errorf("vc short: %q %q", s, m)
	}
}

func TestNormPort(t *testing.T) {
	for in, want := range map[string]string{"ge-0/0/42": "0:0:42", " Ethernet1/1 ": "1:1", "swp12s0": "12s0", "et-0/0/1.0": "0:0:1",
		"xe-0/0/29:0": "0:0:29:0", "e1/49": "1:49", "mge-0/0/1": "0:0:1", "eth0": "0"} {
		if got := NormPort(in); got != want {
			t.Errorf("NormPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenScan(t *testing.T) {
	text := "# <COMMENT_TOKEN>\n  ! <BANG>\n// <SLASH>\nset system <ROOT_HASH> x <A.B-c &d>\n<lower> <X1> <Y2> <Z3>\n"
	if got := FileHasTokensText(text); !reflect.DeepEqual(got, []string{"ROOT_HASH", "A.B-c &d", "X1"}) {
		t.Errorf("tokens: %v", got)
	}
	if !TokenScanApplies("bootstrap/x.conf") || TokenScanApplies("images/x.bin") || TokenScanApplies("configs/README.txt") ||
		TokenScanApplies("ztp/notes.md") || TokenScanApplies("configs/a/b.csv") {
		t.Error("token scan scope")
	}
}

func TestPBKDF2CompatibleWithPython(t *testing.T) {
	if !PBKDF2OK("correct horse battery", "pbkdf2$240000$0123456789abcdef0123456789abcdef$660a4594185e3446daadbd7a97d5cd8832f878852f7552dbf9bd0ba9140a2385") {
		t.Error("python hash (240000) not accepted")
	}
	if !PBKDF2OK("pässwörd", "pbkdf2$1000$abababababababababababababababab$e1e5031fdcd8b1d33544d46d0857d46273ac9a7de1dcecaec048c8656710f21f") {
		t.Error("python hash (utf-8, 1000) not accepted")
	}
	if PBKDF2OK("wrong", "pbkdf2$1000$abababababababababababababababab$e1e5031fdcd8b1d33544d46d0857d46273ac9a7de1dcecaec048c8656710f21f") {
		t.Error("wrong password accepted")
	}
	if PBKDF2OK("x", DummyPW) || PBKDF2OK("x", "garbage") {
		t.Error("dummy or garbage accepted")
	}
	h := PBKDF2("abc", "", 1000)
	if !PBKDF2OK("abc", h) || len(h) != len("pbkdf2$1000$")+32+1+64 {
		t.Errorf("round trip %s", h)
	}
}

func TestParseHook(t *testing.T) {
	body := "LEASES4_SIZE=2\nLEASES4_AT0_ADDRESS=10.0.0.5\nLEASES4_AT0_HWADDR=AA:BB:CC:DD:EE:01\nLEASES4_AT0_CLIENT_ID=00:53:4e:31\n" +
		"LEASES4_AT1_ADDRESS=10.0.0.6\nLEASES4_AT1_HWADDR=aa:bb:cc:dd:ee:02\nQUERY4_OPTION_82_SUB_OPTION_1=0x7377313a6574682d312f31\nQUERY4_GIADDR=10.0.0.1\n"
	ev := ParseHook([]byte(body), "leases4_committed")
	if len(ev) != 2 || ev[0].Str("ip") != "10.0.0.5" || ev[0].Str("circuit") != "sw1:eth-1/1" || ev[1].Str("giaddr") != "10.0.0.1" {
		t.Fatalf("hook: %v", ev)
	}
	if len(ParseHook([]byte("LEASE4_ADDRESS=1.2.3.4"), "lease4_expire")) != 0 {
		t.Error("other event parsed")
	}
}

func TestPyFormatting(t *testing.T) {
	if pyD(0.05*2) != "0" || pyD(2.9) != "2" || pyD(45) != "45" {
		t.Error("pyD")
	}
	if n, err := pyInt(" +1_0 "); err != nil || n != 10 {
		t.Error("pyInt")
	}
	if _, err := pyInt("5.0"); err == nil {
		t.Error("pyInt float")
	}
	if PyRepr([]any{"fe", "edge"}) != "['fe', 'edge']" || PyStr(nil) != "None" {
		t.Error("repr")
	}
}

func TestKeaUnreachableMessage(t *testing.T) {
	_, err := keaCommand("/nonexistent/kea4-ctrl-socket")
	if err == nil || err.Error() != "[Errno 2] No such file or directory" {
		t.Errorf("got %v", err)
	}
}

func TestParseDHCP(t *testing.T) {
	f := make([]byte, 14+20+8+240)
	f[12], f[13] = 0x08, 0x00
	ip := f[14:]
	ip[0], ip[9] = 0x45, 17
	udp := ip[20:]
	udp[2], udp[3] = 0, 67
	b := udp[8:]
	copy(b[28:34], []byte{0x02, 0, 0, 0, 0, 0x01})
	copy(b[236:240], []byte{0x63, 0x82, 0x53, 0x63})
	opts := []byte{53, 1, 1, 60, 5, 'C', 'i', 's', 'c', 'o', 82, 6, 1, 4, 's', 'w', ':', '1', 255}
	f = append(f[:len(f)], opts...)
	ev := ParseDHCP(f)
	if ev == nil || ev.Str("mac") != "02:00:00:00:00:01" || ev.Int("type") != 1 || ev.Str("vendor_class") != "Cisco" || ev.Str("circuit") != "sw:1" {
		t.Fatalf("parse: %v", ev)
	}
}
