# AIDC DBD Desktop

DWR Networking internal. The AIDC DBD Hyperscale builder as a Windows app, with the site runner built in.

- **The builder** runs unchanged in the main window (builder `HS-v2.09`).
- **Saves:** every download lands in your project folder. Nothing is overwritten: a second copy is saved as `name (2)`.
- **Past 512 racks**, the Final package, the Review package and the .run are built by the built-in site runner.
  - The runner is a separate process with the workstation's memory, so a window's 4 GB limit does not apply to it.
  - A panel shows progress and has a Cancel button. When a job finishes, **Open folder** shows the output.
- **DC Layout Builder:** past 512 racks it opens in its own window in **large-site mode**: a Layout service (a separate process with the workstation's memory) rebuilds the design, holds every link and measures every run; the window holds the cabinets and devices and asks the service for a device's links, a cabinet's power cords, the site-wide checks and length schedule, and the site-wide files.
- **Cable matrix past 512 racks:** the builder's Cable Matrix (Excel or PDF) is measured and written by the site runner.
- **No Wait/Exit dialogs.**

## Install

1. Get `AIDC-DBD-Setup-<version>.exe`: from the release, from the latest **Build Windows installer** run (Actions tab, artifact **AIDC-DBD-Windows**), or from **Releases** for a tagged version. Check it with `Get-FileHash <file> -Algorithm SHA256`.
2. Run it. It is **unsigned**, so Windows SmartScreen shows "Windows protected your PC". Click **More info**, then **Run anyway**.
3. Choose **Only for me** (no administrator rights; installs in `%LOCALAPPDATA%\Programs\AIDC DBD` by default) or **Anyone who uses this computer** (asks for administrator rights). It adds **AIDC DBD** shortcuts on the desktop and in the Start menu.
4. **Update:** save, close the app (and any DC Layout Builder windows), run the new Setup. It replaces the installed version in place; the shortcuts keep working. Keep each Setup file to go back to that version.
5. **Uninstall:** Settings > Apps > Installed apps > AIDC DBD. Project folders are not touched.
6. **Portable zip** (`AIDC-DBD-<version>-win-x64-portable.zip`), for a machine where installing is not allowed: unzip into its own folder and run `AIDC DBD.exe` there.
7. Start each customer with **File > New project folder…** (2.0.57): a new or empty folder, named from the build ID and the date, under `Documents\AIDC DBD Projects`. It becomes the active project, and the app offers to load the input sheet. **File > Open existing project folder…** switches to a folder you already have; **Show project folder in File Explorer** opens it. The grayed **Project:** line in the File menu names the active project. If none is set, the app asks on the first save. **Reset** (2.0.57) returns every setting to its default and clears the customer's details; tuned profiles stay in the library.

## Use

- **User guide (2.0.65):** **Help > User guide** or **F1** opens `builder/aidc-dbd-help.html` in its own window: getting started, workflows, troubleshooting and terms. The **?** beside the Fabric health panel, the input sheet report and the Deploy panel opens the matching section. It ships beside the builder and passes the neutrality gate. The reference half (input sheet keys, toolbar controls, platforms) is generated from the builder (2.0.66): after a builder change run `node tools/gen-help-ref.js`; `node tools/help-ref-check.js` fails a release whose guide differs from its builder.

- **Design** as in the browser.
- **Site build menu:**

| Item | What it does |
|---|---|
| Final package and .run | Both, then the parity check: every device configuration in the package must be byte-identical to the .run's. This is the delivery build. |
| Final package / Review package / .run only | One output. |
| DC Layout Builder | The layout tool. Past 512 racks: its own window in large-site mode, with a Layout service. |
| Export site-runner file | The file for the command-line runner on another machine. |
| Show DSX Air test files… | Takes air/ out of a .run (checksum verified) into `<run name>-air/` next to it, for the DSX Air import. |

- **Up to 512 racks**, the Blueprint package and Deploy buttons work in the window, as in the browser.
- **Past 512 racks**, those buttons and the menu items send the job to the site runner.
- **File menu, customer input sheet (2.0.52):** the load shows a stepped meter (2.0.57), and a failed step leaves the open design as it was.
  - **Load input sheet…** sets the design from a filled sheet (`.xlsx`, or CSV `key,value,unit,source`) in one step and shows the import report. A value outside the offered options is built at the nearest one and flagged as a design choice; rows the builder does not model are listed as not modeled. The session input file (the record) is saved to the project folder.
  - **Input sheet template (blank)** and **Input sheet filled from this design** save the template, generated from the builder's catalog. In a browser, the builder's **File ▾ > Input sheet** does the same.
- **File menu, Baseline and equivalence report (2.0.56):**
  - **Load Baseline…** reads the customer's Baseline (`.xlsx`, or CSV `key,value,unit,source`), lists any rows it cannot use (unknown keys, bad values), and opens the equivalence report.
  - **Baseline template (blank)** and **Baseline filled from this design** save `AIDC-DBD-Baseline-v1.xlsx`, generated from the same key catalog as the report.
  - **Equivalence report** shows one row per metric: Baseline, Product, Delta, within tolerance, class and reason. A class can be overridden with a reason. **Download report** saves `<build>-equivalence.pdf` and `.csv`; the status line gives the SHA-256 of each. In a browser, the builder's **File ▾ > Equivalence** does the same.
- **Health verdict (2.0.56):** the Fabric health panel and the Release check show one line, Errors (N) / Open inputs (N) / Advisories (N). Placeholders count as open inputs, never as errors.
  - The values without a control (compute rack feed, busbar rating) ride in the design file and its fingerprint, so the site runner rebuilds what was imported. The builder file and its platform profiles are never changed.
  - 2.0.53 r2 adds two rows: `fabric.be.keep_planes` (Keep plane count, NVL72 platforms) and `fe.ns_dpu` (north-south DPU, Vera Rubin NVL72: BlueField-3 B3240 or BlueField-4 B4240V).
- **Storage by scalable unit (2.0.57):** on rack-scale platforms (NVL72) storage is sized per scalable unit, the platform's 8-rack unit, so it no longer changes with the back-end switch. The control and the input sheet row (`fe.storage_nodes_per_pod`, key unchanged) read "Storage nodes per scalable unit (N racks)". Saved designs and sheets load unchanged.
- **Switch BMC drops (2.0.57):** every NVIDIA SN6000 switch (SN6600, SN6600-LD, SN6810-LD, SN6800-LD), back end or front end, gets a BMC drop to an OOB access switch, with an MGMT-COMPUTE address. On the SN6800-LD and SN6810-LD the second OOB port is a spare, not cabled.
- **Out-of-date canvas past 512 racks (2.0.58):** BoM, Release check, Infra, Facility and Count links are built by the design service from the settings. If the canvas was built before a setting changed, the app offers **Rebuild and continue**. Their exports are saved straight into the project folder, each with a **Saved** card (2.0.60).
- **Output folders:** each job writes to its own folder in the project folder, `{BUILD}-{job}-{date-time}/`. The folder holds the package and/or .run, the site-runner file it was built from, and `site-runner.log`.
- **The release gate (2.0.51):** the site runner refuses a design unless it was exported from the same builder file (same SHA-256). Site-runner files exported before 2.0.51 carry no SHA-256: export them again.
- **The fingerprint gate:** the runner and the Layout service rebuild the design from its settings and refuse unless the design fingerprint matches the window's. "Does not match" means the canvas has hand edits the settings don't carry.
- **Large-site layouts:** Save layout writes the cabinets, devices and your physical edits to the project folder, without the links (they come from the design when the file is opened here again). The all-links CSV and PDF are written to the project folder by the Layout service. Refresh from Fabric Designer takes the design as the builder window has it now and keeps your edits.

## Memory and workstation

- **Each window has about 4 GB of JavaScript memory**, whatever the workstation has: Chromium, which Electron is built on, compiles its JavaScript engine with that limit. (2.0.14 to 2.0.19 asked for up to 16 GB and showed that figure; the request had no effect. The About box and the crash log now show the window's real limit.) That is why anything large past 512 racks runs in a separate Node process.
- The site runner and the Layout service get up to 75% of RAM, leaving at least 8 GB (32 GB: 24 GB).
- Measured, Layout (2.0.20): a 1,625-rack design with 2.1 million links, the Layout service 4.2 GB peak and about 2.5 minutes to start; the Layout window under 100 MB.
- Measured: 295K GPUs (4,096 racks), 14.3 GB peak for the site runner, 99 minutes on an i7-8700T with 32 GB.

| Site | RAM |
|---|---|
| Up to 512 racks | 16 GB (no site runner) |
| 512 to 2,048 racks | 32 GB |
| 2,048 to 4,096 racks | 64 GB recommended, 32 GB minimum (close other programs; no rebuild while a runner job runs) |

- **CPU:** a build runs on one core per process, so single-core speed decides the time: a current desktop Core i7/i9 or Ryzen 7/9, 6 to 8 cores. Low-power (T, U) and server parts with low clocks are slow.
- **Disk:** NVMe SSD, 50 GB or more free for packages and .run files.
- **Display:** 1920 × 1080 minimum; 2560 wide or more for the floor plan and Cable Matrix.
- **Windows 10 or 11, 64-bit.** The graphics card does not matter.

## Build (maintainers)

GitHub Actions builds the installer on every push to `main` and on `v*` tags (`.github/workflows/windows.yml`).

Locally on Windows (Node 22):

```
node tools/neutrality-check.js
node tools/help-ref-check.js
npm ci
npm ci --prefix runner --omit=dev
# put node.exe (Node 22 x64) in vendor\node\
npx electron-builder --win nsis --x64 --publish never
```

**Neutrality gate** (`tools/neutrality-check.js`, also the first step of the GitHub build): it fails on an internal standards, deviation, tenancy or Edge PoP clause id, a person's title, or an organization name on the banned list, in the builder (and the pages and binaries it embeds), the runner, the app and `provgo`. Before a release, run it on the outputs of a test build too: `node tools/neutrality-check.js <job folder>` reads into the package zip, the .run payload, workbooks and PDFs.

**Acceptance-script check** (`provgo/acctest/acctest.py`, 2.1.5): before a release, every acceptance test of the reference `.run` files is run on a simulated site, correct and with each fault planted, and scored by both engines: `python3 provgo/acctest/acctest.py --work <dir> --jobs 4 --run <A.run> --run <B.run> ...`. Any unexpected verdict or engine difference fails the release. A change to an acceptance script or its scoring updates its case in the same commit (see `provgo/acctest/README.md`).

**Release identity:** a builder release is its file, identified by SHA-256. The app hands the SHA-256 to the window, the site-runner file carries it, and the site runner refuses a design exported from a different builder file.

On Linux (2.0.67): the Setup can be built from an unpacked Windows tree with Wine (32- and 64-bit) installed: `npx electron-builder --win nsis --x64 --prepackaged <AIDC-DBD-<version>-win-x64> --publish never -c.win.signAndEditExecutable=false`.

Run from source: `npm start`. Without a bundled Node it uses the `node` on PATH, or `AIDC_NODE`.

## Not yet

- Code signing.
- Automatic updates.
- Licensing (v2.1).
- The design build in a background worker (v2.1).
