# AIDC DBD Desktop

DWR Networking internal. The AIDC DBD Hyperscale builder as a Windows app, with the site runner built in.

- **The builder** runs unchanged in the main window (builder `HS-v1.25`).
- **Saves:** every download lands in your project folder. Nothing is overwritten: a second copy is saved as `name (2)`.
- **Past 512 racks**, the Final package, the Review package and the .run are built by the built-in site runner.
  - The runner is a separate process with the memory the builder window and Windows leave, so the browser's 4 GB tab limit no longer applies.
  - A panel shows progress and has a Cancel button. When a job finishes, **Open folder** shows the output.
- **DC Layout Builder:** past 512 racks it opens in its own window with its own memory.
- **No Wait/Exit dialogs.**

## Install

1. Download `AIDC-DBD-Setup-<version>.exe`:
   - from the latest **Build Windows installer** run (Actions tab, artifact **AIDC-DBD-Windows**, a zip holding the .exe), or
   - from **Releases**, for a tagged version.
2. Run it. It is **unsigned**, so Windows SmartScreen shows "Windows protected your PC". Click **More info**, then **Run anyway**.
3. Choose the install folder. The installer adds a desktop shortcut, **AIDC DBD**.
4. On the first save, the app asks for a **project folder**. Change it later under **File > Choose project folder**.

## Use

- **Design** as in the browser.
- **Site build menu:**

| Item | What it does |
|---|---|
| Final package and .run | Both, then the parity check: every device configuration in the package must be byte-identical to the .run's. This is the delivery build. |
| Final package / Review package / .run only | One output. |
| DC Layout Builder | The layout tool (own window past 512 racks). |
| Export site-runner file | The file for the command-line runner on another machine. |
| Show DSX Air test files… | Takes air/ out of a .run (checksum verified) into `<run name>-air/` next to it, for the DSX Air import. |

- **Up to 512 racks**, the Blueprint package and Deploy buttons work in the window, as in the browser.
- **Past 512 racks**, those buttons and the menu items send the job to the site runner.
- **Output folders:** each job writes to its own folder in the project folder, `{BUILD}-{job}-{date-time}/`. The folder holds the package and/or .run, the site-runner file it was built from, and `site-runner.log`.
- **The fingerprint gate:** the runner rebuilds the design from its settings and refuses to build unless the design fingerprint matches the window's. "Does not match" means the canvas has hand edits the settings don't carry.

## Memory and workstation

- The builder window gets 40% of RAM (4–16 GB). The site runner gets what is left after the builder window and 4 GB for Windows, up to 75% (32 GB: 13.0 and 15.4 GB).
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
npm ci
npm ci --prefix runner --omit=dev
# put node.exe (Node 22 x64) in vendor\node\
npx electron-builder --win nsis --x64 --publish never
```

Run from source: `npm start`. Without a bundled Node it uses the `node` on PATH, or `AIDC_NODE`.

## Not yet

- Code signing.
- Automatic updates.
- Licensing (v2.1).
- The design build in a background worker (v2.1).
