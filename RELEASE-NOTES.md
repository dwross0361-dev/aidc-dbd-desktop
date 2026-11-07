# AIDC DBD Desktop 2.0.88 (builder HS-v1.89, site runner 1.11.0)

- **setup.sh asks for the secrets.** On a terminal it creates `/etc/fabric/secrets.env` from the template and asks for each blank value the device files need: passwords twice, hidden, stored as SHA-512 hashes (the NX-OS SHA-256 and SR OS bcrypt forms from the same entry; bcrypt needs htpasswd); entering the read-only password also sets READ_PASSWORD; Enter leaves a value blank. Values are saved as entered. Unattended (`--yes` or no terminal) it stops for hand editing as before.
- **One secrets file per site.** The file records its site; a package for another site asks whether to keep that site's secrets or start new (the old file is kept as `secrets.env.<site>`). Keys new in a package are added to an existing file (blank or with the template default) instead of leaving their tokens unfilled.
- **Clock check:** setup warns when the host clock is not NTP-synchronized.

# AIDC DBD Desktop 2.0.87 (builder HS-v1.88, site runner 1.11.0)

- **SRX4700 interfaces are FPC 1** (Juniper port checker): et-1/<pic>/<port>, was et-0. The MNHA configs (border-leaf legs et-1/0/1-4 in ae1/ae2, the ICL et-1/1/1-2 in ae0, the SRG monitors), the physical model, the Cable Matrix and the faceplate (labels 1/0/0 ... 1/1/13) all use the new names. The legs stay on the QSFP28 ports at 100G; port 0 (400G) is unused, so the 400G pairing rule (port N+1 unused) is never hit.

# AIDC DBD Desktop 2.0.86 (builder HS-v1.87, site runner 1.11.0)

- **SRX4700 faceplate matches the front panel** (Juniper SRX4700 chassis and interfaces pages): per PIC, left to right, port 0 QSFP56-DD (400G) over port 1 (100G), ports 2-5 QSFP28, then 6-9 and 10-13 SFP56 (50G), each block numbered down its columns (0/2 over 0/3, 0/4 over 0/5); PIC 0 then PIC 1; the two 1G SFP HA (CTL) ports; MGMT (fxp0) over CON. Was one row per port type. MGMT and CON now light when cabled.

# AIDC DBD Desktop 2.0.85 (builder HS-v1.86, site runner 1.11.0)

