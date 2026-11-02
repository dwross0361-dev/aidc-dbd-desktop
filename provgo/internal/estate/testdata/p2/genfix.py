#!/usr/bin/env python3
"""Writes the Phase 2 reader fixtures: per-OS command outputs as the vendors print them (JSON where the command asks
for JSON), a small plan with a good and a bad device per platform, planned configurations, and per-device state.

usage: genfix.py OUTDIR      (OUTDIR/sim/<device>/<slug>.txt, OUTDIR/plan.json, OUTDIR/bundle/configs/*.conf)

The slug of a command is estate_build.slug(cmd); it is recomputed here so the script needs nothing but the stdlib.
"""
import json, os, re, sys

OUT = sys.argv[1]


def slug(c):
    return re.sub(r"[^A-Za-z0-9]+", "_", c).strip("_")[:90]


SIM = {}


def put(dev, cmd, text):
    SIM.setdefault(dev, {})[cmd] = text


def J(o):
    return json.dumps(o, indent=4)


# ------------------------------------------------------------------------------------------------ Junos
def junos_bgp(peers):
    return J({"bgp-information": [{
        "attributes": {"xmlns": "http://xml.juniper.net/junos/23.4R2/junos-routing"},
        "group-count": [{"data": "2"}], "peer-count": [{"data": str(len(peers))}], "down-peer-count": [{"data": str(sum(1 for p in peers if p[1] != "Established"))}],
        "bgp-rib": [{"name": [{"data": "inet.0"}], "total-prefix-count": [{"data": "24"}], "active-prefix-count": [{"data": "24"}]}],
        "bgp-peer": [{
            "attributes": {"junos:style": "terse", "heading": "Peer                     AS      InPkt     OutPkt    OutQ   Flaps Last Up/Dwn State|#Active/Received/Accepted/Damped..."},
            "peer-address": [{"data": a}], "peer-as": [{"data": "4200002000"}], "input-messages": [{"data": "18231"}],
            "output-messages": [{"data": "18010"}], "route-queue-count": [{"data": "0"}], "flap-count": [{"data": "1"}],
            "elapsed-time": [{"data": "5d 3:12:09", "attributes": {"junos:seconds": "443529"}}],
            "peer-state": [{"data": st, "attributes": {"junos:format": st}}],
            "description": [{"data": desc}]} for a, st, desc in peers]}]})


put("T-JUNOS-A", "show bgp summary | display json", junos_bgp([("10.200.0.0", "Established", "to SP01"), ("10.200.0.2", "Established", "to SP02"),
                                                            ("fd00:200::1", "Established", "to SP03")]))
put("T-JUNOS-B", "show bgp summary | display json", junos_bgp([("10.200.0.4", "Established", "to SP01"), ("10.200.0.6", "Active", "to SP02"),
                                                            ("10.200.0.8", "Idle", "admin shut by ops")]))
JVER = """
Hostname: {h}
Model: qfx5240-64od
Junos: {v}
JUNOS OS Kernel 64-bit  [20240910.4f3a1c2_builder_stable_13]
JUNOS OS libs [20240910.4f3a1c2_builder_stable_13]
JUNOS OS runtime [20240910.4f3a1c2_builder_stable_13]
JUNOS Packet Forwarding Engine Support (qfx-ms-fpc) [{v}]
"""
put("T-JUNOS-A", "show version", JVER.format(h="T-JUNOS-A", v="25.2X100-D30.5"))
put("T-JUNOS-B", "show version", JVER.format(h="T-JUNOS-B", v="24.4R1.9"))
put("T-JUNOS-A", "show configuration class-of-service | display set | match \"pfc|explicit-congestion|ecn\"",
    "set class-of-service congestion-notification-profile CNP-ROCE input ieee-802.1 code-point 011 pfc\n"
    "set class-of-service schedulers SCH-ROCE explicit-congestion-notification\n"
    "set class-of-service interfaces et-0/0/0 congestion-notification-profile CNP-ROCE")
put("T-JUNOS-B", "show configuration class-of-service | display set | match \"pfc|explicit-congestion|ecn\"",
    "set class-of-service congestion-notification-profile CNP-ROCE input ieee-802.1 code-point 011 pfc")
put("T-JUNOS-A", "show lldp neighbors | display json", J({"lldp-neighbors-information": [{"attributes": {"junos:style": "brief"},
    "lldp-neighbor-information": [
        {"lldp-local-port-id": [{"data": "et-0/0/0"}], "lldp-local-parent-interface-name": [{"data": "-"}],
         "lldp-remote-chassis-id-subtype": [{"data": "Mac address"}], "lldp-remote-chassis-id": [{"data": "9c:5a:80:11:22:33"}],
         "lldp-remote-port-id-subtype": [{"data": "Interface name"}], "lldp-remote-port-id": [{"data": "et-0/0/48"}],
         "lldp-remote-system-name": [{"data": "T-JUNOS-SP1.lab"}]}]}]}))
