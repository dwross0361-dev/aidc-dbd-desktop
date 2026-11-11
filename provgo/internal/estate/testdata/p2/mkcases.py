#!/usr/bin/env python3
"""Writes cases.json: the scenario list both the Python generator (genexp.py) and the Go test replay."""
import json, os

HERE = os.path.dirname(os.path.abspath(__file__))
plan = json.load(open(os.path.join(HERE, "fixtures", "plan.json")))
names = [d["name"] for d in plan["devices"]]
checks = []
for n in names:
    for k in ("bgp", "evpn", "roce", "lossless", "software", "cabling"):
        checks.append({"kind": k, "dev": n})
checks += [
    {"kind": "mnha", "dev": "T-JUNOS-FW1", "c": {"devices": ["T-JUNOS-FW1", "T-JUNOS-FW2"]}},
    {"kind": "mnha", "dev": "T-JUNOS-FW2", "c": {"devices": ["T-JUNOS-FW1", "T-JUNOS-FW2"]}},
    {"kind": "mnha", "dev": "T-JUNOS-CL", "c": {"devices": ["T-JUNOS-CL"]}},
    {"kind": "mnha", "dev": "T-FTD-A", "c": {"devices": ["T-FTD-A", "T-FTD-B"]}},
    {"kind": "mnha", "dev": "T-FTD-B", "c": {"devices": ["T-FTD-A", "T-FTD-B"]}},
    {"kind": "mnha", "dev": "T-EOS-A", "c": {"devices": ["T-EOS-A"]}},
    {"kind": "bgp", "dev": "T-JUNOS-A", "c": {"min": 4}},
    {"kind": "bgp", "dev": "T-EOS-A", "c": {"min": "2"}},
    {"kind": "zzz", "dev": "T-EOS-A"},
    # a session goes down between two reads: marked (new)
    {"write": {"T-CUM-B/nv_show_vrf_default_router_bgp_neighbor_o_json.txt": json.dumps({
        "swp51": {"state": "idle"}, "swp52": {"state": "idle"}, "swp53": {"state": "established"}})}},
    {"kind": "bgp", "dev": "T-CUM-B"},
    {"kind": "bgp", "dev": "T-CUM-B"},
    {"write": {"T-CUM-B/nv_show_vrf_default_router_bgp_neighbor_o_json.txt": json.dumps({
        "swp51": {"state": "established"}, "swp52": {"state": "connect"}, "swp53": {"state": "established"}})}},
    {"kind": "bgp", "dev": "T-CUM-B"},
    # every session toward a set-aside peer
    {"write": {"T-CUM-A/nv_show_vrf_default_router_bgp_neighbor_o_json.txt": json.dumps({"swp53": {"state": "idle"}})}},
    {"kind": "bgp", "dev": "T-CUM-A"},
    # an LLDP output nobody understands, and lldpctl keyvalue text
    {"write": {"T-CUM-A/nv_show_interface_lldp_o_json.txt": "garbage that is not json\n"}},
    {"kind": "cabling", "dev": "T-CUM-A"},
    {"write": {"T-CUM-A/nv_show_interface_lldp_o_json.txt": "lldp.swp51.chassis.name=T-CUM-SP1\nlldp.swp51.port.descr=to leaf\nlldp.swp51.port.ifname=swp1\n"
                                                        "lldp.swp52.chassis.name=T-CUM-SP2.lab.example\nlldp.swp52.port.descr=swp1\n"}},
    {"kind": "cabling", "dev": "T-CUM-A"},
    # NVUE vrf list that is not JSON: default only; a list: an error
    {"write": {"T-CUM-B/nv_show_vrf_o_json.txt": "Error: timeout"}},
    {"kind": "evpn", "dev": "T-CUM-B"},
    {"write": {"T-CUM-B/nv_show_vrf_o_json.txt": "[\"default\"]"}},
    {"kind": "evpn", "dev": "T-CUM-B"},
]
P = {
    "nv_expand": ["swp1-4,swp9", "swp1-24,33-36,swp1s0-1", "swp7", "eth0,swp1-2", "swp5-3", "swp1-5000", "swp0-0,lo", "x10-12,y,1-2", "", "swp1s0-3,s4-5"],
    "cfg_norm": [
        ["nv set interface swp1-3,swp5 link mtu 9216  # comment here\nnv set qos egress-scheduler default-global traffic-class 0,3,4 mode dwrr\n"
         "nv set vrf default router static 10.0.0.0/8 via 10.1.1.1 type ipv4-address\nnv config apply -y\nnv set system aaa user x password 'abc def'\n"
         "nv set system aaa user y hashed-password \"$6$z\"\n  # just a comment\n! bang\n\nnv set system message pre-login \"hello  world\"\n"
         "nv set interface swp1s0-1 type swp", "cumulus"],
        ["set system login user a authentication encrypted-password \"$6$abc\"\nset snmp community s3cret\nset interfaces et-0/0/1 mtu 9216 # trailing\n"
         "set protocols bgp group G authentication-key \"k3y\"\n   set  x   y  \nsnmp-server community readonly-community public RO\n"
         "username u secret 5 $1$abc\nset system tacacs-server 1.1.1.1 secret 'my secret'\n", "junos"],
        ["nv set interface swp1-2 link mtu 9216\nnv set qos roce mode lossless", ""],
        ["nv set interface swp10-12,swp20 qos pfc-watchdog\nnv set qos pfc-watchdog polling-interval 100\nnv set x 1,2", "cumulus"],
    ],
    "same_dev": [["T-CUM-SP1.lab", "T-CUM-SP1", "CUM-SP1"], ["cum-sp1", "T-CUM-SP1", "CUM-SP1"], ["", "a", "b"], ["x", "y", ""], ["Y.z", "y", None]],
    "vnum": ["25.2X100-D30", "4.33.2F", "10.4(5)", "v24.10.1-492", "", "abc"],
    "lldp_text": ["lldp.swp1.chassis.name=sp1\nlldp.swp1.port.ifname=swp9\n", "lldp.eth0.port.descr=x\n", "", "{}", "[]",
                  "{\"swp1\": {\"neighbor\": {\"x\": {\"chassis\": {\"system-name\": \"\", \"name\": \"alt\"}, \"port\": {\"id\": 7}}}}}",
                  "{\"Ethernet1\": [{\"systemName\": true, \"description\": \"d\"}], \"other\": {\"et-0/0/1\": {\"chassis-name\": 12}}}"],
    "bgp_texts": [["{\"peers\": {\"1.1.1.1\": {\"peerState\": [\"Established\"]}, \"2.2.2.2\": {\"state\": \"Idle (Admin)\"}}}"],
                  ["{\"a\": [{\"neighbor\": [{\"data\": \"3.3.3.3\"}], \"state\": [{\"data\": \"Active\"}]}]}", "{\"b\": {\"connection-state\": \"openconfirm\"}}"],
                  ["Neighbor V AS\n10.0.0.1 4 65000 1 2 3 0 0 1d02h 5\n10.0.0.2 4 65000 0 0 0 0 0 never Idle\n  admin down text\n10.0.0.3 4 65000 0 0 0 0 0 never Connect"],
                  ["fe80::1:2 0 65000 1 1 1 0 0 01:00:00 3\n2001:db8:0:1::2\n   65001 10 10 0 01h00m00s 1/1/1 (IPv6)"],
                  ["", "   ", "Error: nothing"],
                  ["{\"x\": {\"peer-state\": \"Established\", \"peer-address\": [\"a\", \"b\"]}, \"y\": {\"peerState\": \"Idle\", \"note\": \"disabled by administrator action\"}}"]],
    "acc_flat_text": ["", "error: nope", "% Invalid input", "rx_pause: 12\ntx pause (pri3) = 7.5\n  queue 3 drops : -2\nbad line\n",
                      "{\"a\": {\"B\": [1, \"2\", true, null, \"x\", 3.5, \" 4 \", \"1_000\", \"-3e2\"]}}", "5", "[1, [2, {\"C\": 3}]]"],
    "acc_parse": [["ACCEPT T1 port_gbps=400 min_gbps=391.2 note=ok\nNIC T1 rx_discards_phy 0\nNIC T1 rx_discards_phy 3\nNIC T2 x 1\n  ACCEPT T1 extra=1e3\n", "T1"],
                  ["ACCEPT T10 a=1\nACCEPT T1a b=2\n", "T1"], ["", "T4"]],
    "slug": ["show bgp summary | display json", "nv show vrf default router bgp neighbor -o json", "info from state network-instance * protocols bgp neighbor * session-state | as json" * 2, "@@"],
}
counters = ["T-JUNOS-A", "T-EOS-A", "T-NXOS-A", "T-CUM-A", "T-SRL-A", "T-JUNOS-B", "T-CUM-B"]
leaf = {"pfc_tx": 10.0, "pfc_rx": 5.0, "ecn": 3.0, "drops": 0.0, "wd": 0.0, "seen": ["pfc", "ecn", "drops", "wd"]}
leaf2 = {"pfc_tx": 30.0, "pfc_rx": 9.0, "ecn": 40.0, "drops": 0.0, "wd": 0.0, "seen": ["pfc", "ecn", "drops", "wd"]}
spine0 = {"pfc_tx": 1.0, "pfc_rx": 1.0, "ecn": 0.0, "drops": 2.0, "wd": 1.0, "seen": ["pfc", "drops"]}
spine1 = {"pfc_tx": 1.0, "pfc_rx": 0.0, "ecn": 0.0, "drops": 5.0, "wd": 1.0, "seen": ["pfc", "drops"]}
TXT = ("ACCEPT T1 port_gbps=400 min_gbps=392.5\nNIC T1 rx_discards_phy 0\nACCEPT T2 min_gbps=90 max_gbps=97 mean_gbps=95.25\n"
       "NIC T2 np_ecn_marked_roce_packets 12\nACCEPT T3 busbw=401.5\nACCEPT T4 busbw=383.25\n")
