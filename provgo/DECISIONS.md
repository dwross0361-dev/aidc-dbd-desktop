# Go vs Python: deliberate differences and copied quirks (Phase 1)

Rule applied: where the Python service does something that looks like a bug, Go copies it if a host's saved state or
the console depends on it; otherwise Go does the correct thing. Each case below says which.

## Different in Go

| # | Python does | Go does | Why |
|---|---|---|---|
| 1 | A request target with a malformed percent escape (`/%zz`) is decoded leniently, looked up, and answered 404 with a "not served" fail event for a known device. | `net/http` refuses it with `400 Bad Request` before the handler runs; no event. | No state or console dependency. Devices never send such paths, and the file is refused either way. Matching it would mean replacing Go's HTTP request parser. |
| 2 | HTTP/1.0: one request per connection. | HTTP/1.1 keep-alive (a `Connection: close` request is honored). | Faster for many fetches; status codes, headers that matter and bodies are the same. |
| 3 | `HEAD /licenses/<serial>.lic` sends the license body anyway. | HEAD never sends a body. | Python's reply is invalid HTTP; no state effect. |
| 4 | `_timeouts` walks every key in kv `bstate`; a build key no longer in the plan raises `KeyError` on every tick ("engine error" each 2 s, and no timeouts run). | Unknown build keys are skipped. | Bug with no state dependency; the stored `bstate` is left as is. |
| 5 | Readiness probes (NTP, DNS, TACACS) run one after another, 2 s timeout each. | Run in parallel (sized to the probe count, capped). | Same results; at scale Python's page took N x 2 s. |
| 6 | Console certificate "self-signed" check runs `openssl x509 -issuer -subject` and compares the text; with `openssl` missing it reports self-signed (warn). | Parses the certificate with `crypto/x509` and compares issuer and subject names (both must carry a CN). | Same outcome for real certificates; no dependency on an external tool. A host without `openssl` gets a correct result instead of a warning. |
| 7 | Error texts from the OS come from Python (`[Errno 2] No such file or directory`). | Same format, built from the errno (`[Errno N] <message>` with the first letter capitalized); timeouts read `timed out`. | Matches for the common errors (Kea socket missing or refused, file not writable); verified by test. Rare errors may word differently. |
| 8 | argparse accepts unambiguous flag abbreviations (`--pla` for `--plan`) and prints its own usage text. | Full flag names only; flags may appear anywhere; usage text is similar but not identical. | No state dependency; setup.sh uses full names. |
| 9 | `--console` file is read on every start. | The console page is embedded (byte-identical to `console.html`); `--console` still overrides it. | Single static binary. |
| 10 | (Phase 1 only) Phase 2/3 work answered 501. | Superseded: every endpoint and CLI command is implemented (Phase 2 and Phase 3 sections below). | |
| 11 | (Phase 1 only) No push credential could be entered. | Superseded by Phase 3 (pushes run). | |

## Copied from Python on purpose

| # | Python behavior (looks odd) | Why copied |
|---|---|---|
| 1 | `bind` audit detail always ends `(was none)` (the old identity is read after the "already bound" check). | Audit log and exports. |
| 2 | `HEAD /ci/<host>/<stage>` performs the check-in. | Changes device state. |
| 3 | `on_fetch` moves a device to Bootstrap/Image even when its wave is not open. | Device state and console. |
| 4 | Failed logins per source address are not cleared by a successful login (5 in 300 s still lock it out). | Console behavior. |
| 5 | `count` check-ins use Python `int()` (spaces, sign and `_` accepted; `5.0` refused with 400 `count?n=`). | Device state. |
| 6 | Timeout minutes are formatted with `%d` (a fractional limit prints truncated, e.g. `0 min`). | Event and detail text in the console. |
| 7 | Stored JSON keeps Python's `json.dumps` form exactly (separators, ASCII escapes, float repr, key order, 1 vs 1.0). | Engine switch both ways; verified byte-identical by the differential test. |
| 8 | The schema script's whitespace (SQLite keeps it in `sqlite_master`). | Identical `estate.db`. |
| 9 | `GET /api/device/<n>` creates `state_dir/backups/<n>/`. | Identical state directory. |
| 10 | A non-integer `since`/`limit` on `/api/events`, or a non-object JSON body, drops the connection without a reply (Python raises). | Same observable behavior; harmless. |
| 11 | The wave-held event names only `failed`/`missing` devices; quarantined devices settle a wave. | Engine behavior. |

# Phase 2: device reader, probes, automatic gate checks, backups, TFTP

Row 10 of "Different in Go" above now covers phase 3 only: probes, the reader, every automatic gate check, results
(`result` CLI, `/local/result`, `/api/result`), backups and the difference view, gate re-check and TFTP run in Go.

## Different in Go (Phase 2)

| # | Python does | Go does | Why |
|---|---|---|---|
| P2-1 | Probes: a fixed pool of 32 threads, 3 s TCP connect timeout for every probe, and the three groups (aggregate members, devices waiting for Verified/reachable, Done devices) one after the other, then 5 s pause. With many silent devices a cycle takes minutes (10,000 silent devices: about 10,000 / 32 x 3 s = 940 s). | All probes of a cycle run at once, one goroutine per probe, capped at 4,096 connects in flight. Devices not yet expected up (reachable-signal devices before Config, aggregate members) get a 1 s timeout; configured and Done devices keep 3 s. Results are applied in Python's order (aggregate counts, Done devices, stage changes). A cycle longer than `probe_every_s` is logged to stderr. | Requested deliberate difference: a cycle must not be stalled by silent devices. On the management network a live device completes a TCP handshake in milliseconds, so the results (Verified, Done, down, counts) are the ones Python reaches; only the cycle time changes. Measured (difftest/scale.py, 16,000 devices: 6,000 answering, 6,000 configured but silent, 4,000 silent before Config; SYNs dropped by iptables): worst cycle 13,662 probes in 7.4 s, steady cycles of 7,662 silent probes in 6.1 s, against probe_every_s 15; engine RSS peak 280 MB, 7.8 % of one core. |
| P2-2 | The reader is netmiko (paramiko): `ConnectHandler(...).send_command()` per command. | The reader is `golang.org/x/crypto/ssh` with the same session handling netmiko applies per device type: PTY vt100 511 x 1000 (IOS XR 511 x 511), the platform's session preparation (Junos `set cli ...`, EOS/NX-OS/IOS `terminal width/length`, SR Linux `environment ...`, SR OS MD-CLI `environment ...`), prompt detection, command echo and trailing prompt stripped, Junos `{master}`/`[edit]`, SR Linux `--{ ... }--` and SR OS context lines stripped, line feeds normalized (NX-OS variant), ANSI codes removed for EOS/Cumulus/NX-OS/SR Linux. Password authentication with keyboard-interactive fall-back (TACACS logins); any host key accepted (paramiko AutoAddPolicy, as Python). Timeouts as Python: TCP 15 s, banner/auth 90 s, 120 s per command. Error texts for TCP failure and authentication failure are netmiko's. | Static binary without Python. Verified byte-identical against netmiko on fake SSH devices of all nine platforms (difftest/readerdiff.py) and in the end-to-end run. Rare netmiko errors (paramiko internals, read timeouts) word differently. One deliberate refinement: the command ends when the prompt ends the text read; netmiko stops at the prompt anywhere, so on FTD (prompt `>`) an output containing `->` can come back cut at a read boundary (timing-dependent). Go returns the full output there. |
| P2-3 | Python `re` and `str` methods treat `\s`, `\d`, `\w`, `strip()` and `splitlines()` as Unicode. | Go uses RE2: patterns are rewritten so `\s`/`\d`/`\w` match Python's Unicode classes (`pyRe`), and `strip`/`splitlines` follow Python. `\b` stays ASCII in RE2. | Same results on device output; a word boundary next to a non-ASCII letter can differ (descriptions only). |
| P2-4 | `json.loads` of device output accepts `NaN`/`Infinity` and integers of any size. | The Go JSON reader rejects `NaN`/`Infinity` (the output is then read as text, as Python does for non-JSON) and keeps integers beyond 64 bits as floats. | Vendors do not print these in the commands read here. |
| P2-5 | A build with no waves (BE-JUNOS plan: edge, fe) raises `IndexError` on every tick: "engine error: list index out of range", the rest of the tick (timeouts, Kea) skipped. | Same text, same skipped tick, same in-memory build state (copied: the console shows the build building). | Console and event log depend on it. Listed here because the panic is deliberate in Go. **Superseded by P4-10** (fixed in both engines: an empty build is complete when the run starts). |
| P2-6 | The bond-steering helper and `rma_automation` run in the checker loop. | Both run there (`bond_steer` only acts when `/sys/class/net/bond0/bonding` exists); RMA automation since Phase 3. | Same as Python. |

