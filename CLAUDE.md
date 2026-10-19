# terraform-provider-fastiron-icx

Terraform provider for Brocade/Ruckus ICX switches running FastIron firmware 08.0.95. Communicates via SSH CLI — no REST API available on this firmware version.

## Architecture

### SSH Client (`internal/sshclient/`)
- Single persistent SSH shell session per provider instance
- Mutex-serialized command execution (FastIron doesn't support concurrent config sessions)
- Prompt detection handles `SSH@HOSTNAME Router#` format with `@` prefix
- Enable mode: sends `enable`, watches for `Password:` prompt (exact match, not substring), handles "No password has been assigned yet" gracefully
- `skip-page-display` sent on connect to disable `--More--` paging
- ANSI escape codes stripped from output via `sanitizeOutput()`
- `CommandExecutor` interface enables mock testing

### Config Parser (`internal/parser/`)
- Line-oriented state machine parser for `show running-config` output
- Port range expand/compress handles FastIron's `ethe 1/1/19 to 1/1/24` syntax
- All 4 real switch configs in `testdata/` used as test fixtures
- Cross-module ranges (e.g., `1/1/24 to 1/2/1`) are rejected — FastIron doesn't support them

### Firmware Dialects (`internal/dialect/`)
- Version- and image-dependent behaviour lives in a single place. `dialect.Detect(ver, image)` / `FromConfig(cfg)` yields a `Dialect{Version, Image, Known}`, built once in `provider.Configure` and handed to resources as `ProviderData.Dialect`.
- `d.Has(Cap...)` reads a declarative table in `capabilities.go`, where each capability maps to a predicate over the version and image. There are two kinds:
  - **Features** (`CapVEInterfaces`, `CapRouterInterface`, `CapGlobalSTP`, `CapManager`, `CapOpticalMonitor`) gate commands. Resources call `requireCapability(d, cap, feature, diags)` in ModifyPlan and as a CRUD backstop. Its error text comes from `Title`/`Why`/`Docs`.
  - **Syntax forms** (`CapDualMode`, `CapLegacyDHCPClient`) choose a spelling. Resources use the named renderers (`DHCPClientDisable`, `DualMode`, `NoDualMode`) instead of branching on them.
- **Never block on unknown.** An unparseable version makes version features true. An undetected image makes image features true. Forms fall back to the 08.0.9x spelling. The two axes are independent, so a detected switch image still blocks router-only features even without a version.
- Hardware facts (PoE, optics, port layout) are deliberately not capabilities, because the firmware dialect does not determine them.
- **Adding a model or firmware**: capture a sanitized running config into `testdata/`, then add or adjust rows in the `capabilities.go` table (a new `Capability` const plus a predicate built on `before(v)` / `switchImage()`). Next, add the fixture's version string to `TestDetectHas` and `TestFromConfigFixtures`, and update the matrix in `docs/index.md#firmware-dialects-and-capabilities`. Do not compare versions or `ImageType` inside resources; parser image detection is consumed only through the dialect.

### Resource Design
- **VLAN resource (`icx_vlan`)**: Manages VLAN existence and properties ONLY (name, STP, multicast, router-interface). Does NOT manage port membership — that's exclusively handled by `icx_interface_ethernet`.
- **Ethernet interface (`icx_interface_ethernet`)**: Manages port config AND VLAN membership. `tagged_vlans` is `Set(Int64)`. `tag_all_vlans` bool auto-tags all VLANs on the switch (excluding `untagged_vlan` and VLAN 1).
- **All resources**: `raw_config` list attribute for escape-hatch CLI commands (auto `no` prefix on destroy).
- **Singleton resources** (system, aaa): Use fixed IDs "system"/"aaa".
- **`write memory`**: Called after every CRUD operation, but skipped if no CLI commands were actually sent.

### Provider Config
- Uses provider aliases for multi-switch management (one alias per switch)
- No default provider needed if all resources specify `provider = icx.<alias>`
- Env vars: `FASTIRON_HOST`, `FASTIRON_USERNAME`, `FASTIRON_PASSWORD`, `FASTIRON_ENABLE_PASSWORD`

## Key Decisions & Gotchas

1. **Port membership is interface-only**: `icx_vlan` does NOT have `tagged_ports`/`untagged_ports`. This prevents dual-ownership diffs that caused an outage during early development.
2. **User passwords are write-only**: Can't be read from switch. Import always shows a password diff — this is safe (idempotent `username X password Y` command).
3. **`tag_all_vlans` is Terraform-only**: Can't be read from switch. On import, Read sets it to `false`. Switching from explicit VLANs to `tag_all_vlans = true` is a no-op if the VLAN sets match.
4. **VLAN 1 can't be deleted**: Delete resets it to defaults instead.
5. **Port names with spaces**: Automatically quoted when sent to CLI.
6. **The c10zp model** uses global `ip address`/`ip default-gateway` instead of VE interfaces. No `global-stp`. Parser handles both patterns.
7. **Enable password**: May not be configured on switch ("No password has been assigned yet..."). The provider handles this — the enable password is still required in config but the switch may not prompt for it.
8. **`tag_all_vlans` drift is invisible to `plan`, and only the create path self-heals.** `icx_vlan.Create` reconciles trunk-all ports as of **v0.1.1** (`findTrunkAllPorts`), but `Read` still skips reconciling `tagged_vlans` whenever `tag_all_vlans = true`. So membership that drifts any other way — a hand-run `no tagged ethe`, a VLAN that predates Terraform managing the switch, a half-completed apply — stays permanently invisible and `plan` keeps reporting "No changes".
   - Pin **>= v0.1.1**. On v0.1.0 there is no reconciliation at all: adding an `icx_vlan` leaves it off every trunk.
   - Where a trunk's membership needs to be *auditable* and not merely correct, prefer an explicit `tagged_vlans` list. Read populates that from the device, so drift surfaces as an ordinary diff and applies additively. The trade: every VLAN must be listed, because omitting one **removes** it from the trunk.
   - **Field signature**, worth recognising because it does not look like a switch problem: a host on the missing VLAN has link up and a correct static address but answers nothing. ARP for it stays `INCOMPLETE` everywhere, and the near switch's `show mac-address vlan <id>` lists only that host, never learning the gateway. It reads as a broken NIC or bad host config; it is a missing tag on a *transit* trunk — a switch that carries the VLAN for others without being a member itself.
9. **Never `-replace` an `icx_interface_ethernet` that is a live trunk.** Delete runs `buildVLANRemovalCommands`, stripping **all** VLAN membership from the port before Create re-adds it. On a switch's own uplink that removes the management VLAN mid-apply, so the provider loses its SSH session and the Create half never runs — leaving the switch reachable only by console. This is the trap behind wanting to "force" a `tag_all_vlans` re-expansion; there is no safe way to do it via `-replace`. Upgrade to >= v0.1.1 and let `icx_vlan.Create` reconcile, convert that trunk to an explicit `tagged_vlans` list (in-place and additive), or add the one membership by hand.
10. **Two resources must never manage the same port.** Declaring a port twice (e.g. one `tagged_vlans` uplink resource and one `untagged_vlan` access resource for `1/2/1`) makes each apply clobber the other, so `plan` never converges — the workspace shows a permanent pending change. This is the same dual-ownership failure that gotcha #1 prevents between `icx_vlan` and `icx_interface_ethernet`, and nothing in the provider currently detects it. To fix: `terraform state rm` the redundant resource *before* deleting it from config, so its destroy cannot strip a port the survivor still owns.

11. **FastIron < 08.0.80 uses `dual-mode` for trunk-untagged.** On 08.0.30 (ICX 6430/6450) a tagged port cannot be `untagged` in VLAN context; it is tagged in the untagged VLAN too and ` dual-mode  <id>` (two spaces) under `interface ethernet` picks the untagged one. Bare `dual-mode` = VLAN 1; `no dual-mode` = tagged-only. Parser: `EthernetInterface.DualModeVLAN`; dialect gate: `dialect.CapDualMode` (version < 08.0.80, or any `dual-mode` line present via `Dialect.Observe`). `icx_interface_ethernet` Read takes `untagged_vlan` from dual-mode and drops that VLAN from `tagged_vlans`; `buildDualModeMembershipCommands` emits tagged-then-`dual-mode` on create, re-issues `dual-mode <new>` directly (never `no dual-mode` first — that drops the management VLAN on an uplink) when the untagged VLAN changes, and `no dual-mode` precedes membership removal on destroy. Never emit `dual-mode` on 08.0.9x; the per-command CLI error detection in `ExecuteInConfigMode` is the backstop. Not yet validated on a live 08.0.30 device.

## Testing
- `go test ./...` — runs parser tests against real config fixtures
- `make build` / `make install` — build and install locally
- `make testacc` — acceptance tests (require `TF_ACC=1` and live switch)
- Debug SSH tool: `go run ./cmd/debug-ssh/` with `FASTIRON_HOST/USERNAME/PASSWORD` env vars

## Switch Models Tested
- ICX 7250-24 — 2 modules, SFP+, full feature set
- ICX 7150-24 — 3 modules including SFP+
- ICX 7150-C12P — 12-port PoE compact, optical-monitor
- ICX 7150-C10ZP — 10-port PoE compact, no global-stp, ISP VLANs

## Future Work
- **Reconcile `tagged_vlans` in Read when `tag_all_vlans` is set.** v0.1.1 fixed the create path; Read still skips reconciliation, so any drift not caused by `icx_vlan.Create` remains undetectable (gotcha #8). Comparing actual membership against the expanded set would turn a silent outage into a plan diff.
- **Detect duplicate port ownership** — two `icx_interface_ethernet` resources on one port currently produce permanent, silent plan churn (gotcha #10). A validation error would catch it at plan time.
- **Validate the 08.0.30 `dual-mode` dialect live** (gotcha #11): whether re-issuing `dual-mode <new>` on a port that already has dual-mode is accepted or needs `no dual-mode` first, and how a bare `dual-mode` is printed in the running config.
- LAG / Port Channel support
- Telnet transport
- PoE Read from `show inline power` output
- SSH hardening settings
- SNMP / Logging
- Terraform Registry publishing