JCFG_A = """set version 25.2X100-D30.5
set system host-name T-JUNOS-A
set system root-authentication encrypted-password "$6$abc$def"
set system login user netadmin authentication encrypted-password "$6$xyz$uvw"
set interfaces et-0/0/0 mtu 9216
set interfaces et-0/0/1 mtu 9216
set class-of-service congestion-notification-profile CNP-ROCE input ieee-802.1 code-point 011 pfc
set class-of-service schedulers SCH-ROCE explicit-congestion-notification
set class-of-service drop-profiles DP-ROCE interpolate fill-level [ 10 50 ] drop-probability [ 0 20 ]
set protocols bgp group UNDERLAY neighbor 10.200.0.0 peer-as 4200002000
set snmp community "s3cret" authorization read-only
"""
put("T-JUNOS-A", "show configuration | display set", JCFG_A)
put("T-JUNOS-B", "show configuration | display set", JCFG_A.replace("T-JUNOS-A", "T-JUNOS-B").replace(
    "set class-of-service schedulers SCH-ROCE explicit-congestion-notification\n", "").replace("set interfaces et-0/0/1 mtu 9216\n", "set interfaces et-0/0/1 mtu 1514\n"))
put("T-JUNOS-A", "show interfaces queue | display json", J({"interface-information": [{"physical-interface": [
    {"name": [{"data": "et-0/0/0"}], "queue-counters": [{"interface-cos-summary": [{"intf-cos-forwarding-classes-supported": [{"data": "16"}]}],
     "queue": [
         {"queue-number": [{"data": "3"}], "forwarding-class-name": [{"data": "ROCE"}], "queue-counters-queued-packets": [{"data": "9182733"}],
          "queue-counters-tail-drop-packets": [{"data": "0"}], "queue-counters-red-packets": [{"data": "0"}], "queue-counters-ecn-ce-packets": [{"data": "1203"}]},
         {"queue-number": [{"data": "0"}], "forwarding-class-name": [{"data": "best-effort"}], "queue-counters-queued-packets": [{"data": "12"}],
          "queue-counters-tail-drop-packets": [{"data": "7"}]}]}]}]}]}))
put("T-JUNOS-A", "show interfaces extensive | display json", J({"interface-information": [{"physical-interface": [
    {"name": [{"data": "et-0/0/0"}], "mtu": [{"data": "9216"}],
     "ethernet-pfc-statistics": [{"input-pfc-statistics": [{"pfc-priority-3": [{"data": "44"}]}], "output-pfc-statistics": [{"pfc-priority-3": [{"data": "17"}]}]}],
     "pfc-watchdog": [{"storm-detected-count": [{"data": "0"}], "watchdog-recovery-time": [{"data": "200"}]}]}]}]}))
put("T-JUNOS-FW1", "show chassis high-availability information", """Node failure codes:
    HW  Hardware monitoring    LB  Loopback monitoring
    MB  Mbuf monitoring        SP  SPU monitoring

Node Status: ONLINE
Local-id: 1
Local-IP: 10.255.0.1
HA Peer Information:

  Peer Id: 2   IP address: 10.255.0.2   Interface: ge-0/0/2.0
   Routing Instance: default
   Encrypted: YES    Conn State: UP
   Cold Sync Status: COMPLETE

Services Redundancy Group: 0
        Current State: ONLINE
        Peer Information:
          Peer Id: 2

SRG failure event codes:
    BF  BFD monitoring

Services Redundancy Group: 1
        Deployment Type: ROUTING
        Status: ACTIVE
        Activeness Priority: 200
        Preemption: ENABLED
        Process Packet In Backup State: NO
        Control Plane State: READY
        System Integrity Check: N/A
        Failure Events: NONE
        Peer Information:
          Peer Id: 2
          Status : BACKUP
          Health Status: HEALTHY
          Failover Readiness: READY
""")
put("T-JUNOS-FW2", "show chassis high-availability information", """Node Status: ONLINE
Local-id: 2

Services Redundancy Group: 0
        Current State: ONLINE

Services Redundancy Group: 1
        Deployment Type: ROUTING
        Current State: ACTIVE
        Activeness Priority: 100
""")
put("T-JUNOS-CL", "show chassis cluster status", """Monitor Failure codes:
    CS  Cold Sync monitoring        FL  Fabric Connection monitoring

Cluster ID: 1
Node   Priority Status               Preempt Manual   Monitor-failures

Redundancy group: 0 , Failover count: 1
node0  200      primary              no      no       None
node1  100      secondary            no      no       None

Redundancy group: 1 , Failover count: 1
node0  200      primary              yes     no       None
node1  100      secondary            yes     no       None
""")

# ------------------------------------------------------------------------------------------------ Arista EOS
def eos_bgp(peers, vrf="default"):
    return J({"vrfs": {vrf: {"routerId": "10.0.250.11", "asn": "4200001001", "peers": {
        a: dict({"description": "to-spine", "version": 4, "msgReceived": 4521, "msgSent": 4380, "inMsgQueue": 0, "outMsgQueue": 0,
                 "asn": "4200002000", "prefixAccepted": 12, "prefixReceived": 12, "upDownTime": 1761900000.123456,
                 "underMaintenance": False, "peerState": st}, **extra) for a, st, extra in peers}}}})


put("T-EOS-A", "show ip bgp summary vrf all | json", eos_bgp([("10.201.0.0", "Established", {}), ("10.201.0.2", "Established", {})]))
put("T-EOS-B", "show ip bgp summary vrf all | json", eos_bgp([("10.201.0.4", "Established", {}), ("10.201.0.6", "Active", {}),
                                                         ("10.201.0.8", "Idle", {"peerStateIdleReason": "AdminShutdown"})]))