- **Gateway links scale with north-south.** Above 800G the gateway links become LACP LAGs of 400G members: edge router to each gateway N-S / 800 members (one edge router alone carries the whole rate over its two gateway LAGs), border leaf to each gateway (block share) / 800 members once a firewall block carries more than 800G. Generated in every gateway OS (Junos, EOS, NX-OS, SR Linux, SR OS) and border-leaf OS (Junos, EOS, NX-OS, Cumulus, SR Linux): member ports, the bundle, LACP active and fast, the routed /31 and the per-tenant edge subinterfaces on the LAG, and the SR OS edge router's lag-11 / lag-12. One cable per member in the physical model, Cable Matrix and Port Mapping; the FE Render labels each mesh ("each link 2 x 400G LAG"). At or below 800G every output is unchanged.
- **Gateway sizing:** new Gateway links and Gateway capacity checks (north-south plus DCI waves, in and out of one gateway, against the platform's full-duplex line rate), the gateway port check counts the LAG members, and every failing check names the next catalog platform that fits (same OS first). Line rates: 7280DR3A-36 14.4, N9332D-GX2B 12.8, QFX5130-48C 8.0, IXR-X3b and X3 14.4, SR-1x-48D 6.0, SR-1-48D 2.8, SR-1se 19.2, SR-2se 36 Tb/s.
- **Nokia 7750 SR-2se** added for the gateway, edge router and DCI hub roles: 5RU, 2 XMA2-s slots (18 Tb/s FD each), 36 Tb/s FD (38.4 with intelligent aggregation), 36 x 800G / 84 x 400G at line rate, FP5, integrated power shelf, ANYsec/MACsec. Ports 1/1/c1-c24 and 2/1/c1-c24; faceplate of two 24-connector cards (connector count and power to confirm). In the SR OS release list.
- **eOOB console sizing:** the edge-zone console servers now count every border leaf (two per firewall block) and the racked gateway pair; scaled designs ran short ("2 edge-zone console port(s) have no console-server port").
- **Fixes:** the IP plan puts the carrier address on lag-1 when the ISP circuit is a LAG; the N9332D-GX2B throughput is 12.8 Tb/s full duplex (was 25.6, both directions); the SR-1x-48D's MACsec, MPLS and FIB fields were lost inside a comment.

# AIDC DBD Desktop 2.0.84 (builder HS-v1.85, site runner 1.11.0)

- **7250 IXR-6e spine at line rate:** 72 x 800G (18 per IMM slot, 14.4 Tb/s FD each; 2 x 400G breakout gives 144 x 400G), was 144 x 800G (twice what the slots carry). Port names ethernet-<slot>/1-18; the faceplate shows four IMM slots of 18 x 800G QSFP-DD. IMM type to confirm. Designs with an IXR-6e spine get more spines for the same fabric.

# AIDC DBD Desktop 2.0.83 (builder HS-v1.84, site runner 1.11.0)

- **Nokia profiles from the data sheets.** 7750 SR-1se: 3RU with an integrated power shelf (was 4RU, to confirm), 9.6 to 19.2 Tb/s FD, 24 x 800G / 48 x 400G / 192 x 100G; power draw still to confirm (the sheet gives feed ratings only). 7750 SR-1x-48D: 6.0 Tb/s FD at line rate (12 x 400G), 19.2 with intelligent aggregation (was 38.4). 7750 SR-1-48D: 2.8 Tb/s FD at line rate (6 x 400G), 19.2 with intelligent aggregation (was 19.2).
- **Edge router port check counts line-rate 400G ports.** A 1,500G LAG (8 x 400G per router) fails on the SR-1-48D, which runs only 6 x 400G at line rate, and names the SR-1se and SR-1x-48D.
- **Nokia faceplates and catalog matched to Nokia's data sheets and SR Linux hardware overview.** 7220 IXR-H6-64: the faceplate was drawn as a nonexistent "H6-128" (128 x 800G); now 64 x 1.6T OSFP224 (2 x 800G breakout per cage) + 2 x 25G SFP28. 7250 IXR-X4: 1RU (was 2), 32 x 800G QSFP112-DD (OSFP112 variant), 25.6 Tbps. 7250 IXR-6e: 10RU (was 7), 4 IMM slots at 14.4 Tb/s FD each (was "6 line-card slots"). 7220 IXR-H5-64O/-64D, IXR-D5, D2L and D3L faceplates gain their 2 x 10G SFP+ ports; H5 cages named OSFP112 / QSFP112-DD. RU confirmed on the H5, H6, D5, X3b and X3 (the CONFIRMs drop; power draw stays to confirm).

# AIDC DBD Desktop 2.0.82 (builder HS-v1.83, site runner 1.11.0)

- **Internet circuits past 400G: a LAG per ISP.** When N-S Gbps exceeds 400, each ISP's circuit becomes a LAG of 400G members (N-S / 400, rounded up), each ISP still carrying the whole rate alone, landing on that ISP's edge router with one BGP session. Generated on the Nokia edge routers (7750 SR-1SE, SR-1-48D, SR-1X-48D, 7250 IXR-X3): LAG and member ports (1/1/c1 then c7 onward) in the SR OS config, one cable per member in the physical model and Cable Matrix, 400G optics per member in the BoM, the DIA rows per member, the Public IPs panel and the FE Render label ("4 x 400G LAG"). The sizing table adds an Edge router ports check. The NCS-57B1, 7280CR3K and ACX7100 (every 400G port already used by the ISL and gateway links) and the MX304 (no LAG port map) fail the check and name the Nokia edges; their configs carry a NOT SIZED banner. At or below 400G every output is unchanged (configs, links, optics, fingerprints, plan digests).

# AIDC DBD Desktop 2.0.81 (builder HS-v1.82, site runner 1.11.0)

- **FE Render, firewalls:** HA pairs side by side (odd units left, even right), one row per pair, the last row level with the border leaves. The rows above take extra space between the gateways and the border leaves, so the firewalls never reach the tier below. Each border-leaf link runs in its own lane under the leaf row and up a channel into the firewall; no link crosses a box or the Edge PoP line. The block sits right of the widest tier, past the DCI backbone zone when there is one, tucked beside the border leaves when there is not.
- **FE Render, edge rows:** the drawing widens to fit the border-leaf, gateway and edge-router rows (12 border leaves at 6 FTD pairs ran off the left edge).

# AIDC DBD Desktop 2.0.80 (builder HS-v1.81, site runner 1.11.0)

- **Back end group:** more space between Total racks and BE switch.

# AIDC DBD Desktop 2.0.80 (builder HS-v1.81, site runner 1.11.0)

- **Back end group:** more space between "Total racks … racks" and "BE switch (leaf · spine · super-spine)".

# AIDC DBD Desktop 2.0.79 (builder HS-v1.80, site runner 1.11.0)

- **User guide: System requirements** is now the first Getting started section, above Install and update: operating system, memory by site size, CPU, disk, display and graphics. Large sites links to it.

# AIDC DBD Desktop 2.0.78 (builder HS-v1.80, site runner 1.11.0)

- **BE switch (leaf · spine · super-spine)** replaces the "BE platform" label: one switch model covers every back-end tier, and the tooltip says why (same radix and lane speed, 1:1 non-blocking, uniform planes, breakouts, optics and spares).

# AIDC DBD Desktop 2.0.77 (builder HS-v1.79, site runner 1.11.0)

- **Front-end platforms strip:** the storage node count sits with Storage, labeled "nodes per scalable unit (N racks)"; the pair wraps together. The strip lists use the body font.

# AIDC DBD Desktop 2.0.76 (builder HS-v1.78, site runner 1.11.0)

- **FE+EDG cabinets** moved into the Front end group, after N-S Gbps; shown only when the front end is on.

# AIDC DBD Desktop 2.0.75 (builder HS-v1.77, site runner 1.11.0)

- **Front end group:** Ingress Gbps now sits next to Assign tenants, before N-S Gbps (ingress is sized per tenant service).

# AIDC DBD Desktop 2.0.74 (builder HS-v1.76, site runner 1.11.0)

- **Toolbar row 1 by group.** Blue tags, as on row 2: **Site** (Customer, Code, Site code, Region, Site #, Device IDs, Site params, Public IPs), **Back end** (Fabric, Pod size, Total racks, BE platform) and **Front end** (On, Tenants, Assign tenants, N-S Gbps, Ingress Gbps, Cold spares). With the front end off its tag is gray and only On shows. The front-end platforms have their own strip, shown only when the front end is on. The status line ends with the green **Build from rack count**.
- **FE+EDG cabinets** moved from Layout settings to the front-end platforms strip: it applies only with a front end.
- **Renames:** row 2 tags Designer and Builder are now **Design** and **Build**; the Tenants button is **Assign tenants**; Site # in region is **Site #**; Frontend plane is the Front end group's **On**.
- **User guide:** row 1 table, the new names; the toolbar reference lists row 1 by group. Fingerprints, plan digests and parity unchanged.

# AIDC DBD Desktop 2.0.73 (builder HS-v1.75, site runner 1.11.0)

- **Toolbar spacing restored.** Wider gaps between buttons and between the tool groups, full-size buttons and settings. On a wide screen each row fits on one line; at 1920 the Deploy group and the last settings wrap neatly.
- **Build from rack count is green**, like the other action buttons.

# AIDC DBD Desktop 2.0.72 (builder HS-v1.74)

- **Toolbar redesigned.** Row 2 is one line of tools: a blue tag per tool (a label, not a button), then its operations in green. **Designer:** BE Render (was Render), FE Render, Configs (was Config), Infra, Facility, BoM, Release check, Blueprint package (gold). **Builder:** DC Layout (was DC Layout Builder), Physical model, Cable Matrix. **Deploy:** Deploy panel (was Deploy), Site runner, Day-2 SoT, Tuned profiles. On the right in amber: Save, Load and **File ▾** (Input sheet, Equivalence, Share link, Site registry, Reset). The layout settings have their own strip below. Every button keeps its id and behavior; fingerprints and plan digests are unchanged.
- **User guide** updated to the new names and layout; the toolbar reference lists row 2 by tag, the File menu and the Layout settings strip.

# AIDC DBD Desktop 2.0.71 (builder HS-v1.73, site runner 1.11.0)

- **Toolbar grouped by tool.** The second toolbar row is now four labeled groups: **Fabric Designer** (Render, FE Render, Config, Infra, Facility, BoM, Release check, Blueprint package), **DC Layout Builder** (DC Layout Builder, Physical model, Cable Matrix, and the physical settings), **Deployment Console** (Deploy, Site runner, Day-2 SoT, Tuned profiles) and **File** (Input sheet, Equivalence, Save, Load, Share link, Site registry, Reset). Every button keeps its id and behavior; design fingerprints and plan digests are unchanged.
- **Tenants explained.** The Tenants box and button tooltips say what a tenant is, what each gets (VRF and VNI, firewall routing instance, DPU configuration, VIPs and SNAT pool) and the limits (120 per edge block, up to 989).
- **User guide restructured by tool.** New sections: **Fabric Designer** (build, tenants, the button groups), **DC Layout Builder** (views, the settings that drive it, refresh and save) and **Deployment Console** (sign-in and roles, header buttons, device stages and actions, Unclaimed, gates, tuning, backups, hand-over). The navigation panel follows the same groups; How to deploy links into each. The toolbar reference lists row 2 by group.

# AIDC DBD Desktop 2.0.70 (builder HS-v1.72, site runner 1.11.0)

- **User guide: Cable Matrix, steps 0 to 4.** What each step holds and the order inside it, why a large step splits into numbered sheets (4a, 4b: sheet 1, sheet 2, one step and one gate), the columns, lengths and PDF files, and the deployment path: each step's Deployment Console gate opens the next. How to deploy, stage 4, now follows the same order.

# AIDC DBD Desktop 2.0.69 (builder HS-v1.72, site runner 1.11.0)

- **User guide: How to deploy, start to finish.** The whole path in seven stages (Workflows > How to deploy, start to finish), with every command and gate: design freeze, procure, site readiness, install and cable, provision, validate, handover.

# AIDC DBD Desktop 2.0.68 (builder HS-v1.72, site runner 1.11.0)

- **N-S Gbps and Ingress Gbps explained.** Their tooltips now say what to enter and what each sizes:
  - **N-S Gbps:** the busy-hour traffic between the site and the internet through the Edge PoP, in the busier direction. Each of the two diverse circuits must carry it alone (10, 100 or 400G); the edge routers are checked against it; it sets the firewall pairs (one per 1,400G on Junos SRX, per 145G on Cisco FTD). Blank: not sized.
  - **Ingress Gbps:** the busy-hour traffic arriving at the tenants' published services through the F5 BNK ingress nodes. It sizes the nodes N+1 at 200G each (at least 2, at most 4). Blank: two nodes.
- **User guide reference: more context.** A control whose tooltip sits on its label now shows it, so every toolbar control has a description. The input sheet table gains a "What it is" column, taken from the row's control (41 of 71 rows).

# AIDC DBD Desktop 2.0.67 (builder HS-v1.71, site runner 1.11.0)

- **Windows installer.** Each release now comes as `AIDC-DBD-Setup-<version>.exe`: **Only for me** (no administrator rights, in `%LOCALAPPDATA%\Programs\AIDC DBD` by default) or **Anyone who uses this computer** (administrator rights), with desktop and Start menu shortcuts and an entry in Settings > Apps for uninstalling. Running a newer Setup replaces the installed version in place. Still unsigned: SmartScreen asks once (More info, Run anyway). The portable zip stays, for machines where installing is not allowed.
- **User guide: Install and update rewritten** for the installer (install, update, uninstall, the portable zip as the fallback), with a troubleshooting row for the SmartScreen prompt. README Install rewritten to match.
- The builder is unchanged (HS-v1.71).

# AIDC DBD Desktop 2.0.66 (builder HS-v1.71, site runner 1.11.0)

- **User guide, pass 2: the reference, generated from the builder.** Three new sections, read from the builder file the guide ships with, so they cannot drift from it:
  - **Input sheet keys:** all 71 rows, by tab, with the label, the unit and what each takes (the options, the range, the platforms that offer it).
  - **Toolbar controls:** the 67 controls on the two toolbar rows, what each does (its tooltip or its input sheet row) and its input sheet key.
  - **Platforms:** the compute platforms (GPUs and kW per rack, cooling, planes, north-south DPU), the back-end switches (rack units, cooling, ports, logical ports, notes) and the front-end, edge, border, gateway and hub switches.
  - `tools/gen-help-ref.js` writes them into the guide; `tools/help-ref-check.js` fails a release whose guide differs from its builder (run with the neutrality gate). The builder gives the data through `__aidcRunner.helpRef()`.
- **Tooltips** on the toolbar buttons that had none: Build from rack count, Render, FE Render, BoM, Config, Infra, Deploy, Save, Load, Reset.

# AIDC DBD Desktop 2.0.65 (builder HS-v1.70, site runner 1.11.0)

- **User guide, pass 1.** **Help > User guide** (or **F1**) opens the guide in its own window, offline. It covers getting started (install and update, a customer project, the input sheet, the build, the health verdict), the workflows (Release check and the delivery build, equivalence and the Baseline, DC Layout Builder, large sites, Deploy, Reset, tuned profiles, a dry run), troubleshooting by the message you see, and terms. A search box filters and highlights the sections.
  - A **?** beside the Fabric health panel, the input sheet report and the Deploy panel opens the matching section. In a browser, the same **?** opens `aidc-dbd-help.html` beside the builder.
  - The guide is `builder/aidc-dbd-help.html`, shipped beside the builder file and read by the neutrality gate. Pass 2 adds a reference generated from the builder (input sheet keys, controls, platforms).

# AIDC DBD Desktop 2.0.64 (builder HS-v1.69, site runner 1.11.0)

- **SN6600-LD faceplate: the "BMC · OOB" label is centered over its two ports.**

# AIDC DBD Desktop 2.0.63 (builder HS-v1.68, site runner 1.11.0)

- **SN6600-LD faceplate: BMC and OOB positions confirmed.** The top RJ45 pair, BMC on the left and OOB (eth0) on the right, is confirmed from the NVIDIA and router-switch.com data sheets, so the label reads "BMC · OOB" without CONFIRM. Still to confirm on the SN6600-LD: the cage numbering. The SN6600, SN6810-LD and SN6800-LD keep their BMC and OOB CONFIRM, since their positions come from other panels.

# AIDC DBD Desktop 2.0.62 (builder HS-v1.67, site runner 1.11.0)

- **Fix: SN6600 and SN6600-LD faceplates show the breakout the build uses.** The OSFP header was fixed text: "8 x 200G" on the SN6600-LD and "2 x 800G" on the SN6600, whatever the design. It now reads the speed of the links on the switch's cages: 2 x 800G (swpNs0-s1), 4 x 400G (swpNs0-s3) or 8 x 200G (swpNs0-s7), and lists each where a switch carries more than one. With no links it lists the three options. GB300 256 racks on SN6600-LD: 2 planes show 4 x 400G, 4 planes show 8 x 200G.

# AIDC DBD Desktop 2.0.61 (builder HS-v1.66, site runner 1.11.0)

- **Faceplates: auxiliary ports labeled by type.** The QSFP112 and SFP28 ports on the SN6600, SN6600-LD, SN6810-LD and SN6800-LD read "QSFP112 (aux)", "1 x QSFP112", "4 x QSFP112" and "2 x 25G SFP28", without a name to confirm: the data sheets give the port type, not an interface name, and the design does not cable these ports. Their tooltip reads "labeled by type; not cabled by this design". Still to confirm on the SN6600-LD: which top RJ45 is BMC and which OOB, and the cage numbering.

# AIDC DBD Desktop 2.0.60 (builder HS-v1.65, site runner 1.11.0)

- **Saved cards for design-service files.** Past 512 racks the BoM, Infra, Facility, physical model and Day-2 SoT exports are written by the design service straight into the project folder. Each file now gets the same **Saved** card as a download, with **Show in folder**; before, only the status line named them. The builder is unchanged (HS-v1.65).

# AIDC DBD Desktop 2.0.59 (builder HS-v1.65, site runner 1.11.0)

The DC Layout hall outlines after a refresh, and a storage block that scales with the nodes.

- **Fix: hall outlines after the floor columns change (DC Layout Builder).** A refresh that keeps your edits rebuilt the rows without the builder's placement and hall, so the rows were laid out by the page while the outlines stayed where the builder put them. The rows now keep the new model's place and hall, so the rows and the outlines follow the new floor columns. This also applies to a saved large-site layout opened again.
- **Storage block scales with the nodes.** Up to 256 nodes (64 on a standard site) the block is unchanged. Past 256 on a hyperscale or mega site, storage moves to the region's storage range in the unused 10.176.0.0/12, 10.(176+2r).0.0/15 for region r, sized to the nodes: /17 up to 512, /16 up to 1,024, /15 up to 2,048. A /26 per node, as before.
  - Fix: past the /18, the old block ran the third octet past 255 (addresses such as 10.240.395.192). GB300 1,625 racks: 816 WEKA nodes now in 10.176.0.0/16, from 10.176.0.0/26 to 10.176.203.192/26, no duplicates.
  - The ACLs, prefix lists, host routes, NetBox prefixes and the IP plan summary all take the block from one place, so they follow it.
- **Proved before shipping:**
  - Unchanged where storage fits the template: 16-rack fingerprints and the plan digest (759 of 759 configurations identical in the package and the .run).
  - DC Layout: after a 3-to-4-column refresh with edits kept, every row keeps its place and every hall outline encloses its cabinets (before: no row kept its place).
  - `go test ./...` passes. Neutrality gate: 0 hits.

# AIDC DBD Desktop 2.0.58 (builder HS-v1.64, site runner 1.11.0)

Three fixes found in use: the DC Layout hall outlines, Count links in the Fabric health panel, and the BoM and Release check past 512 racks.

- **Fix: hall outlines follow the layout (DC Layout Builder).** Each dashed hall outline was drawn from the builder's floor plan and did not move when the cabinets did, so it could cut through rows. The outline is now the extent of the hall's rows as laid out now, and follows every move. Cable routing between halls and the drop targets use the same outline. Cable lengths are unchanged on a layout nobody has moved (GB300 256 racks: identical).
- **Fix: Count links did not update the health panel.** Building the physical model places the super-spines (pod and spread), which the model's cache key reads, so the key taken before the build never matched again and the OOB links stayed "not counted". The key is now taken after the build; the model itself is the same. GB300 1,625 racks: the panel fills in OOB worst leaf oversubscription, spine ports and SSP ports.
- **Fix: BoM and Release check past 512 racks opened nothing when the canvas was out of date.** The design service builds the design from its settings. A canvas built before a setting changed (or with hand edits) differs, so the service refused and only the status line said so. A prompt now explains it and offers **Rebuild and continue**, which rebuilds the canvas from its settings and opens the view. The same applies to Infra, Facility, Count links and the physical model and Day-2 SoT exports.
- **Proved before shipping:**
  - Design fingerprints and configurations unchanged: the builder changes are in the layout page, the physical-model cache key and the design-service prompt.
  - GB300 256 racks: every hall outline encloses its cabinets, with the builder's row positions and with rows laid out by the page; run lengths identical to HS-v1.63.
  - GB300 1,625 racks (SN6600-LD, 2 planes): Count links fills the panel; through the design service the BoM opens and exports to Excel and PDF, and the Release check opens.
  - The prompt: a refused view rebuilds the canvas and opens on the second request.
  - `go test ./...` passes. Neutrality gate: 0 hits.

# AIDC DBD Desktop 2.0.57 (builder HS-v1.63, site runner 1.11.0)

Storage sized by the scalable unit, so it no longer depends on the back-end switch. Switch BMC drops for the NVIDIA SN6000 switches. SN6600-LD faceplate redrawn. File > New project folder, an input sheet load meter, a Reset that starts a new design, and a fix for Deploy freezing on large designs.

- **Storage by scalable unit.** On a rack-scale platform storage is now nodes per scalable unit, the platform's unit (NVL72: 8 racks). It was nodes per back-end pod, and above 64 ports the back-end pod is fit to the switch, so storage followed the switch: at 364 Vera Rubin racks, 184 WEKA nodes on SN6600-LD and 12 on SN6800-LD. Both now give 184 nodes in 92 chassis (8 in 4 at 16 racks).
  - Host platforms (HGX, Supermicro) keep the back-end pod, as before.
  - A back-end pod that holds several units places each unit's nodes in its own row's end-of-row cabinets, within the U and kW limits.
  - The control and the input sheet row read "Storage nodes per scalable unit (N racks)". The key stays `fe.storage_nodes_per_pod`, so saved designs and sheets load unchanged.
- **Other counts that followed the back-end pod** (364 racks, SN6600-LD 4 planes and SN6800-LD 8 planes, before and after):
  | 364 racks | SN6600-LD 4 planes | SN6800-LD 8 planes |
  |---|---|---|
  | Storage nodes (WEKA) | 184, unchanged | 12 to 184 |
  | Front-end pods (spine rule) | 46, unchanged | 3 to 46 |
  | CDU and leak-detection sets | 46, unchanged | 3 to 46 |
  | Console servers (zoned by row) | 145, unchanged | 40 to 54 |
  | Switch BMC drops | 0 to 3,520 | 0 to 416 |
  | OOB access switches | 187 to 234 | 35 to 52 |
  | OOB spine switches | 28 to 30 | 26, unchanged |

  Front-end spines (272) and leaves (274) are unchanged on both. GB300 256 racks SN5610: 128 storage nodes and no BMC drops (the SN5610 has no BMC port).
- **Switch BMC drops (Owner decision, option A).** A switch is cabled where its vendor's manual documents a BMC port. The NVIDIA SN6000 manual lists one on the SN6600, SN6600-LD, SN6810-LD and SN6800-LD.
  - Each such switch, back end or front end, gets a BMC drop to an OOB access switch and an MGMT-COMPUTE address after the existing BMCs, so no existing address moves.
  - The SN6810-LD and SN6800-LD have two OOB ports: eth0 is cabled, the second is a spare.
  - The drops are in `cabling-expected.csv`, the BoM copper count, the OOB access sizing and port bands, DNS and the physical model. BMCs get no DHCP or Kea entry, as before for host and storage BMCs.
  - The SN5600 and SN5610 have no BMC port (SN5000 manual). Other vendors' switches are not cabled.
  - **Default designs change.** The SN6600-LD and SN6600 designs gain a BMC drop per back-end switch and with it OOB access switches. The OOB fabric loopbacks and links renumber, the console server and OOB switch files change, and the SNMP location of some back-end switches moves one cabinet. Data-plane configuration is unchanged.
- **Faceplates (DC Layout).** The SN6600-LD is drawn as the unit: four rows of 16 OSFP cages in two blocks of eight, the port LEDs between rows 2 and 3, and a management column (BMC and OOB RJ45, USB-A, console RJ45, status LEDs and reset, QSFP112 at the bottom right). Cage numbering follows the faceplate LED legend, swp1-4 down the first column; the manual does not state it, so CONFIRM. BMC added on the SN6600, SN6810-LD and SN6800-LD, console on the SN5600 and SN5610. Interface names in configurations are unchanged.
- **OPNs and platform facts in the BoM notes:** SN6600-LD 920-9N62F-00LI-GC1, SN6810-LD 920-9N64K-00LI-GC0, SN6800-LD 920-9N67K-00LA-GC0, with CPU, memory, storage, power, coolant flow, weight and rail kits. All are in the NVIDIA SN6000 manual and the router-switch.com datasheet (reseller). NVIDIA's figures are used over the PNY listing.
- **File > New project folder... (app).** Creates a new or empty folder for one customer, named from the open design's build ID and the date (for example `ACME-SITE-18K-001-20261105`), under `Documents\AIDC DBD Projects`. It refuses a folder that already holds files, makes the new folder the active project and offers to load the input sheet.
  - **Choose project folder...** is renamed **Open existing project folder...**, and **Open project folder** is renamed **Show project folder in File Explorer**. A grayed line under them names the active project.
- **Input sheet load meter.** Loading a sheet shows the stepped meter that opening a design uses: reading the file, reading the sheet (N rows), applying the settings, building N racks, drawing, checking the result, each with its time. If a step fails, the status line names the step and the open design is put back.
- **Input sheet report: values that match are no longer flagged.**
  - A model named without its vendor (`SN5610`, `SN-5610`, `NVIDIA SN5610`) is applied as `NV-SN5610`, with the note "read as NV-SN5610", when it names one option only. This applies to every switch, NIC and DPU row. A partial name such as `5610` is still "nearest supported".
  - `fe.ns_dpu` and `fabric.cooling` on a platform with one DPU or one cooling: a matching value is "derived" ("matches the builder: the GB300 NVL72 platform builds with BlueField-3 B3240"). A different value is "not supported", with the value the build uses. The rows read "North-south DPU" and "Cooling", since they now apply to every platform.
  - `fabric.nic` names the NIC on the HGX B300 (ConnectX-8). The HGX B300 4U and the Supermicro profile do not name theirs, so the row still reads "no such choice" there.
- **Fix: Reset now starts a new design.** Reset cleared only the customer name and code and the canvas. Every control, the locked build ID, Device IDs, Public IPs, tenants, site parameters, imported values and the per-platform choices stayed, and the browser's saved copy brought them back on the next launch.
  - Reset now asks first (**Save first**, **Reset**, **Cancel**), then puts every control and design record back to the state of a fresh launch, clears the page's input sheet and Baseline records, and drops the saved copy.
  - Kept: the tuned-profile library, so measured tuning still applies to later designs (a design that pinned a profile goes back to the library's match), and the app's project folder.
- **Fix: Deploy froze the window on large designs.** The ZTP bootstrap and DHCP server tabs ran outside the builder's working cache, so every device recomputed the out-of-band plan. GB300 256 racks: ZTP bootstrap 291 s to 0.3 s, DHCP server 95 s to 0.4 s. The text is unchanged. The preview stops at 2 MB with a note; Copy and Download shown give the whole file. FE Render runs in the same cache (about 10 s to 7 s at 256 racks).
- **Proved before shipping:**
  - Design fingerprints unchanged: GB300 256 SN5610 (b98ba38d0e5f6230), Vera Rubin 364 SN6800-LD 8 planes (10407090a45b80e9), SN6600-LD 4 planes (190bb91cd2582856). Full parity on all three.
  - GB300 256: every configuration identical to 2.0.56; the other text changes are timestamps and the builder SHA-256.
  - Vera Rubin 364 SN6600-LD: back-end configuration changes are the SNMP location only; no front-end change.
  - Nine 16-rack designs: health verdict Errors (0), 0 cabling-BoM mismatches, equivalence self-test 0 outside tolerance.
  - Old input sheets, including the HS-v1.61 template, load to the same fingerprints as in 2.0.56.
  - Reset: after an import and edits, every control, the saved design and the fingerprint equal a fresh launch's.
  - Deploy: the text of every tab is byte-identical to 2.0.56 on a 16-rack design.
  - `go test ./...` passes; Python against Go difftest: 129 comparisons, 0 mismatches. Neutrality gate: 0 hits.

# AIDC DBD Desktop 2.0.56 (builder HS-v1.62, site runner 1.11.0)

Evaluation readiness Phase 2: the equivalence report against a customer Baseline, and one health verdict. Two fixes found by the new checks.

- **Baseline template and parser.** File > **Baseline template (blank)** or **Baseline filled from this design** saves `AIDC-DBD-Baseline-v1.xlsx`. The tabs are Topology, BoM device counts, Cable summary and Platform specs, 122 keys, generated from the same catalog the report uses. **Load Baseline...** reads the `.xlsx` or a CSV `key,value,unit,source`, and lists unknown keys, bad values and duplicates by row.
- **Equivalence report.** One row per metric: Baseline, Product, Delta (Delta %), within tolerance, class and reason.
  - The Product side is read from the BoM's own rows, so the report and the BoM agree.
  - Tolerance: topology parameters and models must match exactly. Device, cable and transceiver counts may differ by floor(2%) of the Baseline value, per role and in total.
  - Proposed classes: Design choice, Error (Product), Error (Baseline) and Differs from NVIDIA public reference. The NVIDIA rules come in Phase 2b.
  - Design choices come from the nearest-supported rows of the session input file (or the last import), from inputs that differ from the Baseline, and from parameters the builder has no option for, such as back-end oversubscription. Error (Baseline) comes from arithmetic checks on the Baseline itself, such as GPUs against trays and leaves against planes and rails.
  - Any class can be overridden. An override needs a reason before the report is written.
  - **Download report** saves `<build>-equivalence.pdf` and `.csv`, in plain ASCII. The PDF prints the CSV's SHA-256, and the status line gives the SHA-256 of both.
- **Health verdict.** The Fabric health panel and the Release check show Errors (N) / Open inputs (N) / Advisories (N). The warning texts are unchanged.
  - Errors: hard stops, high items tagged integrity (22 of the 37), the new cabling-BoM reconciliation, and switch ports used twice or past the switch.
  - Placeholders count as open inputs, never as errors.
  - A site feed below the facility load ("Facility load exceeds the stated feed") counts as an error. The "N% of the feed" messages stay informational.
  - The panel uses the last BoM; when there is none, it offers **Run it**.
- **Fix: front-end out-of-band drops.** When a pod grew a management switch, every front-end cabinet drop was recorded on the last front-end management switch, so up to 11 devices shared one port in the cable schedule. Each switch now keeps its own drops.
  - Default output changes. GB300 256 racks: the plan digest changes, as do `acceptance/cabling-expected.csv`, `estate.json`, the physical model and the Visio drawing. No configuration file changes.
  - Vera Rubin 364 racks SN6600 4 planes: the same, plus the SNMP location of ACME-OOB-MGT200 and the console server files that name it.
  - Vera Rubin SN6800-LD 8 planes and the 16-rack designs are unchanged.
- **Fix: front end off.** The physical model no longer draws the two firewall HA links between firewalls it did not place.
- **Proved before shipping:**
  - Fingerprints unchanged: GB300 256 SN5610 (b98ba38d0e5f6230), Vera Rubin 364 SN6800-LD 8 planes (10407090a45b80e9), the SN6600 4-plane test design (190bb91cd2582856), and six 16-rack designs. Full parity on all three large designs.
  - Health verdict Errors (0) and 0 reconciliation mismatches on every default design.
  - Self-test: the Product summary used as the Baseline gives 0 differences. Counts moved by 1% stay within tolerance; counts moved by 3% are all flagged.
  - A nearest-supported import (Vera Rubin SN6800 at 2 planes, built at 8) gives Design choice rows only.
  - `go test ./...` passes. Neutrality gate: 0 hits.

# AIDC DBD Desktop 2.0.55 (builder HS-v1.61, site runner 1.11.0)

Bug fix: an input sheet that asks for the air-cooled NV-SN6600 now gets it. Also ASCII apostrophes in package text and the SN6800-LD name.

- **Fix: input sheet with `NV-SN6600`.** The importer applied each value through the saved-design path, which maps the pre-HS-v1.28 name `NV-SN6600` to the liquid-cooled `NV-SN6600-LD`. The sheet then reported "nearest supported" with "the builder set it from the other inputs". Input sheet values are current names and are no longer migrated. This applied to every row that names an SN6600: back-end switch, front-end leaf and front-end spine.
  - The NV-SN6600 is offered on every platform, both NVL72 coolings and every plane count; it is now applied as requested.
  - Saved designs and site-runner files from before HS v1.28 still migrate to the SN6600-LD, as before.
- **Fix: share links.** A link made in this release carries `sk=2` and opens an NV-SN6600 as the NV-SN6600. Links without it (every earlier release) keep the migration, so an air-cooled SN6600 in an earlier link still opens as the SN6600-LD.
- **Input sheet reason for the CPO switches.** On an HGX platform, a request for the SN6800-LD or SN6810-LD says: "NV-SN6800-LD is a co-packaged switch for the NVL72 lane fabrics only; the HGX B300 platform does not offer it".
- **ASCII apostrophes in package text.** Text the builder writes (READMEs, Markdown, CSV, YAML, scripts, DPU NVUE files, `estate.json`) uses a straight apostrophe instead of U+2019. The interface text changes the same way. Exceptions: the regular expressions that read text, the Deployment Console page and the deviation records inside the design.
  - Fingerprints are unchanged. Plan digests change, because `estate.json` labels contain apostrophes. The profiles digest changes, because two Vera Rubin profile texts contain apostrophes.
  - Other non-ASCII characters are unchanged: em dash, middle dot, arrows, multiplication sign, ellipsis. Switch configurations were already ASCII.
- **SN6800-LD and SN6810-LD names.** The back-end list and the input sheet show `NV-SN6800-LD` and `NV-SN6810-LD`. The keys stay `NV-SN6800` and `NV-SN6810`, so saved designs and fingerprints are unchanged. The input sheet accepts either name.
- **SN6800-LD BoM note** (NVIDIA SN6000 hardware manual):
  - 16-core x86 CPU, 96 GB DDR5, 160 GB NVMe SSD, 4 x 256 MB packet buffers;
  - RS-232 console, 1GbE OOB and 1GbE BMC (all RJ45), and USB Type-A;
  - MGX v1.2 rail kit 930-9SRKT-00F0-00C, one per switch, as a note because the BoM lists no rail kits;
  - from a reseller listing only, marked CONFIRM: 4 x QSFP112 ports and a second OOB port.
  - Management cabling is unchanged: one OOB drop and one console port per switch, as for every switch model. No switch model gets a BMC drop.
- **Proved before shipping:**
  - Input sheet `NV-SN6600` on Vera Rubin, 2 planes, Keep plane count: applied, built at 2 planes. `NV-SN6800` at 2 planes: 8 planes, with the lane reason, as before. 330-case switch sweep (every back-end, front-end leaf and spine switch, 6 platforms, both NVL72 coolings, every plane count): 34 requests changed in HS-v1.60, 8 now, all CPO switches on HGX platforms with the specific reason.
  - Pre-HS-v1.28 design file (no `skuRev`) with NV-SN6600: still opens as the NV-SN6600-LD. Share links: without `sk`, SN6600-LD as before; with `sk=2`, SN6600.
  - Same fingerprints and full parity as 2.0.54: GB300 NVL72 256 racks SN5610 (b98ba38d0e5f6230, 11,635 of 11,635), Vera Rubin 364 racks SN6800-LD at 8 planes (10407090a45b80e9, 14,958 of 14,958) and the 4-plane SN6600 test design (190bb91cd2582856, 18,321 of 18,321). That file carries no `skuRev`, so it builds as the SN6600-LD, as in every release since HS v1.28.
    - Every switch configuration is byte-identical.
    - Files that differ from 2.0.54 only in the apostrophe: 4,630 or 6,574 package files, mostly the DPU NVUE and bf.cfg files (6,553 on Vera Rubin), plus the BNK, WEKA and k8s files and the design register. Also 4,656 or 6,600 .run files.
    - Other changes: the plan digest in `estate.json`, README and readiness. SN6800-LD builds also get the BoM note.
  - Input sheet round trip keeps the fingerprint, including an air-cooled SN6600 design (HS-v1.60 changed it to the SN6600-LD). The template's back-end list now shows `NV-SN6800-LD` and `NV-SN6810-LD`.
  - `go test ./...` passes. The engine comparison on a 16-rack Vera Rubin BlueField-4 .run: 129 comparisons, 0 mismatches. Neutrality gate: 0 hits.

# AIDC DBD Desktop 2.0.54 (builder HS-v1.60, site runner 1.11.0)

BlueField-4 Health: one item instead of two.

- **One medium Health item for a BlueField-4 north-south DPU** (Vera Rubin NVL72). It replaces the "software target pending" item and the "reference-architecture assumption" item: "BlueField-4 B4240V north-south DPU (reference-architecture assumption, 2 x 400G per host): DPU configuration files (HBN NVUE, bf.cfg) and the DPU image install step held until a DOCA release names BlueField-4 (hbn/README.md). OPN (B4240V is the RA's part name), physical port count and cage CONFIRM; power not published. Cabling, port map, addressing, front-end leaf configs and the 1GbE OOB drop are built as for BlueField-3."
- The same wording in `hbn/README.md`, the provisioning notes, the BoM line, the device inventory and the deployment wave labels.
- BlueField-3 (default) unchanged: every configuration file byte-identical to 2.0.53, same fingerprints. The Vera Rubin source note is unchanged.
- **Proved before shipping:**
  - BlueField-3 (default), every configuration file byte-identical to 2.0.53, same fingerprints and parity: Vera Rubin 364 racks SN6800 at 8 planes (14,959 files, 10407090a45b80e9) and SN6600 at 4 planes (18,322, 190bb91cd2582856); GB300 NVL72 256 racks SN5610 (11,636, b98ba38d0e5f6230).
  - BlueField-4, Vera Rubin 364 racks SN6600 at 4 planes (418d99e85b269587): one BlueField-4 Health item (medium), no hard stops; every switch configuration byte-identical to 2.0.53; no DPU configuration files; only `hbn/README.md` changed; parity 5,217 of 5,217; neutrality 0 hits on the outputs.
  - `go test ./...` passes; the engine comparison on a 16-rack Vera Rubin BlueField-4 .run: 129 comparisons, 0 mismatches. Input sheet round trip keeps the fingerprint. The input sheet rows are unchanged. Neutrality gate 0 hits.

# AIDC DBD Desktop 2.0.53 (builder HS-v1.59, site runner 1.11.0)

Vera Rubin NVL72 at 2 planes x 800G, NVIDIA's public reference layout (Owner decision D11). Also a spine-sizing fix for the NVL72 lane fabrics, a **Keep plane count** setting and a **BlueField-4** north-south DPU option (r2).

- **Source checked (3 Nov 2026).** NVIDIA's DGX SuperPOD Spectrum-X Reference Architecture with DGX Vera Rubin NVL72 (docs.nvidia.com PDF, no document number, revision or date; no newer public revision found):
  - eight ConnectX-9 per compute tray, 2 x 800G per GPU, one per independent fabric;
  - 2 planes x 4 rails, rail-aligned and non-blocking, "8x 800G connections per compute tray";
  - scalable unit of 3 racks (216 GPUs), with 4 leaves and 2 spines per plane, published to 6,912 GPUs;
  - compute switch SN6600 (128 x 800G);
  - one BlueField-4 B4240V dual-port 400G per tray for north-south.
  - The Spectrum-6 SN6000 hardware manual (last updated 16 Sep 2026) lists SN6800-LD at 2,048 x 200GbE only.
- **Planes 2 for Vera Rubin.** The Vera Rubin profile offers 2, 4 or 8 planes (was 4 or 8). With ConnectX-9 at 2 planes, each GPU runs one 800G port per plane, 1.6 Tb/s per GPU. The default stays 4, so a design without a plane setting builds as before.
  - Host side at 2 planes: one 800G optic per GPU NIC port (two per GPU), not one 1.6T optic split into legs. The BoM and the rack-to-leaf cable plan say so. The host cage form factor is marked CONFIRM.
  - Valid on every back-end platform except the SN6800-LD, which has no 800G lane. There, a 2-plane request builds at 8 planes and says why.
  - Like every plane count, 2 planes is raised when a plane's leaves would exceed one spine's ports and a larger plane count stays on two tiers. Examples: 364 racks on the 1.6T and CPO switches build at 8 or 4. On the 64 x 800G switches no plane count stays on two tiers, so 2 planes is kept and super-spines are added.
- **GB300 NVL72 reviewed:** 2 or 4 planes on ConnectX-8 800G, as NVIDIA's dual-plane reference. No change.
- **Front-end NIC of Vera Rubin:** the source note now cites BlueField-4 B4240V, dual-port 400G. That is the same 2 x 400G the front end is sized for. The default stays BlueField-3 B3240 because the generated DPU software (HBN bundle, bf.cfg) is BlueField-3's. BlueField-4 is now an option (below).
- **Keep plane count (r2, off by default).** A checkbox next to Planes on the NVL72 platforms. On: when the requested plane count does not stay on two tiers on the chosen back-end switch, the builder keeps it and adds super-spines instead of raising the count. A count the switch cannot run at all still falls back and says why, for example 2 or 4 planes of Vera Rubin on the SN6800-LD (200/100G lanes only): "Keep plane count cannot apply: 2 planes are not possible on this switch". Off: unchanged.
  - Checked on all 12 back-end platforms, both NVL72 profiles, every plane count, at 364 racks: on, every requested count is kept except where the switch has no lane at that speed (SN6800-LD: Vera Rubin 2 and 4 planes, GB300 2 planes).
  - Saved in the design, the site-runner file and the fingerprint only when it is on, so a design that does not use it keeps its fingerprint. Input sheet row `fabric.be.keep_planes` (Yes/No).
- **North-south DPU: BlueField-3 or BlueField-4 (r2, Vera Rubin NVL72).** A selector next to the cooling choice; BlueField-3 B3240 stays the default with unchanged output.
  - Sources: NVIDIA BlueField-4 DPU datasheet (doc 5193854, JUN26): 800 Gb/s, 800G per port on 200G SerDes, up to 8 split ports, PCIe Gen6 x16 with Socket Direct, 64-core Grace, integrated DPU BMC, 1GbE OOB, Redfish; no power, OPN, port count, cage or DOCA version stated. NVIDIA's VR NVL72 Spectrum-X RA: one B4240V at 2 x 400G per tray.
  - BlueField-4 builds as 2 x 400G (a 2-way split of the 800G port), so the topology and the front-end sizing do not change. BoM line "NVIDIA BlueField-4 B4240V -- OPN CONFIRM", one per tray. The cable plan, the FEF cable map and the cable matrix name the host end as a 400G leg of the split 800G port, physical port count, cage and assembly CONFIRM. Host PCIe Gen6 x16. Same 1GbE OOB management cabling and MGMT-DPU addressing as BlueField-3.
  - Rack power keeps the current figure; the design register says "BF-4 power not published".
  - **No BlueField-4 DPU configuration files.** No published DOCA release names BlueField-4, so the HBN NVUE files, the bf.cfg files, the DPU image install step and the DPU telemetry job are not generated. `hbn/README.md` says "software target pending", `hbn/dpu-management.csv` is kept, and Health shows a medium item (not an error).
  - Saved only when BlueField-4 is selected. Input sheet row `fe.ns_dpu`.
- **Fix: flat spine count on the lane fabrics.** When the leaves of a plane had different uplink counts (a partly filled pod), the spine count fell back to the largest power of two, which could be short of the plane's uplinks. Leaves were then left without uplinks, with no warning. Now such a plane gets one spine per uplink lane of its fullest leaf. On a 96-rack sweep of 192 builds, 20 were affected in HS-v1.58 (GB300 at 2 planes and Vera Rubin at 4 planes on the 64 x 800G switches with 16-rack pods: 98 or 196 leaves without uplinks), and 0 are affected now. A design affected in HS-v1.58 builds with more spines, so its fingerprint changes.
- **Proved before shipping:**
  - Vera Rubin, 96 racks (6,912 GPUs, NVIDIA's published scale), SN6600 at 2 planes, 16-rack pods: 2 planes x 4 rails at 800G (1.6 Tb/s per GPU), 120 leaves and 58 spines per plane, 356 switches. Every GPU has one leg in each plane; 13,824 host legs and 13,824 uplink lanes. No hard stops or high warnings. Parity 4,262 of 4,262. NVIDIA's Table 3 gives 128 leaves and 64 spines per plane at this size.
  - Vera Rubin, 364 racks, SN6800 at 2 planes: built at 8 planes x 200G, parity 14,958 of 14,958, plan digest 36c4c2a744fa391e, the same as at 4 planes.
  - Unchanged against 2.0.52 (every configuration file byte-identical, same fingerprints): Vera Rubin 364 racks on SN6800 at 4 planes (20297e2cfe6e3716) and at 8 planes; Vera Rubin 364 racks on SN6600 at 4 planes; GB300 NVL72 256 racks on SN5610 (b98ba38d0e5f6230). Across all 12 back-end platforms, the 4- and 8-plane picks of both profiles are unchanged.
  - `go test ./...` passes; the engine comparison on a 16-rack Vera Rubin 2-plane .run shows 129 comparisons, 0 mismatches. Neutrality gate 0 hits.
- **Proved before shipping (r2):**
  - Both new settings at their defaults, every configuration file byte-identical to the first 2.0.53 build, same fingerprints: Vera Rubin 364 racks on SN6800 at 4 planes (14,959 files, 20297e2cfe6e3716) and at 8 planes (14,959, 10407090a45b80e9), on SN6600 at 4 planes (18,322, 190bb91cd2582856); GB300 NVL72 256 racks on SN5610 (11,636, b98ba38d0e5f6230). A 24-rack sweep of all 12 back-end platforms and every plane count of both NVL72 profiles (60 builds), and the four HGX profiles, keep their fingerprints.
  - Keep plane count on, Vera Rubin 364 racks at 2 planes: SN6600 keeps 2 x 800G with 1,104 leaves, 1,104 spines and 552 super-spines in all (parity 17,514 of 17,514); SN5610 keeps 2 x 800G with 1,840 leaves, 2,668 spines and 1,334 super-spines (parity 20,739 of 20,739). Every GPU has one leg per plane, no switch port used twice, no leaf with fewer uplinks than downlinks, no hard stops or high warnings. SN6800-LD falls back to 8 planes with the reason.
  - BlueField-4, Vera Rubin 364 racks on SN6600 at 4 planes: no HBN NVUE or bf.cfg files (13,104 fewer files); every switch configuration byte-identical to the BlueField-3 build; the pending note in `hbn/README.md`; the BoM line (6,552 DPUs) and the cable plan and FEF cable map text present; parity 5,217 of 5,217. Health: no hard stops or high warnings; the pending software target is a medium item.
  - Input sheet round trip keeps the fingerprint with both settings set (Keep plane count Yes, BlueField-4) and at their defaults; the platform profiles digest is unchanged.
  - `go test ./...` passes; the engine comparison on a 16-rack Vera Rubin .run with both settings set shows 129 comparisons, 0 mismatches. Neutrality gate 0 hits on the repository and the BlueField-4 build.

# AIDC DBD Desktop 2.0.52 (builder HS-v1.58, site runner 1.11.0)

Evaluation readiness, Phase 1: the customer input sheet and importer (decisions D1 to D3).

- **Input sheet template (D1).** `AIDC-DBD-Input-Sheet-v1.xlsx` is generated by the builder from its own catalog, so its drop-downs are the options the release offers.
  - Tabs: Read me, Site & footprint, Power, Fabric, Platforms, Front end & storage, Constraints. Each row: Key, Value, Unit, Allowed, Customer source (document, revision), Notes.
  - Covers rack count, customer and site codes, rows and racks per row, row spacing, network-rack position, cabinet height, obstructions, hall MW, rack feed kW (all racks, or per hall), busbar rating, planes, rails per plane, back-end and front-end oversubscription, platform per role, NIC, front end and storage, and free-text constraints.
  - Values outside the lists are allowed with a warning, so the customer can state what they want and see what the builder does with it.
  - The importer also takes a long-format CSV (`key,value,unit,source`).
- **One-step import (D2).** **File > Load input sheet…** in the app, the builder's **Input sheet** button, or `--inputs` in the site runner:
  - checks each value against the live option list or range;
  - sets the controls;
  - builds once;
  - shows the import report and saves the session input file (`aidc-dbd-session-input/1`: the sheet's SHA-256, every row, the controls set, the overlay, the report).
- **The session overlay, `SESSION_IN`.** The values that have no control (compute rack feed kW, busbar rating) are saved with the design. They are therefore in the site-runner file and the design fingerprint, so the runner rebuilds what was imported. An overlay value equal to the platform's default is not stored, so a sheet that restates a design leaves its fingerprint unchanged. The builder file, `PROFILES` and the catalog defaults are never written.
- **Outside the options (D3).** A value outside the offered options is built at the nearest supported one, for example 3 planes as 4, 2:1 back-end oversubscription as 1:1, a 50U cabinet as 52U, or SN5600 as NV-SN5600. The import report gives the row, the requested value, the applied value and the reason, and pre-classifies the row as a design choice for the equivalence report. A value with no usable match keeps the builder's value and is reported as not supported. Rows the builder does not model (obstructions, row spacing, per-hall feeds, constraints, unknown keys) are listed as not modeled, never dropped.
- **Site runner 1.11.0:** `--inputs <sheet>` builds from the sheet. It writes `customer-data/<build>-session-input.json`, `customer-data/<build>-import-report.csv` and `<build>-site-runner.json` next to the package and the .run. `--input-template <file>` writes the template (blank, or filled from `--design`).
- Designs without an input sheet are unchanged: same fingerprint, same files.
- **Proved before shipping:**
  - Round trip: a sheet filled from a design and imported on a fresh page gives the same controls and the same fingerprint. Tested on 16-rack GB300, 24-rack Vera Rubin, 256-rack GB300 NVL72 on SN5610 (b98ba38d0e5f6230, as in 2.0.51) and 364-rack Vera Rubin NVL72 on SN6800 (20297e2cfe6e3716).
  - Contract 5.2: the profiles' SHA-256 is identical at start, after the import and after the build, and the builder file is unchanged by a build.
  - Out of range: a 25-row CSV produces 11 nearest-supported rows, 2 not supported and 6 not modeled. Each has its row, requested value, applied value and reason.
  - Site runner, built from filled sheets against the same settings without the importer:
    - 256-rack GB300 NVL72 (SN5610): parity 11,635 of 11,635. Plan digest 3e234672d4271114 on both. All 11,636 package configuration files are byte-identical, also against the 2.0.51 build.
    - 364-rack Vera Rubin NVL72 (SN6800): parity 14,958 of 14,958. Plan digest 36c4c2a744fa391e on both. All 14,959 configuration files are byte-identical. The sheet asked for 4 planes; the import report shows them built at 8, because SN6800 has no 400G lane.
  - The site-runner file written from an import passes the release and fingerprint gates in a `--design` build (overlay included). An HS-v1.57 builder is refused.
  - `go test ./...` passes; the engine comparison shows 129 comparisons, 0 mismatches. Neutrality gate: 0 hits on the repository, both Test Point output folders and the portable build.

# AIDC DBD Desktop 2.0.51 (builder HS-v1.57, site runner 1.10.0)

Evaluation readiness, Phase 0 (the Owner approved decisions D1 to D15 of the evaluation readiness design).

- **Customer-neutral builder and outputs (D14).** Internal standards document ids, recorded-deviation ids, tenancy and Edge PoP clause ids, and personal attributions are gone from the builder, the pages and files it embeds, the runner and the app, in UI strings, generated files and code comments alike.
  - Standards are named by subject instead (for example "the IP plan", "the front-end standard").
  - Decision sources in the design register and the provenance rows read "Owner decision".
  - The recorded deviations are renumbered DV-01 to DV-04, owner "Owner decision". A design saved by an earlier release loads with the built-in records, not its old copies.
  - The requirement and clause ids of the former standards set (back-end, front-end, OOB, IP plan, naming, cabling, cabinet, interconnect, edge, tenancy, firewall, delivery and emergency-access families) are gone too: dropped where the sentence stands alone, otherwise replaced by the subject.
  - Open-item and question ids are gone from on-screen warnings, the register and generated comments (the sentence stays), and the sample site code in the build-number and device-ID examples is now ACME-LAX.
  - Device configurations change in comment lines only (the clause references in the node network, DCI, rail overlay, border leaf and edge hardening comments). Every configuration statement is unchanged (trailing comments on a statement line may change). Outside the device configurations, the EVE-NG lab's edge-router interconnect description and the lab and BNK READMEs lose their clause references.
- **Neutrality gate.** `tools/neutrality-check.js` fails on any of those patterns, the clause-id families, open-item and question ids, and old sample site codes, in the builder (including its embedded DC Layout Builder page, Deployment Console and provisioning engines), the runner, the app and `provgo`. It is the first step of the GitHub build. Given a job folder, it reads into the package zip, the .run payload, workbooks and PDFs.
- **The release is the builder file (D15).** HS-v1.56 r1, r2 and r3 were three files with one version string, so the runner's release check could not tell them apart. Now:
  - HS-v1.57 is a clean release string.
  - The app reads the builder file's SHA-256 and gives it to the window; the site-runner file carries it (`builderSha256`), and so does the package README (Builder SHA-256 row).
  - The site runner, the design service and the Layout service refuse a design exported from a different builder file, naming both SHA-256 values. A site-runner file without a builder SHA-256 (exported before 2.0.51, or from a builder opened in a browser) is refused by the site runner: export it again from the app, or use `--settings` for a test build.
  - The About box shows the builder's SHA-256.
- **Proved before shipping:**
  - Neutrality gate: 0 hits on the repository and on the unzipped package and .run of a 256-rack GB300 test build (25,867 files). On the 2.0.50 build of the same design it finds 27,761.
  - 256-rack GB300 (SN5610): parity 11,635 of 11,635, plan digest unchanged. Against 2.0.50, 5,347 of 11,636 device configuration files are byte-identical; 6,282 differ in comments only (every statement identical with comments stripped), plus one line of the BNK README.
  - Release gate: a file without a builder SHA-256, a wrong SHA-256, the HS-v1.56 r3 builder and an earlier HS-v1.57 build are refused; a 16-rack design exported from HS-v1.57 builds with its fingerprint matched (parity 775 of 775).
  - `go test ./...` passes; the engine comparison (Phase 1, 129 comparisons) shows 0 mismatches.

# AIDC DBD Desktop 2.0.50 (builder HS-v1.56 r3, site runner 1.9.0)

- **SSH to the switches holds up on slow logins** (both engines, Python and Go). DSX Air TC-208 lost most backups and gate reads to SSH timeouts. Now:
  - At most one SSH session per switch at a time (`ssh_per_device` in estate.conf), across gate checks, backups, RMA reads, tuning, Release DWR access and pushes. Others wait their turn instead of piling onto a slow login.
  - One session serves all the reads of a gate pass on a switch, so a switch logs in once per pass instead of once per check.
  - A banner, prompt, reset or login timeout is retried twice (after 5 s, then 15 s) before it is reported. A wrong password is never retried.
  - A switch with a session open is not probed on port 22 again (it is counted as up).
  - The backups of a pass start 2 s apart instead of in the same second, and each switch's next scheduled backup is offset so they stay spread out.
- **DSX Air lab mode:** `air/ztp-host.sh` now serves lab copies of the slice switches' configurations with the TACACS lines commented out (`air/lab-auth.sh`), so logins use the local accounts at once. Lab only: the package and the .run keep the production configurations byte for byte. Use `--prod-auth` to keep TACACS. The test sheet adds TC-208b, which lets the gate checks run and requires every backup to succeed in both engines.
- **Acceptance scripts follow the design:**
  - `acceptance/nccl.sh` takes the GPUs per host from the GPU profile (4 per NVL72 tray, 8 per HGX host). Before, it assumed 8.
  - `acceptance/lossless.sh` T1 and T2 now test every rail NIC of every host, in every plane. Before, they tested only `mlx5_0`, one rail per host. `ACCEPT_HCA` still picks a single NIC for debugging. A host with a rail missing fails T1.
  - `provision/rail_check.sh` checks each rail at the design's speed (for example 400G legs on dual-plane 800G, 200G on 4-plane NVL72), not a fixed 200G.
- **Proved before shipping:** both engines on real OpenSSH servers set up like Cumulus in Air (slow TACACS-style logins, low MaxStartups), before and after; side-by-side engine comparisons on four designs; device configurations unchanged.

# AIDC DBD Desktop 2.0.49 (builder HS-v1.56 r2, site runner 1.9.0)

- **A run of all no longer stalls on a design with an empty plane** (both engines, Python and Go). A build with no devices, for example the edge and front end of a back-end-only design, is marked complete when the run starts, with one event: "No devices in plane edge: build marked complete". The run goes straight on to the next build.
  - Before, the service hit an error on every tick from then on: Kea (DHCP) updates and pushes stopped, and a push-based back end (Junos) never finished.
  - Starting the back end alone no longer needs an override when only empty builds come before it.
  - A build record with no waves can no longer stop the service's tick.
- **Proved before shipping:** both engines compared side by side on the four designs with an empty plane (provisioning and gate checks), and on B-BE-JUNOS through pushes, RMA, runners, tuning, hand-over and switching engines mid-build. Device configurations are unchanged.

# AIDC DBD Desktop 2.0.48 (builder HS-v1.56, site runner 1.9.0)

- **The .run carries the provisioning service as a Go binary too.** `provision/bin/` holds `aidc-prov` for Linux amd64 and arm64 (static, gzip). `sudo ./setup.sh --engine go` (or `sudo bash <run> --engine go`) installs the one for the host's CPU and runs the service on it.
  - **Python stays the default.** Without `--engine`, setup.sh installs and runs `estate_build.py` as before; a re-run keeps whichever engine is installed.
  - **Same plan, state, console and commands.** Either engine takes over the other's state, so `--engine python` switches back with nothing lost. The console page is byte-identical.
  - **Proved before shipping:** both engines compared side by side on fake devices of all nine platforms: provisioning and gate checks on all 25 regression designs; pushes, RMA, runners, tuning, hand-over and switching engines mid-build on 7 of them; the standby host on one.
  - **DSX Air TC-208** runs the zero-touch test on the Go engine (`sudo bash air/ztp-host.sh --engine go`) to compare with Python.
- **Two fixes to the provisioning service** (both engines):
  - Release DWR access on Junos now commits the account removal. Before, the removal was discarded and the device reported failed.
  - A GPU host that failed its rail check passes on retry once fixed. Before, it kept failing until its log file was deleted.
- The builder file is about 14 MB larger (the two binaries).
- **CONFIRM:** run TC-208 in DSX Air, and the first real pod on the Go engine, before making it the default.

# AIDC DBD Desktop 2.0.47 (builder HS-v1.55, site runner 1.9.0)

- **The light Deploy panel is compact.** It's now a 560-pixel box instead of a full-width panel, with the same button, the same facts in fewer words, and file names that don't break across lines.

# AIDC DBD Desktop 2.0.46 (builder HS-v1.54, site runner 1.9.0)

- **Deploy on a large design opens a light Deploy panel** instead of a yes/no box. Above 512 racks in the app, Deploy shows the same panel without the configuration previews the window can't hold. The panel gives:
  - the build number;
  - what the site runner does (fingerprint, failed-export gate, parity);
  - where the files land, with the command to run the .run on the provisioning host;
  - the **▶ Build installer (.run) — site runner** button. Nothing starts until you click it.
  - Smaller designs and the builder opened in a browser keep the full panel.

# AIDC DBD Desktop 2.0.45 (builder HS-v1.53, site runner 1.9.0)

Redundancy pass. Efficiency: nothing derived or shipped twice without need. Effectiveness: nothing necessary dropped.

- **Cumulus ZTP fetches the switch's configuration instead of carrying a copy of it.** `ztp/<switch>.sh` now:
  1. fetches `configs/.../<switch>.conf` from the provisioning service, which serves it to that switch only and refuses it while a secret is unfilled;
  2. checks the file sets this switch's hostname;
  3. applies it and runs the same LLDP cabling check, reading the planned neighbors from the fetched file.
  - **One copy in the .run.** The .run payload is about 35% smaller (a 1,152-GPU test design goes from 46 to 30 MB), and secrets no longer sit in a second copy under `ztp/`.
  - **The configuration a switch applies is now the one the parity check verifies.** Before, nothing checked the copy inside the script.
  - **Tested on a mock switch** (back-end leaf, OOB switch, border leaf): the identical NVUE command sequence as the old script, and the same check-in. A wrong file or a failed fetch stops ZTP with the reason.
  - **Cabling-check expectations** are identical on all 3,864 Cumulus switches across the 25 designs.
  - The provisioning plan ties the configuration file to its switch, and the service's RMA restore from backup cuts the script at the fetch.
  - **CONFIRM:** prove one Cumulus ZTP on real hardware or in DSX Air before delivery.
- **Each switch configuration is generated once per build.** Choosing the DSX Air test slice used to generate every back-end leaf's configuration just to pick two leaves. It now stops at the first match and finds devices through the index. Generations per .run dropped from two per switch to one, plus about a dozen for the Air slice itself.
- **The DPU tenant scan runs once per build** instead of once for every front-end configuration.
- **Save suggests the build number,** for example `ACME-LAX-295K-001.json`. Without a customer code it falls back to the old name.
- **25-design regression:** parity holds on all 25. The configurations, source of truth and every other file are unchanged, except the new ZTP scripts, the provisioning plan and its design digest, the secrets manifest (no `ztp/` copies) and the OS table text.

# AIDC DBD Desktop 2.0.44 (builder HS-v1.52, site runner 1.9.0)

- **XL .run: no more silent stop.** At 4,096 racks, two deploy-package files grew past JavaScript's longest string (536,870,888 characters): `spectrum-x/topology.json` (about 950 M characters) and `acceptance/cabling-expected.csv` (about 840 M). Building them failed, the failure was swallowed, and the site runner waited with nothing to report.
  - Both files are now written as bytes past their size. A test with the size limit forced low gives byte-identical files, and the .run payload matches 2.0.43.
  - A failed .run build now stops the site runner within seconds, with the cause and where it happened.
  - A .run or deploy package written with a failed section (an `ERROR` file) now stops the site runner with "must not be delivered", as a package with a failed export already does.
- **Site runner watchdog, sharper.**
  - It says when the builder is inside one long native call (no stack yet) instead of going quiet.
  - It logs `still: …` every 10 minutes when the status line hasn't changed.
  - It logs page errors as they happen.
- **The physical model is derived once per site-runner job.** The .run reuses the package's model of the same design, keyed on it. The configurations are still derived separately in each build, so parity still compares two independent derivations.
- **Deploy → installer in the app runs the site runner** (Final package and .run, with the fingerprint, the failed-export gate and parity), relabeled "Build installer (.run) — site runner".
  - "Quick .run (this window, no checks)" stays for labs and test builds, up to 512 racks.
  - On a design too large to open Deploy in the window, the memory guard offers the site runner instead of refusing.
  - The builder opened in a browser is unchanged, and so is the Site runner menu.
- 25-design regression: identical source of truth, configurations and .run payloads; parity holds on all 25.

# AIDC DBD Desktop 2.0.43 (builder HS-v1.51, site runner 1.8.0)

- **XL source of truth in parts.** The 4,096-rack Final package finished every step, then stopped on one file: `sot.json` was over 4 GB.
  - Above 1,000,000 records, a section (interfaces, cables, IP addresses or BGP sessions) is written to `sot-parts/<section>-0001.json` and onward, 1,000,000 records per file.
  - `sot.json` keeps every other section and lists the parts under `"parts"`. The source-of-truth README says which sections were split and how.
  - Below that size, `sot.json` is the same file as before. The 25-design regression shows it unchanged.
- **Loaders read the parts one file at a time.** This covers `nautobot/load_nautobot.py` and `netbox/load_netbox_extras.py`.
  - If a part is missing from beside `sot.json`, the loader stops and names it.
  - The Nautobot Job takes a single upload, so for a split SoT it says to use `load_nautobot.py`.
  - The primary-IP step now makes one pass over the IP addresses instead of one pass per device.
- **ZIP64 for single files over 4 GB.** Every package, source-of-truth and workbook zip now writes such a file instead of refusing it. Below 4 GB the zip bytes are unchanged. Tested with the limit forced low: `unzip -t` and Python's `zipfile` read every member.

# AIDC DBD Desktop 2.0.42 (builder HS-v1.50, site runner 1.8.0)

- **XL Final package: the design viewer no longer stalls past 8.4 million interfaces.** The 2.0.41 watchdog traced the 4,096-rack stall to the interface table in the source of truth. That table was one JavaScript object keyed by device and port. Past 8,388,607 entries the engine renumbers such an object on every insert, so each new interface cost a pass over all the others. ACME-LAX-295K has about 9.5 million interfaces.
  - The table is now 16 sharded Maps. It stays linear past 10 million entries (about 5 s per 2 million) and keeps the insertion order.
  - The interface list is built in one pass instead of a sort, and the design viewer skips it, since it doesn't use it.
  - Every package file is unchanged: the 25-design regression shows identical source of truth, interfaces and configurations.

# AIDC DBD Desktop 2.0.41 (builder HS-v1.49, site runner 1.8.0)

- **Site runner watchdog.** A builder step runs as one long stretch, and the status line can't change until it finishes, so a slow step and a stuck step looked the same. When one step has run for 3 minutes, the runner now logs where the builder is (its call stack) and the memory in use, then again every 3 minutes until the step ends:
  `1500s  watchdog: busy 21 min in one step: viewerHtml:29455 < stepRun:29003 < ...  (21450 MB RSS)`
  This costs nothing measurable: a 1,024-rack design document took 92 s with it, against 95 s without. The lines also go to `site-runner.log`. `--no-watchdog` turns it off.
- **F5 BNK warning, clearer.** The tenancy line now says one INGRESS VRF and one external VLAN **shared by all tenants**, each tenant with its own VIP, SNAT pool and policy. The old wording read as if each tenant had its own VRF.

# AIDC DBD Desktop 2.0.40 (builder HS-v1.48, site runner 1.7.0)

- **Console server configuration.** The Opengear OM2200-L console servers were already sized, placed, cabled, powered and in the BoM; now the package also configures them. They're managed by **Lighthouse**: each unit dials out over its own LTE modem, so nothing reaches it from the internet. Both packages carry a new `console/` folder:
  - **`console/<name>.sh`, one per console server.** Applied as a Lighthouse Script template to that unit, it does four things:
    - labels every serial port with the device cabled to it (from the OOB cable map);
    - sets pinout X2 and the baud rate for each device: 9600 for Junos, EOS, NX-OS and FTD; 115200 for Cumulus and Nokia;
    - leaves spare ports disabled;
    - sets the hostname, NTP, TACACS+ (the same servers and key as the network devices) and the customer's local break-glass account.
  - **`console/console-ports.csv`:** every console server port, the device on it, the device's cabinet and the serial settings.
  - **`console/README.md`:** the site procedure, written for a first Opengear/Lighthouse site. It covers what Lighthouse is, what to have ready (Lighthouse instance, enrollment token, SIMs and APN, secrets), first power-on, enrollment, applying the scripts, SNMP and syslog in Lighthouse, and the proof tests.
- **Secrets.** `secrets.env` adds `CONSOLE_BREAKGLASS_PASSWORD`. The .run renders it and `TACACS_KEY` into the scripts on the provisioning host only. The template lists them as used by "opengear".
- **New OOB gate confirmation, "Console servers".** It covers:
  - every unit enrolled in Lighthouse;
  - every serial port reaching its device;
  - TACACS+ and break-glass logins working;
  - the cellular path proven with the Ethernet uplink pulled.
- **To confirm on the first unit** (marked CONFIRM in the scripts): the serial port id form, pinout X2, baud rates per platform, the admin group id, and the TACACS+ mode that keeps local login when TACACS+ is down.

# AIDC DBD Desktop 2.0.39 (builder HS-v1.47, site runner 1.7.0)

- **The Final package no longer stalls on "design document" at XL sizes.** A 4,096-rack build sat at this step for hours with the heap near 12 GB. Two parts of the work grew with the square of the design size; both now grow in proportion to it, and the output is identical byte for byte:
  - **Cabinet balancing in the physical model.** Every trial move of an overfull MoR/EoR or network cabinet used to re-size every cabinet's patch field. Now only the two cabinets involved are re-sized. At 512 racks the physical model drops from 25.8 s to 12.5 s, with the same cabinet layout.
  - **The deployment plan's device lookups.** Each device name was found by scanning the whole device list. A name index replaces the scan, and the plan digest drops from 4.7 s to 1.5 s at 512 racks.
- **What's still slow at XL:** the physical model repeats 3 or 4 times until the network-cabinet PDUs settle, re-deriving the cable matrix each time. Expect several minutes on this step at 4,096 racks, not hours.

# AIDC DBD Desktop 2.0.38 (builder HS-v1.46, site runner 1.7.0)

- **Fixed: the front-end super-spine tier at 4,096 racks.** Each super-spine needs one port per pod plus one for the border leaves. At 64 pods on a 64-port super-spine (the SN6600 at 1.6T), that's 65. The builder warned, then built 33 super-spines per group anyway, while each pod spine has only 32 uplinks. One super-spine in every group had no interfaces at all: 16 unconnected switches in the BoM, rack plan and configs.
- **When 1:1 doesn't close, the pod spine takes more leaf pairs** (decision of 29 Oct 2026). The builder picks the smallest pod-spine oversubscription that closes the tier, never past the front end's own target.
  - At 4,096 racks: 17 leaf pairs per pod, 34 ports down and 30 up (1.13:1), 61 pods and **480 super-spines**. Every super-spine has 61 or 62 links.
  - The ratio shows in the health panel and as a Release check row. Wherever 1:1 fits (up to 63 pods), nothing changes.
- **A tier that can't close stops the build.** It's a load-bearing Release check row, and neither the Blueprint package nor the deployment package or .run is built. The site runner reports the job as failed instead of producing a package.

# AIDC DBD Desktop 2.0.37 (builder HS-v1.45, site runner 1.7.0)

- **Ingress isolation (F5 BNK) is stated and tested.** Decision of 29 Oct 2026: BNK stays on the shared-VLAN tenancy model, with one INGRESS VRF and external VLAN for all tenants and a VIP, SNAT pool and policy for each. BNK's VRF-per-tenant mode waits until F5 publishes its limits. Ingress traffic doesn't cross the firewalls, so BNK's per-tenant policy is the only isolation control on that path, and it's now proven at acceptance:
  - **New front-end gate line, Ingress isolation (BNK).** The Console runs `acceptance/ingress_isolation.sh` (estate.conf `isolation_cmd`). It checks three things for every tenant: requests to its VIP reach only its own namespace, the backend sees only the tenant's own SNAT address, and a pod in its namespace can't reach the next tenant's probe. Without the runner, use **Upload results** on the gate line.
  - **The package carries the script, `isolation-tenants.csv` and `isolation-probe.yaml`** (a probe in each tenant namespace, removed after the run). Confirm the probe image tag and BNK's support for the URLRewrite filter on the release in service.
- **Site-wide tenant ceiling.** BNK is shared by every edge block, so its limits apply to the whole site.
  - The builder now warns when the tenant count exceeds the VIP block. Each tenant needs a public /32 from the primary front-end block; before, tenants past the block got no VIP, noted only in the BNK README.
  - The builder also warns when the count exceeds BNK's per-tenant limit, once that limit is entered (`BNK_TENANT_MAX`, blank until F5 confirms it).
- **Release check.** New rows:
  - **F5 BNK throughput per ingress node:** inferred, and load-bearing when ingress Gbps is set. The 200 Gbps planning figure isn't published; the only public figure is 75–77 Gbps L7.
  - **F5 BNK per-tenant limit:** not published.
- **The BNK warning** now states the tenancy model, that BNK's per-tenant policy is the only control on the ingress path, and the deferred VRF option.
- **Fixed: the onboarding sheet lists one SNAT address per BNK node.** It always listed two, so with 3 or 4 nodes (ingress over 200 Gbps) it didn't match the SNAT pools.
- **Regression set: 25 designs** (was 20). Added Junos, EOS and NX-OS back ends, a 150-tenant design (two edge blocks) and an FTD front end.

# AIDC DBD Desktop 2.0.36 (builder HS-v1.44, site runner 1.7.0)

- **Tuned back-end profiles (stage 3 of 3).** A platform tuned at one site now ships tuned to the next.
  - **Export profile** (Deployment Console, on the Back-end tuning line) is available once a version is approved and T1–T4 have passed. It writes `tuning-profile-<model>-<release>-<id>.json` with the model, OS and release, the values, the test summary, the NIC and port speed you enter, and a SHA-256 checksum. It carries no names, addresses or counters, so the file can leave the site.
  - **Tuned profiles** (builder toolbar) imports the file and checks the checksum. A profile that was edited after export is refused.
  - **The design records the profile it uses**, so a rebuild in the site runner reproduces the same configs. To build a design without a profile, use **Don't use a profile in this design**.
- **What a profile changes.**
  - The back-end configs carry the measured values: Junos ECN, PFC and watchdog; EOS ECN and watchdog; NX-OS ECN; and Cumulus ECN on TC3, written only where the values differ from the RoCE defaults.
  - The markers read `TUNED (profile …, proven on <release>, <date>)`, or `VERIFIED` for defaults that passed.
  - The Release check row **Back-end lossless thresholds** reads confirmed, and the tuning item closes for that platform.
  - The Console's v0 starts from the profile.
- **Release match is major.minor.** A profile proven on another release is still applied, but its markers stay TUNE with "re-run acceptance", and the Release check row reads inferred. The row isn't load-bearing, because thresholds are set at acceptance, not for construction.
- **Without a profile, the configs are unchanged.**

# AIDC DBD Desktop 2.0.35 (builder HS-v1.43, site runner 1.7.0)

- **Back-end tuning in the Deployment Console (stage 2 of 3).** When lossless acceptance fails on the test pod, you tune from the back-end gate instead of editing switches by hand.
  - The gate's new **Back-end tuning** line lists every tunable value: the package value and each version proposed since.
  - **Propose** a version. The Console checks each value's range and step, and the ordering the procedure requires (ECN minimum below maximum, PFC xon below xoff).
  - **Apply** it to the test pod, one pod, the shared spines or every back-end switch. Then re-run T1 to T4.
  - **Approve** the version that passes. Only an admin login can approve.
  - **Roll out** applies the approved version pod by pod. After each pod, a T2/T3 smoke test runs on that pod, and a failure stops the rollout. The smoke test needs `{POD}` in `accept_cmd`.
  - Rolling back is applying an earlier version. v0 restores the package values.
- **Platforms.** What can be tuned depends on the back-end platform:

  | Platform | Tunable values | How the change is applied |
  |---|---|---|
  | Junos | ECN Kmin, Kmax and mark probability; PFC xoff, xon and headroom cable length; PFC watchdog detection and recovery | `commit confirmed`: the switch rolls itself back unless the Console confirms the commit |
  | EOS | ECN thresholds and probability; PFC watchdog timeout and recovery | Saved to the startup configuration |
  | NX-OS | ECN thresholds and probability | Saved to the startup configuration |
  | Cumulus | ECN thresholds and probability | `nv config apply`. The Spectrum-X defaults normally pass, and the overlay syntax is marked CONFIRM |

- **Overlays are applied over SSH** with the push credential, or with the new optional `WRITE_USER` / `WRITE_PASSWORD` in `secrets.env`.
- **The lossless precondition check reads each switch against the version that switch runs.** A tuned switch no longer shows as missing its package lines.
- **The back end holds until every rail leaf and spine runs the approved version.** Approving v0 accepts the package values as they are.
- **The hand-over export adds `tuning.json`.** It records each version with its values and configuration lines, who proposed and approved it, the rollout, and the version each switch runs.
- **`accept_cmd` takes `{POD}`** (and `ACCEPT_POD` in the environment) alongside `{TEST}`.

# AIDC DBD Desktop 2.0.34 (builder HS-v1.42, site runner 1.7.0)

- **Back-end lossless acceptance in the Deployment Console** (stage 1 of 3, per the BEF Lossless Tuning and Acceptance Procedure). The back-end gate gains four lines ahead of NCCL:
  - **Lossless preconditions:** every planned MTU, PFC, ECN, watchdog and RoCE line is in the running configuration of every rail leaf and spine, compared after normalization against the planned config.
  - **Lossless acceptance T1–T4:** the Console runs `acceptance/lossless.sh` on the head node over SSH, one test at a time (estate.conf `accept_cmd`, with `{TEST}`). It reads the test pod's switch counters before and after each test (PFC on priority 3, ECN marks and drops on the RoCE queue, watchdog events), adds the NIC counters the script reports, and scores the pass criteria: line rate, no lossless drops, marks before pauses, incast fairness, no spine PFC under all-to-all, and bus bandwidth at target. Each criterion is listed under Evidence.
  - **T5 and T6** (failure and watchdog tests) are operator confirmations.
  - **Without the runner**, Upload results takes the script's output; the switch-side criteria are then confirmed by hand.
- **Counters the Console can't read are never passed.** Each switch vendor reports PFC and ECN counters in its own format. The Console reads each vendor's structured (JSON) output, and any counter it doesn't recognize shows as "confirm by hand". Confirm the readers on the first unit of each platform.
- **The Total racks field** shows four digits.

# AIDC DBD Desktop 2.0.33 (builder HS-v1.41, site runner 1.7.0)

- **Cable Matrix tab order:** Build order, Cabling rules, 0 Power, 1 OOB Cable Map, 2 Edge PoP, 3 FEF Cable Map, 4 BEF Cable Map. The rules are read before any cable is pulled, so they now sit beside the build order, and the Build order note says to read them first. The order is the same in the Cable Matrix workbook and the Final package's design workbook. Nothing else changes; the plan digest is the same as in 2.0.32.

# AIDC DBD Desktop 2.0.32 (builder HS-v1.40, site runner 1.7.0)

- **Every network-cabinet PDU and power shelf is on the OOB network.** The OOB plan took its network cabinets from the front page's plan, before the layout fills and splits them by kW, rack units and outlets. As a result most network-cabinet PDUs had no address or drop, the SN6800 cabinets' DC power shelves were never in the plan, and the plan held PDUs for EoR cabinets the layout had merged away. The plan is now built in passes: plan, layout, then the plan again from the cabinets the layout built, until the two agree (two passes in practice). Each unit gets an address in MGMT-INFRA and a drop on its pod's MGT switches, and the pods get the switches they need. On a 64-rack Vera Rubin build: 172 units covered (8 before), one more MGT switch in the pod, and no phantom PDUs. A fresh physical model takes longer: 31 s instead of 20 s at 512 racks.
- **The Final package's design workbook drops OOB Mgmt and Facility.** The address plan is in the Day-2 SoT and the configs; the facility plan is in the design document and viewer.
- **The Cable Matrix tabs are renamed** 1 OOB Cable Map, 3 FEF Cable Map and 4 BEF Cable Map, in the Cable Matrix workbook and in the package.
- **The Cable Matrix PDF matches the Excel exactly.** At 128 racks the cable counts are identical step by step: 834, 9,143, 26, 7,727 and 147,456.

# AIDC DBD Desktop 2.0.31 (builder HS-v1.39, site runner 1.7.0)

- **On a site with data halls, a large Cable Matrix PDF is written per hall.** Each build step now gets one PDF per hall, based on the hall of the switch or PDU each cable is listed under. Files are named hall first (`…-cable-matrix-hall07-step4-back-end.pdf`, with `core-…` for the core room), so all of a hall's files sort together. A tech opens the files for the hall they're working in. At 256 racks across 7 halls: 32 files, the largest back-end hall 2,882 pages. The 10,000-page volume limit still applies but is rarely reached.
- **Bookmarks in every Cable Matrix PDF.** The sidebar lists each part, then each cabinet, then each switch or device in it, so a soft copy jumps straight to the switch being cabled.
- **Cables between halls are marked.** When the far end is in another hall, the far-cabinet column reads, for example, `… U24 (HALL-5)`, in the PDF and in the Excel.

# AIDC DBD Desktop 2.0.30 (builder HS-v1.38, site runner 1.7.0)

- **Large Cable Matrix PDFs are written in volumes.** At 1,625 racks the PDF is about 130,000 pages. The PDF library holds a whole document as one string, which tops out at about 25,000 of these pages, so the export failed after an hour with nothing written. Past 140,000 cables the matrix is now written as one PDF per build step (`…-cable-matrix-step0-power.pdf` through `…-step4-back-end-vol4.pdf`), with any step over 10,000 pages split into numbered volumes. Each file is saved as soon as it's finished. At 512 racks: 8 files and 40,220 pages in about 4 minutes. Smaller matrices are still one PDF, page for page as before.
- **A failed PDF says so.** If the PDF library can't write a file, the job stops at once with the reason, instead of waiting and then failing on an empty file.
- **The DC Layout Builder's all-links PDF** rolls into volumes of 10,000 pages the same way.
- **The BoM PDF and Excel are 3 to 4 times faster.** They derived the front-end pairs, the DPU plan and the site checks again for every row that asked, and classed every link seven times. At 512 racks the PDF went from 15.4 s to 3.6 s, with the same content.
- **Release check shows the selected rack's power.** The rack-power rows showed the GB300 NVL72 figures (135 kW, confirmed) on every NVL72 profile. On Vera Rubin NVL72 they now read 210 kW, inferred from industry reporting, pending the OEM's QuickSpecs. The gate counts 7 unconfirmed inputs instead of 5. GB300 is unchanged.
- **Fabric health labels.** "Network devices" counted only the back end, so it now reads "Back-end switches". The power line reads "BE network racks … (N switches, ~W each with their rack equipment)".
- **Closing the app with a job running asks first.** A separate warning lists each running site-runner job and how long it has run. **Keep running** is the default.
- **The design file a job runs from is named `input-design-site-runner.json`**, so the only file named for an export is the export itself.

# AIDC DBD Desktop 2.0.29 (builder HS-v1.37, site runner 1.6.0)

- **No crash on Exit with a large design.** Exit, and Save, made the whole design into one text inside the window, and Exit did it before asking whether to save. At 1,625 racks that ran the window out of memory. Now Exit asks first. A design past 512 racks is written in parts that stream straight into the file: 3 s and 68 MB at 800 racks. It's the same JSON that Load reads, indented by one space instead of two. Up to 512 racks, saving is unchanged.
- **A rebuild lets go of the old design first.** During a rebuild, caches still held the old design while the new one was built, so the window briefly held both. At 1,625 racks a back-end platform change peaked at 2.3 to 2.9 GB; at 4,096 racks it would have passed the window's 3.5 GB. The caches are now cleared where the rebuild starts. With the window capped at 1.8 GB, 2.0.28 crashed on that change at 1,625 racks and 2.0.29 completes it, with the same design.
- **The Cable Matrix PDF builds about 25% faster, with the same bytes.** Repeated values are cleaned and split once. The PDF library's Arabic and right-to-left passes are switched off, since this document's text is always Latin.
- **One file per crash.** Each crash is logged only to its own dated file, `crash-log-YYYY-MM-DD-HHMMSS.txt` in `%APPDATA%\AIDC DBD`, with the local time and the app and builder versions. The cumulative `crash-log.txt` is no longer written; an existing one is left where it is.

# AIDC DBD Desktop 2.0.28 (builder HS-v1.36, site runner 1.6.0)

- **DC Layout Builder: Find device answers at once on a large site.** At 800 racks, pressing Enter took over 9 minutes, and at 1,625 racks it hung the Layout tool. It now takes about half a second.
  - Find re-ran the whole site layout and, in large-site mode, re-synced with the Layout service, only to select a cabinet. Nothing moves when a device is found, so it now selects the cabinet the way a click on the floor plan does.
  - The suggestion list held every device in the site (39,355 at 800 racks). Opening the faceplate under it jammed the window. Past 500 devices, the list now shows the first 50 names that match what you type, names that start with it first.

# AIDC DBD Desktop 2.0.27 (builder HS-v1.35, site runner 1.6.0)

- **Settings changes on large designs are two to three times faster, with identical results.** At 1,625 racks an edge-router change takes 13 s (it took 26 s), a back-end platform change 40 s (104 s) and a rack-count change 59 s (126 s). The design, its fingerprint and the whole front page come out the same.
  - Since 2.0.23, a change applied with the progress meter redrew outside the page cache. Past 512 racks that cache holds every plan to one derivation per redraw, so the redraw derived the same plans many times. It runs inside the cache again.
  - The device and per-type indexes were rebuilt every time a device was added. They now grow in place.
  - The hyperscale-site check ran without any cache. It now has its own.
  - The OOB pod assignment, FE switch renumbering and the link index did repeated or wasteful work. They now do each pass once.

# AIDC DBD Desktop 2.0.26 (builder HS-v1.33, site runner 1.6.0)

- **The physical model builds about twice as fast, with identical outputs.** At 162 racks it takes about 1.6 s, down from 3.2 s. At 512 racks it takes about 10 s, down from 18 s. Everything that builds it is faster: front-panel changes up to 512 racks (the cabinet row), Infra, BoM, Facility, Physical model, the DC Layout Builder, the cable maps and packages. Packages, the physical model, the SoT and every Excel sheet are byte-identical to 2.0.25's, timestamps aside. HS-v1.32 was a development build that was never released.

# AIDC DBD Desktop 2.0.25 (builder HS-v1.31, site runner 1.6.0)

- **Mgmt switch configs: a device click shows that device's config in about 1 to 2 seconds at 1,625 racks (it took 75 to 200 s).** A click had rebuilt the whole view, and each device's config rebuilt the management plan, physical model and OOB fabric. The view and its exports now build those once. Past 512 racks, the design service keeps them across clicks that change nothing and builds the OOB fabric when the configs view opens. Expect about 2 minutes on that switch at 1,625 racks; device clicks after that are quick. Download of all 3,501 configs takes about 6 s.
- **The mirrored view keeps every list's scroll position** after a click, the device list included.

# AIDC DBD Desktop 2.0.24 (builder HS-v1.30, site runner 1.6.0)

- **The design service: the heavy views on a large design are built outside the window.** Past 512 racks, Infra, BoM, Facility, Release check, the OOB link count, Physical model and Day-2 SoT run in a separate process that has the workstation's memory. It works from a copy of the design rebuilt from its site-runner file. The view comes back into the window as usual, with a progress meter while it builds. Clicks and changes in the view go to the service, a design setting changed there carries back to the window's design, and downloads are saved to the project folder. The first view on a design takes the rebuild time; later views reuse that copy until the design changes.
- **Long on-screen tables are capped past 512 racks.** The Infra address plan and power patch plan show their first 2,000 rows, with a note saying so. Download, PDF and Copy still carry every row. At 1,625 racks, Infra now comes back as 1.3 MB of page instead of about 75 MB.
- **Large exports are streamed to disk.** The design service writes a large file in pieces, so it never holds a second whole copy. At 1,625 racks the physical model is a 618 MB file; writing it peaks at 4.3 GB of memory.
- **A memory guard before views that are still built in the window.** On a design past 512 racks, Render, FE Render, Config and Deploy check the window's free memory first. If the window has already used more than half of it, they stop with a message instead of crashing the window. The Blueprint package carries those outputs. Moving the rest of the builder out of the window is planned for v2.1.
- **Each crash gets its own log file, with the local date and time in its name** (`crash-log-YYYY-MM-DD-HHMMSS.txt`, in `%APPDATA%\AIDC DBD`). Each file has the app and builder versions. `crash-log.txt` still collects every crash.

# AIDC DBD Desktop 2.0.23 (builder HS-v1.29, site runner 1.6.0)

- **A progress meter while a change is applied to a large design.** A setting change or Build from rack count on a design past 128 racks (or one whose last redraw took over 1.5 s) runs in stages, rebuild the design, then recompute the plans and redraw, each ticked with its time and the expected time from the last run on that design size. Further changes made meanwhile are applied after it, with one redraw. The window still pauses inside each stage; moving the build out of the window is planned for v2.1.

# AIDC DBD Desktop 2.0.22 (builder HS-v1.28, site runner 1.6.0)

- **NVIDIA SN6600 is the air-cooled, AC-powered 3RU model** (920-9N63F-00RI-3C0), and the default wherever an SN6600 is chosen: back end, front-end leaf and spine.
  - 64 × 1.6T OSFP cages (128 × 800G), 1 QSFP112, 2 SFP28; 200–240 VAC on four PSUs; air-cooled, port-side intake; 52 kg; 3.94 kW typical with fully retimed optics. Standard network cabinet with AC PDUs; four cords per switch in the power plan.
  - The liquid-cooled, DC-busbar 2RU model is **SN6600-LD**, unchanged (MGX v1.2 rack, power shelves, liquid).
  - Designs saved before this release named the LD "NV-SN6600": they load as the SN6600-LD, with a notice.
  - DC Layout Builder: the SN6600 faceplate from NVIDIA's product image (management, QSFP112 and two SFP28 along the top; 64 cages in four rows of 16, two blocks of eight; cage numbering assumed).
  - To confirm with NVIDIA: PSU rating and inlet, maximum power, cage numbering.
- **Network cabinets are filled by power outlets and branch breakers as well as rack units and kW**, for every AC platform. A cabinet takes switches only while each side's PDU has an outlet of the right class for every cord, on a branch that carries it with the other side failed (80%). With the SN6600 that is six per cabinet (about 21 kW, 12 C19 per PDU); the middle-of-row block grows wider. Cord placement prefers a branch that still holds the cord.

# AIDC DBD Desktop 2.0.21 (builder HS-v1.27, site runner 1.6.0)

- **DC Layout Builder past 512 racks: a progress panel.** While the Layout service builds the model (and on Refresh from Fabric Designer), a panel shows the four stages (load the builder, rebuild the design, build the physical model, place cabinets and check every link), one progress bar, the elapsed time and an estimate of the time left, and the counts as they become known.
  - Inside the rebuild and the physical-model build the bar moves on an estimate, marked as one, and jumps to the true point when the stage ends. The estimate is scaled to the workstation once the rebuild has been timed.
  - The site-wide files (all links, CSV or PDF) show real progress, rows written of the total, and the saved path in the panel.
  - An error appears in the panel, with the reason and the log location, instead of a pop-up.

# AIDC DBD Desktop 2.0.20 (builder HS-v1.26, site runner 1.6.0)

- **DC Layout Builder past 512 racks: large-site mode.** A 1,625-rack design crashed the builder window when the Layout Builder opened.
  - **The cause:** each window has about 4 GB of JavaScript memory, whatever the workstation has, because Chromium builds its JavaScript engine that way. 2.0.14 to 2.0.19 asked for up to 16 GB and showed that figure, but the request had no effect. The builder window was building the whole physical model (2.1 million links) beside the design and ran out.
  - **Now:** a **Layout service**, a separate process with the workstation's memory, rebuilds the design from its settings (fingerprint checked, as the site runner does), holds every link and measures every run. The Layout window holds the cabinets and devices and asks the service for a device's links (cable matrix, faceplate), a cabinet's power cords, the site-wide checks and length schedule. The builder window no longer builds the model for it.
  - Moves, cabinet order and geometry are mirrored to the service, so lengths and checks follow your edits. Refresh from Fabric Designer takes the design as the builder window has it now and keeps your edits.
  - **Save layout** writes the cabinets, devices and edits without the links; opening that file here applies the edits to the design. **All links (CSV/PDF)** is written to the project folder by the service.
  - Measured on the 1,625-rack design: the service starts in about 2.5 minutes and peaks at 4.2 GB; the Layout window stays under 100 MB (188 MB with three times that floor, 7,524 cabinets).
- **Cable matrix past 512 racks:** the builder's Cable Matrix (Excel or PDF) is measured and written by the site runner, as a job in the panel. Building it in the window had the same memory problem.
- **Fixed:** past 512 racks, every faceplate in the Layout Builder failed to open (a variable name clash). Every edit re-checked port speeds slowly on large sites; the check is now cached.
- The About box and the crash log show a window's real memory limit, and the crash dialog says when a large design can run a window out of memory.

# AIDC DBD Desktop 2.0.19 (builder HS-v1.25, site runner 1.5.0)

- **Builder HS-v1.25: readiness, and the fixes from the TC-202 to TC-206 run.**
  - **Readiness** before Start build: customer services, identities, images, push credential, people per plane, disk, certificate and three manual confirmations. Start build waits until each passes or is confirmed, or the rest is accepted with a reason.
  - **Discovery and Ready N of M:** each device's reported model and switch port are checked against the plan as it calls in; every build card shows how many of its devices are ready; a Readiness export.
  - **Check before apply (Cumulus):** ZTP stops on a release other than the target, and on a cable the plan does not match (LLDP, right after the apply), with the reason on the device card.
  - **Software and Cabling checks** in every gate.
  - **Fixes:** a device whose sessions all go to set-aside peers passes; the gate panel keeps your build across a refresh; Evidence lists sessions with the raw output behind Show raw; Diff vs planned ignores value lists, plan actions and more defaults; dialog buttons show progress and Hand-over updates while open; the export is a button and counts once its receipt is confirmed against its SHA-256; Release DWR access is offered only when it applies; setup.sh refreshes a stale package index by itself.
  - **DSX Air:** air/topology-tc207.json for TC-207 (one miswire, one older release).

# AIDC DBD Desktop 2.0.18 (builder HS-v1.24, site runner 1.5.0)

- **Builder HS-v1.24: identities, accounts and hand-over, plus the TC-202 to TC-205 re-run findings.**
  - **Deployment Console:**
    - Every button shows that its action is running (amber, spinner), then done (green check) or failed (red, with the reason in a status line instead of a pop-up).
    - Gate checks no longer count sessions toward set-aside peers or wait on set-aside devices; the down-session list gives the total, newly down first; check buttons sit under the text.
    - Diff vs planned compares the plan and the switch in one form (ranges, order, quoting, secrets, device defaults), and **Diff vs previous** compares the last two backups. An unchanged backup says so, and each backup shows when it was last confirmed.
    - A Done switch that stops answering is red and listed under Problems. Start RMA asks for the vendor ticket plainly and refuses a MAC there; an RMA can load a configuration file from the customer's backup system.
    - Logins are marked **DWR** or **Customer**. A new **Hand-over** dialog: final as-built backup; the hand-over export (customer admins only: as-built, the as-built SoT overlay with apply_asbuilt.py for NetBox or Nautobot, the device backups, audit and events), after which scheduled backups stop; **Release DWR access** (removes dwr-netadmin from every device and confirms it, disables the DWR logins, locks DWR's host accounts and keys, clears the push credential).
  - **Device accounts on every platform:** netadmin (customer break-glass), dwr-netadmin (DWR, build only) and estate-ro (read-only; the service reads with it).
  - **Identities:** the manifest import reads serial, MAC and model by column header and keeps both identifiers; NX-OS is matched by the serial POAP sends in DHCP option 61, the MAC a cross-check.
  - **setup.sh** `--release-vendor` and `--decommission`; the **.run** takes `--rma EXPORT.zip DEVICE` for a maintenance install after decommission. setup.sh reports the real reason PyEZ is not usable.
  - Checks and backups read the switches in parallel.
  - **DSX Air:** the service reads as estate-ro; the README adds TC-206 (hand-over and Release DWR access).

# AIDC DBD Desktop 2.0.17 (builder HS-v1.23, site runner 1.5.0)

- **Builder HS-v1.23: the TC-202 to TC-205 findings from DSX Air.**
  - **Deployment Console:**
    - Gates have **Re-check**, which runs their automatic checks once, now. The checks skip devices that were set aside.
    - A Done device that stops answering shows **"Done · not answering"**, with a hint to start an RMA.
    - The RMA identity box no longer clears while you type, and a registered replacement is shown.
  - **Provisioning service:** device reads wait out unreachable TACACS servers (longer login timeout), and BGP reads skip VRFs without BGP.
  - **setup.sh** waits for the package lock after a boot, and a failed PyEZ install says why.
  - **DSX Air TC-202:**
    - air/ztp-host.sh now does the whole host setup. It asks for the password twice and makes it the read login, puts the service addresses on the host, and binds the slice by MAC from Air's own list.
    - air/ztp-kick.sh is gone.
    - The README explains the console tunnel.

# AIDC DBD Desktop 2.0.16 (builder HS-v1.22, site runner 1.5.0)

- **Builder HS-v1.22: TC-202 (zero-touch end to end in DSX Air).**
  - **air/ztp-host.sh** makes Air's oob-mgmt-server the provisioning host. It stops Air's DHCP, puts the provisioning and gateway addresses on the OOB interface, writes test secrets, runs setup.sh, sets aside the devices outside the slice and starts the run.
  - **air/ztp-kick.sh** restarts ZTP on the slice switches and lists their eth0 MACs for binding from Unclaimed.
  - **estate_build.py lab-slice SLICE.csv** sets aside (quarantines, audited) every device outside a lab slice, so its waves settle.
  - **Cumulus ZTP scripts** reach the provisioning host through the management VRF when the default VRF has no route.

# AIDC DBD Desktop 2.0.15 (builder HS-v1.21, site runner 1.5.0)

- **Show DSX Air test files:** Site build > Show DSX Air test files… takes air/ out of a .run into a folder next to it (`<run name>-air`) and opens it. The package checksum is verified first; nothing is installed. No Linux machine is needed to get topology.json for the Air import.
- **Builder HS-v1.21:** the fixes from the Prov_Serv and DSX Air tests (HS-DEP-002 stage 1 and TC-201):
  - **Cumulus configurations:** SNMP contact and location are quoted; OSPF BFD on the SN2201 uses a BFD profile; telemetry (and every per-port line) never names the parent of a broken-out port, which had stopped the front-end leaves from applying. The same parent rule now covers SR Linux, Junos and NX-OS.
  - **Cumulus ZTP** locks and expires the factory cumulus account once the configuration is applied. The switch is managed with the admin login from secrets.env.
  - **DSX Air kit:** imports as is (name, model labels, 4 GB / 20 GB switches, unused ports unconnected instead of stub nodes). run-tests.sh answers the first-login password change itself, waits up to 5 minutes for BGP, and counts sessions on cabled ports apart from EVPN. air/ztp.sh is an optional lab ZTP script. The README gives the slice's memory against Air's 60 GiB limit.
  - **.run:** --extract-only runs as any user, and the destination folder is created only after the package is verified.
  - **setup.sh:** no Recommends (no compilers), a PyEZ result line, and netmiko in its own environment (/opt/estate-build/pyenv) so Ubuntu 24.04 installs it.
  - **Junos edge releases pinned** to 24.4R2-S3 (MX304, SRX4700, PTX10001-36MR EVO). Confirm before a Build.
  - **Provisioning service:** the DHCP sniffer listens only on the provisioning NIC; temporary passwords have no look-alike characters; the Server header no longer shows software or version.
  - **Deployment Console:** a forced password change is no longer wiped by the live refresh and has Cancel (back to sign-in); signing out clears the previous user's view; Unclaimed shows the client-ID MAC when it differs and has Dismiss… (with a reason, audited).

# AIDC DBD Desktop 2.0.14 (builder HS-v1.20, site runner 1.5.0)

- **Faster packages and .run files:** about 20% faster (1,024 racks: 944 s to 742 s; the Final package 519 s to 416 s, the .run 425 s to 326 s), with less memory. The package and the .run no longer rebuild the physical model, management plan and deploy plan at each step. Output is unchanged: every file matches HS-v1.19 except the version and date, and parity passes.
- **Front end off: no front-end edge.** The package used to write edge router and firewall configurations that the .run never shipped, so parity failed. Both now leave them out.
- **DC Layout Builder scrolls inside the window:** on a wide floor (3 or 4 columns) the horizontal scroll bar is at the bottom of the window, not below the whole floor.
- **A crashed window says so:** if the builder or Layout window stops (for example out of memory), the app names the reason, offers Reload and writes it to crash-log.txt (in the app's settings folder). Before, the window went white.
- **Memory split:** the site runner gets what the builder window and Windows leave (32 GB: 13.0 GB builder window, 15.4 GB runner, was 24.4 GB). The two limits no longer add up to more than the machine.
- **Workstation guidance** in the README: 32 GB up to 2,048 racks, 64 GB recommended for 4,096; single-core speed decides build time.

# AIDC DBD Desktop 2.0.13 (builder HS-v1.19)

- **No sleep while building:** the app asks Windows not to sleep while a build holds the builder window (and for two minutes after) and while a site-runner job runs. The window title reads "sleep blocked while building" while it holds; the job card logs "Sleep blocked" and "Sleep released". The display may still turn off. A lid close or a manual Sleep still sleeps.
- **Site runner 1.4.0:** run on its own (lab server or command line), the runner holds sleep off for the length of the run (Windows, macOS, Linux with logind) and says so in its first line. `--no-sleep-hold` turns it off.

# AIDC DBD Desktop 2.0.12 (builder HS-v1.19)

- **Front end and OOB health stats, like the Back end's:** the Fabric health panel now has BACK END, FRONT END and OOB blocks.
  - **Front end:** DPU links, worst leaf sub., leaf ports, leaf uplinks, spine ports, worst spine sub. (three tiers), SSP ports and tenant VRFs.
  - **OOB:** leaf ports, worst leaf sub., spine ports, worst spine sub. and SSP ports. The MCS pair, firewalls and edge routers are not counted.
- **IP & BGP plan per plane:** Front end and OOB tables now follow the Back end table, in the same format (switches, /32, subnets, AS, loopback and P2P ranges).
  - **Front end rows:** Leaf, Spine, SSP, Border leaf, Border gateway and Host DPU.
  - **OOB rows:** Leaf, In-rack leaf, Spine and SSP, all in AS 65100.
- **The Front end line always shows** in the per-plane device counts. It reads "off" when the plane is off, or names the problem when the plan can't be built, instead of disappearing.
- **Front end power counts only when the plane is on.**
- Numbers on the health panel use thousands separators.
- **Front end ASNs at mega scale:** past 1,000 front end leaves, leaf ASNs now continue in the mega-site band. Before, FE-LF2001 and up took the FE spine, super-spine and border leaf ASNs (a 4,096-rack site). Sites with 1,000 FE leaves or fewer keep every ASN. Rebuild and retest any 4,096-rack package.
- **OOB link stats:** the OOB spine and SSP port counts and subs need the layout model. Up to 128 racks they show right away; on larger sites, click **count links** (or open Layout, the BoM or the Cable Matrix first).

# AIDC DBD Desktop 2.0.11 (builder HS-v1.18)

- **No rebuild until you click Build:** a setting that reshapes the fabric (profile, planes, BE platform, pod size and the like) no longer rebuilds it on its own. The Build button is ringed in amber and the status line says the settings changed; nothing rebuilds until **Build from rack count** is clicked. Loading a file, restoring and share links still build.
- **GPU rows edge-aligned:** every pod row starts at the same left edge, instead of being centered.

# AIDC DBD Desktop 2.0.10 (builder HS-v1.17)

- **Data halls:** a site with more than one data hall (about 10 MW each; editable next to Floor) is laid out as a grid of halls with 6 m corridors between them.
  - **Each hall gets its own network row.** Every network cabinet goes to the hall that holds most of the devices it connects to (FE pod spines with their leaves, OOB spines with their racks).
  - **The core sits in a CORE block in the middle of the grid:** FE super-spines, border leaves, Edge PoP, firewalls and OOB core.
  - **Placement only:** devices, links and configurations are unchanged.
  - **Runs between halls go through the corridors.** In the BoM, structured cabling terminates on hall patch panels, and the cross-hall link counts are listed.
  - **The DC Layout Builder and the package floor plan both draw the halls.** Single-hall sites are laid out as before.
- **Rows spread evenly across halls:** the facility plan now uses the same hall count with the rows spread evenly (512 racks: 8 halls of 8 rows, was 7 of 9 and 1 of 1).
- **Hover card above the tile:** it opens above the tile (below it near the top of the window), so the tile's ports stay clickable.

# AIDC DBD Desktop 2.0.9 (builder HS-v1.16, site runner 1.3.0)

- **Cable Matrix lengths in runner-built packages:** packages built by the site runner (past 512 racks) now carry measured run lengths, the same as packages built in the window. They used to show "not placed".

# AIDC DBD Desktop 2.0.8 (builder HS-v1.15)

- **Cross-column cabling:**
  - Runs between columns route through a cross tray at the head or the foot of the columns, whichever is shorter. The builder, the DC Layout Builder and the Cable Matrix all use this routing.
  - The BoM counts cross-column links per plane.
- **AOC reach (default 30 m, editable next to Floor):** a longer run becomes structured cabling, in the BoM and the Cable Matrix:
  - a transceiver at each end (one per breakout cage);
  - two patch cords per run, and a trunk lane between the column-head patch panels;
  - the patch panels themselves.
  - MPO-16 for 800G and 1.6T, MPO-12 for 200G and 400G, LC duplex for 100G and below.
  - This applies to any run past the reach, within a column as well as across columns.
- **Keep pods in one column (off by default):** columns break at pod boundaries where the row counts allow. Each pod goes to the column its middle row falls in.

# AIDC DBD Desktop 2.0.7 (builder HS-v1.14)

- **Floor columns (1 to 4):** a new **Floor** selector lays the rows out in side-by-side columns (vertical stacks of rows), with a 3 m aisle between columns.
  - Rows fill each column in order, so a pod may span two columns.
  - The DC Layout Builder, cable run lengths, the Cable Matrix and the package floor-plan drawing all follow it.
  - In the DC Layout Builder, the column count and the aisle between columns are editable with the other geometry.
- **Row network:** the "block, 2/3/4 columns" options are removed; it's one block or MoR + EoR again. Saved designs that used them load as one block.

# AIDC DBD Desktop 2.0.6 (builder HS-v1.13)

- **Load progress meter:** shows each step (reading, parsing, building, applying, drawing) with the percentage read and the time each step took.
- **Fabric health, device counts per plane:**
  - Back end: leaves, spines, super-spines and platform.
  - Front end: leaves (pairs), spines, super-spines, border leaves, platforms, leaf oversubscription, spine port use, and DPU links per leaf.
  - OOB: leaves, spines and super-spines.
  - Edge PoP devices and the OOB MCS pair, firewalls and edge routers are not counted.
- **DC Layout Builder, Refresh from the builder:**
  - A cabinet new to a row lands where the builder places it (it used to land at the end of the row).
  - The message counts devices apart from cabinet power items (PDUs, power shelves).
- **Arista 7280DR3A border leaf on an SN6600-LD spine** is flagged as unconfirmed (400G-8 lane mismatch), pending NVIDIA confirmation.

# AIDC DBD Desktop 2.0.5 (builder HS-v1.12)

- **Load takes a site-runner file.** The design is rebuilt from the file's settings, and the status line reports the rack and device counts. It also reports whether the design fingerprint matches the file; a mismatch means the exported design had canvas edits its settings don't carry.

# AIDC DBD Desktop 2.0.4 (builder HS-v1.11)

- **400G border leaves on an SN6600-LD spine:** the spine's border cages now run 2 × 400G. They were set to 2 × 800G, so those links would not come up. Applies to the Arista 7280DR3A, Cisco N9332D, Nokia 7250 IXR-X3b, Juniper ACX7100 and QFX5130 border leaves.
- **Front-end leaf tiles** read "16 × 1.6T ↑ to spine" (they said "to border" on every design).
- **Front-end summary:**
  - The uplink bank shows the leaf's own form factor (it said QSFP-DD on every leaf).
  - On an SN6600-LD leaf, the storage breakout reads per 1.6T cage (4 × 400G).
- **BoM:** the front-end transceiver line counts the uplinks in use, not the whole uplink bank.
- **SN6600-LD leaf with 800G spines:** storage always starts on the next whole cage after the uplinks. With an odd uplink count, it could share a cage already broken out for an uplink.
- **Build:** the Windows installer build runs on the current GitHub Actions versions.

# AIDC DBD Desktop 2.0.3 (builder HS-v1.10)

- **NVIDIA SN6600-LD in the front end:** now in the FE leaf and FE spine catalogs (Spectrum-6, 102.4T, 64 × 1.6T OSFP224 cages, liquid-cooled, DC busbar). The FE leaf stays 1:1.
  - **SN6600-LD leaf:** 128 × 400G host ports (4 × 400G per cage).
    - To an 800G spine: 64 × 800G uplinks (2 × 800G per cage).
    - To an SN6600-LD spine: 32 × 1.6T straight uplinks, one per cage, no breakout. That's half the cables of 2 × 800G at both ends, and no fiber shuffles.
  - **SN6600-LD spine:** 128 × 800G leaf ports (2 × 800G per cage), or 64 × 1.6T straight to SN6600-LD leaves. Border leaves take lane 0 of a broken-out cage at their own speed.
  - **Storage cages** on a 1.6T leaf are broken out to match the storage NIC speed.
  - The BoM lists the right assemblies: 1.6T OSFP224 AOCs, 1.6T-to-4 × 400G and 2 × 800G breakouts, and the spare leg where a border leaf uses one.
  - The DC power shelf line names every DC model it feeds, back end and front end.
  - The SN6800-LD stays out of the front end (2 × 200G per MPO limit).
- **Cumulus configs: no stray breakouts.** The breakout block now ignores comments. Before, a peer's port named in a comment could channelize an unused cage. Some back-end Cumulus leaf configs lose those breakout lines. Only unused cages are affected, and the Air slice drops their stub nodes.

# AIDC DBD Desktop 2.0.2 (builder HS-v1.09)

- **Hover cards:** the hovered tile is magnified in place (1.8x) in dark teal with white text, so it reads clearly over its neighbors.
  - Names and labels show in full (no "...").
  - Port and cage faceplates sit on a light inset, so their colors still read.
  - It opens after 250 ms and closes when the pointer leaves.

# AIDC DBD Desktop 2.0.1 (builder HS-v1.08)

- **Responsive at mega scale:**
  - The physical model is built once per design state and reused. At 512 racks the BoM opens in 2–10 s (was about 30), and Config and Infra in under a second.
  - Front-end settings (N-S Gbps, Ingress Gbps, spine target and auto-scale) redraw only. They no longer rebuild the back end, so hand edits on the canvas are kept.
  - Restoring a design draws the canvas once, at the end. A 512-rack restore takes 15 s (was 39) in testing.
- **Row network: block, 2, 3 or 4 columns.** Sets the width of each row's network block up front.
- **Network cabinets that fit at 4,096 racks:** OOB spines and front-end super-spines now spread over more cabinets (OOB 8 → 18, front end 180 → 208). No cabinet is over-full.
- **Back-end topology (condensed):** where several leaf groups feed the same rack boxes, each group gets one label under it, so the labels no longer pile up at the rack row.
- **Save and close:**
  - Save opens a native Save dialog.
  - Closing the app offers to save the design first (Save…, Don't save, Cancel), and warns if a site-runner job is still running.
  - Site names and other prompts work in the app.
