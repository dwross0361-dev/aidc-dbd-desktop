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
