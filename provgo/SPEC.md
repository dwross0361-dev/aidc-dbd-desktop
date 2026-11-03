# aidc-prov: external interface specification

Source of truth: `estate_build.py` (Python 3.8+, stdlib only, ~5,300 lines) and `console.html`, as shipped in the
installer under `provision/`. `aidc-prov` is a drop-in replacement: same plan, same configuration, same state
directory, same HTTP endpoints and JSON, same CLI. A host can stop one engine, start the other, and keep its state.

Phase tags: **P1** = this phase (plan, `check`/`init`/`user`, state, web-root serving, check-ins, stages and
timeouts, run/wave sequencing, Kea config writing, console login/sessions, console read APIs and the state-only
operator actions the console needs). **P2** = device reader, probes, automatic gate checks, backups/diff, TFTP.
**P3** = push, RMA, runners, hand-over/export, release, standby/HA sync and promote, cross-site peers, alerts,
tuning, results. All three phases are implemented (sections 13 and 14 describe P2 and P3).

---

## 1. CLI

`aidc-prov [cmd] [args...] [flags]` (Python: `estate_build.py`). Default `cmd` is `serve`.

| Command | Phase | Behavior | Exit |
|---|---|---|---|
| `serve` | P1 | Load plan, open state, start listeners and the 2 s engine tick. | runs |
| `init [--admin-pass P]` | P1 | If `users` has any row: print `logins already exist; use 'user add NAME' or 'user reset NAME'`, exit 0. With a password: `pw_rule("admin", P)` must pass (else `initial admin password: <why>`, exit 2); creates `admin` (role admin, org dwr, must_change=1). Without: temporary 16-char password from `ABCDEFGHJKMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789`, printed. `ESTATE_ADMIN_PASS` env is the flag default. | 0/2 |
| `check` | P1 | Preflight (see 1.1). | 0 ok / 1 problems |
| `check --readiness` or `check readiness` | P1 | Prints each readiness item `"%-10s %s%s"` (RESULT, label, `  -- note`), then `Ready: Start build is open.` or `Start build is held: N item(s) fail or wait for confirmation (console: Readiness)`. | 0/1 |
| `user list` | P1 | `"%-20s %-8s %-16s %s%s"` per user (name, role, planes or `all`(admin)/`-`, `disabled `, `change-required`), then `people per plane: oob N, ...` + `   WARNING: fewer than two on ...`. | 0 |
| `user add NAME [--role R] [--planes P] [--org O]` | P1 | Role default `admin`, org default `customer`. Prints `temporary password for NAME: PW   (must be changed at first login)`; audit `cli:root user-add`. Error (exists, bad role/plane): message, exit 2. | 0/2 |
| `user reset NAME` | P1 | New temporary password, must_change=1, sessions deleted, audit `password-reset`. | 0/1 |
| `user disable\|enable NAME` | P1 | `user_update(disabled=...)` with the last-two-admins rule; prints message. | 0/1 |
| `user` (other) | P1 | usage line, exit 2. | 2 |
| `status` | P1 | GET `http://127.0.0.1:<internal_port>/local/status`, print body. | 0/1 |
| `start [--plane X] [--reason R]`, `pause`, `resume` | P1 | POST `/local/<cmd>` with `{"plane","reason"}` and `X-Estate-Token: <local token>`; print body. HTTP error: print body, exit 1. Unreachable: `service not reachable on <base>: <err>`, exit 1. | 0/1 |
| `lab-slice SLICE.csv [--reason R] [--macs F]` | P1 | Reads `node` column; `--macs` file `NODE MAC` / `NODE,MAC` lines; POST `/local/lab-slice`. | 0/1/2 |
| `result BUSBW TARGET` | P2 | `float()` both (missing/bad: traceback, exit 1); POST `/local/result` `{"kind":"nccl","busbw","target"}`; prints the body. | 0/1 |
| `promote` | P3 | POST `/local/promote` (as `start`); prints the body. | 0/1 |
| `fingerprint` | P1 | SHA-256 (hex) of the DER of `tls_cert`; error `cannot read <path>: <err>`, exit 1. | 0/1 |
| `probe-dhcp [--nic N]` | P3 | Broadcast DISCOVER, report offers. | 0/1 |
| `record-vendor --account A` | P3 | Writes `/etc/fabric/vendor-access.json` (0600, indent 1): `accounts` (not root/none), `sudoers` files naming them, `ssh_keys` fingerprints (`ssh-keygen -lf`), `recorded`. | 0 |
| `release-vendor` | P3 | Asks the four confirmations and the netadmin password, POST `/local/release-vendor`, then polls `/local/handover` every 5 s until `release.state` is not running; prints removed/failed/host lines. | 0 done / 1 |
| `handover-export --out D` | P3 | POST `/local/handover-export` `{"out": D or /root}` (600 s): `[ok] hand-over export written: P ...` or `[!] msg`. | 0/1 |
| `rma-restore ZIP --device N` | P3 | Service stopped: `restore_asbuilt` (identities from `asbuilt.csv` as `as-built` bindings, Done devices restored, every build complete, run complete, `handover.exported`, RMA opened on N with ticket `maintenance`): `[ok] ...` / `[!] ...`; usage, exit 2. | 0/1/2 |

Flags (all commands): `--plan` (default `<dir of program>/estate.json`), `--conf` (default `/etc/fabric/estate.conf`),
`--kea-template` (default `<dir>/kea-dhcp4.json`), `--console` (default: embedded copy; a given path is served instead),
`--plane {oob,edge,fe,be,all}` (all), `--reason`, `--role {admin,operator,viewer}` (admin), `--planes`, `--readiness`,
`--org {customer,dwr}` (customer), `--out`, `--account`, `--device`, `--admin-pass`, `--nic`, `--macs`.
`status/start/pause/resume/result/lab-slice/promote/fingerprint` read only the config file (no plan load).

### 1.1 `check` (preflight)
For each plan device: each `files[]` entry must exist under `web_root` (`[FAIL] missing in web root: F`) and carry no
unfilled token (`[FAIL] F carries unfilled <TOK>`); a device with `dhcp` and no identity is a warning
(`[warn] NAME has no serial or MAC: it will appear under Unclaimed and need binding`). For each `images[]`: missing →
warn `image missing: images/F (devices stay on their shipped release if at or above baseline)`; `sha256` set and
different → `[FAIL] image checksum mismatch: images/F`. Kea template must render (`[FAIL] Kea template does not render: E`).
Output: up to 40 warnings, up to 80 problems, then `[ok|!!] N device(s), N build(s), N problem(s), N warning(s)`.

## 2. Configuration (`--conf`, JSON) — P1

