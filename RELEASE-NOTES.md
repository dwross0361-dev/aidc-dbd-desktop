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