put("T-EOS-A", "show bgp evpn summary | json", eos_bgp([("10.250.0.1", "Established", {}), ("10.250.0.2", "Established", {})]))
put("T-EOS-B", "show bgp evpn summary | json", eos_bgp([("10.250.0.1", "Connect", {}), ("10.250.0.2", "OpenSent", {})]))
EVER = """Arista DCS-7280DR3A-36-F
Hardware version: 11.01
Serial number: JPE21120001
Hardware MAC address: 2899.3a00.0001
System MAC address: 2899.3a00.0001

Software image version: {v}
Architecture: x86_64
Internal build version: {v}-38936741.4321F
Internal build ID: 3a9b0f1e-7f2c-4d7a-9a3a-1d2e3f4a5b6c
Image format version: 3.0
Image optimization: Strata-4GB

Uptime: 1 week, 2 days, 3 hours and 4 minutes
Total memory: 32556392 kB
Free memory: 25163720 kB
"""
put("T-EOS-A", "show version", EVER.format(v="4.33.2F"))
put("T-EOS-B", "show version", EVER.format(v="4.32.1F"))
put("T-EOS-A", "show priority-flow-control", """The hardware supports PFC on priorities 0 1 2 3 4 5 6 7
The hardware supports PFC watchdog
PFC receive processing : Enabled
Global PFC : Enabled

E: PFC Enabled, D: PFC Disabled, A: PFC Active, W: PFC Watchdog Enabled
Port        Status  Priorities  Action  Timeout  Recovery           Polling
                                                 Interval/Mode      Config/Oper
----------- ------- ----------- ------- -------- ------------------ -------------
Et1/1       E A W   3           drop    0.2      0.4/auto           0.05/0.05
Et2/1       E A W   3           drop    0.2      0.4/auto           0.05/0.05
""")
put("T-EOS-B", "show priority-flow-control", """The hardware supports PFC on priorities 0 1 2 3 4 5 6 7
PFC receive processing : Disabled
Global PFC : Disabled
""")
put("T-EOS-A", "show lldp neighbors | json", J({"tablesLastChangeTime": 1761900000.5, "tablesAgeOuts": 0, "tablesInserts": 4, "tablesDeletes": 0,
                                               "tablesDrops": 0, "lldpNeighbors": [{"port": "Ethernet1/1", "neighborDevice": "T-EOS-SP1", "neighborPort": "Ethernet49/1", "ttl": 120}]}))
ECFG = """! Command: show running-config
! device: T-EOS-A (DCS-7280DR3A-36, EOS-4.33.2F)
!
! boot system flash:/EOS-4.33.2F.swi
!
no aaa root
username netadmin privilege 15 role network-admin secret sha512 $6$saltsalt$hash
!
hostname T-EOS-A
!
interface Ethernet1/1
   mtu 9214
   priority-flow-control on
   priority-flow-control priority 3 no-drop
   qos trust dscp
!
qos map dscp 26 to traffic-class 3
!
router bgp 4200001001
   neighbor 10.201.0.0 remote-as 4200002000
   neighbor 10.201.0.0 password 7 abcdefgh
!
end
"""
put("T-EOS-A", "show running-config", ECFG)
put("T-EOS-B", "show running-config", ECFG.replace("T-EOS-A", "T-EOS-B").replace("   priority-flow-control priority 3 no-drop\n", ""))
put("T-EOS-A", "show priority-flow-control counters detail | json", J({"interfaceCounters": {"Ethernet1/1": {"rxFrames": 120, "txFrames": 33,
    "priorities": {"3": {"rxFrames": 120, "txFrames": 33}, "0": {"rxFrames": 0, "txFrames": 0}}}}}))
put("T-EOS-A", "show interfaces counters queue detail | json", J({"egressQueueCounters": {"interfaces": {"Ethernet1/1": {"ucastQueues": {"trafficClasses": {
    "TC3": {"enqueuedPackets": 1000, "droppedPackets": 0, "ecnMarkedPackets": 77}, "TC0": {"enqueuedPackets": 5, "droppedPackets": 2}}}}}}}))
put("T-EOS-A", "show priority-flow-control counters watchdog | json", J({"interfaces": {"Ethernet1/1": {"txQueues": {"3": {"stuckCount": 0, "droppedPackets": 0, "recoveryCount": 0}}}}}))

# ------------------------------------------------------------------------------------------------ Cisco NX-OS
def nxos_bgp(neigh, af="IPv4 Unicast"):
    rows = [{"neighborid": a, "neighborversion": 4, "msgrecvd": 31337, "msgsent": 31220, "neighbortableversion": 182, "inq": 0, "outq": 0,
             "neighboras": "4200002000", "time": "1w2d", "state": st, "prefixreceived": 24} for a, st in neigh]
    return J({"TABLE_vrf": {"ROW_vrf": [{"vrf-name-out": "default", "vrf-router-id": "10.0.250.21", "vrf-local-as": "4200001001",
        "TABLE_af": {"ROW_af": {"af-id": 1, "TABLE_saf": {"ROW_saf": {"safi": 1, "af-name": af, "tableversion": 182, "configuredpeers": len(rows),
            "capablepeers": len(rows), "totalnetworks": 24, "totalpaths": 48,
            "TABLE_neighbor": {"ROW_neighbor": rows if len(rows) > 1 else rows[0]}}}}}}]}})


