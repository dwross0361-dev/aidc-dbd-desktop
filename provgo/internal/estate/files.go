package estate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aidc-dbd/provgo/internal/pyjson"
)

func readTextPy(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := strings.ToValidUTF8(string(b), "�")
	// text mode: universal newlines
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s, nil
}

// FileHasTokens returns up to three unfilled tokens of a served file (empty outside the token directories).
func (e *Estate) FileHasTokens(rel, path string) []string {
	if !TokenScanApplies(rel) {
		return nil
	}
	t, err := readTextPy(path)
	if err != nil {
		return nil
	}
	return FileHasTokensText(t)
}

// LicenseFile is <dir of secrets>/licenses/<serial>.{lic,txt,xml}, or "".
func (e *Estate) LicenseFile(serial string) string {
	for _, ext := range []string{".lic", ".txt", ".xml"} {
		p := filepath.Join(filepath.Dir(e.Conf.Str("secrets")), "licenses", serial+ext)
		if isFile(p) {
			return p
		}
	}
	return ""
}

// ServedOverride is the Cumulus RMA-from-backup ZTP script (nil: serve the file as it is).
func (e *Estate) ServedOverride(n, rel string) []byte {
	e.Mu.Lock()
	r := e.St[n].M("rma")
	d := e.Devs[n]
	var apply *pyjson.Map
	if r != nil {
		apply = r.M("apply")
	}
	files := pyjson.StrList(d.V("files"))
	if r == nil || r.Str("state") != "open" || apply.Str("source") != "backup" || d.Str("os") != "cumulus" ||
		len(files) == 0 || rel != files[0] {
		e.Mu.Unlock()
		return nil
	}
	bkFile := apply.Str("file")
	prov := e.provIP()
	e.Mu.Unlock()
	orig, err := readTextPy(pyJoin(e.Conf.Str("web_root"), rel))
	if err != nil {
		return nil
	}
	bk, err := readTextPy(bkFile)
	if err != nil {
		return nil
	}
	var body []string
	for _, l := range splitLinesPy(orig) {
		if strings.HasPrefix(l, "nv ") || strings.HasPrefix(l, "# ---- configuration: fetched") {
			break
		}
		body = append(body, l)
	}
	for _, l := range splitLinesPy(bk) {
		if strings.HasPrefix(l, "nv set") || strings.HasPrefix(l, "nv unset") {
			body = append(body, l)
		}
	}
	body = append(body, "nv config apply -y",
		fmt.Sprintf(`curl -fsS -m 5 "http://%s/ci/%s/config?msg=restored%%20from%%20backup" >/dev/null 2>&1 || true`, prov, n),
		"exit 0")
	return []byte(strings.Join(body, "\n") + "\n")
}

// Preflight is `check`: prints the report and returns the exit code.
func (e *Estate) Preflight(w io.Writer) int {
	var probs, warns []string
	root := e.Conf.Str("web_root")
	for _, dv := range e.Plan.L("devices") {
		d := dv.(*pyjson.Map)
		for _, f := range pyjson.StrList(d.V("files")) {
			p := pyJoin(root, f)
			if !isFile(p) {
				probs = append(probs, "missing in web root: "+f)
			} else if t := e.FileHasTokens(f, p); len(t) > 0 {
				probs = append(probs, fmt.Sprintf("%s carries unfilled <%s>", f, t[0]))
			}
		}
		if _, has := e.Ident.Get(d.Str("name")); d.Truthy("dhcp") && !has {
			warns = append(warns, fmt.Sprintf("%s has no serial or MAC: it will appear under Unclaimed and need binding", d.Str("name")))
		}
	}
	for _, imv := range e.Plan.L("images") {
		im := imv.(*pyjson.Map)
		p := filepath.Join(root, "images", im.Str("file"))
		if !isFile(p) {
			warns = append(warns, fmt.Sprintf("image missing: images/%s (devices stay on their shipped release if at or above baseline)", im.Str("file")))
		} else if im.Truthy("sha256") {
			if fileSHA256(p) != strings.ToLower(im.Str("sha256")) {
				probs = append(probs, "image checksum mismatch: images/"+im.Str("file"))
			}
		}
	}
	if _, _, err := e.RenderKea(); err != nil {
		probs = append(probs, "Kea template does not render: "+err.Error())
	}
	for i, x := range warns {
		if i >= 40 {
			break
		}
		fmt.Fprintln(w, "[warn] "+x)
	}
	for i, x := range probs {
		if i >= 80 {
			break
		}
		fmt.Fprintln(w, "[FAIL] "+x)
	}
	tag := "ok"
	if len(probs) > 0 {
		tag = "!!"
	}
	fmt.Fprintf(w, "[%s] %d device(s), %d build(s), %d problem(s), %d warning(s)\n", tag, len(e.Devs), len(e.Builds), len(probs), len(warns))
	if len(probs) > 0 {
		return 1
	}
	return 0
}