Effective config = `DEFAULTS` updated with the file's top-level keys (unreadable/invalid file ignored), then each
`plan.service` key fills in only if absent.

| Key | Default | Used by |
|---|---|---|
| `http_bind` / `http_port` | `""` / 80 | boot listener; bind = key or `site.prov_ip` or `0.0.0.0` (P1) |
| `console_bind` / `console_port` | `""` / 8443 | console listener (P1) |
| `internal_port` | 8081 | internal listener, always `127.0.0.1` (P1) |
| `web_root` | `/var/www/html` | served files (P1) |
| `state_dir` | `/var/lib/estate-build` | `estate.db` and files (P1) |
| `bundle_dir` | `/opt/estate-build/bundle` | runners, push cwd, planned configs (P2/P3) |
| `kea_live` | `/etc/kea/kea-dhcp4.conf` | Kea config written (P1) |
| `secrets` | `/etc/fabric/secrets.env` | env file; its directory holds `licenses/`, `foreign-inventory/` (P1) |
| `tls_cert` / `tls_key` | `/etc/fabric/console.crt` / `.key` | console TLS (both files present → HTTPS, TLS ≥ 1.2) (P1) |
| `hook_token_file` | `/etc/fabric/estate-hook.token` | `/hook/lease` token (P1) |
| `local_token_file` | `/etc/fabric/estate-local.token` | `/local/*` token; created 0600 (24 random bytes hex) at serve if absent (P1) |
| `strict_fetch` | `true` | owner checks (P1) |
| `sniff` / `prov_nic` | `true` / `""` | DHCP sniffer (P1) |
| `callin_timeout_min` | 30 | (P1) |
| `stage_timeout_min` | `{"1":20,"2":75,"3":75,"4":30}` | (P1) |
| `os_timeouts` | junos/sros `{callin:45,1:30,2:90,3:90,4:30}`, iosxr `{callin:45,1:30,2:120,3:120,4:30}` | (P1) |
| `slow_factor` | 2 | (P1) |
| `role` | `primary` | `standby` restricts actions (P1 read; P3 HA) |
| `reader_sim_dir` | — | reader simulation; readiness service probes forced true (P1/P2) |
| `vendor_access` | `/etc/fabric/vendor-access.json` | (P3) |
| `probe_every_s` 15, `done_probe_s` 120, `push_concurrency` 4, `check_every_s` 60, `reader_workers` 16, `backup_every_h` 6, `backup_keep` 20 | | (P2/P3) |
| `ssh_per_device` 1, `ssh_retries` 2, `ssh_backoff_s` 5, `backup_stagger_s` 2 | | (HS v1.56 r3, section 16) |
| `tftp` true, `tftp_standby`, `tftp_bind`, `tftp_port` 69, `tftp_root` `/srv/tftp` | | (P2) |
| `peer_url`, `peer_cert_sha256`, `sync_every_s` 60, `sites`, `sites_every_s` 3600 | | (P3) |
| `alert_webhook`, `smtp_host`, `smtp_port` 25, `smtp_from`, `smtp_to`, `smtp_starttls`, `smtp_user`, `smtp_password` | | (P3) |
| `break_glass_user` netadmin, `nccl_cmd`, `accept_cmd`, `isolation_cmd`, `dpu_parallel` 8, `weka_nodes`, `host_ssh` | | (P3) |

`secrets.env`: `KEY=VALUE` lines, `#` comments, matching surrounding quotes stripped. Keys used: `INVENTORY_TOKEN`,
`ESTATE_SYNC_TOKEN`, `READ_USER[_OS]`, `READ_PASSWORD[_OS]`, `ESTATE_SSH_KEY`, `ESTATE_SSH_USER`, runner secrets.

## 3. Plan (`estate.json`) fields used

Digest = SHA-256 of the file text; shown as `first8...last4`.

| Path | Use |
|---|---|
| `site.code`, `site.prov_ip`, `site.services.{ntp,dns,tacacs,syslog,radius}` | events, local address, readiness |
| `dhcp.subnet_id` | Kea: must exist in template |
| `service` | config defaults |
| `builds[]`: `key`, `name`, `num`, `waves[]` (`name`, `devices[]`, `warn_only`), `gate[]` (`key`, `label`, `type` devices_done/reachable/auto/confirm, `kind`, `note`, `warn_only`, `devices` for lossless) | engine, views |
| `devices[]`: `name`, `short`, `role`, `os`, `os_label`, `ip`, `seed_ip`, `ztp_links[]` (`subnet`,`ip`,`router`), `rack`, `release`, `model`, `files[]`, `tftp_files[]`, `id` (`kind` mac/sn, `value`, `mac`), `dhcp` (`sn_forms` client-id/vendor-class, `vc_prefix`, `options[]`, `boot-file-name`, `next-server`, `server-hostname`), `mgmt_link` (`switch`,`port`), `config_signal` (checkin/push/reachable/manual/runner/aggregate), `manual_hint`, `aggregate`, `probe_ips`, `probe_port`, `verify_ports`, `push`, `planned_config` | everything |
| `images[]` (`file`, `sha256`), `floor`, `runners[]` (`key`, `label`, `kind`, `targets`, `devices`...), `registry`, `ha.enabled`, `tuning` (`os`,`note`,`params[]`), `acceptance.criteria` | views, readiness |

Indexes: `by_ip` = device `ip`, then `seed_ip`, then every `ztp_links[].ip` → device. `file_owner` = each `files[]`
entry with leading `/` stripped → device (last device wins). `wave_of` = device → (build key, wave index).
`foreign` = `<dir of secrets>/foreign-inventory/<SITE>.csv` rows `device,value` (value MAC-normalized or upper-cased)
plus kv `foreign_peers`.

## 4. State directory — P1

| File | Content |
|---|---|
| `estate.db` | SQLite, schema below (rollback journal, default settings) |
| `backups/<dev>/*.cfg` | configuration backups (created dir at start; P2 writes) |
| `push-<dev>.log`, `runner-<key>.log`, `runner-accept-smoke-pod<N>.log`, `rma-hold-<dev>.conf` | P3 |
| `snapshot.db`, `primary.db`, `estate.db.standby-<ts>` | P3 (HA) |

Device names in file names: `[^A-Za-z0-9_-]` → `-`.

