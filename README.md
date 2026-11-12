# fb

One Go binary to manage AVM FRITZ!Box routers: a scriptable CLI and an interactive TUI.
It covers what the Python `fritzconnection` tools do (`fritzstatus`, `fritzhosts`, `fritzwlan`,
`fritzcall`, `fritzphonebook`, `fritzhomeauto`, `fritzmonitor`, `fritzconnection -s/-a`)
and adds named router profiles.

## Install

```sh
make build            # → bin/fb
# or: go install github.com/mkrajinovic/fbctl/cmd/fb@latest
```

## Router setup

On each box:

1. **Home Network → Network → Network Settings → Allow access for applications** (enables TR-064).
2. **System → FRITZ!Box Users**: create a user (e.g. `fbctl`) with the rights
   *FRITZ!Box settings*, *Voice messages, fax messages, FRITZ!App Fon and call list*, and *Smart Home*.
3. Optional, for live call events: dial `#96*5*` on a connected phone (disable with `#96*4*`).

Then:

```sh
fb router add home --host fritz.box --user fb      # password goes to the OS keychain
fb router add office --host 192.168.10.1 --user fb --tls   # pins the box's certificate
fb router use home
fb router test        # checks login and every permission, one line per feature
```

Config lives in `~/.config/fbctl/config.yaml` (`$FBCTL_CONFIG` overrides). It never holds passwords.
Password lookup order: OS keychain → `$FRITZ_PASSWORD` → `password_cmd` (e.g. `op read op://…`).
Without any profile, `FRITZ_ADDRESS`/`FRITZ_USERNAME`/`FRITZ_PASSWORD` work like in fritzconnection.

## CLI

```
fb status [--watch 2s]                         model, firmware, WAN, external IP, live rates
fb status --all                                one line per router profile, polled concurrently
fb hosts [--active] [--sort ip|name]           known devices
fb hosts wake <mac|ip|name>                    Wake-on-LAN through the box
fb hosts rename <mac|ip|name> <new-name>       change the name the box shows
fb hosts block|unblock|access <mac|ip|name>    internet access per host (LAN keeps working)
fb mesh                                        router, repeaters and which device is connected where
fb log [-g sys|net|fon|wlan|usb] [-n 50]       the box's event log
fb reconnect | reboot [--yes]                  asks first; refuses without --yes when not on a TTY
fb wlan list | on|off <2.4|5|6|guest|index>
fb wlan qr guest [--show-password] [--png f]   join QR code
fb wlan password guest [--generate 16|--set PW] show or rotate a WLAN password
fb calls [--missed|--in|--out] [--days N]
fb calls monitor                               live RING/CALL/CONNECT/DISCONNECT events
fb phonebook list | show <id> [--search x]
fb home list | switch <ain|name> on|off|toggle | temp <ain|name> <°C|off|on>
fb raw services | actions <svc> | call <svc> <action> Name=value...
fb exporter [--listen 127.0.0.1:9133]          Prometheus metrics for all profiles on /metrics
fb tui                                         also the default with no args in a terminal
```

Global flags: `-r/--router`, `--json`, `--timeout`, `-v` (logs SOAP traffic to stderr with
passwords, WLAN keys and session IDs redacted). Exit code 3 means wrong credentials.

## TUI

Tabs: Overview · Hosts · Mesh · WLAN · Calls · Contacts · Smart Home · Log (`tab`/`1–8`). `[` `]` switch
routers, `j`/`k` move, `r` refresh, `?` help.

- Overview: `A` status of all routers (like `fb status --all`), `R` reconnect, `B` reboot (both confirm).
- Hosts: `/` filter, `a` active only, `s` cycle sort (ip → name → active), `enter` details including
  internet access, `n` rename, `w` wake, `b`/`u` block/unblock internet (blocking confirms).
- WLAN: `space` toggle (disabling a main network confirms), `c` QR code, `p` show password, `P` change
  password (pre-filled with a generated one you can edit, confirms, then shows the new join QR code).
