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
- LAG / Port Channel support
- Telnet transport
- PoE Read from `show inline power` output
- SSH hardening settings
- SNMP / Logging
- Terraform Registry publishing