```sql
CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT);                -- v = json.dumps(value)
CREATE TABLE IF NOT EXISTS devices(name TEXT PRIMARY KEY, data TEXT);     -- data = json.dumps(device state)
CREATE TABLE IF NOT EXISTS bindings(name TEXT PRIMARY KEY, kind TEXT, value TEXT, source TEXT, ts REAL, user TEXT);
CREATE TABLE IF NOT EXISTS blocked(value TEXT PRIMARY KEY, reason TEXT, ts REAL);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, level TEXT, device TEXT,
                                  stage INTEGER, flag TEXT, text TEXT, user TEXT);
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY AUTOINCREMENT, ts REAL, user TEXT, action TEXT,
                                 target TEXT, detail TEXT);
CREATE TABLE IF NOT EXISTS users(name TEXT PRIMARY KEY, pw TEXT, role TEXT, created REAL);
CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY, user TEXT, expires REAL);
CREATE TABLE IF NOT EXISTS unclaimed(key TEXT PRIMARY KEY, data TEXT);    -- data = json.dumps(entry)
CREATE TABLE IF NOT EXISTS gates(build TEXT, chk TEXT, result TEXT, user TEXT, note TEXT, ts REAL, PRIMARY KEY(build, chk));
CREATE TABLE IF NOT EXISTS rma(id INTEGER PRIMARY KEY AUTOINCREMENT, device TEXT, ticket TEXT, old_kind TEXT,
                               old_value TEXT, new_kind TEXT, new_value TEXT, state TEXT, opened REAL,
                               closed REAL, user TEXT, checklist TEXT);
-- added when missing (ALTER TABLE users ADD COLUMN ...):
--   must_change INTEGER DEFAULT 0, planes TEXT DEFAULT '', disabled INTEGER DEFAULT 0, org TEXT DEFAULT 'dwr'
```

JSON in `kv`/`devices`/`unclaimed` is written the way Python's `json.dumps` writes it (`", "`/`": "` separators,
ASCII-escaped, float repr, key order kept); Go reads and writes the same text.

kv keys: `run` `{state idle|running|paused|complete, builds[], started, plane}`; `bstate` per build
`{state queued|building|gate|blocked|complete, open_waves, wave_ts{wi:ts}, gate_ts, passed, override, held}`;
`readiness`, `handover`, `auto_res` (`"build|check"` → result), `results`, `runners`, `tuning`, `foreign_sync`,
`foreign_peers`, `last_sync`, `peer_pins`, `promoted_from`.

Device state (`devices.data`), defaults merged under stored values: `stage 0, flag "", detail "", ts {}, lease_ip "",
mac "", serial "", client_id "", circuit "", last_seen 0, attempts 0, fetched {}, count 0, rma null, manual false,
clock 0`; also written: `disc {model,port}`, `disc_ok`, `mac_check`, `id_note`, `items` (P3), `rma{...}` (P3).

Unclaimed entry: `key` (MAC or serial, or `manual:<v>`), `first`, `last`, `state open|bound|dismissed`, `mac`,
`serial`, `client_id`, `vendor_class`, `model`, `circuit`, `remote_id`, `ip`, `giaddr`, `why`, `foreign`,
`suggest[{device, why}]`, `bound_to`, `dismissed{by,ts,reason}`.

Identity map (`ident`): plan `id` (source `inventory`) then `bindings` rows override. Index: normalized MAC / upper
serial → first device holding it (plan order then binding order).

## 5. Stage model — P1

`STG = [Expected, DHCP, Bootstrap, Image, Config, Verified, Done]` (0..6). Flags: `"" failed missing waiting slow
quarantined rma down unclaimed(view only)`.

- `set_stage(n, s, detail)`: ignored if `s <= stage`. Clears flag failed/missing/waiting/slow; fills `ts[k]` for
  skipped stages (setdefault), sets `ts[s]=now`; event `"<short>: <STG[s]>[ (detail)]"`; at 6 also
  `SoT status planned -> active` and closes an open RMA.
- `fail(n, detail)`: once (flag failed); event fail `"<short>: failed at <STG[min(6,stage+1)]> -- detail"`.
- `wave_open(n)`: open RMA → true; else build state in building/gate/complete/blocked and wave index < open_waves.
- `settled(n)`: stage 6 or quarantined.

Timeouts (each tick, builds in building/gate/blocked, open waves; devices with flag `""`/`slow`, stage < 6, not
aggregate). Limits per device OS: `callin = os_timeouts[os].callin || callin_timeout_min`, stage limits =
`stage_timeout_min` updated by the OS entry; `fac = max(1, slow_factor)`.
- `config_signal == manual` and stage ≥ 1: stage < 4 → detail `waiting for operator: <manual_hint|mark configured once applied>`; no timeout.
- Stage 0 (not in a warn_only wave): since = max(wave opened, clock). `> callin*fac` min → flag missing, event fail
  `not called in after N min`; `> callin` min → flag slow, event warn `... -- waiting (slow)`.
- Stage ≥ 1 with a limit: since = max(ts[stage], last_seen, clock, every fetched time). `> lim*fac` → `fail("no
  progress past STG for N min")`; `> lim` → slow.
`%d` of float minutes truncates (Python).

Engine tick (every 2 s; standby only writes Kea): run `running` → first non-complete build of `run.builds`:
queued → building, open_waves 1, event `Build started: NAME; wave 1 open (WAVE)`. Building: when every device of
the newest open wave is settled (or the wave is warn_only and opened > 60 s ago) open the next wave (`NAME: wave N
open (WAVE)`) or go to `gate` (`NAME: all waves sequenced; gate checks running`); else record `held` (failed or
missing devices) with a warn event. Gate/blocked: evaluate the gate; override → pass; all pass/warn/confirmed →
pass (`Gate passed: NAME[ (note)]`, P2 backups queued); blocked transitions. All builds complete → `Run complete`.
Then timeouts, then Kea apply if dirty. (P2 adds probes and auto checks; P3 pushes.)

Gate evaluation (P1 for state-derived checks): `devices_done`, `reachable`, `confirm` and recorded `auto`
(from `gates` table) as in Python; an `auto` check without a recorded confirmation shows `auto_res` (P2 writes it)
or `running / automatic check queued`.

## 6. Check-in matching — P1

Evidence (Kea hook or sniffer): `mac` normalized; serial from `client_id` (`serial_from_client_id`) else from
`vendor_class` (`Juniper-<model>-<serial>` → serial upper, model lower).

`ident_match`: serial or MAC in `blocked` → `retired: <reason>`. Index hits for MAC and serial; the hit with the
lower position in the identity map wins (`MAC`/`serial`). Else `ip` in `by_ip`: no MAC in the evidence or the device
has no identity → `reserved address`; identity kind sn → `reserved address (serial class)`. Else `not in inventory`.

Matched: copy mac/serial/client_id/circuit into state; discovery (model from vendor class `Cisco <PID>` or
`DCS-/CCS-`, port from option 82 circuit vs `mgmt_link`) — a mismatch before Config flags `failed` with
`discovery: ...` unless `disc_ok`; matched by serial with a manifest MAC → `mac_check`/`id_note`; `last_seen=now`;
committed with ip → `lease_ip`. Wave not open → flag `waiting` (`called in (how); its wave is not open yet --
holding lease only`, event once). Else clear waiting/slow; committed lease on the device's ip/seed/ztp-link ip and
stage < 1 → stage 1 `lease IP, matched by HOW`.

