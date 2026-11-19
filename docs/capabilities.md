# What a FRITZ!Box exposes, and what fb uses

Research from November 2026, based on AVM's TR-064 PDFs (https://fritz.support/resources/TR-064_<Name>.pdf, index at
https://fritz.com/en/pages/interfaces), fritzconnection 1.15.1 and Home Assistant's `fritz` integration.

Legend: ✅ implemented in fb · ○ documented, not implemented · ✗ not available through a documented API (web UI only)

## Hosts and network

| Capability | Status | Interface |
|---|---|---|
| Host list, Wake-on-LAN | ✅ | `Hosts:1` `X_AVM-DE_GetHostListPath`, `X_AVM-DE_WakeOnLANByMACAddress` |
| Rename host | ✅ `fb hosts rename` | `Hosts:1` `X_AVM-DE_SetHostNameByMACAddress` (NewMACAddress, NewHostName) |
| Block or allow internet per host | ✅ `fb hosts block/unblock/access`, TUI `b`/`u` | `X_AVM-DE_HostFilter:1` `DisallowWANAccessByIP` (NewIPv4Address, NewDisallow), `GetWANAccessByIP` → NewWANAccess. Fails with 880 for FRITZ! and PLC devices. |
| Mesh topology | ✅ `fb mesh`, TUI Mesh tab | `Hosts:1` `X_AVM-DE_GetMeshListPath` (JSON) |
| Parental-control profiles, time tickets | ✅ `fb profiles`, `fb hosts profile/ticket`, TUI `f`/`t` | `X_AVM-DE_HostFilter:1` `GetFilterProfiles`, `AddHostEntryToFilterProfile`, `GetHostEntryByIP`, `AddTicketTimeToHostEntryByIP`, `MarkTicket` |
| Auto-WOL, priority (device class: ○) | ✅ `fb hosts autowake/priority`, TUI `o`/`p` | `Hosts:1` `X_AVM-DE_SetAutoWakeOnLANByMACAddress`, `X_AVM-DE_SetPrioritizationByIP`, `X_AVM-DE_SetDeviceClassUserByMAC` |
| DHCP range (server on/off: ○) | ✅ `fb dhcp`, TUI System `e` | `LANHostConfigManagement:1` `GetInfo`, `SetAddressRange`, `SetDHCPServerEnable` |
| **Static IP / DHCP reservation per host** | ✗ | No TR-064 action. Web UI only, through undocumented `data.lua` / `query.lua` requests. Would need a browser devtools capture and could break with any firmware update. |
| Static routes | ○ | `Layer3Forwarding:1` `AddForwardingEntry`, `DeleteForwardingEntry` |

## Port forwarding

| Capability | Status | Interface |
|---|---|---|
| IPv4 port mappings: list, add, delete | ○ | `WANIPConnection:1` / `WANPPPConnection:1` `GetGenericPortMappingEntry`, `AddPortMapping`, `DeletePortMapping`. **AVM only lets a host add mappings for its own IP.** |
| Forwards for other hosts, MyFRITZ sharing | ✗ | Web UI only |
| IPv6 pinholes | ○ | UPnP IGDv2 `WANIPv6FirewallControl:1` (own host only) |

## VPN and remote access

| Capability | Status | Interface |
|---|---|---|
| **WireGuard** | ✗ | Not in any TR-064 document. Web UI only. |
| IPsec per app | ○ | `X_AVM-DE_AppSetup:1` `RegisterApp`, `SetAppVPN` / `SetAppVPNwithPFS` |
| Remote HTTPS, Let's Encrypt | ✅ `fb remote https/letsencrypt`, TUI System | `X_AVM-DE_RemoteAccess:1` `GetInfo`, `SetConfig`, `SetLetsEncryptEnable` |
| DynDNS | status ✅, setting it ○ (needs the provider password) | `X_AVM-DE_RemoteAccess:1` `GetDDNSInfo`, `SetDDNSConfig` |
| MyFRITZ | ✅ `fb remote myfritz` | `X_AVM-DE_MyFritz:1` `GetInfo`, `SetMyFritz` |

## WLAN

