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
fb hosts info <host>                           model, profile, online time, priority, auto wake
fb hosts priority|autowake <host> on|off
fb hosts profile <host> [profile] | ticket <host>   parental control: access profile, extra time
fb profiles                                    list access profiles
fb wlan wps <wlan> [--stop]                    push-button WPS, waits until a device joins
fb wlan all on|off                             all radios, like the WLAN button
fb wlan set <wlan> [--ssid X] [--channel N|auto] [--hidden true|false]
fb tam [machines | mark <t:i> | delete <t:i> | switch <t> on|off]   answering machine
fb forward [on|off <id>]                       call forwarding rules (created in the web UI)
fb dial <number|contact> [--phone NAME] | fb hangup   dialing help: rings your phone, then calls
fb home templates | apply <template>           smart home templates (e.g. guest WLAN on)
fb update [check | install | auto notify|important|all]   firmware
fb backup [-o file]                            encrypted config export ($FB_BACKUP_PASSWORD or prompt)
fb dhcp [range <first> <last>]                 DHCP server and address range
fb remote [https|letsencrypt|myfritz on|off]   internet access to the box, DynDNS status
fb watch [--interval 30s] [--no-notify]        desktop notifications: new device, internet down/up,
                                               missed and incoming calls
fb raw services | actions <svc> | call <svc> <action> Name=value...
fb exporter [--listen 127.0.0.1:9133]          Prometheus metrics for all profiles on /metrics
fb tui                                         also the default with no args in a terminal
```

Global flags: `-r/--router`, `--json`, `--timeout`, `-v` (logs SOAP traffic to stderr with
passwords, WLAN keys and session IDs redacted). Exit code 3 means wrong credentials.

## TUI

Tabs: Overview · Hosts · Mesh · WLAN · Calls · Contacts · Phone · Smart Home · Log · System
(`tab`, `1–9`, `0`). `[` `]` switch routers (the other routers' pills show ● online / ○ unreachable),
`j`/`k` move, `r` refresh, `?` help.

- Overview: `A` status of all routers, `R` reconnect, `B` reboot (both confirm).
- Hosts: `/` filter, `a` active only, `s` cycle sort, `enter` details (internet access, access profile,
  online time, priority, auto wake), `n` rename, `w` wake, `b`/`u` block/unblock internet, `p` priority,
  `o` auto wake, `f` next access profile (confirms), `t` online-time ticket.
- WLAN: `space` toggle, `c` QR code, `p`/`P` show/change password, `e` SSID, `n` channel, `H` hide SSID,
  `W` WPS, `O` all WLANs off/on. Changes that drop devices confirm first.
- Calls: `f` all/missed/in/out, `d` 30/90/all/7 days, `c` call back, `x` hang up. Live monitor on top.
- Contacts: `/` search, `p` next phonebook, `d` dial, `x` hang up.
- Phone: answering machines, messages and forwarding rules in one list. `space` switches a machine or
  rule, or marks a message heard/new; `x` deletes a message (confirms); `c` calls back.
- Smart Home: `space` switch (thermostat: off ↔ 20 °C) or run a template, `+`/`-` thermostat.
- Log: `g` cycle all/sys/net/fon/wlan/usb.
- System: firmware `u` check, `I` install, `a` auto mode; `e` DHCP range; `H`/`L`/`M` remote HTTPS,
  Let's Encrypt, MyFRITZ; `b` encrypted backup to the current directory.

CLI only, on purpose: `router …` (password prompt and keychain), `raw`, `exporter`, `watch`, `completion`.

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

If the box asks for a confirmation (UPnP error 866), fb starts AVM's `X_AVM-DE_Auth` flow and tells you
how to confirm: press a button on the box, or dial the shown code on a connected phone. It waits up to
two minutes, then repeats the action with the token. In the CLI ctrl+c cancels; in the TUI a modal shows
the instructions and esc cancels. Nothing to configure.

## Grafana

[docs/grafana-dashboard.json](docs/grafana-dashboard.json) is a dashboard for `fb exporter` (import it
and pick your Prometheus data source).

## Roadmap

See [docs/capabilities.md](docs/capabilities.md) for everything the box exposes and what fb covers.

- Not possible through documented APIs: per-host static IP (DHCP reservation), WireGuard, port
  forwards for other hosts. They would need the undocumented web UI requests.
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
- the second-factor flow end to end (codes, states, `NewMethods` format and the token header follow AVM's
  Authentication PDF; the box's action list was checked against its SCPD)
- the format of `GetFilterProfiles` (parsed loosely: XML items with ID/Name, or `id:name` pairs)
- `SetChannel` with 0 for automatic channel selection
- the absolute URL `X_AVM-DE_GetConfigFile` returns (fb rewrites the host to the configured address)

Action and argument names for all new features were checked against the SCPD files of a FRITZ!Box
5590 Fiber (FRITZ!OS 8.25).
- `LANConfigSecurity1.X_AVM-DE_GetUserList` without credentials (fritzconnection sends credentials; fb tries
  without first, then falls back to the legacy `dslf-config` user)