put("T-NXOS-A", "show ip bgp summary vrf all | json", nxos_bgp([("10.202.0.0", "Established"), ("10.202.0.2", "Established")]))
put("T-NXOS-B", "show ip bgp summary vrf all | json", nxos_bgp([("10.202.0.4", "Idle")]))
put("T-NXOS-A", "show bgp l2vpn evpn summary | json", nxos_bgp([("10.250.0.1", "Established")], "L2VPN EVPN"))
put("T-NXOS-B", "show bgp l2vpn evpn summary | json", nxos_bgp([("10.250.0.1", "Established"), ("10.250.0.2", "Shut (Admin)")], "L2VPN EVPN"))
NVER = """Cisco Nexus Operating System (NX-OS) Software
TAC support: http://www.cisco.com/tac
Copyright (C) 2002-2025, Cisco and/or its affiliates.

Software
  BIOS: version 01.12
  NXOS: version {v}
  BIOS compile time:  06/14/2024
  NXOS image file is: bootflash:///nxos64-cs.{v}.bin
  NXOS compile time:  3/20/2025 12:00:00 [03/20/2025 20:21:22]

Hardware
  cisco Nexus9000 C9332D-GX2B Chassis
  Intel(R) Xeon(R) CPU D-1633N @ 2.50GHz with 32737096 kB of memory.
  Processor Board ID FDO24070001
  Device name: T-NXOS
"""
put("T-NXOS-A", "show version", NVER.format(v="10.4(5)"))
put("T-NXOS-B", "show version", NVER.format(v="10.3(6)"))
put("T-NXOS-A", "show interface priority-flow-control", """
slot  1
=======

============================================================
Port               Mode Oper(VL bmap)  RxPPP      TxPPP
============================================================

Ethernet1/1        Auto On  (8)        1022       331
Ethernet1/2        Auto On  (8)        0          0
""")
put("T-NXOS-B", "show interface priority-flow-control", """
============================================================
Port               Mode Oper(VL bmap)  RxPPP      TxPPP
============================================================

Ethernet1/1        Auto Off            0          0
""")
put("T-NXOS-A", "show lldp neighbors detail | json", J({"TABLE_nbor_detail": {"ROW_nbor_detail": [{"chassis_type": "Mac Address", "chassis_id": "b08b.d0aa.0001",
    "l_port_id": "Eth1/1", "port_type": "Interface Name", "port_id": "Ethernet1/49", "ttl": 120, "sys_name": "T-NXOS-SP1", "sys_desc": "Cisco NX-OS"}]},
    "neigh_hdr": "neigh_hdr", "neigh_count": 1}))
NCFG = """!Command: show running-config
!Running configuration last done at: Fri Oct 31 20:00:00 2025
!Time: Fri Oct 31 22:00:00 2025

version 10.4(5) Bios:version 01.12
hostname T-NXOS-A
feature bgp
username admin password 5 $5$abcd$efgh role network-admin
policy-map type network-qos RDMA-NQ
  class type network-qos c-8q-nq3
    mtu 9216
    pause pfc-cos 3
interface Ethernet1/1
  mtu 9216
  priority-flow-control mode on
  priority-flow-control watch-dog-interval on
router bgp 4200001001
  neighbor 10.202.0.0
    remote-as 4200002000
"""
put("T-NXOS-A", "show running-config", NCFG)
put("T-NXOS-B", "show running-config", NCFG.replace("T-NXOS-A", "T-NXOS-B").replace("  priority-flow-control mode on\n", "  priority-flow-control mode off\n"))
put("T-NXOS-A", "show interface priority-flow-control detail | json", J({"TABLE_pfc": {"ROW_pfc": [{"if_name_out": "Ethernet1/1", "pfc_mode": "on",
    "TABLE_pfc_cos": {"ROW_pfc_cos": [{"cos": 3, "rx_pause_pfc3": 1022, "tx_pause_pfc3": 331}]}}]}}))
put("T-NXOS-A", "show queuing interface | json", J({"TABLE_queuing_interface": {"ROW_queuing_interface": [{"if_name_str": "Ethernet1/1",
    "TABLE_qosgrp_egress_stats": {"ROW_qosgrp_egress_stats": [{"eq-qosgrp": "3", "TABLE_qosgrp_egress_stats_entry": {"ROW_qosgrp_egress_stats_entry": [
        {"eq-stat-type": "Tx Pkts", "eq-uc-stat-value": 99887}, {"eq-stat-type": "Dropped Pkts", "eq-uc-stat-value": 0},
        {"eq-stat-type": "ECN", "eq-uc-stat-value": 45}]}}]}}]}}))
put("T-NXOS-A", "show queuing pfc-queue detail | json", J({"TABLE_pfc_queue": {"ROW_pfc_queue": [{"if_name": "Ethernet1/1", "qos_grp": 3,
    "pfc_watchdog_shutdown_count": 0, "pfc_watchdog_restore_count": 0, "pfc_watchdog_enable": "yes"}]}}))

# ------------------------------------------------------------------------------------------------ Cumulus (NVUE)
put("T-CUM-A", "nv show vrf -o json", J({"default": {"evpn": {"enable": "on"}, "router": {"bgp": {"enable": "on"}}}, "mgmt": {"table": "1001"}}))
put("T-CUM-A", "nv show vrf default router bgp neighbor -o json", J({
    "swp51": {"peer-group": "underlay", "remote-as": "external", "state": "established", "type": "unnumbered", "uptime": 812345},
    "swp52": {"peer-group": "underlay", "remote-as": "external", "state": "established", "type": "unnumbered", "uptime": 812345}}))