Unmatched with MAC or serial → unclaimed (key MAC else serial; bound/dismissed entries untouched); `suggest`: up to
the planned-port matches (`circuit` `sw:port` / `sw|port` vs `mgmt_link`), then same-model stage-0 open-wave devices
(max 5). New entry → warn event (`refused ... retired`, `refused serial X: it belongs to site S`, or `unknown
serial|MAC X called in[ -- likely DEV]`).

Pure helpers (exact): `norm_mac` (12 hex digits → `aa:bb:..`, else `""`); `hex_to_bytes` (`0x` prefix, `:`/space
removed, even length); `serial_from_client_id` (type 0 → printable rest, type 1 → `""`, else printable all; strip,
upper); `serial_from_vendor_class` (`^Juniper-([A-Za-z0-9-]+?)-([A-Za-z0-9]+)$`); `norm_port` (lower, strip
`ethernet|eth|et-|ge-|xe-|mge-|swp|e` prefix, `/`→`:`, cut at `.`).

Kea hook body (`POST /hook/lease?event=E`): `KEY=VALUE` lines. `leases4_committed`: `LEASES4_SIZE`,
`LEASES4_AT<i>_{ADDRESS,HWADDR,CLIENT_ID}`; `lease4_renew|lease4_rebind`: `LEASE4_{ADDRESS,HWADDR,CLIENT_ID}`; all
committed. `QUERY4_OPTION_82_SUB_OPTION_1/2` (hex → printable, else as given) → circuit/remote_id;
`QUERY4_GIADDR` → giaddr.

Sniffer: AF_PACKET raw IPv4 on `prov_nic` (all interfaces + warn event if unset); UDP dst 67, BOOTP magic; options
53/60/61/12/50/82(1,2); DISCOVER and REQUEST only (never committed). Socket failure → warn event, sniffer off.

Device check-in `GET /ci/<host>/<stage>[/<msg>]` (also `?msg=` / `?n=`): unknown host 404; `strict_fetch` and source
not in (device ip, lease_ip, 127.0.0.1, prov_ip) → 403 `address mismatch` + warn event. `fail` → fail; `ztp|image`
→ 3; `config` → 4 (fills ts[3]); `verify` → 5; `count` (aggregate only) → clamp, 6 at total else 4, event per
change; other → 400 `unknown check-in`; bad count → 400 `count?n=`.

## 7. Web-root serving (boot listener) — P1

Plain text replies `"<msg>\n"`, `Content-Type: text/plain`; `Server: estate`. GET and HEAD (HEAD: no body, no
fetch record). Path = URL-decoded path of the request target (`//` prefix collapsed, as Python 3.12+).

1. `/ci/...` → check-in (above).
2. `canon_rel`: strip leading `/`; empty, NUL, any empty or `..` segment → **400 `bad path`** + warn event
   `refused P to N|IP: not a canonical file path`; `.` segments dropped.
3. `licenses/<serial>.<ext>`: requester (by ip, then lease_ip) must have identity kind sn equal to the serial and
   `<dir of secrets>/licenses/<SERIAL>.{lic,txt,xml}` must exist → 200 raw; else **403 `license belongs to another serial`**.
4. realpath under the real web root and a regular file, else **404 `not found`**: requester known and `images/` →
   warn event; requester known otherwise → fail event, and `fail(device)` if it owns that file.
5. `strict_fetch`, file has an owner, requester not local (127.0.0.1, ::1, prov_ip) and not the owner → **403
   `this file belongs to another device`** + warn event (device = owner).
6. `strict_fetch`, requester unknown and not local → **403 `unknown requester`** + warn event.
7. Unfilled token: first segment in `bootstrap configs ztp tftp eos srl sros hbn xr` and not README*/`*.md`/`*.csv`;
   lines not starting (after spaces) with `#`, `!`, `//`; regex `<([A-Z][A-Za-z0-9_. &-]*)>`; first 3 → **409
   `unfilled token`** + fail event, `fail(device)`.
8. Served override (Cumulus RMA from backup, P3 state): device's first file is rebuilt as the original head (lines
   before the first `nv ` or `# ---- configuration: fetched`) + backed-up `nv set|unset` lines + `nv config apply
   -y`, check-in curl, `exit 0`. 200 text/plain.
9. 200, `Content-Type` `application/octet-stream` for `images/` else `text/plain`, `Content-Length` = size.
   After a GET by a known device: `on_fetch` → `fetched[rel]=now`; `images/` → stage 3 (`image F served (N MB)`);
   owner's file → stage 1 (`lease seen through the fetch`) if needed, then 2 (`REL served`).
Other methods → Python `BaseHTTPRequestHandler` 501.

## 8. Internal listener (127.0.0.1:internal_port)

| Method path | Auth | Phase | Response |
|---|---|---|---|
| GET `/local/status*` | none | P1 | `{"run", "builds": [[name,state]...], "unclaimed": N, "kea": str}` |
| GET `/local/handover*` | none | P1 | kv `handover` |
| GET other | | | 404 `{}` |
| POST `/hook/lease?event=E` | `X-Estate-Token` = hook token | P1 | 200 `{"ok": true}` |
| POST `/local/start` | local token | P1 | `{"ok","msg"}` 200/409 (`start_run(plane, "cli:"+user, reason)`) |
| POST `/local/pause`, `/local/resume` | local token | P1 | 200 `{"ok": true}`; event `Run <state> from the command line` |
| POST `/local/lab-slice` | local token | P1 | `{"ok","msg"}` 200/409 |
| POST `/local/result` | local token | P2 | `submit_result(kind, busbw, target, "cli")` → 200 `{"ok","msg"}` |
| POST `/local/release-vendor` | local token | P3 | `release_vendor("cli:root", password, body)` → `{"ok","msg"}` 200/409 |
| POST `/local/handover-export` | local token | P3 | export written to `<out>/<name>.zip` (0600), receipt confirmed with its SHA-256 → 200 `{"ok": true, "msg": path}` |
| POST `/local/promote` | local token | P3 | `promote()` (no role check, as Python) → 200/409 |
| POST other | | | 404 `{}` |
Missing/empty token or mismatch → 403 `{"error": "token"}`; body > 1 MiB or bad length → 413 `{"error": "length"}`.

## 9. Console listener (console_bind:console_port, HTTPS when cert+key exist)

