# acctest -- the acceptance-script release check

Every acceptance test a `.run` ships is run on a simulated site and scored by both engines, first on a correct site and then
with each fault planted. A release does not ship with an unexpected verdict or a difference between the engines.

```
python3 provgo/acctest/acctest.py --work /tmp/acctest --jobs 4 --run A.run --run B.run ...
```

Run it on the reference designs built for the release (the same `.run` files the differential test uses), after the
`.run` builds and before `dist:win`. Exit 1 on any unexpected verdict. `--verbose` lists every case.

## What it does

| Part | Does |
|---|---|
| `acctest.py` | Unpacks each `.run`; makes the fake site from its plan and acceptance files; runs every case; scores; reports |
| `bin/` | Mock `ssh`, `rdma`, `ip`, `ethtool`, `ib_write_bw`, `mpirun`, `iperf3`, `fio`, `hey`, `kubectl`, `curl`, `lldpctl`, `mlnx_qos` (each reads the fake host's data under `$MOCK/fake/<host>`; faults are files under `$MOCK`) |
| `pyjob.py` | Scores the console-scored results with the installer's own `estate_build.py` |
| `internal/estate/acctest_test.go` | Scores the same jobs with the Go engine (`TestAccJob`, skipped unless `ACCTEST_JOBS` is set) |

The fake site: every host in `soak.hosts`, `t7_pairs.txt` and `failover-cases.csv`, with its rail NICs in the rail block,
a storage NIC in the storage block and a tenant address; RoCE v2 IPv4 GIDs at index 3 on most hosts and 5 on some; NIC and
RDMA counters; 40 outside clients; a test host beyond the edge. Switch LLDP tables come from `cabling-expected.csv` in each
platform's format (NX-OS short port names, hosts running lldpd, Junos far ends sending SNMP indexes in one variant).

## Cases

| Test | Correct site | Faults planted |
|---|---|---|
| `lossless.sh` T1-T4, T7 | pass on both engines | a slow rail (T1), a hash collision (T7), a rail down (T1) |
| `nccl.sh` / `nccl_nvlink.sh` | pass; NCCL on the rail NICs only | below target; mpirun cannot launch; no rail NIC (setup failure) |
| `rail_check.sh` | pass | an unaddressed DPU port (still pass); a rail down (named) |
| `soak.sh` | pass on both engines | a link flapping; no rail NIC (stopped, named) |
| `storage_lossless.sh` | S1 and S2 pass | a host at 40%; storage not mounted (setup failure); discards |
| `failover.sh` | pass on both engines | a 1.8 s outage; the down command failing |
| `alerts.sh` | pass on both engines | a fault raising no alert; a device not reporting |
| `inference_load.sh` | pass with the clients it needs | one client (setup failure naming how many); p99 over target |
| `ingress_isolation.sh` | pass on both engines | a VIP in the wrong namespace; tenants reaching each other; a probe its own namespace cannot reach |
| Console cabling check | every switch passes on both engines; its cable plan equals `cabling-expected.csv` link for link | two cables swapped; Junos SNMP indexes (pass, by device) |
| `check_cabling.py`, Cumulus ZTP check | pass | two cables swapped |
| Console software check, `check_os_release.py` | pass (IOS XE zero-padded, Junos build numbers) | a device below its release |

## Upkeep

A change to an acceptance script, its generator or its scoring adds or updates its case here in the same commit, as the
parity fixtures do. A new acceptance test is not released without a correct-site case and at least one planted fault.