put("T-CUM-A", "nv show vrf mgmt router bgp neighbor -o json", "Error: The requested item does not exist.")
put("T-CUM-B", "nv show vrf -o json", J({"default": {}, "TENANT-01": {"table": "1002"}, "mgmt": {}}))
put("T-CUM-B", "nv show vrf default router bgp neighbor -o json", J({
    "swp51": {"peer-group": "underlay", "remote-as": "external", "state": "established", "type": "unnumbered"},
    "swp52": {"peer-group": "underlay", "remote-as": "external", "state": "idle", "type": "unnumbered"},
    "swp53": {"remote-as": "external", "state": "established", "type": "unnumbered"}}))
put("T-CUM-B", "nv show vrf TENANT-01 router bgp neighbor -o json", J({"10.240.0.1": {"remote-as": "65010", "state": "active", "type": "numbered"}}))
put("T-CUM-B", "nv show vrf mgmt router bgp neighbor -o json", "Error: The requested item does not exist.")
put("T-CUM-A", "cat /etc/lsb-release", "DISTRIB_ID=\"Cumulus Linux\"\nDISTRIB_RELEASE=5.18.3\nDISTRIB_DESCRIPTION=\"Cumulus Linux 5.18.3\"\n")
put("T-CUM-B", "cat /etc/lsb-release", "DISTRIB_ID=\"Cumulus Linux\"\nDISTRIB_RELEASE=5.11.1\nDISTRIB_DESCRIPTION=\"Cumulus Linux 5.11.1\"\n")
put("T-CUM-A", "nv show qos roce -o json", J({"enable": "on", "mode": "lossless", "pfc": {"pfc-priority": "3", "switch-priority": 3},
                                           "congestion-control": {"congestion-mode": "ecn", "min-threshold": "150.00 KB", "max-threshold": "1500.00 KB", "probability": 100}}))
put("T-CUM-B", "nv show qos roce -o json", J({"enable": "on", "mode": "lossy"}))


def cum_lldp(links):
    return J({p: {"lldp": {"neighbor": {nm: {"chassis": {"chassis-id": "48:b0:2d:00:00:01", "system-name": nm, "system-description": "Cumulus Linux version 5.18.3",
                                                      "management-address-ipv4": "192.168.0.11"},
                                         "port": {"name": pp, "description": pp, "ttl": 120, "type": "ifname"}}}}} for p, nm, pp in links})


put("T-CUM-A", "nv show interface lldp -o json", cum_lldp([("swp51", "T-CUM-SP1", "swp1"), ("swp52", "T-CUM-SP2", "swp1"), ("eth0", "oob-mgmt", "swp7")]))
put("T-CUM-B", "nv show interface lldp -o json", cum_lldp([("swp51", "T-CUM-SP2", "swp2"), ("swp53", "cumulus", "swp9")]))
CUM_PLAN = """nv set system hostname {h}
nv set interface swp51 link mtu 9216   # -> T-CUM-SP1 swp1
nv set interface swp52 link mtu 9216   # -> T-CUM-SP2 swp1
nv set interface swp53 link mtu 9216   # -> T-CUM-SP3 swp1
nv set interface swp54 link mtu 9216   # -> T-CUM-SP4 swp1
nv set vrf default router bgp neighbor swp51 remote-as external   # T-CUM-SP1
nv set vrf default router bgp neighbor swp52 remote-as external   # T-CUM-SP2
nv set vrf default router bgp neighbor swp53 remote-as external   # T-CUM-SP3
nv set qos roce mode lossless
nv set interface swp51-54 qos pfc-watchdog
nv set qos pfc-watchdog polling-interval 100
nv set qos traffic-pool roce-lossless switch-priority 3
nv set qos egress-scheduler default-global traffic-class 0,3 mode dwrr
nv set system aaa user netadmin password '$nvsec$abc'
nv config apply -y
"""
put("T-CUM-A", "nv config show -o commands", CUM_PLAN.format(h="T-CUM-A").replace("nv config apply -y\n", "") +
    "nv set system message pre-login 'added on the device'\nnv set system aaa role nvue-admin class nvapply\nnv set interface swp51 type swp\n")
put("T-CUM-B", "nv config show -o commands", "nv set system hostname T-CUM-B\nnv set interface swp51-52 link mtu 9216\nnv set qos roce mode lossless\n")
for i in ("swp51", "swp52", "swp53", "swp54"):
    put("T-CUM-A", "nv show interface %s counters qos -o json" % i, J({"egress-queue-stats": {"3": {"tx-frames": 1200, "tx-bytes": 99000, "ecn-marked-packets": 3 if i == "swp51" else 0,
        "uc-buffer-discards": 0}, "0": {"tx-frames": 10, "uc-buffer-discards": 5}}, "pfc-stats": {"3": {"rx-pause-frames": 2, "tx-pause-frames": 1, "rx-pause-duration": 120}},
        "ingress-buffer-stats": {"priority-group-3": {"discards": 0}}}))
put("T-CUM-A", "nv show qos pfc-watchdog -o json", J({"polling-interval": "100", "robustness": 3, "state": "enabled", "swp51": {"storm-detected": 0, "storm-restored": 0}}))

# ------------------------------------------------------------------------------------------------ Nokia SR Linux
def srl_bgp(peers):
    return J({"network-instance": [{"name": "default", "protocols": {"srl_nokia-bgp:bgp": {"neighbor": [
        {"peer-address": a, "session-state": st} for a, st in peers]}}}, {"name": "mgmt"}]})