All replies: `Cache-Control: no-store`, `X-Frame-Options: DENY`, CSP `default-src 'self'; style-src 'self'
'unsafe-inline'; font-src 'self' data:; script-src 'self' 'unsafe-inline'; img-src 'self' data:`.
Session: cookie `estate_session=<token_urlsafe(32)>`, 12 h (`sessions.expires`), user not disabled.
Login: `Set-Cookie: estate_session=T; Path=/; HttpOnly;[ Secure;] SameSite=Strict; Max-Age=43200` (Secure only over
TLS). 5 failures per source IP in 300 s → 429. Unknown/disabled names are hashed against a dummy.
Order of checks for GET: `/`,`/index.html` (no auth) → `/inventory` → 401 `{"error":"login"}` → `/api/whoami` →
must_change 403 `{"error":"password change required"}` → routes.

| GET | Phase | Response |
|---|---|---|
| `/`, `/index.html` | P1 | console.html (`text/html; charset=utf-8`), byte-identical |
| `/inventory` (`X-Estate-Inventory` = secrets `INVENTORY_TOKEN`) | P3 | `{"site", "items": [{device,kind,value}]}`; 403 `{"error":"inventory token"}` |
| `/api/whoami` | P1 | `{user, role, planes[], must_change, org}` |
| `/api/users` (admin) | P1 | `{"users": [{name, role, org, planes[], must_change, disabled, created}], "coverage"}`; else 403 `admins only` |
| `/api/plan` | P1 | `{site, builds[{key,name,num,waves[{name,devices,warn_only}]}], devices{n:{short,role,os,os_label,ip,rack,release,aggregate,push,config_signal,files,model}}, floor, runners[{key,label}], digest}` |
| `/api/state` | P1 | `{server_time, version, site, digest, run, builds[{key,name,num,state,waves[{name,devices,warn_only,open}],gate[],override,passed,held,ready[ready,total]}], devices{n:{stage,flag,detail,count,items,id,id_source,rma}}, unclaimed[open entries], kea, push_cred, access(coverage), pushing[], tuning, handover(+customer_admins), runners, results, ha{role,peer,last_sync}, registry, foreign_sync, foreign_n}`; open unclaimed suggestions mark stage-0 devices `unclaimed` in the view |
| `/api/events?since=ID&limit=N` | P1 | rows `{id,ts,level,device,stage,flag,text,user}` id > since, limit ≤ 200000 (default 500) |
| `/api/device/<n>` | P1 | `{plan, state, identity, events(last 40), backups[≤10], backup_seen{}, push_log}`; 404 `unknown device` |
| `/api/device/<n>/diff[?vs=prev]` | P2 | planned vs newest backup through `cfg_norm` (`backup, planned, diff, added, removed, same, defaults_hidden, backups[≤10]`); `vs=prev`: difflib unified diff (n=1, ≤400 lines) of the last two backups |
| `/api/device/<n>/backup/<ts>.cfg` | P2 | text/plain attachment; 404 `no such backup` |
| `/api/gate-evidence?build=&check=` | P1 | `{result, note, evidence}` from `auto_res` |
| `/api/runner-log/<key>` | P1 | text/plain, last 3000 chars of `runner-<key>.log` |
| `/api/readiness` | P1 | `{items[{key,label,result,note}], fails, accepted, ok}` |
| `/api/export/{asbuilt,sot-status,audit,events,readiness}.csv` | P1 | text/csv attachment `<SITE>-<name>`; 404 `unknown export` |
| `/api/handover/export.zip` | P3 | customer admins only (else 403 `{"ok": false, "msg": ...}`); `application/zip`, `Content-Disposition`, `X-SHA256`; records kv `handover.built` |
| other | | 404 `{"error":"not found"}` |

POST: body JSON ≤ 1 MiB (413 `length`, 400 `json`). `/api/login` first (no auth); then 401 login; header
`X-Requested-With: estate-console` required (403 `csrf`); `/api/logout`, `/api/password` allowed while must_change;
then 403 password change required; viewer 403 `read-only login`; standby 409 (except promote); `device` named and
unknown → 404. Operator scope: device → its build; gate → `build`; run → `plane`; users/push-credential/promote/
tuning approve → admin only; result → be (isolation → fe); else any. Denied: 403 `{"ok": false, "msg": "not
permitted: your login acts on <planes|no plane> only"}`. Default reply `{"ok","msg"}` 200/409.

| POST | Phase | Notes |
|---|---|---|
| `/api/login` `{user,password}` | P1 | 200 `{"ok":true}` + cookie; 401 `wrong user or password` (audit login-failed); 429 |
| `/api/logout` | P1 | deletes session; `Set-Cookie: estate_session=; Path=/; Max-Age=0` |
| `/api/password` `{current,new}` | P1 | rules: ≥ 12 chars, not the user name, differs from current; other sessions end; 409 on error |
| `/api/users/add` `{name,role,planes,org}` | P1 | name `^[A-Za-z0-9._@-]{2,40}$`; `{"ok","msg":"created","password"}` |
| `/api/users/reset` `{name}` | P1 | `{"ok","msg","password"}` |
| `/api/users/update` `{name,role,planes,disabled,org}` | P1 | last two admins protected |
| `/api/run` `{plane,reason}` | P1 | readiness not ok and run idle → 409 `{"ok":false,"readiness":true,"msg":...}` |
| `/api/pause` | P1 | toggles running/paused; 409 when idle/complete |
| `/api/bind`, `/api/dismiss`, `/api/unbind`, `/api/retry`, `/api/reserve`, `/api/quarantine`, `/api/mark-configured`, `/api/count`, `/api/gate/confirm`, `/api/gate/override`, `/api/readiness/confirm` | P1 | state-only operator actions, as Python |
| `/api/gate/recheck` `{build}` | P2 | runs the build's auto checks, waits ≤60 s: `re-checked N automatic check(s): 2 pass, 1 blocked` |
| `/api/backup` `{device}` | P2 | `take_backup(n, "requested by U")`: 200 `{"ok": "saved F"|"unchanged since F", "msg": "Backed up HH:MMZ: ..."}`; 409 `backup not possible (reader unavailable or unsupported)` |
| `/api/result` `{kind, text|busbw,target}` | P2 | isolation (FE scope) / accept text (BE scope) / bus bandwidth (`bus bandwidth and target in GB/s` on bad numbers) |
| `/api/push-credential` `{user,password}` | P3 | admin; held in memory only; 409 `user and password` |
| `/api/rma/start` `{device,ticket}`, `/api/rma/identity` `{device,value}`, `/api/rma/upload` `{device,text}`, `/api/rma/check` `{device,key}`, `/api/rma/apply` `{device,source}`, `/api/rma/mnha-restore` `{device}` | P3 | device required (none: connection dropped, Python KeyError) |
| `/api/handover/final-backup` (admin), `/api/handover/received` `{sha}` (customer admin), `/api/release-vendor` (customer admin) | P3 | |
| `/api/tuning/propose` `{values,note}`, `/api/tuning/apply` `{v,scope}`, `/api/tuning/approve` `{v}` (admin), `/api/tuning/rollout`, `/api/tuning/profile` `{nic,port}` | P3 | `v` through Python `int()`: ValueError → `version: a number`, TypeError → connection dropped; profile replies `{ok,msg,profile}` |
| `/api/runner/retry` `{key}`, `/api/promote` (standby only; else `already primary`) | P3 | |
| `/sync/upload` | P3 | before login: standby role and `X-Estate-Sync` = `ESTATE_SYNC_TOKEN` (else 403 `{"error":"sync"}`), body ≤ 512 MiB, `receive_snapshot` → `{"ok": true}` |

