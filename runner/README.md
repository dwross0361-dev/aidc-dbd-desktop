# AIDC DBD site runner

DWR Networking internal tool. It builds a design's **Final Blueprint package** and its **.run** on a DWR workstation, for sites too large for the browser. A browser tab is limited to 4 GB of script memory, and a 117K-GPU design uses about 2.4 GB before its package starts.

The browser is still where you design. The design reaches the runner as a file only. The runner never connects to anything.

## Requirements

- A DWR workstation with enough RAM for the site. Measured: 512 racks peaked at 5.7 GB and took 16 minutes. Plan on 32 GB up to about 2,000 racks (117K GPUs is 1,625) and 64 GB for 4,096.
- Node.js 20 or later.
- The builder release the design was made in, for example `aidc-dbd-hyperscale-v1.07.html`.

## Install (once per release of this folder)

```
npm install
```

## Use

1. In the builder: build the design, set the customer code, then click **Site runner**. The builder downloads `{CUST}-{SITE}-...-site-runner.json`. The file holds the settings the design is rebuilt from, the builder release and the design fingerprint (64 bits over every setting, device and link).
2. On the workstation:

```
node aidc-runner.js --html aidc-dbd-hyperscale-v1.07.html --design ACME-LAX-...-site-runner.json --out ./ACME-LAX
```

Options:

| Option | Effect |
|---|---|
| `--heap MB` | Script memory limit. The default is 75% of the workstation's RAM. |
| `--package-only` | Build only the Final Blueprint package. |
| `--run-only` | Build only the .run. |
| `--review` | Build the Review package (watermarked) instead of the Final. |
| `--settings` | Build from the file's settings alone: no fingerprint gate, any release. For testing only, never for delivery; the log says so, and the output files are named `TEST-FROM-SETTINGS-...`. |
| `--racks N` | With `--settings` (implied): build the design at N racks, up to 4,096. For a cap test without a browser export. |
| `--parity-only` | Re-check the newest package zip and .run already in `--out`, without rebuilding. Writes `site-runner-parity.log`. |

## What it checks

- **The release:** the design file and the builder file must be the same release, or nothing is built.
- **The fingerprint:** the runner rebuilds the design from the file and stops unless its design fingerprint matches the one the browser exported. A match means it is building exactly the approved design. Hand edits on the canvas are not carried, and a design that has them stops here.
- **Failed exports:** if any export in the Final package fails, the package is written but the run stops with "must not be delivered".
- **Parity:** every device configuration in the package must be byte-identical to the one the .run installs.

The output folder receives the package zip, the .run and `site-runner.log`. The log records each step, the build number, the fingerprint, the plan digest (the one the package README and the Deployment Console show) and the parity result.

The .run is never handed to customers. The Final package is delivered the usual way.