put("T-SRL-A", "info from state network-instance * protocols bgp neighbor * session-state | as json", srl_bgp([("10.203.0.0", "established"), ("10.203.0.2", "established")]))
put("T-SRL-B", "info from state network-instance * protocols bgp neighbor * session-state | as json", srl_bgp([("10.203.0.4", "established"), ("10.203.0.6", "connect")]))
put("T-SRL-A", "info from state system information version", "    system {\n        information {\n            version v24.10.1-492-g7a1b2c3d4e\n        }\n    }")
put("T-SRL-B", "info from state system information version", "    system {\n        information {\n            version v23.10.3-21-g0000000000\n        }\n    }")
put("T-SRL-A", "info flat qos", "set / qos classifiers dscp-policy dscp-default dscp 26 forwarding-class fc3\nset / qos output-queues ... pfc-enable true\n"
                               "set / qos interfaces interface ethernet-1/1 output pfc pfc-enable true\n")
put("T-SRL-B", "info flat qos", "set / qos classifiers dscp-policy dscp-default dscp 26 forwarding-class fc3\n")
put("T-SRL-A", "info from state system lldp interface * neighbor * | as json", J({"system": {"lldp": {"interface": [{"name": "ethernet-1/1",
    "neighbor": [{"id": "1A:2B:3C:00:00:01", "system-name": "T-SRL-SP1", "port-id": "ethernet-1/49", "port-description": "to leaf"}]}]}}}))
put("T-SRL-A", "info flat /", "set / system name host-name T-SRL-A\nset / interface ethernet-1/1 mtu 9232\n"
                             "set / qos interfaces interface ethernet-1/1 output pfc pfc-enable true\nset / system aaa authentication user admin password $y$j9T$abc\n")
put("T-SRL-B", "info flat /", "set / system name host-name T-SRL-B\nset / interface ethernet-1/1 mtu 1500\n")

# ------------------------------------------------------------------------------------------------ Nokia SR OS (text)
SROS_HDR = """===============================================================================
BGP Router ID:10.0.250.31      AS:4200009000  Local AS:4200009000
===============================================================================
BGP Admin State         : Up          BGP Oper State              : Up
Total Peer Groups       : 2           Total Peers                 : 3
Total VPN Peer Groups   : 0           Total VPN Peers             : 0
Current Internal Groups : 2           Max Internal Groups         : 2
Total BGP Paths         : 1702338     Total Path Memory           : 220303776

===============================================================================
BGP Summary
===============================================================================
Legend : D - Dynamic Neighbor
===============================================================================
Neighbor
Description
                   AS PktRcvd InQ  Up/Down   State|Rcv/Act/Sent (Addr Family)
                      PktSent OutQ
-------------------------------------------------------------------------------
"""
put("T-SROS-A", "show router bgp summary all", SROS_HDR + """192.0.2.1
                64496  1834512    0 05d02h11m 950123/950012/12 (IPv4)
                         4521    0
192.0.2.5
                64497  1834500    0 05d02h11m 950120/1/12 (IPv4)
                         4519    0
2001:db8::1
                64496  1834512    0 05d02h11m 210300/210111/4 (IPv6)
                         4521    0
-------------------------------------------------------------------------------
""")
put("T-SROS-B", "show router bgp summary all", SROS_HDR + """192.0.2.9
Lumen transit (pending circuit)
                64496        0    0 00h00m00s Active
                            0    0
192.0.2.13
                64497        0    0 01h12m03s Connect
                            0    0
192.0.2.17
                64498        0    0 00h00m09s Idle
                            0    0
-------------------------------------------------------------------------------
""")
put("T-SROS-A", "show router bgp summary family evpn", SROS_HDR + """10.250.0.1
                4200009000  9182    0 05d02h11m 120/120/80 (Evpn)
                         9001    0
""")
put("T-SROS-B", "show router bgp summary family evpn", SROS_HDR + "-------------------------------------------------------------------------------\nNo Matching Entries Found\n")
put("T-SROS-A", "show version", "TiMOS-C-26.7.R1 cpm/x86_64 Nokia 7750 SR Copyright (c) 2000-2026 Nokia.\nAll rights reserved. All use subject to applicable license agreements.\nBuilt on Thu Jul 16 12:00:00 PDT 2026 by builder in /builds/c/267B/R1/panos/main/sros\n")
put("T-SROS-B", "show version", "TiMOS-C-25.10.R2 cpm/x86_64 Nokia 7750 SR Copyright (c) 2000-2025 Nokia.\n")
put("T-SROS-A", "admin show configuration /configure flat", "/configure system name \"T-SROS-A\"\n/configure port 1/1/c1/1 ethernet mtu 9212\n/configure router \"Base\" bgp neighbor \"192.0.2.1\" authentication-key \"abc\" hash2\n")