## 10. Auth — P1

- Password hash: `pbkdf2$<iters>$<salt>$<hex>`; PBKDF2-HMAC-SHA256, salt = 32 hex chars used as UTF-8 bytes,
  default 240000 iterations, 32-byte key. Verify recomputes with the stored iterations and salt (constant-time).
  Dummy: `pbkdf2$240000$000...0(32)$000...0(64)`.
- Roles: admin (all), operator (its `planes` subset of oob/edge/fe/be; `all` needs every build key), viewer (read).
- Coverage: per build key, active admins + operators scoped to it; `short` = fewer than 2; `admins` count.
- New/reset passwords are temporary (`must_change=1`): only whoami, logout and password change work until changed.
- Local token (`local_token_file`, 0600) for `/local/*`; hook token (`hook_token_file`) for `/hook/lease`.
  No token configured → POST refused.

## 11. Kea — P1

`render_kea`: deep copy of the template; `plan.dhcp.subnet_id` must be a subnet id (`subnet4` + shared networks),
else error. Each `ztp_links` /30 gets a stable id from 1000 upward skipping used ids (plan order). For each device
with an open wave and not quarantined: link subnets (pool = the link ip, `routers` + device options, boot fields);
reservation (`ip-address`, `hostname`, boot fields, `option-data` without `image-file-name` options whose file is
missing under web_root, `hw-address` for MAC identities, `client-id` `00:<hex of serial>` for sn+client-id) in the
subnet holding the ip, plus a copy on `seed_ip`; sn+vendor-class → client class `dev-<n>` with test
`option[60].text == '<vc_prefix><serial>'` (hex form when the text is outside `^[A-Za-z0-9 ._/:+-]*$`) and pinned
single-address pools on ip and seed. `PINNED` class test = OR of the tests (or `'pinned' == 'none'`); generated
classes are prepended. Count = reservations + classes + links.

`kea_apply`: write `<kea_live>.estate-tmp` (`json.dump(indent=2)`), rename over `kea_live`; `config-reload` over the
template's `control-socket.socket-name` (5 s timeout). `kea_status` texts: `N reservations live (Kea: <text>)`,
`N reservations written; Kea control socket unreachable (<err>)`, `N reservations written; no control socket
configured`, `could not write <path>: <err>`. A failure logs one warn event per distinct status. Applied when
dirty: start, run start, wave open, bind/unbind, retry, quarantine, RMA.

## 12. Events and audit

`events` rows carry the device's current stage/flag when a device is named. Levels: info, warn, fail. Each event is
also printed `"<iso> <level:-4> <device|-> <text>"`. warn/fail, `Gate passed*`, `Build complete*` are queued as
alerts (P3: section 14). Audit actions: login, login-failed, password-change, password-reset, user-add, user-update,
start, run-paused/running, bind, unbind, dismiss, retry, re-serve, quarantine, mark-configured, gate-confirm,
gate-override, readiness-confirm/accept, discovery-accept, plus P2/P3 actions.

## 13. Phase 2: reader, probes, automatic checks, backups, TFTP

Implemented in `internal/estate/{reader,parsers,checks,results,probes,backups,tftp,difflib,pyre}.go`.

- **Reader** (`read`): `reader_sim_dir` → `<dir>/<device>/<slug(cmd)>.txt` (missing: unavailable `no recorded output for D: C`). Else OS
  must be in `DT` (junos eos nxos cumulus srl sros iosxr iosxe ftd) with an `ip` (`no SSH reader for OS`); login
  `READ_USER_<OS>`/`READ_USER`, `READ_PASSWORD_<OS>`/`READ_PASSWORD` from secrets.env (`READ_USER / READ_PASSWORD not set in
  secrets.env`); one SSH session per call (HS v1.56 r3: inside the device's session limit, reused within a checker pass, transient
  failures retried: section 16), netmiko session semantics per device type (DECISIONS P2-2). Errors other than
  "unavailable" become `read failed: <text>` in the check.
- **Command sets**: `CMDS` (bgp, evpn, mnha, roce; `@nvue-bgp` = `nv show vrf -o json` then `nv show vrf V router bgp neighbor -o json`
  per sorted VRF, outputs starting `Error:` dropped unless all do), `CNT_CMDS` (`@per-if:` expands over the planned
  `nv set interface X qos pfc-watchdog` interfaces), `VERSION_CMD`, `LLDP_CMD`, `BACKUP_CMD`.
- **Check kinds** (`eval_kind`): bgp/evpn (sessions toward quarantined peers set aside, `min`, newly down marked `(new)`,
  summary + `\x00RAW\n` + raw), mnha (SRG1 ACTIVE/BACKUP by position in `devices`; Junos without "MNHA" in the role: chassis
  cluster; FTD failover), roce, lossless (planned lines matching `LOSSLESS_RE` with the tuning overlay swapped in, vs
  `cfg_norm` of the running configuration), software (release target in the output or by digits), cabling (planned
  `interface X link mtu N # -> PEER PORT` vs `lldp_pairs`; far end quarantined or below Config and silent: not checked).
  `nccl` (results), `accept` (T1-T4 scores), `tuned` (tuning record), `isolation` (results) evaluate stored data.
- **run_auto**: devices below Verified (not aggregate) fail "not yet verified"; others read in parallel (`reader_workers`,
  default 16); any unavailable → `unavailable` (console: needs confirmation); failures → `running` until `grace_s`
  (default 300) after `first_fail`, then `blocked` (`warn` with `warn_only`). Stored in kv `auto_res` (`build|check`),
  event on a change to pass/blocked/warn. **checker**: every 10 s, builds at gate/blocked, unconfirmed auto checks whose
  result is not pass and older than `check_every_s` (default 60); also `bond_steer`.