| P2-7 | (Python's sqlite3.) | SQLite driver raised from `ncruces/go-sqlite3` v0.21.0 (wazero 1.8.2) to v0.25.2 (wazero 1.9.0). v0.21.0 corrupts the database (free-list damage, "database disk image is malformed") once rows of a few hundred KB are rewritten repeatedly — kv `auto_res` with 80 devices' evidence reaches that in the first back-end gate check. Phase 1 never wrote rows that large. Reproduced and guarded by `TestDBLargeValues*`; the differential runs pass on v0.25.2. | Data integrity. `golang.org/x/crypto` comes from its GitHub mirror (v0.31.0) through a `replace`, as `x/sys` and `x/text` do. |

## Copied from Python on purpose (Phase 2)

| # | Python behavior (looks odd) | Why copied |
|---|---|---|
| P2-a | `POST /api/backup` answers `{"ok": "saved <file>", ...}` (the description string, not `true`) on success. | Console reads it. |
| P2-b | `submit_result` (and the accept/isolation submissions) drop the in-memory automatic result of that check but do not rewrite kv `auto_res`; the stored entry changes at the next evaluation. | Stored state identical. |
| P2-c | The isolation result pattern is `^ISOLATION\s+(\S+)\s+(bind|snat|cross|setup)\s+(PASS|FAIL)\s*(.*)$` with MULTILINE: a line with no note text lets `\s*` cross the newline, so the next line becomes its note and is not counted as a record of its own. | Stored results and the gate result; unit-tested against Python. Worth fixing in both engines (`[ \t]*` instead of `\s*`). |
| P2-d | Parsers are ported exactly, gaps included (see "Unproven / suspected gaps" below). | Gate results must match Python. |
| P2-e | `/api/backup` without a device and `/local/result` with a non-numeric value drop the connection (Python raises). | Same observable behavior. |
| P2-f | The prober runs on a standby host too (only the tick is role-gated in Python). | Same state on a standby. |

## Unproven / suspected gaps (Python and Go alike; only real devices can settle them)

- LLDP: `lldp_pairs` understands only outputs keyed by interface name (Cumulus NVUE `nv show interface lldp -o json`,
  lldpctl keyvalue). The real Junos (`lldp-neighbor-information` list), EOS (`lldpNeighbors` list, non-detail), NX-OS
  (`TABLE_nbor_detail`) and SR Linux (`interface` list) JSON shapes parse to an empty map: every planned link would read
  "no neighbor". Today only NVUE planned configurations carry the `# -> PEER PORT` cable-plan comments, so other
  platforms' cabling checks pass as "no planned links" and the gap does not show.
- SR OS text BGP summary: a peer whose description line sits between the address line and the counters line is not
  recognized (the parser looks at the address line and the next line only), so such a session is not counted.
- Lossless counters (CNT_CMDS) are classified by key name; vendor JSON key names were written from documentation in
  the fixtures, not captured from devices ("CONFIRM on the first unit" in Python).

# Phase 3: pushes, RMA, runners, tuning, hand-over, standby, peers, alerts

Every endpoint and CLI command of SPEC.md section 14 runs in Go; none answers 501.

## Different in Go (Phase 3)

| # | Python does | Go does | Why |
|---|---|---|---|
| P3-1 | Push concurrency `push_concurrency` (default 4); every candidate probed on its push port by a pool of 32 threads, 3 s each, inside the engine tick. | `push_concurrency` set in estate.conf or the plan's `service` block is honored as is. Left unset, Go sizes it to the work: 4 x CPUs, at least 8, at most 64 (logged to stderr at start). All candidates are probed at once (capped at 4,096 connects in flight), 3 s timeout. The push script runs with `python3` from PATH (Python runs it with its own interpreter). | Requested: concurrency sized to the work, as Phase 2 did for probes. A 1,000-switch wave due a push no longer waits 4 at a time behind the default; each push is an external process, so the cap keeps memory bounded. Results (stages, events, logs) are the same; only how many run at once changes. |
| P3-2 | Standby snapshot with the sqlite3 backup API. | `VACUUM INTO snapshot.db`. | Same rows and schema (the file's page layout differs); the standby and promote read it with SQL. |
| P3-3 | A runner past `timeout_s` is killed with `Popen.kill()` (the direct child only: `bash -c` children keep running and keep writing to the log). | The whole process group is killed. | No state dependency; the result is the same (124). |
| P3-4 | The rails runner records each host's item as it finishes (completion order, not repeatable). | In the plan's host order, after all hosts finish. | Key order of `items` is not used by the console; Go's order is deterministic. The differential test compares items as a set. |
| P3-5 | The hand-over zip is written by `zipfile` (zlib deflate, local time stamps, external attributes 0600). | `archive/zip` (Go deflate, same names, order, contents, mode 0600). | Byte-identical zips are impossible across compressors; the SHA-256 differs between any two downloads anyway (time stamps). Verified member by member. |
| P3-6 | The configuration writer (tuning apply, Release DWR access) is netmiko (`send_config_set`, `commit`, `save_config`). | The Phase 2 SSH session with netmiko's commands per platform: configuration-mode commands, Junos `commit confirmed 5 comment "..."` + `commit and-quit` with netmiko's commit markers, SR Linux `commit stay`/`save startup`, SR OS `exit all`/`commit`/`/admin save`, IOS XR `commit` (no save: empty error, as netmiko's NotImplementedError), EOS/NX-OS `copy running-config startup-config`, IOS XE `write mem`. | Static binary. Verified against netmiko (Python's own `_tune_write` and `_remove_dwr`) on fake devices with a configuration mode (`difftest/writerdiff.py`, Junos, EOS, NX-OS, Cumulus: applied, rejected line, rejected apply, account removed, account still present, wrong password): 25 comparisons, 0 mismatches, notes included. SR Linux, SR OS, IOS XR and IOS XE follow netmiko's commands but have no fake configuration mode yet (unproven). |
| P3-7 | **Fixed in both engines, approved by the Owner 2026-11-01** (HS v1.56). Before the fix, Release DWR access on Junos: `send_config_set` left configuration mode before `commit()` ("Exit with uncommitted changes? yes" discarded the delete), so the read-back still showed dwr-netadmin and the device was reported failed. | Both stay in configuration mode and commit (platforms whose removal is "commit"). Python: `c.send_config_set(cmds, read_timeout=120, exit_config_mode=(how != "commit"))`. | Proven by `writerdiff.py` (fake Junos discards uncommitted changes on exit): HS v1.55 Python 2 mismatches on Junos (removal reported failed; commit-error text carried the exit dialogue), HS v1.56 Python 0 of 49. |
| P3-8 | HTTP client headers (`User-Agent: Python-urllib/3.x`, peer requests `Accept-Encoding: identity`). | Go's request writer (webhook User-Agent `Python-urllib/3`, peers `estate`). | Receivers see the same method, path, body and Content-Type. |
| P3-9 | The alert queue is unbounded (a dead receiver makes it grow without limit: 10 s webhook + 15 s SMTP per alert). | Bounded at 10,000 alerts; overflow is counted on stderr. | Memory safety; no state dependency. |
| P3-10 | `/sync/upload` reads up to 512 MiB under the 30 s socket timeout per read. | The request read deadline is extended to 15 minutes for that endpoint. | A large snapshot over a slow link is not cut off by Go's whole-request timeout. |
| P3-11 | `rma-restore` with a file that is not a zip ends with a traceback. | Prints `[!] <error>`, exit 1. | CLI only. |
| P3-12 | `probe-dhcp` uses Python sockets; `release-vendor` prompts with `input()`/`getpass`. | Raw sockets (`SO_BINDTODEVICE`, port 68, broadcast); the password prompt reads `/dev/tty` without echo (`stty`). | Same output. |

## Copied from Python on purpose (Phase 3), with the one-line Python fix

| # | Python behavior | Why copied | Python fix |
|---|---|---|---|
| P3-a | `rma_start` blocks the MAC, serial and client-ID seen on the wire in memory only (not in the `blocked` table): after a restart the old unit's identity matches again. | Device matching and the database must match Python's. | Also `INSERT OR REPLACE INTO blocked` for those values. |
| P3-b | `rma_identity` puts a `manual:` unclaimed entry in memory before binding; when the bind fails (a serial on a MAC-only platform) the entry stays open in Unclaimed until a restart (never saved). | Console view (`unclaimed`). | `self.unclaimed.pop(key, None)` when `bind` fails. |
| P3-c | **Fixed in both engines, approved by the Owner 2026-11-01** (HS v1.56). Before the fix, the rails runner appended to `runner-rails.log.<host>` and took its first `RAILS ok N` line: a host that once failed failed every retry until its log file was deleted. | Each attempt starts the host log afresh (Python `open(lf, "w").close()` before `_exec` in `_run_rails.one`; Go truncates the same file). | The Phase 3 scenario fails a rail host, fixes it and retries: the retry passes in both engines (no log deleted). |
| P3-d | `tuning_rollout` smoke tests (T2/T3 per pod) are recorded with `submit_accept_result`, replacing the full T1-T4 record: the acceptance gate needs confirmation (T1, T4 "not run") until the acceptance runner runs again. | Results and gate state. | Record smoke tests under `results["accept-smoke"]`. |
| P3-e | `_pushes` returns before collecting finished pushes when no push credential is held (after Release DWR access clears it, a push running then stays in `pushing`). | Console `pushing` list. | Move the completion loop before the credential check. |
| P3-f | `receive_snapshot` merges bindings and blocked identities but never drops ones the primary removed (an unbind on the primary stays bound on the standby until promote); the standby's `last_sync` is memory only. | Standby state and Kea configuration. | Rebuild `self.ident` from the plan plus the snapshot's bindings. |
| P3-g | `/local/promote` does not check the role (a primary holding a `primary.db` would promote itself). | Same CLI behavior. | `if est.role != "standby": return 409`. |
| P3-h | `GET /api/handover/export.zip` changes state (kv `handover.built`, audit, event) on a GET. | Console flow (download then confirm). | (none: by design) |
| P3-i | `_rma_auto`'s license note keeps a non-reader error's first 120 characters and retries in 300 s; a reader error message differs between netmiko and Go's reader for rare paramiko failures. | State text for the common errors matches (DECISIONS P2-2). | |

## Verification (Phase 3)

`difftest/p3diff.py` (`difftest.py --p3 RUN`, `--p3ha RUN`), Python and Go side by side; devices answer from recorded outputs
(`reader_sim_dir`, from Phase 2's `Answers`), their management ports are `fakedev.py` listeners; ssh is a fake on PATH that
runs the bundle's scripts locally or plays `bfb-install`; `push_configs.py` is a fake. Compared: every API reply, events
(multiset), database, stored JSON text, rma table, state-dir files and their contents, backups, Kea configuration (byte-
identical), export zip members, webhook payloads and SMTP envelopes and messages (multisets), CLI output. Normalization
beyond Phase 2 (each is timing, not engine behavior): SHA-256 values and the tuned profile's id hash (it covers the approval
time; the harness recomputes both with Python's canonical JSON and checks them), zip time stamps, `estate.db.standby-<ts>`,
runner state key order (kv `runners` compared sorted: which runner becomes due first depends on the 10 s worker phase),
rails `items` order (P3-4), backup events per device as in Phase 2 (which 30 s worker pass reached a device), check-result
events (`<build> gate: '<check>' pass -- ...`) as a set (one evaluation can cover several dropped results), on the standby
its own pre-promote events as a set of kinds and `promoted_from` (its first probe of a device races the primary's sync).
Scenario: B-NV-SN5600 (160 devices; 17 pushed Junos incl. an MNHA pair; runners bnk, weka, dpu (288 DPUs), rails (288
hosts), nccl, accept, isolation; tuning on Cumulus) with injected failures: a push failing mid-wave then retried; bnk hanging
past its timeout (124); a WEKA check failing; a DPU in NIC mode (42); a rail host short of a rail (and its retry, P3-c);
acceptance T4 below target; a tuning apply to an empty scope; RMA of both MNHA firewalls (license staged and installed,
preemption held, sync read, and sync unreadable then confirmed by the operator), a Cumulus leaf restored from an uploaded
backup (served override), an RMA on a quarantined device; hand-over checksum mismatch and short prefix; every Release DWR
access refusal; a peer site pinned on first contact, one with a wrong pin, one refusing connections, one with a bad token;
alert receivers (and, in the standby scenario, receivers that are down). Standby scenario: primary and standby per engine
over TLS with pinning, sync every 2 s, standby Kea configuration compared byte for byte, the primary stopped mid-build, the
standby promoted from the CLI, restarted and carrying the run to the OOB gate.
Unit tests (`p3_test.go`): the SMTP message (50 subject/alert pairs, folding and encoded words included), `shlex.quote` and
the canonical JSON, expected values written by Python (`testdata/p3/genexp.py`).

Harness changes for Phase 3 (regression runs): Phase 1's "phase 3 endpoints answer 501" check now compares both engines'
replies; Phase 1's timeouts scenario waits 7 ticks (was 4) after the lab slice, because Python applies the slice one lock
per device and its ticks open waves while it is still being applied (a tick or two sooner than Go, which applies it in
one): the stop came between Python's "missing" and Go's; Phase 2's plans keep their runners (with no runner configuration
they report "not configured", in both engines; kv `runners` is compared entry by entry, not in order); Phase 2's TFTP cases
wait until the TFTP owner is settled (Done) in both engines, because those events carry the owner's stage and Go's probe
cycle (P2-1) often reaches Done before Python's. `fakedev.py` gained a configuration mode (Junos, EOS, NX-OS, Cumulus) and
a per-device prompt for `writerdiff.py`; its Phase 2 behavior is unchanged.

## Unproven (Phase 3)

Superseded by "Unproven after Phase 4" at the end; kept as written at the end of Phase 3.

- Only one plan (B-NV-SN5600) went through the Phase 3 scenario. The C-T150 plan, with 150 isolation tenants and four MNHA
  firewalls, would need a more general harness.
- The SSH writer on SR Linux, SR OS, IOS XR and IOS XE has no fake configuration mode, so it is not compared with netmiko.
  Python's Junos Release DWR access bug (P3-7) needs a fake that discards on "exit configuration-mode" to show up.
- No real devices, ssh hosts, bfb-install, WEKA or BNK were used: the runners' scripts and ssh are fakes, so the runners
  were tested only for how they are launched and how their results are recorded.
- Alerts were not sent with SMTP STARTTLS or AUTH (the fake receiver offers neither). The message bytes are unit-tested
  against Python.
- `probe-dhcp`, `record-vendor`, the interactive `release-vendor` CLI, and host account locking on a real DWR account were
  not run (the test locks a nonexistent account, with the same `usermod` error in both engines).
- Push concurrency was measured only at this plan's scale (17 pushes). No scale test was run for thousands of pushes.

# Phase 4: proof and cutover (HS v1.56)

## Decisions (Phase 4)

| # | Decision | Why |
|---|---|---|
| P4-1 | The two approved Python fixes (P3-7 Junos Release DWR access commits the removal; P3-c rails host log truncated per attempt) ship in HS v1.56 through `hs_v156.py` (the `prov-py` block) and in the `/tmp/ex153` reference; Go already had both. The harnesses compare against the fixed Python; `difftest.py` applies `fixes.json` to installers built before v1.56. The P3 scenario's rail host fails, is fixed and is retried with no log deleted. | Approved by the Owner 2026-11-01. With both fixed there is no engine difference left in either place. |
| P4-2 | An unstamped Go binary (`Version == "__BUILDER_VER__"`) reports the plan's `builder` field as its version. | The builder stamps that same release into Python's `VERSION`; one binary serves every release and `/api/state` `version`, the "Service ... up" event and `status` read the same as Python's. The release binaries are not stamped. |
| P4-3 | SIGTERM/SIGINT: Go takes the engine lock, closes the database and exits 0. Python dies on the default SIGTERM (a write in progress leaves a hot journal, rolled back at the next open). | Found by Phase 1 on B-BE-EOS: Go was stopped mid-transaction and left `estate.db-journal` (state dir files differed). `systemctl stop` now leaves a clean database. Different in Go; no state dependency (the next open of either engine reads the same rows). |
| P4-4 | `setup.sh --engine go\|python`; default python on a fresh host, else the engine recorded in `/etc/fabric/estate-engine`; the CLI calls inside setup.sh go to the recorded engine; the unit's `ExecStart` is rewritten for go; unsupported CPUs refused before anything is installed. | Python stays the default (Owner). A re-run without `--engine` (for example `--yes --plane oob`) must not switch engines silently; both engines read the same state, so switching either way is safe and is a single re-run. |
| P4-5 | The binaries are embedded in the builder html (`<script type="text/plain" id="prov-go-amd64\|arm64">`, base64 of gzip -9) and added to the deploy package by the builder, not by the site runner. | A .run made in the browser and one made by the site runner carry the same files. Cost: the builder html grows from 4.9 MB to about 19 MB. gzip (not xz) because every Linux host has it. |
| P4-6 | `release-vendor` with standard input ended before every answer: Python stops with EOFError (traceback on stderr, exit 1); Go prints `[!] input ended before every answer was given: nothing was sent` and exits 1. Neither sends anything. | CLI only; `clidiff.py` checks Go's line and compares the rest. |
| P4-7 | Harness: a device that called in before its wave opened calls in again once its wave is open (lease event and first file), in p2diff and p3diff, the same for both engines. | Without it, plans whose later waves are NX-OS or IOS XE devices signalling "reachable" (B-BE-NXOS) never progressed in either engine: a harness gap, not an engine difference (both engines were stuck identically). Real devices retry DHCP. |
| P4-8 | Harness: `switch_probe` in p3diff snapshots each engine's state mid-build (database through the sqlite3 backup API) after the tuning rollout, after the RMA section and, on the standby pair, after promote, and opens every snapshot with BOTH engines; what they show must match. | Engine switch both ways on live mid-build state, including RMA, tuning and standby state. Phase 1 covered Python's state opened by Go only. |
| P4-9 | Go reader (bug fixes, found while chasing a read failure on one of C64-3p's 1,536-link spines, not reproduced since): (a) the read buffer held back up to 4 trailing bytes whenever the pending output held an invalid UTF-8 byte anywhere (it re-validated the whole buffer), so a prompt at the end could be withheld until the 120 s read timeout; now only an incomplete sequence at the very end waits. (b) `readUntil` searched the whole output after every read (quadratic in its size); it now searches the new text and 8 KiB before it (no pattern read for is start-anchored or longer). | Python (netmiko) returned these reads; Go reported "read failed: Pattern not detected" on one spine, blocking the cabling check. Unit tests `TestTakeUTF8Tail`, `TestReadUntilIncremental`. |
| P4-10 | **Fixed in both engines, approved by the Owner 2026-11-02** (HS v1.56 r2). A build with no devices in any wave (a plane the design leaves empty: edge and front end on the B-BE-* plans and B-feoff) is marked complete when a run starts: `start_run` completes every empty build in the run and before it (`passed` set, the run record saved, one event "No devices in plane <key>: build marked complete"); an empty build no longer counts as unpassed for the builds after it, so a run of all goes from the OOB build straight to the back end, and starting the back end alone needs no override. Defensive guard: the tick completes an empty build in the run instead of starting it (a record saved before the fix), and the timeouts pass skips a build with no waves and stops at its last wave, so a malformed record never raises and never stalls Kea or the push pass. Go's Phase 4 "raise to match Python" is removed. Python: `_empty`, `_complete_empty`, `start_run`, `tick`, `_timeouts` (hs_v156.py, `/tmp/ex153`, `fixes.json` for installers built before v1.56). Go: `emptyBuild`, `completeEmpty`, `startRun`, `tickLocked`, `timeouts`. | Before: Python raised IndexError in `_timeouts` on every tick, so a run of all on a plan with an empty plane stalled every Kea update and push (B-BE-JUNOS's push-based back end never finished). Same events and saved state in both engines (verified below). Unit tests `TestEmptyBuildCompleteAtStart`, `TestEmptyBuildRunAll`, `TestEmptyBuildTickGuard`. P2-5 no longer occurs. |
| P4-11 | Push pass (Different in Go, efficiency): candidates are still all probed at once (P3-1), but the pass stops waiting as soon as the choice Python makes is settled -- the first free-slot count of answering candidates in plan order, every candidate before the last one chosen having answered or timed out. Later probes are dropped (not started once settled); those devices are probed again next pass. | Measured (scale.py --push): with 200 silent push devices in a 3,000-device wave, every pass waited the 3 s probe timeout for them, so a slot was refilled only every ~6 s (10.3 pushes/s at 64 at once, 2 s pushes). The devices chosen and the order pushes start are unchanged (Python's rule). Unit test `TestProbeInOrder`. |

## Verification (Phase 4)

Go side: the engine as it stood at each run; every design a later fix could affect was rerun after it (P4-10: the four plans
with an empty build, Phases 1-2; P4-9: 11 of the 25 Phase 2 designs ran on the fixed reader, the reader itself compared on 9
platforms afterwards; P4-11: the push scenarios B-NV-SN5600 and B-BE-EOS rerun on the final binary). Python side: `/tmp/ex153/estate_build.py` with the two HS v1.56 fixes (`fixes.json` patches the
v1.53 installers' copy the same way).

- Phase 1 (`difftest.py --run`): all 25 regression designs, 129 comparisons each, 0 mismatches (B-BE-* and B-feoff rerun after P4-10).
- Phase 2 (`--p2`): all 25 designs, 46-58 comparisons each, 0 mismatches. Harness changes: DHCP retry for late waves (P4-7),
  gate override when the fault fixes cannot satisfy a plan's checks (C-T150 edge: compared held, then overridden), gate views
  let settle before comparing on the 64-rack plans, gate backups let finish, newest backup per device compared (C64-3p).
- Phase 3 (`--p3`): B-NV-SN5600, C-NK-7250-IXR-X3B (SR Linux push), B-BE-EOS, C-AR-7280DR3A-36, C-CS-N9332D-GX2B,
  C-ACX7100-32C (330-343 comparisons each, 0 mismatches) and C-FTD (330 comparisons; the only failure is the harness's own wait
  for the isolation runner, not done in either engine: both engines agree on everything compared). Engine switch probes (P4-8)
  after tuning and after RMA on each. Standby (`--p3ha`) on B-NV-SN5600 with the switch probe after promote: 0 mismatches.
  B-BE-JUNOS's push-based back end could not finish in either engine before P4-10, so it was not run through Phase 3 then (see
  the P4-10 lines below).
- `writerdiff.py`: 8 platforms (Junos, EOS, NX-OS, Cumulus, SR Linux, SR OS, IOS XR, IOS XE) x applied / rejected line /
  rejected apply / account removed / still present / commit refused / wrong password: 49 comparisons, 0 mismatches (HS v1.55
  Python: 2 on Junos, P3-7). `readerdiff.py`: 9 platforms, 23 sessions, 0 mismatches. `clidiff.py` (probe-dhcp on a veth pair
  with fake DHCP servers, record-vendor, release-vendor over a pipe and a pty): 16, 0. `smtpdiff.py` (STARTTLS with CRAM-MD5,
  PLAIN, LOGIN, wrong password, untrusted certificate, no STARTTLS): 6, 0.
- Scale (`scale.py`, final static binary, 2-CPU host): 16,000 devices (6,000 answering, 6,000 silent configured, 4,000 silent
  before Config): worst probe cycle 11.7 s against probe_every_s 15, RSS peak 277 MB, 6.9 % of one core. Pushes: 3,000 push
  devices (200 silent), push_concurrency 64, 2 s pushes: 2,800 pushed in 151 s (18.5/s; 10.3/s before P4-11), 64 at once, RSS
  peak 124 MB; engine-sized concurrency (8 on 2 CPUs), 1,000 devices: 3.6/s, RSS peak 84 MB.
- P4-10 fix (2026-11-02, final binaries amd64 sha256 afcc76f1d9dfddee, arm64 bc7b1c15665304db; Python `/tmp/ex153` and
  `fixes.json` with the fix): Go unit tests `TestEmptyBuildCompleteAtStart`, `TestEmptyBuildRunAll`, `TestEmptyBuildTickGuard`
  pass (full `go test ./...` passes). Harnesses: Phase 2 and 3 now run all on every plan (the override start of the back end is
  gone) and check, on a plan with an empty build, that both engines marked each one complete with one "No devices in plane"
  event and logged no engine error (compared, and Python's checked against that expectation).
  - Phase 1: B-BE-EOS, B-BE-JUNOS, B-BE-NXOS, B-feoff: 129 comparisons each, 0 mismatches.
  - Phase 2: the same four: 46 comparisons each, 0 mismatches.
  - Phase 3: B-BE-JUNOS (first full run: the run of all reaches the back end, the Junos pushes run and the back end completes
    in both engines), 342 comparisons, 0 mismatches, engine switch probes (P4-8) after tuning and after RMA both ways;
    B-NV-SN5600 (no empty build, regression), 343 comparisons, 0 mismatches.
  - Builder HS v1.56 r2 (`hs_prov.py`): only the `prov-py` and `prov-go-*` blocks differ from HS v1.56; the `prov-py` block is
    `/tmp/ex153/estate_build.py`. B-BE-EOS through the site runner: 198 device configurations in the Final package byte-identical
    to HS v1.55, parity 197 of 197 in the .run, `pkgproof.py` PASS (16 checks).

## Unproven after Phase 4 (what only DSX Air, EVE-NG or the first real pod can prove)

- Real devices: every reader, writer and push path has been compared with netmiko and Python on fake SSH devices and recorded
  outputs only. The parser gaps listed under Phase 2 (LLDP shapes other than NVUE, SR OS text BGP, lossless counter key names)
  are unchanged and affect both engines.
- Real ZTP: Cumulus ZTP fetching its configuration from the Go engine (DSX Air TC-208), and Junos/EOS/NX-OS/SR Linux ZTP on
  EVE-NG or hardware.
- setup.sh `--engine go` on a real host (systemd unit, Kea, netplan bond): proven by running its engine lines with paths moved and
  both engines serving the package (`pkgproof.py`), not by a full install. arm64 is proven only by architecture selection and the
  ELF header; the arm64 binary has not been run on an arm64 host.
- Runners against real systems (bfb-install, WEKA, BNK, FMC, rail checks), SMTP and webhooks against real receivers, host account
  locking on a real DWR account, and HA sync between real hosts over a real network.
- Push concurrency at the real push duration (Junos commit confirmed: tens of seconds) and with PyEZ memory per push process.

# Phase 5: SSH reliability and acceptance scripts (HS v1.56 r3)

Evidence: DSX Air TC-208 (11 Cumulus VX switches, both engines reached Done with identical as-built and readiness): Python
backups 6 ok / 11 failed ("Authentication timeout", netmiko auth_timeout 90), Go backups 0 ok / 15+ failed ("Error reading
SSH protocol banner", "Unable to find prompt"), Go gate reads (software, cabling, eBGP) failed on banner errors, 8 concurrent
backups on Python failed on authentication. The switch configurations order TACACS before local logins with two TACACS
servers that do not exist in Air: every login waited on TACACS timeouts, and concurrent logins queued on the VX.

## Decisions (Phase 5)

| # | Decision | Why |
|---|---|---|
| P5-1 | **DSX Air lab mode, lab only.** `air/lab-auth.sh` (run by `air/ztp-host.sh` after setup.sh unless `--prod-auth`) comments out `nv set system aaa tacacs ...` and `nv set system aaa authentication order ...` in the copies served from `/var/www/html` to the switches in `air/slice.csv` (configuration file and, for switches whose ZTP script carries their configuration, `ztp/<node>.sh`), each line prefixed `# LAB (air/lab-auth.sh, DSX Air only): `; it refuses on a host other than `oob-mgmt-server` (`LAB_AIR=1` overrides; ztp-host.sh sets it), lists the files in `/etc/fabric/lab-auth.txt`, `--undo` restores the package copies. Neither engine changes; nothing outside `air/` calls it. `ztp-host.sh` prints `[ok] engine: go (/opt/estate-build/aidc-prov, linux/<arch>)` / `[ok] engine: python (...)` as the test sheet says. | Air has no TACACS servers; the production configurations must stay byte for byte (package and .run parity). The service only serves files: changing the published copy is enough and is engine-agnostic. setup.sh republishes the production copies, so the sheet re-runs lab-auth.sh after an engine switch. |
| P5-2 | **One SSH limiter per device**, the same rules in both engines (Python `SSHGate`, Go `sshGate`): at most `ssh_per_device` (default 1) sessions to a device across gate reads, backups, RMA reads, tuning writes, Release DWR access and pushes (a push holds its slot until its process ends; at the limit the candidate waits a pass). Within a checker pass or a gate re-check a healthy read session is held and reused by the next read of that device with the same login (backups included); the pass closes them. A device with a session open is counted up by the probes and the push pass without a TCP connect. | Concurrent logins to one slow device (a gate read, an after-build backup, a requested backup) queued behind each other until the third passed the auth timeout, and the probes' connects added to sshd's unauthenticated-connection count (MaxStartups) so later logins were dropped before the banner. One login per device per pass also cuts the work: a back-end pass reads five checks (six reads on Cumulus) over one session. |
| P5-3 | **Retries with backoff** on transient SSH failures (`Error reading SSH protocol banner`, `Connection reset by peer`, `Unable to find prompt`, `Authentication timeout`, `Socket is closed`, `SSH session not active`; matched in any case): `ssh_retries` (2) more attempts after `ssh_backoff_s` x 3^(k-1) (5 s, 15 s), the slot freed while waiting; reads retry whole, writes only the connection; a refused connection, a rejected password and ReaderUnavailable are never retried; the last error is reported unchanged. Go reader: paramiko's phases and wording (banner -- a line starting `SSH-` -- within 60 s, then key exchange and authentication within 90 s; a timeout after the banner reads netmiko's `... Authentication timeout.`, EOF or reset before it `Error reading SSH protocol banner[...]`). | Before, Go applied one 90 s deadline to banner, key exchange and authentication together and called any timeout a banner error, so a login Python finished (60 s banner + 90 s authentication) failed in Go. A dropped connection or a lost prompt on a busy device succeeds on a second try; a wrong password must not be retried (account lockout). |
| P5-4 | **Staggered backups**: job i of a backup pass (i below `reader_workers`) starts i x `backup_stagger_s` (2 s) after the first; a device's next scheduled backup is due `backup_every_h` plus crc32(name) mod 1000 / 1000 x min(600 s, 10 % of the interval) (after a failure 900 s plus the same share of 600 s). | Devices that reach Done together were all backed up in the same second, then again together every 6 h. In-memory only (`backup_due` is not stored), so the shared state is unchanged. |
| P5-5 | Harness expectations, timing only (the approved limiter, reuse and stagger change when a read or backup lands, not what it reads): p2diff waits, before the SR OS login is fixed at the Edge-PoP gate, until both engines have tried a backup of every Done edge device (a backup pass now reaches them at a speed that depends on the engine: Go's after-build backup failed on the wrong login, Python's, later, succeeded); p2diff lets "gate after the fixes" settle up to 90 s, as "gate with the NCCL result" already did (C64-3p: Python's re-check reads of 3,073-line spines queue behind backups on the same device); p3diff compares the newest backup per device, as p2diff has since Phase 4 (Go kept one extra intermediate version of a rail leaf taken mid-tuning). difftest's mismatch report also prints what each side of a multiset has more of (diagnostic). | Same rules in both engines; each change only waits or compares what a restore uses. |
| P5-6 | **Approved (Owner, 2 Nov 2026).** `acceptance/nccl.sh` takes GPUs per host from the GPU profile (`ACCEPT_GPUS_PER_HOST` default: 4 on GB300/Vera Rubin NVL72 trays, 8 on gpu8x400, gpu8x800, hgx800dlc, smc4u_b300); `lossless.sh` T3/T4 the same default. | It assumed 8: on NVL72 mpirun asked for twice the GPUs a tray has. |
| P5-7 | **Approved (Owner, 2 Nov 2026).** `acceptance/lossless.sh` T1 and T2 run on every rail NIC of every host: the ACTIVE RDMA devices whose netdev is addressed in the back-end rail block (`RAIL_NET`, from the plan), in address (rail) order, so every plane of a dual- or multi-plane host is covered; T1 a ring per rail, T2 an incast per rail, all rails at once. `ACCEPT_RAILS` defaults to the plan's rails per host; a host with fewer reports `RAILS <host> found N of M` and T1 appends `min_gbps=0 short_rails=N` (the console fails "line rate on every path"). `ACCEPT_HCA` (one NIC) or `ACCEPT_HCAS` (a list) is kept for debugging. NIC counters are summed over every rail NIC. New ACCEPT keys `rails`, `rails_min`, `rails_expected` are read and ignored by both engines' parsers. | T1/T2 tested `ACCEPT_HCA` (default `mlx5_0`) only: "every path" covered one rail per host. |
| P5-8 | **Approved (Owner, 2 Nov 2026).** `provision/rail_check.sh` floor `RAIL_MBPS` = the design's rail leg speed (`laneG()`: NIC speed / planes on the NVL72 lane fabrics, the 400G leg on dual-plane HGX B300, the port speed single-plane), with the reason in a comment; `RAIL_MBPS` overrides. | A fixed 200G floor passed a 400G leg that trained at 200G. |

## Verification (Phase 5)

Reproduction (`provgo-work/p5/farm.py`, `repro.py`): 11 real OpenSSH 9.6 servers on 127.50.0.x:22 shaped like Air's Cumulus VX
(MaxStartups 2; a PAM wait before pam_unix, 50 s per login and one login at a time per device, standing in for the TACACS
time-outs; bash login shell answering from p2diff's recorded outputs), the B-NV-SN5600 back end (3 spines, 8 rail leaves; every
other device set aside with lab-slice), the switches made Done, then 10 minutes without an override: the gate's automatic checks,
after-build backups, and at minute 2 a burst of 11 requested backups (8 switches, plus 3 at once on one spine).

| Run | Login wait | After-build backups | Burst | backup failed events | Logins | Most logins queued on one device | MaxStartups drops |
|---|---|---|---|---|---|---|---|
| Python HS v1.56 r2 | 50 s | 11 of 11 | 1 of 11 (81 s) | 10 (banner) | 108 | 2 | 11 |
| Go HS v1.56 r2 | 50 s | 0 of 11 | 9 of 11 (84 s) | 13 (banner, handshake EOF) | 106 | 2 | 11 |
| Python r3 | 50 s | 11 of 11 | 11 of 11 (22 s) | 0 | 77 | 1 | 0 |
| Go r3 | 50 s | 11 of 11 | 11 of 11 (18 s) | 0 | 77 | 1 | 0 |
| Go r3, release binary, 5 min | 50 s | 11 of 11 | 11 of 11 (18 s) | 0 | 44 | 1 | 0 |
| Python r2, lab mode (no wait) | 0 | 11 of 11 | 10 of 11 | 1 (banner: 3 at once on one spine) | | | 1 |
| Go r2, lab mode (no wait) | 0 | 11 of 11 | 10 of 11 | 1 (handshake reset) | | | 1 |
| Python r3, ssh_per_device 3 (retry path) | 0 | 11 of 11 | 11 of 11 | 0 (1 retry after 5 s) | | | 1 |
| Go r3, ssh_per_device 3 (retry path) | 0 | 11 of 11 | 11 of 11 | 0 (1 retry after 5 s) | | | 1 |

The before runs fail as Air did (Go losing every after-build backup to banner errors; Python losing concurrent backups). The lab
mode alone does not fix a burst on one switch (MaxStartups); the engine fixes do. The retry runs found two Go wording defects,
fixed before the final binary: a reset is lowercase in Go ("connection reset by peer"; the match is now case-insensitive in both
engines), and OpenSSH writes "Exceeded MaxStartups" before dropping, which Go took for a banner (the banner now counts from an
`SSH-` line, as paramiko reads it). Not reproduced here: TC-208's gate-read failures (no gate evidence read failed in any run, before
or after; the gate results were the same before and after in each engine) and Go's "Unable to find prompt".

Unit tests (`sshgate_test.go`): `TestSSHGateLimit` (1 and 2 per device, 24 callers), `TestSSHGateReuse`, `TestSSHGateWaitAndTry`,
`TestSSHBackoffAndTransient`, `TestSSHRetry`, `TestBackupStaggerAndOffset` (offsets equal to Python's), `TestDialMaxStartupsDrop`;
`go test ./...` passes.

Harnesses (final engines: Go binary amd64 sha256 5d1955b7dd94438f, arm64 0e7cf0c08e74086d; Python `/tmp/ex153` with `fixes.json`):
- Phase 1: B-NV-SN5600, B-BE-EOS, C64-3p, C-FTD: 129 comparisons each, 0 mismatches.
- Phase 2: B-NV-SN5600 56, B-BE-EOS 46, C64-3p 56, C-FTD 56 comparisons, 0 mismatches (with the P5-5 waits). Fake-device logins
  on C64-3p: 3,021 (Phase 4: 9,806). One earlier C64-3p run showed Go's "read failed: Pattern not detected" on two 1,536-link
  spines (the P4-9 symptom, on the shared single-process fake under load); the rerun passed.
- Phase 3: B-NV-SN5600, 343 comparisons, 0 mismatches (engine switch probes included).

Package (`hs/b156r3.html` through the site runner): B-BE-EOS 198 and B-NV-SN5600 767 device configuration files byte-identical to
HS v1.55 / v1.56 r2; parity 197 of 197 and 766 of 766; `pkgproof.py` PASS (16 checks; run from a short work path: a long one
puts the fake Kea socket past the AF_UNIX limit, and the two engines word that error differently). .run files that change:
`acceptance/lossless.sh`, `acceptance/nccl.sh`, `provision/rail_check.sh`, `provision/estate_build.py`, `provision/bin/*`,
`provision/estate.json` (time stamp), `air/README.md`, `air/ztp-host.sh`, new `air/lab-auth.sh`. Acceptance values per profile
(built from B-BE-EOS with each profile): GPUs per host 8 (gpu8x400, gpu8x800, hgx800dlc, smc4u_b300), 4 (gpu_nvl72, gpu_vr_nvl72);
rails per host 8 / 16 / 32 as the rails runner's `rails_per_host`; rail speed 400G (gpu8x400, the dual-plane HGX B300 profiles,
NVL72 cx8 2 planes, VR cx9 4 planes), 200G (NVL72 cx8 4 planes, VR cx8 4 planes, VR cx9 8 planes). lossless.sh checked on a mock
(fake ssh, rdma, ip, ib_write_bw): rail NICs found by rail address, a front-end NIC skipped, a ring per rail, a missing rail fails T1.
air/lab-auth.sh checked on the B-NV-SN5600 package's 12-switch slice (84 lines in 12 files; again: no change; --undo restores
byte for byte; refused off oob-mgmt-server).

## Unproven after Phase 5

- The lab mode and the engine fixes in DSX Air itself: TC-208b. The TACACS wait was modeled (a PAM delay), not run against
  Cumulus's own TACACS client and NSS module.
- Retries on Junos, EOS, NX-OS, SR Linux, SR OS and FTD sshd behavior (only OpenSSH was used); a session held for reuse on
  platforms that close idle sessions sooner than a checker pass.
- lossless.sh on real hosts (rdma and ip output formats on the shipped OS, ib_write_bw on every rail at once).

# Evaluation readiness Phase 0: neutrality and release identity (HS v1.57, desktop 2.0.51)

Design: Evaluation Readiness Design, baseline desktop 2.0.50 (c32f8f7), builder HS-v1.56 r3, site runner 1.9.0.

## Owner approval (design Section 7)

The Owner approved every Section 7 recommendation, D1 to D15. Phase 0 implements D14 and D15; D1 to D13 are built in Phases 1 to 4.

| # | Approved |
|---|---|
| D1 | Input sheet: an XLSX template generated from the catalog; the importer also takes long-format CSV. |
| D2 | Customer values live in a session overlay (`SESSION_IN`) inside the design file and its fingerprint, with the session input file as the record. Profiles and the builder source are never touched. |
| D3 | A value outside the supported options takes the nearest supported one, flagged and pre-classified as a design choice. Options are added only where an NVIDIA rule backs them. |
| D4 | Topology parameters match exactly; device counts within floor(2%) per role and in total. |
| D5 | Four classes: design choice, error (Product), error (Baseline), differs from NVIDIA's public reference. Only error (Product) fails; needs the Schedule A wording. |
| D6 | Rules sources pinned: URL, last-updated date and SHA-256 of the retrieved copy, frozen with the release. |
| D7 | The same rules run on the Baseline, informational on both sides. |
| D8 | A health "error" is curated: hard stops, integrity-tagged high items, cabling-BoM reconciliation, port over-allocation. Placeholders show as "Open inputs (N)". |
| D9 | Evaluation package: Schedule B plus the equivalence report. |
| D10 | Viewer delivered as an HTML file: watermarked, export off, Review stripping. |
| D11 | VR 2-plane x 800G: verify the NVIDIA rule against the current revision; if confirmed, add the option (and review GB300 in the same pass) before Test Point (b). |
| D12 | Traceability by names, metadata and manifest only; no addressing salt. |
| D13 | Console license: a signed Ed25519 file, checked by both engines. |
| D14 | Neutrality scrub of strings and comments, and a gate that fails the build on the patterns; register sources read "Owner decision". |
| D15 | A clean release (HS-v1.57), gated on the builder file's SHA-256. |

## Decisions (Phase 0)

| # | Decision | Why |
|---|---|---|
| P0-1 | **Standards named by subject.** Every internal standards document id becomes the subject it covers ("the IP plan", "the front-end standard", "the Edge PoP standard", ...); document revisions and clause numbers that went with them are dropped. Tenancy and Edge PoP clause ids are dropped, or the sentence says what the clause requires. Personal attributions in comments become "Owner decision" with the date kept. | D14. The builder carries the design rules, not the numbering of documents a customer has no copy of. |
| P0-2 | **Recorded deviations renumbered DV-01 to DV-04**, owner "Owner decision"; the power-side clause key becomes "One side carries the whole rack" (the key `deviationFor()` matches). A loaded design's entries with the old ids are skipped, so the built-in records stand. | The old ids and owner are in every design saved before 2.0.51 (deviations are saved with the design); without the skip, loading one would bring them back into the outputs. |
| P0-3 | **Configuration text: comments only.** Clause references in generated comment lines (edge hardening, node network, rail overlay, border leaf, DCI, BNK files, lab and acceptance READMEs) are reworded; no configuration statement changes. | D14 covers what the builder emits; the configuration semantics must not move. |
| P0-4 | **Neutrality gate**, `tools/neutrality-check.js`: banned patterns built from fragments (the gate does not match itself); reads into zips, .run payloads, gzip, PDF streams and hex strings, and the builder's base64 script blocks (DC Layout Builder, Go engines). First step of the GitHub build; run on a test build's job folder before a release. | D14: "CI gate fails the build". The embedded pages and binaries are emitted into the .run and the package, so they are in scope. |
| P0-5 | **Release identity = SHA-256 of the builder file.** The app hashes the builder at start and passes it to the window (`--aidc-builder-sha256`); `builderSha256()` in the builder reads it (or `__aidcBuilderSha256`, set by the runner and services). The site-runner file carries `builderSha256`; the package README gains a Builder SHA-256 row. | D15 and finding F2: one version string covered three different files. |
| P0-6 | **The site runner refuses a file without `builderSha256`** (export from a browser, or before 2.0.51), and a file whose SHA-256 differs from the `--html` builder's, with both values in the message. `--settings` builds still bypass it and are named TEST-FROM-SETTINGS. The design and Layout services refuse a mismatch when the window's file carries a SHA-256. | A version-only fallback would keep the F2 hole open. A browser opened from disk cannot read its own file, so a browser export has no SHA-256; past 512 racks the app is the supported path. |
| P0-8 | **Phase 0b (Owner approved):** the runner stays strict (no version-only fallback; `--settings` is the test bypass). Open-item ids (`OD-*`) and question ids (`Q-0xx`) are dropped from strings and comments, the sentence kept; the sample site code becomes ACME-LAX. The gate adds all three as whole tokens, so part numbers (QFX5240-64OD-AFO) and base64 runs do not match. Clause families outside the list (for example BE-, FE-, DL-) are unchanged. | The ids name a tracker the customer does not have; the site code was a real site's. |
| P0-9 | **Phase 0c (Owner approved):** every requirement and clause id family of the former standards set (BE, FE, OB, IP, AN, CB, CA, GX, FR, FT, FH, DL, EA, three digits) is dropped where the sentence stands alone and otherwise replaced by its subject; superseded/withdrawn tags go with them. Old sample build numbers in the release notes become ACME-LAX. Internal issue numbers are dropped from the code and shipped docs. Kept: DSX Air test case ids (TC-2xx, the product's own Air test sheet; file names under `air/` use them), the DV-0N deviation ids, and DWR Networking's identity on the package. | The gate pattern is a whole token, so part and model numbers (PX3-5547, QFX5240-64OD), interfaces, device names (MCS-01) and base64 or hex runs do not match. |
| P0-7 | **HS-v1.57** (not HS-v1.56 r4): a new string differs from every HS-v1.56 revision, and the output file follows the clean-release name `aidc-dbd-hyperscale-v1.57.html`. Site runner 1.10.0, desktop 2.0.51. | D15. |

## Verification (Phase 0)

- Neutrality gate: repository 0 hits (195 files, the builder's embedded DC Layout Builder, Deployment Console and both Go
  engines read through). Outputs of the 256-rack GB300 SN5610 test build (`--settings --racks 256`), package and .run
  unzipped: 25,867 files, 0 hits. The same design built by 2.0.50: 27,761 hits (Edge PoP clause ids 18,508, standards ids 9,248).
- Parity 11,635 of 11,635. Against the 2.0.50 build: 11,655 package files on both sides; 7,021 of 11,636 device
  configuration files byte-identical; 4,615 differ in comment lines only (4,608 DPU HBN files: the node-network comment;
  gateways and border leaves: node-network and DCI comments). Outside the device configurations: the BNK README line, the
  EVE-NG lab interconnect description and READMEs, the front-end README, `estate_build.py` (one comment and VERSION).
  The design fingerprint for the same settings changes (b98ba38d0e5f6230, was 206ce485c9150cad): the renamed deviation
  records are part of the saved design. Plan digest unchanged (3e234672d4271114).
- Release gate: no `builderSha256`, a wrong one, and the HS-v1.56 r3 builder against an HS-v1.57 file: refused, each with
  both values. A 16-rack design exported headless from HS-v1.57 with the app's SHA-256: release check and fingerprint pass,
  parity 775 of 775, its package passes the neutrality gate.
- `go test ./...` passes. difftest Phase 1 on the 16-rack .run: 129 comparisons, 0 mismatches.
- Phase 0c: gate 0 hits on the repository, the 256-rack outputs (25,867 files) and the portable zip; the 2.0.50 build of
  the same design shows 49,792 hits. Parity 11,635 of 11,635, plan digest 3e234672d4271114. Against 2.0.50: 5,347 package
  configuration files identical, 6,282 differ with every statement identical once comments are stripped; in the .run, only
  the EVE-NG lab's edge-router interconnect description and its two cable lists differ outside comments. `go test ./...`
  passes; difftest Phase 1: 129 comparisons, 0 mismatches; the r2 builder is refused by SHA-256.
- Phase 0b, same tests again: gate 0 hits on the repository and on the 256-rack outputs (25,867 files); parity 11,635 of 11,635,
  plan digest 3e234672d4271114, fingerprint b98ba38d0e5f6230; against 2.0.50, 5,353 configuration files byte-identical and
  6,282 differing with every statement identical once comments are stripped. The earlier HS-v1.57 build (same version string)
  is refused by SHA-256. `go test ./...` passes; difftest Phase 1: 129 comparisons, 0 mismatches.

# Evaluation readiness Phase 1: input sheet and importer (HS v1.58, desktop 2.0.52)

Implements D1 to D3 (design Section 3). The provisioning engines are unchanged.

## Decisions (Phase 1)

| # | Decision | Why |
|---|---|---|
| P1-1 | **The template is generated by the builder** (`inputSheetWorkbook`) from the live catalog: `PROFILES` for the platform, planes, NIC and cooling lists; the builder's own option lists for every platform role and setting. Values outside a list are allowed with a warning. | D1. A hand-made template would drift from the release. The customer must be able to state what they want, so the importer, not Excel, decides what is built. |
| P1-2 | **Keys are stable ids** (`site.racks`, `power.rack_feed_kw`, `fabric.be.planes`, ...). Numbered families (`site.obstruction.N`, `constraint.N`, `power.rack_feed_kw.<hall>`) take any number. The same rows load from XLSX or long-format CSV. | D1. One parser path, one report, whatever the format. |
| P1-3 | **One step, in control order.** The importer checks each control against its live option list after the controls before it are set (the platform before its planes and NIC), holding the rebuild. It then builds the way the site runner does (controls, build, controls again). The pod size is checked against the built design, because its options follow the switch radix and the rack count. | D2. One build instead of one per control. The result is the design the runner rebuilds from the same settings. |
| P1-4 | **`SESSION_IN` holds the inputs that have no control:** the compute rack feed (`rackFeedKw`, read where the profile's `rackKW` was) and the busbar rating (`busbarA`, applied at runtime in place of the 1,400 A default). It is saved in the design (`sessionIn`) only when it holds a value that differs from the platform default. It is checked on load and applied before the rebuild in the runner, the services and Load. | D2 and contract 5.2. A sheet that restates a design leaves the design and its fingerprint as they were (the round trip), and the runner rebuilds exactly what was imported. |
| P1-5 | **Nearest supported (D3):** a number is clamped to the range and rounded to the step; a list value matches exactly (value or label), then by the nearest number (a tie takes the higher), then by a unique partial match (SN5600 as NV-SN5600). Derived rows (rows, racks per row, rails per plane, back-end oversubscription 1:1) are compared after the build. Each such row has status `nearest supported`, class `design choice`. A value with no usable match keeps the builder's value as `not supported`, also classed as a design choice. | D3. The equivalence report (Phase 2) reads the class from the session input file. |
| P1-6 | **Not modeled is reported, never dropped:** obstructions, row spacing, per-hall feeds, constraints and unknown keys go into the report and the session input file. | D3. The customer sees what the builder did not use. |
| P1-7 | **The session input file** (`aidc-dbd-session-input/1`) is the record: the sheet's name and SHA-256, every row as given, the controls set, the overlay, the report, and the build (build number, fingerprint, `profilesDefaultsSha256`). `evaluationId` is reserved for Phase 3. The runner writes it under `customer-data/`. The app saves it to the project folder. | D2 and design Section 3.2 (storage). It stays out of the delivery manifest. |
| P1-8 | **5.2 proof, `profilesDefaultsDigest()`:** SHA-256 over `PROFILES` as declared. The builder's own per-build derivations are left out by name: the planes and host form factor the lane fabric selects, host kW from the design basis, and the Vera Rubin OEM's rack model. | Those fields already moved with ordinary builds before this release. Every other profile field is covered. |
| P1-9 | **App: the File menu reads the file** and hands its bytes to the window (`onInputSheet`), instead of the window opening a file chooser without a click. The builder's **Input sheet** button keeps the browser path. | A file chooser opened from a menu message has no user activation in the window. |
| P1-10 | **Runner `--inputs` builds under the normal names.** There is no exported fingerprint to compare, because the design is made by the `--html` builder file from the sheet. The log gives the sheet's SHA-256 and the fingerprint made, and `<build>-site-runner.json` is written for a later gated `--design` build. `--inputs` refuses `--design`, `--settings` and `--racks`. | The sheet is the approved input. The release freeze for evaluation runs is Phase 3 (`--eval`). |

## Verification (Phase 1)

- Round trip (sheet filled from a design, imported on a fresh page): same controls, same fingerprint. GB300 16 racks
  (0e39a7f1a874fdc9), Vera Rubin 24 racks (c6657bc7add26912), GB300 NVL72 256 racks on SN5610 (b98ba38d0e5f6230, the
  2.0.51 fingerprint of the same settings), Vera Rubin NVL72 364 racks on SN6800 (20297e2cfe6e3716).
- 5.2: `profilesDefaultsDigest()` 13c288ecf5ac704d840aac2f0fc6a491ff69250c4a617ffa36f0e2595f421bd5 at start, after the
  import and after the build, on every run above. The builder file's SHA-256 is the same before and after the runs. The only
  change to the builder is the source change between HS-v1.57 and HS-v1.58.
- Out of range (25-row CSV, GB300 16 racks): 11 nearest supported (planes 3 as 4, BE 2:1 as 1:1, FE 8:1 as 6:1, 50U as 52U,
  700 kW as 600, 33 m as 35, 2,000 tenants as 989, SN5600 as NV-SN5600, racks per row, rows, rails per plane), 2 not supported
  (an unoffered firewall, "maybe" for a Yes/No), 6 not modeled (two obstructions, a constraint, an unknown key, row spacing in
  ft, a per-hall feed). The overlay (600 kW, 1,600 A) is in the site-runner file. A `--design` build of that file passes the
  release check and the fingerprint (2ff8278b28acb4d3), parity 140 of 140. An HS-v1.57 builder is refused by SHA-256.
- Site runner `--inputs` against `--settings` with the same settings: GB300 256 racks, parity 11,635 of 11,635, plan
  digest 3e234672d4271114 on both, 11,636 of 11,636 package configuration files byte-identical (also against the 2.0.51
  build). The other 15 package files differ only by generation time. Vera Rubin 364 racks: parity 14,958 of 14,958, plan
  digest 36c4c2a744fa391e on both, 14,959 of 14,959 configuration files identical.
- `go test ./...` passes; difftest Phase 1 on the 16-rack .run: 129 comparisons, 0 mismatches.
- Neutrality gate 0 hits: repository; `--inputs` output folders for GB300 (25,872 files) and Vera Rubin (32,018 files),
  the customer-data files included; the portable build.

# Evaluation readiness D11: Vera Rubin at 2 planes x 800G (HS v1.59, desktop 2.0.53)

Implements D11 (design Section 7; rule NVR-015). The provisioning engines are unchanged.

## Source check (3 Nov 2026)

| Source | Finding |
|---|---|
| DGX SuperPOD Spectrum-X Reference Architecture with DGX Vera Rubin NVL72, https://docs.nvidia.com/dgx-superpod-spectrum-x-reference-architecture-with-dgx-vera-rubin-nvl72.pdf (no document number, revision or date; (c) 2024; no newer public revision found) | Eight ConnectX-9 per tray, "2x800 Gbps Ethernet connectivity per GPU", two independent fabrics; 2 planes, "4 switches per plane at leaf layer representing 4 rails"; rail-aligned, non-blocking, "8x 800G connections per compute tray"; SU 3 racks (216 GPUs), 4 leaves and 2 spines per plane per SU (Table 3, to 6,912 GPUs); compute switch SN6600 ("SN6600-LD has 128 ports of 800 Gbps"); N-S "BlueField-4 B4240V dual-port card at 400Gbps". NVR-015 confirmed. |
| NVIDIA technical blog, Inside the NVIDIA Vera Rubin Platform: Six New Chips, One AI Supercomputer (5 Jan 2026) | ConnectX-9 "800 Gb/s per port", "1600 Gb/s (2 x 800 Gb/s) per GPU". It gives SN6800 as "512 ports of 800 Gb/s or 2048 ports of 200 Gb/s". |
| Spectrum-6 SN6000 Hardware User Manual, Introduction and Specifications (last updated 16 Sep 2026) | SN6600, SN6600-LD and SN6810-LD: 128 x 800GbE. SN6800-LD: "2048 x 200GbE" (and 100GbE) only. The manual governs, so the SN6800's lane list stays 200/100G. |

## Decisions (D11)

| # | Decision | Why |
|---|---|---|
| D11-1 | **Vera Rubin `planeOpts` [2,4,8]**; `planeDef` stays 4. | NVIDIA's reference is 2 planes. A design without a plane setting builds as before; making 2 the default is open to the Owner. |
| D11-2 | **ConnectX-9 option carries `ports:2, portG:800`.** When a plane's lane equals a whole port (`hostPortPerPlane()`), the host side is one 800G optic per NIC port (two per GPU), not one 1.6T optic split into legs. The BoM rows, the rack-to-leaf connector and the CPO connector text follow. The 4- and 8-plane host model is unchanged. | The RA and the blog give two 800G ports per GPU. Only the new layout's text and optic counts change. |
| D11-3 | **No new lane on the SN6800.** A 2-plane request there builds at 8 planes and says so, as 4 planes already did. | The hardware manual lists 200GbE only. |
| D11-4 | **Plane raising is unchanged:** the smallest offered count at or above the request that stays on two tiers; if none does, the requested count with super-spines. | Applies to 2 planes as to any other count. At 364 racks (26,208 GPUs) a 2 x 800G plane needs about 410 leaves, more than any offered spine takes. The 1.6T and CPO switches therefore build 8 or 4 planes, and the 64 x 800G switches build 2 planes with super-spines. Keeping 2 planes with a third tier on every switch would be a new preference (Owner decision). |
| D11-5 | **GB300 NVL72 reviewed, unchanged:** 2 or 4 planes, ConnectX-8 800G. | NVIDIA's GB300 RA is dual-plane, 2 x 400G per GPU. |
| D11-6 | **Vera Rubin FE NIC:** the source note cites the BlueField-4 B4240V, dual-port 400G. The model stays BlueField-3 B3240. | The link count and speed that size the front end match. The generated DPU software (HBN bundle, bf.cfg, 13,106 files at 364 racks) is BlueField-3's, and no BlueField-4 software target is pinned (Owner decision). |
| D11-7 | **Fix: flat spine count on lane fabrics.** If the per-plane spine count cannot carry the plane's uplinks (`nP x SPR < upP`), use one spine per uplink lane of the fullest leaf (`nP = mxP`). | With leaves of different uplink counts in a plane, the count fell back to the largest power of two and left leaves without uplinks, with no warning. That happened in HS-v1.58 too, on existing options. The guard fires only on a short spine tier, so a correctly built design is unchanged. |

## Verification (D11)

- Vera Rubin 96 racks (6,912 GPUs), SN6600, 2 planes, 16-rack pods: 2 x 800G per GPU (1.6 Tb/s), 4 rails per plane, 120 leaves and 58 spines per plane, no super-spines, 356 switches. Every GPU has one leg per plane; 13,824 host legs against 13,824 uplink lanes; no switch port used twice. No hard stops, catalog, scheme or ASN problems, no high front-end warnings. Site runner parity 4,262 of 4,262. NVIDIA's Table 3 at 6,912 GPUs: 128 leaves and 64 spines per plane, 384 switches. The builder's 16-rack pods fill leaves more fully than the 3-rack SU.
- Vera Rubin 364 racks, SN6800, 2 planes: built at 8 x 200G ("2 planes do not fit this switch's 200/100G lanes"), parity 14,958 of 14,958, plan digest 36c4c2a744fa391e.
- Unchanged against 2.0.52, all package configuration files byte-identical: Vera Rubin 364 racks on SN6800 at 4 planes (14,959 files, fingerprint 20297e2cfe6e3716), on SN6800 at 8 planes (14,959, 10407090a45b80e9) and on SN6600 at 4 planes (18,322, 190bb91cd2582856); GB300 NVL72 256 racks on SN5610 (11,636, b98ba38d0e5f6230, plan digest 3e234672d4271114).
- Platform sweep at 24 racks, all 12 back-end platforms, both profiles, every NIC: every 4- and 8-plane pick unchanged. Sweep at 96 racks (192 builds, default and 16-rack pods): leaves without uplinks in 20 builds on HS-v1.58, 0 on HS-v1.59. 125 builds keep their fingerprint. Of the 67 that change, 48 are Vera Rubin 2-plane requests (newly offered); the other 19 are builds HS-v1.58 left with leaves without uplinks.
- `go test ./...` passes; difftest on a 16-rack Vera Rubin 2-plane .run: 129 comparisons, 0 mismatches.

# D11 follow-up (r2): Keep plane count and the BlueField-4 north-south DPU (HS v1.59, desktop 2.0.53 r2)

Resolves the two Owner decisions D11 left open (D11-4, D11-6) as options; the defaults are unchanged. The provisioning engines are unchanged.

## Sources (BlueField-4)

| Source | Finding |
|---|---|
| NVIDIA BlueField-4 DPU datasheet, doc 5193854, JUN26, https://dam-cdn.nvd.orangelogic.com/AssetLink/whs8mhb340t412js4612g3356607hapf.pdf | 800 Gb/s max; 800G per port on 200G SerDes; up to 8 split ports; Ethernet and InfiniBand; PCIe Gen6 x16 with Socket Direct; 64-core Grace (Neoverse V2), up to 128 GB LPDDR5x, up to 512 GB SSD; integrated DPU BMC, 1GbE OOB, Redfish, PXE/UEFI; DOCA, no version stated. Not stated: power, OPN, physical port count, cage. |
| DGX SuperPOD Spectrum-X RA with DGX Vera Rubin NVL72, https://docs.nvidia.com/dgx-superpod-spectrum-x-reference-architecture-with-dgx-vera-rubin-nvl72.pdf | North-south: one BlueField-4 B4240V per compute tray at 2 x 400G. |

## Decisions (r2)

| # | Decision | Why |
|---|---|---|
| R2-1 | **Keep plane count** (`keepplanes`, off by default, NVL72 platforms). On: `laneApply` picks the requested count when the switch runs its lane (`laneFor().ok`), with super-spines when it does not stay on two tiers. A count the switch cannot run falls back as before, and the note adds "Keep plane count cannot apply". | D11-4 left keeping the plane count with a third tier as an Owner preference. Off is the D11 rule, byte for byte. On all 12 back-end platforms, the only fallbacks are the SN6800-LD (200/100G lanes): Vera Rubin 2 and 4 planes, GB300 2 planes. |
| R2-2 | **North-south DPU selector** (`nsdpu`: `bf3` default, `bf4`), offered on Vera Rubin NVL72 only. The options sit in `NS_DPU_OPTS`, not in `PROFILES`, so the profiles digest (5.2 proof) is unchanged. | The BlueField-4 source is NVIDIA's Vera Rubin RA. No source pairs BlueField-4 with GB300 or the HGX profiles; adding it there is an Owner decision. |
| R2-3 | **BlueField-4 builds as 2 x 400G**, a 2-way split of the 800G port. The FE NIC count and speed are the BlueField-3's, so the topology, the front-end sizing and every switch configuration are unchanged. BoM: a B4240V line, OPN CONFIRM. Cable plan, FEF cable map and cable matrix: the host end is a 400G leg of the split 800G port, physical port count, cage and assembly CONFIRM. Host PCIe Gen6 x16. OOB management cabling and MGMT-DPU addressing as BlueField-3. | The RA gives 2 x 400G; the datasheet gives 800G ports with splits and no physical port count or cage. |
| R2-4 | **No BlueField-4 DPU configuration files.** `genHbnPackage` emits `hbn/README.md` ("software target pending: no published DOCA release names BlueField-4") and `hbn/dpu-management.csv` only. No HBN OS profile, no DPU image install step in the .run, no DPU telemetry job (its endpoint is set up by bf.cfg). Health: a medium front-end item, not an error. | The datasheet names DOCA without a version, and no published DOCA release names BlueField-4. Generating BlueField-3 HBN files for it would be wrong software. |
| R2-5 | **Rack power keeps the current figure;** the design register adds "BF-4 power not published" (placeholder, not load-bearing). | The datasheet gives no power figure; the host power figures include the DPU. |
| R2-6 | **Saved only when used.** `ctrlSnapshot` leaves out `keepplanes` and `nsdpu` at their defaults (and where they do not apply); a whole snapshot without them sets them back to the default; the input sheet and share-link paths apply only the keys given. Input sheet rows `fabric.be.keep_planes` and `fe.ns_dpu`. | A design that does not use them keeps its fingerprint, so the site-runner release and fingerprint gates accept designs from the first 2.0.53 build. |
| R2-7 | **No NVIDIA reference rule code to change.** The builder has no NVR rule checks; the Vera Rubin FE NIC source note (D11-6) is kept as it is, so the default output is unchanged. | Changing the note would change BlueField-3 outputs. |

## Verification (r2)

- Defaults: every package configuration file byte-identical to the first 2.0.53 build, same fingerprints: Vera Rubin 364 racks SN6800 at 4 planes (14,959 files, 20297e2cfe6e3716) and 8 planes (14,959, 10407090a45b80e9), SN6600 at 4 planes (18,322, 190bb91cd2582856); GB300 NVL72 256 racks SN5610 (11,636, b98ba38d0e5f6230). 24-rack sweep, 12 platforms x every plane count x both NVL72 profiles (60 builds): fingerprints and plane picks identical. The four HGX profiles at 16 racks: fingerprints identical.
- Keep plane count on, Vera Rubin 364 racks at 2 planes: SN6600 (b1fc62dec6575e34) 2 x 800G, 1,104 leaves, 1,104 spines, 552 super-spines, parity 17,514 of 17,514; SN5610 (456a632a14aeb898) 2 x 800G, 1,840 leaves, 2,668 spines, 1,334 super-spines, parity 20,739 of 20,739. One leg per GPU per plane, no port used twice, no leaf with fewer uplinks than downlinks, no hard stops, catalog, scheme or ASN problems, no high front-end warnings. SN6800-LD: 8 planes with the reason.
- BlueField-4, Vera Rubin 364 racks SN6600 4 planes (418d99e85b269587): 13,104 DPU files not generated, every switch configuration byte-identical to BlueField-3, parity 5,217 of 5,217; no hard stops or high warnings; neutrality 0 hits on the outputs.
- Input sheet round trip: fingerprint kept with both settings set and at their defaults; profiles digest unchanged.
- `go test ./...` passes; difftest on a 16-rack Vera Rubin .run with both settings set: 129 comparisons, 0 mismatches.

# BlueField-4 Health item merged (HS v1.60, desktop 2.0.54)

| # | Decision | Why |
|---|---|---|
| BF4-1 | **One medium Health item for BlueField-4.** `fePlan` skips the FE NIC "reference-architecture assumption" item when `nsDpuBf4()`, and the BlueField-4 item carries both: configuration files held until a DOCA release names BlueField-4; OPN B4240V, port count and cage CONFIRM; power not published. `BF4_PENDING` is the one wording, used by `hbn/README.md`, the provisioning notes, the BoM line and the status line; the inventory and wave labels say "DPU config held". Supersedes the R2-4 wording. | Two items said the same thing. BlueField-3 paths are untouched, so default output is unchanged. |

## Verification (BF4-1)

- BlueField-3 (default): every package configuration file byte-identical to 2.0.53, same fingerprints: Vera Rubin 364 racks SN6800 8 planes (10407090a45b80e9), SN6600 4 planes (190bb91cd2582856); GB300 NVL72 256 racks SN5610 (b98ba38d0e5f6230). Parity full.
- BlueField-4, Vera Rubin 364 racks SN6600 4 planes (418d99e85b269587): one BlueField-4 Health item (medium), no hard stops; switch configurations byte-identical; no DPU configuration files; parity 5,217 of 5,217.
- `go test ./...` passes; difftest on a 16-rack Vera Rubin BlueField-4 .run: 129 comparisons, 0 mismatches. Input sheet round trip keeps the fingerprint.