| Capability | Status | Interface |
|---|---|---|
| List, on/off, QR code | ✅ | `WLANConfiguration:N` `GetInfo`, `SetEnable`, `GetSecurityKeys` |
| Change password | ✅ `fb wlan password` | `SetSecurityKeys` (NewKeyPassphrase, NewPreSharedKey, NewWEPKey0–3 = "") |
| WPS push button | ✅ `fb wlan wps`, TUI `W` | `X_AVM-DE_SetWPSConfig` (NewX_AVM-DE_WPSMode = pbc/stop) |
| SSID, channel, hidden | ✅ `fb wlan set`, TUI `e`/`n`/`H` | `SetSSID`, `SetChannel`, `SetBeaconAdvertisement` |
| All radios off (like the WLAN button) | ✅ `fb wlan all`, TUI `O` | `X_AVM-DE_SetWLANGlobalEnable` |
| Signal and link rate per device | ✅ `fb wlan clients`, TUI Hosts | `X_AVM-DE_GetWLANDeviceListPath` (fallback `GetGenericAssociatedDeviceInfo`). Box radios only; repeater clients are listed without signal. |
| Guest timeout, night mode | read only | `X_AVM-DE_GetWLANExtInfo`, `X_AVM-DE_GetNightControl`. Setting them is web UI only. |

## Telephony

| Capability | Status | Interface |
|---|---|---|
| Call list, phonebooks, call monitor | ✅ | `X_AVM-DE_OnTel:1`, TCP 1012 |
| Edit contacts, vCard/CSV import and export | ✅ `fb phonebook add/edit/delete/import/export`, TUI Contacts `n`/`e`/`X` | `X_AVM-DE_OnTel:1` `GetPhonebookEntryUID`, `SetPhonebookEntryUID`, `DeletePhonebookEntryUID`. Online phonebooks are read-only. |
| Call block list | ✅ `fb calls block/unblock/blocked`, TUI Calls `b` | `X_AVM-DE_OnTel:1` `GetCallBarringList`, `GetCallBarringEntryByNum`, `SetCallBarringEntry`, `DeleteCallBarringEntryUID` |
| Answering machine: list, mark, delete, on/off | ✅ `fb tam`, TUI Phone tab (playback: ○) | `X_AVM-DE_TAM:1` `GetMessageList`, `MarkMessage`, `DeleteMessage` |
| Call deflection on/off | ✅ `fb forward`, TUI Phone tab | `X_AVM-DE_OnTel:1` `GetDeflections`, `SetDeflectionEnable` (no create action) |
| Dial, hang up | ✅ `fb dial/hangup`, TUI `c`/`d`/`x` | `X_VoIP:1` `X_AVM-DE_DialNumber`, `X_AVM-DE_DialHangup` |
| DECT handsets | ○ | `X_AVM-DE_Dect:1` |

## System

| Capability | Status | Interface |
|---|---|---|
| Status, reboot, reconnect, event log | ✅ | `DeviceInfo:1`, `DeviceConfig:1`, `X_AVM-DE_GetDeviceLogPath` |
| Firmware check and update, auto mode | ✅ `fb update`, TUI System | `UserInterface:1` `X_AVM-DE_CheckUpdate`, `X_AVM-DE_DoUpdate` |
| Config backup | ✅ `fb backup`, TUI System `b` (restore: web UI) | `DeviceConfig:1` `X_AVM-DE_GetConfigFile` (NewX_AVM-DE_Password) → URL valid for under 30 s |
| NTP, UPnP, NAS/FTP/SMB, file links | ○ | `Time:1`, `X_AVM-DE_UPnP:1`, `X_AVM-DE_Storage:1`, `X_AVM-DE_Filelinks:1` |
| Smart home switch and thermostat | ✅ | AHA `homeautoswitch.lua` |
| Smart home templates (e.g. guest WLAN template) | ✅ `fb home templates/apply`, TUI Smart Home | AHA `gettemplatelistinfos`, `applytemplate`, `settriggeractive` |
| USB devices, LED or button lock, internet speed test | ✗ | `X_AVM-DE_Speedtest` only runs the box's local test server |

## Presence (✅ implemented)

`fb watch` works out home/away from the host list's `Active` flag (with an away delay). It stores the
result for `fb seen` and the TUI, and forwards events to a webhook, ntfy or MQTT. No extra box
interface is needed.

## Second factor (✅ implemented)

Verified in AVM's `TR-064_Authentication.pdf`, version 5 (2025-08-07):

1. The write action fails with 866 (867 = blocked, 868 = busy).
2. `X_AVM-DE_Auth:1` `SetConfig`(NewAction=`start`) → NewState=`waitingforauth`, NewToken (UUID), NewMethods (e.g. `button`, a DTMF code).
3. The user presses a button on the box or dials the code on a phone.
4. `GetState`, with the token in the SOAP header, until it returns `authenticated`.
5. Repeat the original call with the same arguments and the token in the SOAP header:

```xml
<s:Header><avm:token xmlns:avm="avm.de" s:mustUnderstand="1">2C0A2110-…</avm:token></s:Header>
```