- **Probes** (every 5 s after a cycle): devices stage 1-5 (stage ≥ 4 or `config_signal` reachable), not failed/quarantined,
  wave open, `probe_every_s` apart, on all `verify_ports` (default 22): stage ≥4 → Verified + Done; reachable → Config.
  `probe_ips` aggregates every max(60, probe_every_s): count check-in. Done devices with `verify_ports` every
  `done_probe_s` (120): two misses → flag `down`; answering → cleared. Concurrency/timeouts: DECISIONS P2-1.
- **Backups**: worker every 30 s; Done devices with a backup command, due at once after Done (`after build`), then
  `backup_every_h` (6; off after the hand-over export unless requested/gate-queued); a failed read retries in 900 s; a gate
  pass queues every device of the build. Unchanged text: mtime touched, `unchanged since F`; else `YYYYmmddTHHMMSSZ.cfg`,
  oldest beyond `backup_keep` (20) removed.
- **TFTP** (`tftp` true on a primary, or `tftp_standby`): `tftp_bind`/prov_ip/0.0.0.0 : `tftp_port` (69), root `tftp_root`
  (`/srv/tftp`), RRQ only, blksize ≤1468, tsize, timeout options; owner/unknown requester refused with `strict_fetch`;
  a fetch moves the owner to DHCP/Bootstrap, an image to Image. Bind failure: warn `TFTP off (...)`.

## 14. Phase 3: pushes, RMA, runners, tuning, hand-over, standby, peers, alerts

Implemented in `internal/estate/{push,rma,runners,tuning,handover,ha,alerts,writer}.go` and `cmd/aidc-prov/p3cli.go`.

- **Push** (end of each primary tick): no credential → devices with `push`, stage 2-3, no flag get detail `waiting for the push
  credential`. Due = `push`, no flag, stage 2-3, not pushing, wave open, not MNHA `await-sync`/`done-hold`, every
  `blocks_push` RMA checklist item done (else detail `RMA checklist open`). Candidates are probed on `push.port` (22) and
  started up to the push concurrency (DECISIONS P3-1): `python3 <push.cmd...> [--config <override>]` in `bundle_dir`,
  `FABRIC_USER`/`FABRIC_PASSWORD` in the environment, output in `push-<dev>.log`, detail `pushing configuration (<cmd0>)`.
  Exit 0 → Config (`configuration pushed and committed`), or MNHA hold → `await-sync`, restore → `done`; else
  `fail("push exited N: <last log line[:160]>")`. Override: RMA `apply.source == backup` → the backup file; MNHA hold →
  `rma-hold-<dev>.conf` (the base without `services-redundancy-group N preemption` lines).
- **RMA**: checklist (license for sros/junos/iosxr/nxos/eos, blocks push; MNHA; LACP/ESI done; keys). Start retires the old
  identity (blocked table) and the wire identities (memory only), resets the device to Expected/flag rma, opens the `rma`
  row. Identity (MAC or `^[A-Z0-9][A-Z0-9._/-]{2,63}$`) binds a `manual:` unclaimed entry (source `rma`). Upload (≤ 4 MB)
  becomes the newest backup and applies it. Checker pass: license install (`LICENSE_CMD` through the reader, URL
  `http://<prov_ip>/licenses/<file>`; unavailable → install by hand; other error → retry in 300 s) and MNHA sync wait
  (`SYNC_RE` on `show chassis high-availability information`). Done closes the RMA.
- **Runners** (worker every 10 s, primary only): due when not running/done (failed only after Retry), targets' waves open,
  none quarantined, `needs_stage`, `after` done; missing `secrets`/`conf` → `not configured`. Kinds `local`, `ssh-seq`,
  `dpu`, `rails`, `accept`, `isolation`, `nccl` as Python (rails: each attempt truncates the host's log first, HS v1.56); `runner-<key>.log` (per host: `.<host>`), `$ argv` lines, timeout
  `timeout_s` (7200) → 124. Results recorded as Python (runner state, events, stages for local/ssh-seq targets, results).
- **Tuning**: propose (range, step, rules), apply (scope test/rest/pod:N/all; sim: `applied.txt`; else the SSH writer with the
  push credential or `WRITE_USER`), approve, rollout (pod by pod, T2/T3 smoke test when `accept_cmd` has `{POD}`), profile
  (`aidc-tuned-profile/1`, id and SHA-256 over the canonical JSON).
- **Hand-over**: final backup, export zip (asbuilt, sot-status, audit, events, readiness csv/json, tuning.json, sot-asbuilt,
  apply_asbuilt.py, newest backup per device, README), receipt by SHA-256 prefix (≥ 12), Release DWR access (needs the export
  received, two customer admins, four confirmations, the netadmin password; removes dwr-netadmin per OS, disables DWR logins,
  locks the recorded host accounts, clears the push credential).
- **Standby**: primary with `peer_url` and `ESTATE_SYNC_TOKEN` sends a database snapshot every `sync_every_s` to
  `/sync/upload` over HTTPS after pinning the standby's certificate (`peer_cert_sha256`, else first contact, kv `peer_pins`).
  The standby merges devices, bindings, blocked, run and bstate in memory, keeps `primary.db`, and writes its own Kea
  configuration from the synced waves. Promote: keeps `estate.db.standby-<ts>`, takes `primary.db` as `estate.db`, merges
  the standby's events (`[standby] ` prefix), sets `promoted_from`, rewrites estate.conf (role primary, no peer_url, bind
  addresses other than 0.0.0.0/prov_ip removed). Kea stays the DHCP server on both hosts; no Kea HA hook is configured
  (as Python).
- **Peers**: `/inventory` (token); `sites[]` read every `sites_every_s` with pinning; kv `foreign_peers`, `foreign_sync`.
- **Alerts**: one worker; webhook POST `json.dumps(dict(alert, text="[SITE] LEVEL dev: text"))`; SMTP message as
  EmailMessage + smtplib (Subject folded per email.policy.default, quoted-printable body), STARTTLS, AUTH CRAM-MD5/PLAIN/LOGIN.

## 15. Phase 4: cutover (HS v1.56)

- **Engine choice in the .run.** `provision/bin/aidc-prov-linux-{amd64,arm64}.gz` (static, stripped, gzip) ship next to
  `estate_build.py`. `setup.sh --engine go|python` (also through the .run, which passes unknown options on). Default:
  the engine recorded in `/etc/fabric/estate-engine` by an earlier run, else `python` (a fresh host is unchanged from
  HS v1.55). `go`: `uname -m` x86_64/amd64 → amd64, aarch64/arm64 → arm64, anything else refused (exit 2) before anything
  is installed; the binary is unpacked to `/opt/estate-build/aidc-prov` (0755, written then renamed); the unit's
  `ExecStart` becomes `/opt/estate-build/aidc-prov serve --conf /etc/fabric/estate.conf`. setup.sh's own CLI calls
  (`check`, `init`, `user list`, `record-vendor`, `rma-restore`, `status`, `start`, `release-vendor`, `handover-export`) go
  through `estate()`, which runs the recorded engine (`python3 /opt/estate-build/estate_build.py` when none or python).
  `probe-dhcp` (before install) stays Python. The TFTP-holder check ignores `aidc-prov` as it ignores `python3`.
  `--decommission` also removes `/etc/fabric/estate-engine`. `air/ztp-host.sh --engine go|python` passes it to setup.sh and
  runs `lab-slice`/`start` with the installed engine (DSX Air TC-208).
