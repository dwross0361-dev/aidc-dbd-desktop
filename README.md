# AIDC DBD Desktop

DWR Networking internal. The AIDC DBD Hyperscale builder as a Windows app, with the site runner built in.

- **The builder** runs unchanged in the main window (builder `HS-v1.14`).
- **Saves:** every download lands in your project folder. Nothing is overwritten: a second copy is saved as `name (2)`.
- **Past 512 racks**, the Final package, the Review package and the .run are built by the built-in site runner.
  - The runner is a separate process with 75% of the workstation's memory, so the browser's 4 GB tab limit no longer applies.
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

- **Up to 512 racks**, the Blueprint package and Deploy buttons work in the window, as in the browser.
- **Past 512 racks**, those buttons and the menu items send the job to the site runner.
- **Output folders:** each job writes to its own folder in the project folder, `{BUILD}-{job}-{date-time}/`. The folder holds the package and/or .run, the site-runner file it was built from, and `site-runner.log`.
- **The fingerprint gate:** the runner rebuilds the design from its settings and refuses to build unless the design fingerprint matches the window's. "Does not match" means the canvas has hand edits the settings don't carry.

## Memory

- The builder window gets 40% of RAM (4–16 GB); the site runner gets 75%.
- Measured with the command-line runner:
  - 117K GPUs (1,625 racks): 6.2 GB peak, about 30 minutes, on a 32 GB desktop.
  - 295K GPUs (4,096 racks): estimated about 15 GB; use 32 GB or more.

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
