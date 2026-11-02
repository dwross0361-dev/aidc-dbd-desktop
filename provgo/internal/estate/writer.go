package estate

// The configuration writer (Python: netmiko ConnectHandler + send_config_set/commit/save_config in _tune_write and
// _remove_dwr). Built on the reader's session (reader.go); each platform's configuration mode, commit and save are
// netmiko's commands and patterns.

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// WriteResult is what a tuning write reports.
type WriteResult struct {
	OK   bool
	Note string
}

type loginErr struct{ msg string }

func (l *loginErr) Error() string { return l.msg }

var (
	wrCumulusErr = regexp.MustCompile(`(?i)\berror\b|invalid|failed`)
	wrJunosErr   = regexp.MustCompile(`(?i)error|syntax|unknown command|missing`)
	wrOtherErr   = regexp.MustCompile(`(?i)% ?invalid|% ?incomplete|error`)
)

type cfgMode struct {
	enter, check, exit string
}

var cfgModes = map[string]cfgMode{
	"junos": {"configure", "#", "exit configuration-mode"},
	"eos":   {"configure terminal", ")#", "end"}, "nxos": {"configure terminal", ")#", "end"},
	"iosxe": {"configure terminal", ")#", "end"}, "iosxr": {"configure terminal", ")#", "end"},
	"srl":  {"enter candidate private", "candidate", ""},
	"sros": {"edit-config exclusive", "(ex)[", ""},
}

// noExit: platforms whose send_config_set stays in configuration mode (netmiko: SR Linux, SR OS MD-CLI, IOS XR).
var noExit = map[string]bool{"srl": true, "sros": true, "iosxr": true}

// saveCmds: netmiko save_config per platform (IOS XR has none: NotImplementedError, an empty message).
var saveCmds = map[string]string{"eos": "copy running-config startup-config", "nxos": "copy running-config startup-config",
	"iosxe": "write mem", "srl": "save startup", "sros": "/admin save"}

// commitCmds: netmiko commit() per platform (SR OS returns to the root context first).
var commitCmds = map[string][]string{"iosxr": {"commit"}, "srl": {"commit stay"}, "sros": {"exit all", "commit"}, "junos": {"commit"}}

var errNotImpl = errors.New("")

func (s *cliSession) promptRE() *regexp.Regexp {
	return regexp.MustCompile(`(?m)(?:` + regexp.QuoteMeta(s.basePrompt) + `.*$|#.*$|>.*$)`)
}

// sendLine is netmiko's per-command read in send_config_set: the command echo, then up to the prompt (or "#").
func (s *cliSession) sendLine(c string, timeout time.Duration) (string, error) {
	c = pyStripU(c)
	s.write(c + "\n")
	out, err := s.readUntil(regexp.MustCompile(regexp.QuoteMeta(c)), timeout)
	if err != nil {
		return out, errors.New("Pattern not detected: " + pyStrRepr(regexp.QuoteMeta(c)) + " in output.")
	}
	after := out[strings.Index(out, c)+len(c):]
	if !s.promptRE().MatchString(after) {
		rest, err := s.readUntil(s.promptRE(), timeout)
		out += rest
		if err != nil {
			return out, errors.New("Pattern not detected: prompt in output.")
		}
	}
	return out, nil
}