- **Version.** An unstamped binary (`Version == "__BUILDER_VER__"`) reports the plan's `builder` field, which is what the
  builder stamps into Python's `VERSION`; one binary serves every release (DECISIONS P4-2).
- **Stop.** SIGTERM/SIGINT: the engine lock is taken (no statement mid-flight), the database closed, exit 0 (P4-3).
- **Push pass** (P4-11): candidates are probed at once as before; the pass proceeds as soon as the first free-slot count of
  answering candidates in plan order is settled (later probes are dropped and retried next pass).
- **Empty build** (P4-10, both engines, approved by the Captain 2026-11-02): a build with no devices in any wave is marked complete
  when a run starts (`passed` set, the run record saved, event "No devices in plane <key>: build marked complete", once), for
  every empty build in the run and before it; an empty build is not unpassed for the builds after it. The tick completes an
  empty build in the run instead of starting it, and the timeouts pass skips a build with no waves (and stops at a build's last
  wave), so no tick raises on it.
- **Reader** (P4-9): the read buffer holds back only an incomplete UTF-8 sequence at its end; each read searches only the new
  text and the 8 KiB before it.
- **Python fixes shipped in HS v1.56**, identical in Go: Junos Release DWR access commits the removal (P3-7); the rails runner
  starts each host log afresh (P3-c); the empty build (P4-10, HS v1.56 r2).
- **Builder.** `hs/hs_v156.py` (applied by `hs/hs_prov.py` after `hs_v155.py`): BUILDER_VER HS-v1.56, the Python fixes (P3-7, P3-c, P4-10) in
  the `prov-py` block, the binaries as `<script type="text/plain" id="prov-go-amd64|arm64">` (base64 of gzip -9, 76-column
  lines; read from `provgo-work/bin/` when the patch runs), `provisionGoFiles()` adding them to the deploy package after
  the secrets manifest (a damaged block writes `provision/bin/ERROR.txt`, which stops the site runner), the setup.sh,
  README, .run header and air changes above. The Final package is unchanged (device configurations byte-identical).

## 16. SSH reliability (HS v1.56 r3, both engines; DECISIONS P5-1 .. P5-5)

- **Session limit** (`ssh_per_device`, default 1, minimum 1): at most that many SSH sessions to one device at a time, counted
  across gate reads, backups (scheduled, after build, gate, requested), RMA reads, tuning writes, Release DWR access and pushes
  (a push holds a slot from its start to its process's end). A reader or writer waits for a slot; a push candidate at its limit
  is skipped for that pass (`try_take`). A session held for reuse by another login is closed to free a slot.
- **Reuse**: each checker pass (`checker`, one loop iteration including `rma_automation`) and each gate re-check is a pass. Within
  a pass a read session that ended without error is held and taken by the next read of the same device with the same login
  (`read:<user>`), whoever reads (a backup in the same window included); one closed by the device since is replaced. The last
  pass to end closes the held sessions. Writes never reuse.
- **Retries** (`ssh_retries` 2, `ssh_backoff_s` 5): a failure whose text contains `Error reading SSH protocol banner`,
  `Connection reset by peer`, `Unable to find prompt`, `Authentication timeout`, `Socket is closed` or `SSH session not active`
  (matched in any case) is retried after base x 3^(k-1) s (5 s, 15 s); ReaderUnavailable, a refused TCP connection and a rejected password are not.
  Reads retry the whole read; writes retry only the connection. The last error is the one reported (event and check texts
  unchanged); each retry is a stderr line `ssh: <device>: <first line>; retry k of n in Ws`.
- **Probes**: a device with a session open (in use or held) counts as answering in the probe cycle (todo and Done groups) and in
  the push pass, without a TCP connect. Aggregate members are probed as before.
- **Backups**: job i of a backup pass (i < `reader_workers`) waits i x `backup_stagger_s` before its read; the next due time after
  a success is `backup_every_h` x 3600 + offset, after a failure 900 s + offset(600), offset(n, ivl) = (crc32(n) mod 1000) / 1000 x
  min(600, ivl x 0.1) (in-memory `backup_due`, not stored).
- **Go reader phases** (P5-3): server banner (a line starting `SSH-`; lines before it, such as OpenSSH's "Exceeded MaxStartups", are not) within 60 s (`banner_timeout`), then key exchange and authentication within 90 s
  (`auth_timeout`). Wording as paramiko/netmiko: no banner (timeout, EOF) `Error reading SSH protocol banner`, reset
  `...banner[Errno 104] Connection reset by peer`; timeout after the banner: netmiko's authentication message ending
  `Authentication timeout.`.
- **DSX Air lab mode** (P5-1): `air/lab-auth.sh` (lab only; refuses on a host other than `oob-mgmt-server` unless `LAB_AIR=1`)
  comments out `nv set system aaa tacacs ...` and `nv set system aaa authentication order ...` in the published copies
  (`/var/www/html/<cfg>` and `/var/www/html/ztp/<node>.sh`) of the switches in `air/slice.csv`, prefix `# LAB (air/lab-auth.sh,
  DSX Air only): `, list in `/etc/fabric/lab-auth.txt`; `--undo` copies the package files back. `air/ztp-host.sh` runs it after
  setup.sh unless `--prod-auth`, and prints `[ok] engine: go (/opt/estate-build/aidc-prov, linux/<arch>)` or
  `[ok] engine: python (/opt/estate-build/estate_build.py)`. Neither engine changes; the package and .run configurations do not.
- **Acceptance scripts** (P5-6 .. P5-8, builder): `acceptance/nccl.sh` and `lossless.sh` default `ACCEPT_GPUS_PER_HOST` to the
  profile's GPUs per host; `lossless.sh` T1/T2 run on every rail NIC of every host (`ACCEPT_RAILS` per host from the plan,
  `ACCEPT_HCA`/`ACCEPT_HCAS` override; extra ACCEPT keys `rails`, `rails_min`, `rails_expected`, and `min_gbps=0 short_rails=N`
  appended to T1 when a host has fewer rail NICs); `provision/rail_check.sh` floor `RAIL_MBPS` = the design's rail leg speed.

