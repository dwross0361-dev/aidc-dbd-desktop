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
| P3-7 | **Fixed in both engines, approved by the Captain 2026-11-01** (HS v1.56). Before the fix, Release DWR access on Junos: `send_config_set` left configuration mode before `commit()` ("Exit with uncommitted changes? yes" discarded the delete), so the read-back still showed dwr-netadmin and the device was reported failed. | Both stay in configuration mode and commit (platforms whose removal is "commit"). Python: `c.send_config_set(cmds, read_timeout=120, exit_config_mode=(how != "commit"))`. | Proven by `writerdiff.py` (fake Junos discards uncommitted changes on exit): HS v1.55 Python 2 mismatches on Junos (removal reported failed; commit-error text carried the exit dialogue), HS v1.56 Python 0 of 49. |
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
| P3-c | **Fixed in both engines, approved by the Captain 2026-11-01** (HS v1.56). Before the fix, the rails runner appended to `runner-rails.log.<host>` and took its first `RAILS ok N` line: a host that once failed failed every retry until its log file was deleted. | Each attempt starts the host log afresh (Python `open(lf, "w").close()` before `_exec` in `_run_rails.one`; Go truncates the same file). | The Phase 3 scenario fails a rail host, fixes it and retries: the retry passes in both engines (no log deleted). |
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
| P4-1 | The two approved Python fixes (P3-7 Junos Release DWR access commits the removal; P3-c rails host log truncated per attempt) ship in HS v1.56 through `hs_v156.py` (the `prov-py` block) and in the `/tmp/ex153` reference; Go already had both. The harnesses compare against the fixed Python; `difftest.py` applies `fixes.json` to installers built before v1.56. The P3 scenario's rail host fails, is fixed and is retried with no log deleted. | Approved by the Captain 2026-11-01. With both fixed there is no engine difference left in either place. |
| P4-2 | An unstamped Go binary (`Version == "__BUILDER_VER__"`) reports the plan's `builder` field as its version. | The builder stamps that same release into Python's `VERSION`; one binary serves every release and `/api/state` `version`, the "Service ... up" event and `status` read the same as Python's. The release binaries are not stamped. |
| P4-3 | SIGTERM/SIGINT: Go takes the engine lock, closes the database and exits 0. Python dies on the default SIGTERM (a write in progress leaves a hot journal, rolled back at the next open). | Found by Phase 1 on B-BE-EOS: Go was stopped mid-transaction and left `estate.db-journal` (state dir files differed). `systemctl stop` now leaves a clean database. Different in Go; no state dependency (the next open of either engine reads the same rows). |
| P4-4 | `setup.sh --engine go\|python`; default python on a fresh host, else the engine recorded in `/etc/fabric/estate-engine`; the CLI calls inside setup.sh go to the recorded engine; the unit's `ExecStart` is rewritten for go; unsupported CPUs refused before anything is installed. | Python stays the default (Captain). A re-run without `--engine` (for example `--yes --plane oob`) must not switch engines silently; both engines read the same state, so switching either way is safe and is a single re-run. |
| P4-5 | The binaries are embedded in the builder html (`<script type="text/plain" id="prov-go-amd64\|arm64">`, base64 of gzip -9) and added to the deploy package by the builder, not by the site runner. | A .run made in the browser and one made by the site runner carry the same files. Cost: the builder html grows from 4.9 MB to about 19 MB. gzip (not xz) because every Linux host has it. |
| P4-6 | `release-vendor` with standard input ended before every answer: Python stops with EOFError (traceback on stderr, exit 1); Go prints `[!] input ended before every answer was given: nothing was sent` and exits 1. Neither sends anything. | CLI only; `clidiff.py` checks Go's line and compares the rest. |
| P4-7 | Harness: a device that called in before its wave opened calls in again once its wave is open (lease event and first file), in p2diff and p3diff, the same for both engines. | Without it, plans whose later waves are NX-OS or IOS XE devices signalling "reachable" (B-BE-NXOS) never progressed in either engine: a harness gap, not an engine difference (both engines were stuck identically). Real devices retry DHCP. |
| P4-8 | Harness: `switch_probe` in p3diff snapshots each engine's state mid-build (database through the sqlite3 backup API) after the tuning rollout, after the RMA section and, on the standby pair, after promote, and opens every snapshot with BOTH engines; what they show must match. | Engine switch both ways on live mid-build state, including RMA, tuning and standby state. Phase 1 covered Python's state opened by Go only. |
| P4-9 | Go reader (bug fixes, found while chasing a read failure on one of C64-3p's 1,536-link spines, not reproduced since): (a) the read buffer held back up to 4 trailing bytes whenever the pending output held an invalid UTF-8 byte anywhere (it re-validated the whole buffer), so a prompt at the end could be withheld until the 120 s read timeout; now only an incomplete sequence at the very end waits. (b) `readUntil` searched the whole output after every read (quadratic in its size); it now searches the new text and 8 KiB before it (no pattern read for is start-anchored or longer). | Python (netmiko) returned these reads; Go reported "read failed: Pattern not detected" on one spine, blocking the cabling check. Unit tests `TestTakeUTF8Tail`, `TestReadUntilIncremental`. |
| P4-10 | **Fixed in both engines, approved by the Captain 2026-11-02** (HS v1.56 r2). A build with no devices in any wave (a plane the design leaves empty: edge and front end on the B-BE-* plans and B-feoff) is marked complete when a run starts: `start_run` completes every empty build in the run and before it (`passed` set, the run record saved, one event "No devices in plane <key>: build marked complete"); an empty build no longer counts as unpassed for the builds after it, so a run of all goes from the OOB build straight to the back end, and starting the back end alone needs no override. Defensive guard: the tick completes an empty build in the run instead of starting it (a record saved before the fix), and the timeouts pass skips a build with no waves and stops at its last wave, so a malformed record never raises and never stalls Kea or the push pass. Go's Phase 4 "raise to match Python" is removed. Python: `_empty`, `_complete_empty`, `start_run`, `tick`, `_timeouts` (hs_v156.py, `/tmp/ex153`, `fixes.json` for installers built before v1.56). Go: `emptyBuild`, `completeEmpty`, `startRun`, `tickLocked`, `timeouts`. | Before: Python raised IndexError in `_timeouts` on every tick, so a run of all on a plan with an empty plane stalled every Kea update and push (B-BE-JUNOS's push-based back end never finished). Same events and saved state in both engines (verified below). Unit tests `TestEmptyBuildCompleteAtStart`, `TestEmptyBuildRunAll`, `TestEmptyBuildTickGuard`. P2-5 no longer occurs. |
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
