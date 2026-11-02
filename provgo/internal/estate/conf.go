package estate

import (
	"os"

	"aidc-dbd/provgo/internal/pyjson"
)

// Version is the builder release; set with -ldflags "-X aidc-dbd/provgo/internal/estate.Version=...".
var Version = "__BUILDER_VER__"

// Defaults is DEFAULTS of the Python service, in its order.
func Defaults() *pyjson.Map {
	O := pyjson.O
	return O(
		"http_bind", "", "http_port", int64(80), "console_bind", "", "console_port", int64(8443), "internal_port", int64(8081),
		"web_root", "/var/www/html", "state_dir", "/var/lib/estate-build", "bundle_dir", "/opt/estate-build/bundle",
		"kea_live", "/etc/kea/kea-dhcp4.conf", "secrets", "/etc/fabric/secrets.env",
		"tls_cert", "/etc/fabric/console.crt", "tls_key", "/etc/fabric/console.key",
		"hook_token_file", "/etc/fabric/estate-hook.token", "local_token_file", "/etc/fabric/estate-local.token",
		"strict_fetch", true, "sniff", true,
		"callin_timeout_min", int64(30), "stage_timeout_min", O("1", int64(20), "2", int64(75), "3", int64(75), "4", int64(30)),
		"os_timeouts", O(
			"junos", O("callin", int64(45), "1", int64(30), "2", int64(90), "3", int64(90), "4", int64(30)),
			"sros", O("callin", int64(45), "1", int64(30), "2", int64(90), "3", int64(90), "4", int64(30)),
			"iosxr", O("callin", int64(45), "1", int64(30), "2", int64(120), "3", int64(120), "4", int64(30)),
		),
		"slow_factor", int64(2),
		"probe_every_s", int64(15), "push_concurrency", int64(4),
		"alert_webhook", "", "smtp_host", "", "smtp_port", int64(25), "smtp_from", "", "smtp_to", "", "smtp_starttls", false,
		"smtp_user", "", "smtp_password", "",
	)
}

// LoadConf is DEFAULTS updated with the config file (an unreadable or invalid file is ignored).
func LoadConf(path string) *pyjson.Map {
	c := Defaults()
	if b, err := os.ReadFile(path); err == nil {
		if m, err := pyjson.LoadsMap(b); err == nil {
			c.Update(m)
		}
	}
	return c
}