func (s *cliSession) configSet(lines []string, exit bool) (string, error) {
	m, ok := cfgModes[s.os]
	if !ok {
		return "", errNotImpl
	}
	out, err := s.sendLine(m.enter, 15*time.Second)
	if err != nil {
		return out, err
	}
	for _, l := range lines {
		o, err := s.sendLine(l, 15*time.Second)
		out += o
		if err != nil {
			return out, err
		}
	}
	if exit && !noExit[s.os] && m.exit != "" {
		o, err := s.sendLine(m.exit, 15*time.Second)
		out += o
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (s *cliSession) commit(extra string, timeout time.Duration) (string, error) {
	if s.os == "junos" {
		c, marker := "commit", "commit complete"
		if strings.HasPrefix(extra, "confirmed") {
			marker = "commit confirmed will be automatically rolled back in"
		}
		if extra != "" {
			c += " " + extra
		}
		out, err := s.sendLine(c, timeout)
		if err != nil {
			return out, err
		}
		if !strings.Contains(out, marker) {
			return out, errors.New("Commit failed with the following errors:\n\n" + out)
		}
		return out, nil
	}
	cmds, ok := commitCmds[s.os]
	if !ok {
		return "", errNotImpl
	}
	var out string
	for _, c := range cmds {
		o, err := s.sendLine(c, timeout)
		out += o
		if err != nil {
			return out, err
		}
	}
	if s.os == "iosxr" { // netmiko cisco_xr commit(): a refused commit, or other sessions' commits, raise
		if strings.Contains(out, "One or more commits have occurred from other") {
			o, _ := s.sendLine("n", timeout)
			return out + o, errors.New("\nOther commits occurred during this configuration operation--commit aborted!\n")
		}
		if strings.Contains(out, "Failed to") {
			return out, errors.New("Commit failed with the following errors:\n\n" + out)
		}
	}
	return out, nil
}

func (s *cliSession) saveConfig() (string, error) {
	c, ok := saveCmds[s.os]
	if !ok {
		return "", errNotImpl
	}
	if s.os == "sros" { // netmiko nokia_sros save_config is a send_command: echo and prompt stripped
		return s.sendCommand(c, 60*time.Second)
	}
	return s.sendLine(c, 60*time.Second)
}

func lastLine(out string) string {
	ls := pySplitlines(pyStripU(out))
	if len(ls) == 0 {
		return ""
	}
	return ls[len(ls)-1]
}

// WriteConfig applies configuration lines to one device (a tuning write).
func WriteConfig(os_, host string, port int, user, pw string, lines []string, why string) (*WriteResult, error) {
	s, err := dialSession(os_, host, port, user, pw)
	if err != nil {
		return nil, err
	}
	defer s.close()
	var out string
	var ok bool
	switch os_ {
	case "cumulus":
		var outs []string
		for _, l := range lines {
			o, err := s.sendCommand(l, ReaderCmdTimeout)
			if err != nil {
				return nil, err
			}
			outs = append(outs, o)
		}
		a, err := s.sendCommand("nv config apply -y", 300*time.Second)
		if err != nil {
			return nil, err
		}
		out = strings.Join(outs, "\n") + a
		ok = !wrCumulusErr.MatchString(out)
	case "junos":
		o, err := s.configSet(lines, false)
		if err != nil {
			return nil, err
		}
		out = o
		if strings.Contains(pyHead(why, 60), "\"") {
			return nil, errors.New("Invalid comment contains double quote")
		}
		o, err = s.commit("confirmed 5 comment \""+pyHead(why, 60)+"\"", 120*time.Second)
		out += o
		if err != nil {
			return nil, err
		}
		o, err = s.commit("and-quit", 120*time.Second)
		out += o
		if err != nil {
			return nil, err
		}
		ok = !wrJunosErr.MatchString(out)
	default:
		o, err := s.configSet(lines, true)
		if err != nil {
			return nil, err
		}
		out = o
		o, err = s.saveConfig()
		if err != nil {
			return nil, err
		}
		out += o
		ok = !wrOtherErr.MatchString(out)
	}
	if ok {
		return &WriteResult{true, "applied"}, nil
	}
	return &WriteResult{false, "rejected: " + pyHead(lastLine(out), 160)}, nil
}

// RemoveAccount runs the account-removal commands as the break-glass login and returns the configuration read back.
func RemoveAccount(os_, host string, port int, user, pw string, cmds []string, how, backupCmd string) (string, error) {
	s, err := dialSession(os_, host, port, user, pw)
	if err != nil {
		return "", &loginErr{err.Error()}
	}
	defer s.close()
	if how == "shell" {
		for _, c := range cmds {
			if _, err := s.sendCommand(c, 240*time.Second); err != nil {
				return "", err
			}
		}
	} else {
		if _, err := s.configSet(cmds, how != "commit"); err != nil {
			return "", err
		}
		if how == "commit" {
			if _, err := s.commit("", 120*time.Second); err != nil {
				return "", err
			}
		} else if _, err := s.saveConfig(); err != nil {
			return "", err
		}
	}
	return s.sendCommand(backupCmd, 240*time.Second)
}

// IsLoginErr reports a failure to log in (RemoveAccount), which Release DWR access words as "netadmin login: ...".
func IsLoginErr(err error) bool {
	_, ok := err.(*loginErr)
	return ok
}