acc = [
    {"test": "T1", "text": TXT, "s0": {"T-CUM-A": leaf, "T-CUM-SP1": spine0}, "s1": {"T-CUM-A": leaf, "T-CUM-SP1": spine0}, "crit": {"line_rate_pct": 95}},
    {"test": "T1", "text": TXT.replace("392.5", "300"), "s0": {"T-CUM-A": leaf}, "s1": {"T-CUM-A": leaf2}},
    {"test": "T2", "text": TXT, "s0": {"T-CUM-A": leaf, "T-CUM-SP1": spine0}, "s1": {"T-CUM-A": leaf2, "T-CUM-SP1": spine1}, "crit": {"fairness_pct": 5}},
    {"test": "T2", "text": TXT, "s0": {"T-CUM-A": leaf}, "s1": {"T-CUM-A": "no counter reader for srl"}},
    {"test": "T3", "text": TXT, "s0": {"T-CUM-A": leaf, "T-CUM-SP1": spine0}, "s1": {"T-CUM-A": leaf2, "T-CUM-SP1": spine1}},
    {"test": "T3", "text": "ACCEPT T3 busbw=0\n", "s0": None, "s1": None, "rc": 2},
    {"test": "T4", "text": TXT, "s0": None, "s1": None, "conf_target": 380},
    {"test": "T4", "text": TXT, "s0": None, "s1": None},
    {"test": "T4", "text": "nothing here\n", "s0": None, "s1": None, "rc": 1},
    {"test": "T1", "text": "ACCEPT T1 min_gbps=10\n", "s0": {"a": leaf, "b": leaf, "c": leaf, "d": leaf}, "s1": {"a": "x", "b": "y", "c": "z", "d": "w"}},
]
results = [
    {"kind": "accept", "text": TXT, "crit": {"line_rate_pct": 95, "fairness_pct": 10}},
    {"kind": "accept", "text": "ACCEPT T1 port_gbps=400 min_gbps=100\nACCEPT T3 busbw=1\n"},
    {"kind": "accept", "text": "no lines\n"},
    {"kind": "isolation", "tenants": ["TENANT-01", "TENANT-02"], "text":
        "ISOLATION TENANT-01 bind PASS vip on ns tenant-01\nISOLATION TENANT-01 snat PASS 10.240.1.1\nISOLATION TENANT-01 cross PASS none\n"
        "ISOLATION TENANT-02 bind PASS ok\nISOLATION TENANT-02 snat FAIL wrong pool\nISOLATION TENANT-02 cross PASS none\n"},
    {"kind": "isolation", "tenants": ["TENANT-01"], "text": "ISOLATION TENANT-01 bind PASS\nISOLATION TENANT-01 snat PASS\n"},
    {"kind": "isolation", "tenants": None, "text": "ISOLATION T9 setup PASS ok\nISOLATION T9 bind PASS ok\nISOLATION T9 snat PASS ok\n"},
    {"kind": "isolation", "tenants": ["A", "B"], "text": "ISOLATION A bind PASS ok\n"},
    {"kind": "isolation", "tenants": ["A"], "text": "nothing\n"},
    # HS v1.98: inference load
    {"kind": "inference", "tenants": [["TENANT-01", 900], ["TENANT-02", 900]], "text":
        "INFERENCE TENANT-01 rps PASS measured 79650.2/s against 80000/s, 12 of 4779024 failed\nINFERENCE TENANT-01 p99 PASS 612 ms against 900 ms\n"
        "INFERENCE TENANT-02 rps FAIL measured 7000.1/s against 8000/s, 1012 of 420012 failed\nINFERENCE TENANT-02 p99 FAIL 1234 ms against 900 ms\n"},
    {"kind": "inference", "tenants": [["A", 0]], "text": "INFERENCE A rps PASS measured 10.0/s against 10/s, 0 of 600 failed\n"},
    {"kind": "inference", "tenants": [["A", 500], ["B", 500]], "text": "INFERENCE A rps PASS ok\n"},
    {"kind": "inference", "tenants": [["A", 500]], "text": "INFERENCE - setup FAIL hey not found on the client\n"},
    {"kind": "inference", "tenants": None, "text": "INFERENCE T9 rps PASS ok\nINFERENCE T9 p99 PASS 10 ms against 20 ms\nINFERENCE T8 setup FAIL VIP or hostname not set\n"},
    {"kind": "inference", "tenants": [["A", 500]], "text": "nothing\n"},
]
tuned = [
    {"state": {}},
    {"state": {"versions": [{"v": 1, "values": {"ECN_MIN": 200}, "note": "", "by": "a", "ts": 1.0}]}},
    {"state": {"approved": {"v": 0, "by": "admin", "ts": 1.0}}},
    {"state": {"versions": [{"v": 1, "values": {"ECN_MIN": 200}}], "approved": {"v": 1, "by": "admin", "ts": 1.0},
               "dev": {n: {"v": 1, "ok": True} for n in names if n != "T-WEKA"}}},
    {"state": {"versions": [{"v": 1, "values": {"ECN_MIN": 200}}], "approved": {"v": 1, "by": "admin", "ts": 1.0},
               "dev": {"T-CUM-A": {"v": 0, "ok": False, "note": "rejected: x"}, "T-CUM-B": {"v": 1}}, "rollout": {"state": "stopped at pod 1"}}},
    {"state": {"approved": {"v": 0, "by": "admin"}, "rollout": {"state": "complete"}}},
    {"state": {}, "plan": {}},
]
swap = [{"dev": "T-CUM-A", "planned": ["nv set qos congestion-control default-global traffic-class 3 min-threshold 150KB", "nv set qos roce mode lossless"],
         "state": {"versions": [{"v": 1, "values": {"ECN_MIN": 300}}], "dev": {"T-CUM-A": {"v": 1}}}},
        {"dev": "T-CUM-A", "planned": ["a"], "state": {"versions": [], "dev": {"T-CUM-A": {"v": 0}}}}]