- Calls: `f` cycle all/missed/in/out, `d` cycle 30/90/all/7 days. Live call monitor pane on top.
- Contacts: `/` search name or number, `p` next phonebook.
- Smart Home: `space` switch (thermostat: off ↔ 20 °C), `+`/`-` thermostat in 0.5 °C steps.
- Log: `g` cycle all/sys/net/fon/wlan/usb.

CLI only, on purpose: `router add/remove/use/trust/test` (password prompt and keychain), `raw`, `exporter`
and `completion`.

Live rates are polled every 3 s with at most one request in flight per router; full status every 30 s.

## Prometheus exporter

`fb exporter` serves `/metrics` for every profile (or just `-r name`). Each scrape polls all routers
concurrently with a timeout; a dead router reports `fritzbox_up 0` instead of failing the scrape.
Overlapping scrapes are serialised so a slow box is never polled twice at once. Connections and the
smart home session are reused across scrapes. Metrics include `fritzbox_up`, `_info`, `_wan_connected`,
`_wan_rate_bytes_per_second`, `_wan_max_bits_per_second`, `_wan_bytes_total`, `_hosts{state}`,
`_wlan_clients`, `_smarthome_power_watts`, `_smarthome_temperature_celsius`.

```yaml
scrape_configs:
  - job_name: fritzbox
    scrape_interval: 30s
    static_configs: [{targets: ["127.0.0.1:9133"]}]
```

## Second factor

Newer FRITZ!OS versions can require a second-factor confirmation for some write actions
(reboot, WLAN changes and similar). AVM's flow ([X_AVM-DE_Auth](https://fritz.com/fileadmin/user_upload/Global/Service/Schnittstellen/x_auth.pdf)):
`SetConfig(NewAction=start)` returns a token and the allowed methods (button, phone code), the client
polls `GetState` until `authenticated`, then repeats the call with the token in a SOAP header.
**fb does not implement this yet.** It reports the error clearly; do the action in the web UI, or
turn off the extra confirmation for the fb user. `router test` tells you if the protection is on.

## Roadmap

See [docs/capabilities.md](docs/capabilities.md) for everything the box exposes and what fb covers.

- **Second-factor confirmation (queued):** flow and SOAP header verified in AVM's Authentication PDF;
  see docs/capabilities.md. Needed for writes on boxes with 2FA on.
- Parental-control profiles and tickets, answering machine, firmware update, config backup.
- Not possible through documented APIs: per-host static IP (DHCP reservation), WireGuard.
- Replace hand-written fixtures with captures from a real box.

## Development

```sh
make test     # go test -race ./...
```

`internal/fakebox` is an in-process FRITZ!Box double (TR-064 with real HTTP Digest auth, host/call/phonebook
downloads, AHA login with PBKDF2). CLI, domain and TUI tests run against it.

Layout: `internal/tr064` (SOAP + digest + TLS pinning) · `internal/aha` (smart home, SID login) ·
`internal/callmon` (TCP 1012) · `internal/fritz` (domain layer) · `internal/cli` (cobra) ·
`internal/tui` (Bubble Tea) · `internal/ui` (shared styles).

### Not yet verified against a real box

The fixtures follow AVM's documentation and fritzconnection's parsers; these points still need a
check on live hardware (capture with `fb -v raw call …`, scrub secrets, add to `internal/fakebox`):

- unit of `X_AVM-DE_GetOnlineMonitor` `Newus/ds_current_bps` (treated as bytes/s)
- `NewX_AVM-DE_FrequencyBand` in `WLANConfiguration.GetInfo` (falls back to the channel number)
- `X_AVM_DE_GetExternalIPv6Address` on the TR-064 connection service (best effort, hidden if missing)
- `X_AVM-DE_Auth1.GetInfo` `NewEnabled` (error codes 866–868 and the token header are verified in AVM's PDF)
- `LANConfigSecurity1.X_AVM-DE_GetUserList` without credentials (fritzconnection sends credentials; fb tries
  without first, then falls back to the legacy `dslf-config` user)