# ------------------------------------------------------------------------------------------------ Cisco IOS XR (text)
put("T-XR-A", "show bgp vrf all summary", """Fri Oct 31 22:01:02.345 UTC

VRF: default
------------
BGP VRF default, state: Active
BGP Route Distinguisher: 10.0.250.41:0
VRF ID: 0x60000000
BGP router identifier 10.0.250.41, local AS number 4200009100
Non-stop routing is enabled
BGP table state: Active
Table ID: 0xe0000000   RD version: 1203
BGP main routing table version 1203
BGP NSR Initial initsync version 11 (Reached)
BGP NSR/ISSU Sync-Group versions 0/0

BGP is operating in STANDALONE mode.


Process       RcvTblVer   bRIB/RIB   LabelVer  ImportVer  SendTblVer  StandbyVer
Speaker            1203       1203       1203       1203        1203           0

Neighbor        Spk    AS MsgRcvd MsgSent   TblVer  InQ OutQ  Up/Down  St/PfxRcd
10.204.0.0        0 4200002000   43812   43790     1203    0    0     3w0d         24
10.204.0.2        0 4200002000   43811   43790     1203    0    0     3w0d         24
""")
put("T-XR-B", "show bgp vrf all summary", """Fri Oct 31 22:01:02.345 UTC

VRF: default
------------
Neighbor        Spk    AS MsgRcvd MsgSent   TblVer  InQ OutQ  Up/Down  St/PfxRcd
10.204.0.4        0 4200002000   43812   43790     1203    0    0     3w0d         24
10.204.0.6        0 4200002000       0       0        0    0    0 00:00:12 Idle (Admin)
10.204.0.8        0 4200002000       0       0        0    0    0 00:01:40 Active
""")
put("T-XR-A", "show bgp l2vpn evpn summary", "Fri Oct 31 22:01:03.001 UTC\nNeighbor        Spk    AS MsgRcvd MsgSent   TblVer  InQ OutQ  Up/Down  St/PfxRcd\n10.250.0.1        0 4200009100    9921    9900      882    0    0     3w0d        410\n")
put("T-XR-B", "show bgp l2vpn evpn summary", "Fri Oct 31 22:01:03.001 UTC\n% BGP instance 'default' not active\n")
put("T-XR-A", "show version", """Fri Oct 31 22:01:04.000 UTC
Cisco IOS XR Software, Version 24.4.2 LNT
Copyright (c) 2013-2025 by Cisco Systems, Inc.

Build Information:
 Built By     : swtools
 Built On     : Tue Feb 18 08:00:00 UTC 2025
 Version      : 24.4.2
 Location     : /opt/cisco/XR/packages/

cisco 8000 (8201-32FH)
System uptime is 3 weeks, 0 days, 4 hours, 12 minutes
""")
put("T-XR-B", "show version", "Fri Oct 31 22:01:04.000 UTC\nCisco IOS XR Software, Version 7.11.2\n")
put("T-XR-A", "show running-config", "Fri Oct 31 22:01:05.000 UTC\nBuilding configuration...\n!! IOS XR Configuration 24.4.2\nhostname T-XR-A\nusername netadmin\n group root-lr\n secret 10 $6$abc\n!\ninterface FourHundredGigE0/0/0/0\n mtu 9216\n!\nend\n")

# ------------------------------------------------------------------------------------------------ Cisco IOS XE (version, configuration)
put("T-XE-A", "show version", """Cisco IOS XE Software, Version 17.12.04
Cisco IOS Software [Dublin], Catalyst L3 Switch Software (CAT9K_IOSXE), Version 17.12.4, RELEASE SOFTWARE (fc3)
Technical Support: http://www.cisco.com/techsupport
Copyright (c) 1986-2024 by Cisco Systems, Inc.

ROM: IOS-XE ROMMON
BOOTLDR: System Bootstrap, Version 17.12.1r, RELEASE SOFTWARE (P)

T-XE-A uptime is 2 weeks, 1 day, 3 hours, 4 minutes
System image file is "flash:packages.conf"

cisco C9300-48T (X86) processor with 1338490K/6147K bytes of memory.
""")
put("T-XE-B", "show version", "Cisco IOS XE Software, Version 17.09.05\nCisco IOS Software [Cupertino], Version 17.9.5, RELEASE SOFTWARE (fc1)\n")
put("T-XE-A", "show running-config", "Building configuration...\n\nCurrent configuration : 4211 bytes\n!\nversion 17.12\nhostname T-XE-A\nenable secret 9 $9$abc\nusername netadmin privilege 15 secret 9 $9$def\n!\nsystem mtu 9198\nend\n")

# ------------------------------------------------------------------------------------------------ Cisco FTD
put("T-FTD-A", "show bgp summary", """BGP router identifier 10.0.250.51, local AS number 65010
BGP table version is 41, main routing table version 41
24 network entries using 4800 bytes of memory
48 path entries using 3840 bytes of memory
6/4 BGP path/bestpath attribute entries using 1248 bytes of memory
BGP using 10144 total bytes of memory
BGP activity 25/1 prefixes, 50/2 paths, scan interval 60 secs

Neighbor        V           AS MsgRcvd MsgSent   TblVer  InQ OutQ Up/Down  State/PfxRcd
10.205.0.1      4        65020    8812    8810       41    0    0 6d03h          12
10.205.0.5      4        65020    8811    8810       41    0    0 6d03h          12
""")
put("T-FTD-B", "show bgp summary", """BGP router identifier 10.0.250.52, local AS number 65010
Neighbor        V           AS MsgRcvd MsgSent   TblVer  InQ OutQ Up/Down  State/PfxRcd
10.205.0.9      4        65020    8812    8810       41    0    0 6d03h          12
10.205.0.13     4        65020       0       0        1    0    0 never    Active
""")
put("T-FTD-A", "show failover", """Failover On
Failover unit Primary
Failover LAN Interface: FOVER Ethernet1/8 (up)
Reconnect timeout 0:00:00
Unit Poll frequency 1 seconds, holdtime 15 seconds
Interface Poll frequency 5 seconds, holdtime 25 seconds
Interface Policy 1
Monitored Interfaces 4 of 1291 maximum
failover replication http
Version: Ours 7.6.0-1, Mate 7.6.0-1
Last Failover at: 21:05:12 UTC Oct 30 2026
        This host: Primary - Active
                Active time: 90122 (sec)
                slot 0: FPR-3120 hw/sw rev (1.0/7.6.0-1) status (Up Sys)
        Other host: Secondary - Standby Ready
                Active time: 0 (sec)
""")
put("T-FTD-B", "show failover", """Failover On
Failover unit Secondary
        This host: Secondary - Failed
        Other host: Primary - Active
""")