diff = [
    {"dev": "T-CUM-A", "backups": [["20261030T100000Z.cfg", "nv set system hostname T-CUM-A\nnv set interface swp51-52 link mtu 9216\nnv set qos roce mode lossless\n"
                                                             "nv set system aaa role nvue-admin class nvapply\nnv set interface swp51 type swp\n"],
                                   ["20261031T100000Z.cfg", "nv set system hostname T-CUM-A\nnv set interface swp51-54 link mtu 9216\nnv set qos roce mode lossless\nnv set extra 1\n"]]},
    {"dev": "T-JUNOS-A", "backups": [["20261031T000000Z.cfg", "set version 25.2X100-D30.5\nset system host-name T-JUNOS-A\n"]]},
    {"dev": "T-XE-A", "backups": [["20261031T000000Z.cfg", "hostname T-XE-A\n"], ["20261031T000001Z.cfg", "hostname T-XE-A\n"]]},
    {"dev": "T-EOS-A", "backups": []},
    {"dev": "T-NXOS-A", "backups": [["20261031T0%05dZ.cfg" % i, "line %d\n" % (i // 3) + "".join("x%d\n" % j for j in range(i % 7, 30))] for i in range(3)]},
]
json.dump({"checks": checks, "parsers": P, "counters": counters, "acc_score": acc, "results": results, "tuned": tuned, "swap": swap, "diff": diff},
          open(os.path.join(HERE, "cases.json"), "w"), indent=1)
print("cases:", len(checks), "check steps")