# ------------------------------------------------------------------------------------------------ plan
devs = []


def dev(name, os_, role="test", target=None, planned=None, state=None, ip=None):
    d = {"name": name, "short": name[2:], "os": os_, "role": role, "ip": ip or "127.250.0.%d" % (len(devs) + 1)}
    if target is not None:
        d["release"] = {"target": target}
    if planned:
        d["planned_config"] = "configs/%s.conf" % name
    d["_state"] = state or {"stage": 6, "flag": ""}
    devs.append(d)
    return d


for os_, tgt in (("junos", "25.2X100-D30"), ("eos", "4.33.2F"), ("nxos", "10.4(5)"), ("cumulus", "5.18.3"), ("srl", "24.10.1"),
                 ("sros", "26.7.R1"), ("iosxr", "24.4.2"), ("iosxe", "17.12.04"), ("ftd", "7.6.0")):
    tag = {"junos": "JUNOS", "eos": "EOS", "nxos": "NXOS", "cumulus": "CUM", "srl": "SRL", "sros": "SROS", "iosxr": "XR", "iosxe": "XE", "ftd": "FTD"}[os_]
    for ab in ("A", "B"):
        dev("T-%s-%s" % (tag, ab), os_, "backend leaf", tgt, planned=os_ in ("junos", "eos", "nxos", "cumulus", "srl"))
dev("T-JUNOS-FW1", "junos", "FE firewall (SRX MNHA node 1)", "24.4R2-S3")
dev("T-JUNOS-FW2", "junos", "FE firewall (SRX MNHA node 2)", "24.4R2-S3")
dev("T-JUNOS-CL", "junos", "mgmt firewall (SRX HA primary)")
dev("T-NOREL", "junos", "test")
dev("T-NOIP", "junos", "test", "1.0", ip="")
dev("T-WEKA", "weka", "WEKA storage node", "1.0")
# far ends: SP1 Done, SP2 Done, SP3 quarantined, SP4 not configured
dev("T-CUM-SP1", "cumulus", "backend spine")
dev("T-CUM-SP2", "cumulus", "backend spine")
dev("T-CUM-SP3", "cumulus", "backend spine", state={"stage": 6, "flag": "quarantined"})
dev("T-CUM-SP4", "cumulus", "backend spine", state={"stage": 2, "flag": ""})
devs[[d["name"] for d in devs].index("T-JUNOS-A")]["release"] = {"target": "25.2X100-D30"}

plan = {"site": {"code": "TST", "prov_ip": "127.0.0.1"}, "dhcp": {"subnet_id": 1}, "devices": devs,
        "builds": [{"key": "be", "num": 1, "name": "Test", "waves": [{"name": "all", "devices": [d["name"] for d in devs]}], "gate": [
            {"key": "lossless", "type": "auto", "kind": "lossless", "label": "lossless", "devices": [d["name"] for d in devs if d["os"] != "weka"]}]}],
        "tuning": {"os": "cumulus", "params": [{"key": "ECN_MIN", "label": "ECN min", "base": 150, "min": 50, "max": 1000, "step": 10, "unit": "KB"}],
                   "lines": ["nv set qos congestion-control default-global traffic-class 3 min-threshold {ECN_MIN}KB"]}}
os.makedirs(os.path.join(OUT, "bundle", "configs"), exist_ok=True)
for d in devs:
    if not d.get("planned_config"):
        continue
    n, o = d["name"], d["os"]
    if o == "cumulus":
        txt = CUM_PLAN.format(h=n)
    elif o == "junos":
        txt = JCFG_A.replace("T-JUNOS-A", n)
    elif o == "eos":
        txt = ECFG.replace("T-EOS-A", n)
    elif o == "nxos":
        txt = NCFG.replace("T-NXOS-A", n)
    else:
        txt = "set / interface ethernet-1/1 mtu 9232\nset / qos interfaces interface ethernet-1/1 output pfc pfc-enable true\n"
    open(os.path.join(OUT, "bundle", d["planned_config"]), "w").write(txt)
json.dump(plan, open(os.path.join(OUT, "plan.json"), "w"), indent=1)
for n, cmds in SIM.items():
    os.makedirs(os.path.join(OUT, "sim", n), exist_ok=True)
    for c, t in cmds.items():
        open(os.path.join(OUT, "sim", n, slug(c) + ".txt"), "w").write(t)
# the command index: what each fixture file is
json.dump({n: sorted(cmds) for n, cmds in SIM.items()}, open(os.path.join(OUT, "commands.json"), "w"), indent=1)
print("fixtures: %d devices, %d outputs" % (len(SIM), sum(len(v) for v in SIM.values())))
