// Package fakebox is an in-process FRITZ!Box double for tests: TR-064 with real
// HTTP Digest auth, the host/call/phonebook list downloads, and the AHA interface.
//
// Fixtures are modelled on the formats documented by AVM and parsed by
// fritzconnection. Replace them with captures from a real box when available
// (`fb -v raw call ...`, secrets scrubbed).
package fakebox

import (
	"crypto/md5" //nolint:gosec // HTTP Digest (RFC 2617) as used by the box
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"

	"github.com/mkrajinovic/fbctl/internal/aha"
)

const (
	User     = "fbctl"
	Password = "s3cret"
	realm    = "HTTPS Access"
	nonce    = "8F1B3C2D4E5F6071"
)

// Call records an action invocation.
type Call struct {
	Service, Action string
	Body            string
}

// Box is the fake.
type Box struct {
	*httptest.Server

	mu        sync.Mutex
	Calls     []Call
	Responses map[string]map[string]string // "Service#Action" → out args
	Faults    map[string]int               // "Service#Action" → UPnP error code
	AuthFails int                          // number of 401s observed with credentials
	Plug      string                       // "1"/"0" switch state of the fake plug
	Target    int                          // thermostat target
	Blocked   map[string]bool              // IP → internet blocked (X_AVM-DE_HostFilter)
}

// New starts the fake box.
func New() *Box {
	b := &Box{Responses: defaultResponses(), Faults: map[string]int{}, Plug: "1", Target: 42, Blocked: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/tr64desc.xml", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, desc) })
	mux.HandleFunc("/upnp/control/", func(w http.ResponseWriter, r *http.Request) {
		// Like the real box, the user list is readable without credentials so apps can pick a user.
		if strings.HasSuffix(r.Header.Get("SOAPAction"), "#X_AVM-DE_GetUserList") {
			b.soap(w, r)
			return
		}
		b.auth(b.soap)(w, r)
	})
	mux.HandleFunc("/devicehostlist.lua", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, hostList) })
	mux.HandleFunc("/calllist.lua", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, callList) })
	mux.HandleFunc("/meshlist.lua", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, meshList) })
	mux.HandleFunc("/devicelog.lua", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.Calls = append(b.Calls, Call{Service: "http", Action: "devicelog", Body: r.URL.RawQuery})
		b.mu.Unlock()
		io.WriteString(w, deviceLog)
	})
	mux.HandleFunc("/phonebook.lua", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, phonebook) })
	mux.HandleFunc("/login_sid.lua", b.login)
	mux.HandleFunc("/webservices/homeautoswitch.lua", b.homeauto)
	b.Server = httptest.NewServer(mux)
	return b
}

// HostPort returns host and port of the fake.
func (b *Box) HostPort() (string, int) {
	a := b.Listener.Addr().String()
	i := strings.LastIndex(a, ":")
	var p int
	fmt.Sscan(a[i+1:], &p)
	return a[:i], p
}

// CallsTo returns recorded calls of an action.
func (b *Box) CallsTo(service, action string) []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Call
	for _, c := range b.Calls {
		if c.Service == service && c.Action == action {
			out = append(out, c)
		}
	}
	return out
}

var digestParam = regexp.MustCompile(`(\w+)="?([^",]*)"?`)

func md5hex(s string) string {
	h := md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(h[:])
}

// auth enforces RFC 2617 digest with qop=auth, like the box.
func (b *Box) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if strings.HasPrefix(h, "Digest ") {
			p := map[string]string{}
			for _, m := range digestParam.FindAllStringSubmatch(h[7:], -1) {
				p[m[1]] = m[2]
			}
			ha1 := md5hex(p["username"] + ":" + realm + ":" + Password)
			ha2 := md5hex(r.Method + ":" + p["uri"])
			want := md5hex(strings.Join([]string{ha1, p["nonce"], p["nc"], p["cnonce"], p["qop"], ha2}, ":"))
			if p["username"] == User && p["nonce"] == nonce && p["response"] == want {
				next(w, r)
				return
			}
			b.mu.Lock()
			b.AuthFails++
			b.mu.Unlock()
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5, qop="auth"`, realm, nonce))
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "<HTML><HEAD><TITLE>401 Unauthorized (ERR_NONE)</TITLE></HEAD><BODY>401 Unauthorized</BODY></HTML>")
	}
}

func (b *Box) soap(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	sa := strings.Trim(r.Header.Get("SOAPAction"), `"`)
	typ, action, _ := strings.Cut(sa, "#")
	svc := serviceByType(typ, r.URL.Path)
	b.mu.Lock()
	b.Calls = append(b.Calls, Call{Service: svc, Action: action, Body: string(body)})
	key := svc + "#" + action
	if svc == "x_hostfilter" {
		ip := between(string(body), "<NewIPv4Address>", "</NewIPv4Address>")
		switch action {
		case "DisallowWANAccessByIP":
			b.Blocked[ip] = between(string(body), "<NewDisallow>", "</NewDisallow>") == "1"
			b.Responses[key] = map[string]string{}
		case "GetWANAccessByIP":
			acc, dis := "granted", "0"
			if b.Blocked[ip] {
				acc, dis = "denied", "1"
			}
			b.Responses[key] = map[string]string{"NewWANAccess": acc, "NewDisallow": dis}
		}
	}
	code, fault := b.Faults[key]
	out, ok := b.Responses[key]
	b.mu.Unlock()
	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	if fault || !ok {
		if !ok && !fault {
			code = 401
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, faultTmpl, code, "fault")
		return
	}
	var args strings.Builder
	for k, v := range out {
		fmt.Fprintf(&args, "<%s>%s</%s>", k, xmlEscape(v), k)
	}
	fmt.Fprintf(w, respTmpl, action, typ, args.String(), action)
}

func between(s, a, b string) string {
	_, rest, ok := strings.Cut(s, a)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, b)
	return v
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func serviceByType(_, path string) string {
	// Control URLs are /upnp/control/<name>; the last path segment is the service name.
	return path[strings.LastIndex(path, "/")+1:]
}

// AHA interface.

const (
	ahaChallenge = "2$10000$5A1711$2000$5A1722"
	ahaSID       = "9f3c1a2b4d5e6f70"
)

func (b *Box) login(w http.ResponseWriter, r *http.Request) {
	sid := "0000000000000000"
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		want, _ := aha.ChallengeResponse(ahaChallenge, Password)
		if r.Form.Get("username") == User && r.Form.Get("response") == want {
			sid = ahaSID
		}
	}
	fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?><SessionInfo><SID>%s</SID><Challenge>%s</Challenge><BlockTime>0</BlockTime><Rights></Rights></SessionInfo>`, sid, ahaChallenge)
}

func (b *Box) homeauto(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("sid") != ahaSID {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.Calls = append(b.Calls, Call{Service: "aha", Action: q.Get("switchcmd"), Body: r.URL.RawQuery})
	switch q.Get("switchcmd") {
	case "getdevicelistinfos":
		fmt.Fprintf(w, deviceList, b.Plug, b.Target)
	case "setswitchon":
		b.Plug = "1"
		io.WriteString(w, "1\n")
	case "setswitchoff":
		b.Plug = "0"
		io.WriteString(w, "0\n")
	case "setswitchtoggle":
		b.Plug = map[string]string{"1": "0", "0": "1"}[b.Plug]
		io.WriteString(w, b.Plug+"\n")
	case "sethkrtsoll":
		fmt.Sscan(q.Get("param"), &b.Target)
		io.WriteString(w, q.Get("param")+"\n")
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func defaultResponses() map[string]map[string]string {
	return map[string]map[string]string{
		"deviceinfo#GetInfo": {
			"NewModelName": "FRITZ!Box 7590 AX", "NewSoftwareVersion": "8.02", "NewSerialNumber": "ABC123", "NewUpTime": "93784",
		},
		"userif#GetInfo": {"NewX_AVM-DE_Version": ""},
		"layer3forwarding#GetDefaultConnectionService": {"NewDefaultConnectionService": "1.WANPPPConnection.1"},
		"wancommonifconfig1#GetCommonLinkProperties": {
			"NewWANAccessType": "DSL", "NewPhysicalLinkStatus": "Up",
			"NewLayer1UpstreamMaxBitRate": "40000000", "NewLayer1DownstreamMaxBitRate": "250000000",
		},
		"wancommonifconfig1#GetTotalBytesSent":     {"NewTotalBytesSent": "123456789"},
		"wancommonifconfig1#GetTotalBytesReceived": {"NewTotalBytesReceived": "987654321"},
		"wancommonifconfig1#X_AVM-DE_GetOnlineMonitor": {
			"Newus_current_bps": "1000,2000,3000", "Newds_current_bps": "50000,40000,30000",
			"Newmax_us": "5000000", "Newmax_ds": "31250000",
		},
		"wanpppconn1#GetStatusInfo":              {"NewConnectionStatus": "Connected", "NewLastConnectionError": "ERROR_NONE", "NewUptime": "3600"},
		"wanpppconn1#GetExternalIPAddress":       {"NewExternalIPAddress": "203.0.113.7"},
		"wanpppconn1#ForceTermination":           {},
		"deviceconfig#Reboot":                    {},
		"hosts#X_AVM-DE_GetHostListPath":         {"NewX_AVM-DE_HostListPath": "/devicehostlist.lua?sid=deadbeef"},
		"hosts#X_AVM-DE_WakeOnLANByMACAddress":   {},
		"hosts#X_AVM-DE_GetMeshListPath":         {"NewX_AVM-DE_MeshListPath": "/meshlist.lua?sid=deadbeef"},
		"hosts#X_AVM-DE_SetHostNameByMACAddress": {},
		"deviceinfo#X_AVM-DE_GetDeviceLogPath":   {"NewDeviceLogPath": "/devicelog.lua?sid=deadbeef"},
		"wlanconfig3#SetSecurityKeys":            {},
		"wlanconfig1#GetInfo":                    {"NewEnable": "1", "NewStatus": "Up", "NewChannel": "6", "NewSSID": "Home", "NewBeaconType": "11iandWPA3", "NewStandard": "ax", "NewX_AVM-DE_FrequencyBand": "2400"},
		"wlanconfig2#GetInfo":                    {"NewEnable": "1", "NewStatus": "Up", "NewChannel": "36", "NewSSID": "Home", "NewBeaconType": "11iandWPA3", "NewStandard": "ax", "NewX_AVM-DE_FrequencyBand": "5000"},
		"wlanconfig3#GetInfo":                    {"NewEnable": "0", "NewStatus": "Disabled", "NewChannel": "6", "NewSSID": "Guest", "NewBeaconType": "11i", "NewStandard": "n", "NewAllowedCharsPSK": "0123456789ABCDEFabcdef", "NewMaxCharsPSK": "64"},
		"wlanconfig1#GetTotalAssociations":       {"NewTotalAssociations": "4"},
		"wlanconfig2#GetTotalAssociations":       {"NewTotalAssociations": "7"},
		"wlanconfig3#GetTotalAssociations":       {"NewTotalAssociations": "0"},
		"wlanconfig1#GetBeaconAdvertisement":     {"NewBeaconAdvertisementEnabled": "1"},
		"wlanconfig2#GetBeaconAdvertisement":     {"NewBeaconAdvertisementEnabled": "1"},
		"wlanconfig3#GetBeaconAdvertisement":     {"NewBeaconAdvertisementEnabled": "1"},
		"wlanconfig1#SetEnable":                  {},
		"wlanconfig2#SetEnable":                  {},
		"wlanconfig3#SetEnable":                  {},
		"wlanconfig3#GetSecurityKeys":            {"NewKeyPassphrase": "guest;pass"},
		"x_contact#GetCallList":                  {"NewCallListURL": "/calllist.lua?sid=deadbeef"},
		"x_contact#GetPhonebookList":             {"NewPhonebookList": "0,1"},
		"x_contact#GetPhonebook":                 {"NewPhonebookName": "Telefonbuch", "NewPhonebookURL": "/phonebook.lua?sid=deadbeef&pbid=0"},
		"lanconfigsecurity#X_AVM-DE_GetUserList": {"NewX_AVM-DE_UserList": `<List><Username last_user="0">admin</Username><Username last_user="1">fbctl</Username></List>`},
	}
}

const respTmpl = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:%sResponse xmlns:u="%s">%s</u:%sResponse>
</s:Body>
</s:Envelope>`

const faultTmpl = `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<s:Fault>
<faultcode>s:Client</faultcode>
<faultstring>UPnPError</faultstring>
<detail>
<UPnPError xmlns="urn:schemas-upnp-org:control-1-0">
<errorCode>%d</errorCode>
<errorDescription>%s</errorDescription>
</UPnPError>
</detail>
</s:Fault>
</s:Body>
</s:Envelope>`

func svc(typ, id, ctl string) string {
	return fmt.Sprintf(`<service><serviceType>urn:dslforum-org:service:%s</serviceType><serviceId>urn:%s</serviceId><controlURL>/upnp/control/%s</controlURL><eventSubURL>/upnp/control/%s</eventSubURL><SCPDURL>/%sSCPD.xml</SCPDURL></service>`, typ, id, ctl, ctl, ctl)
}

var desc = `<?xml version="1.0"?>
<root xmlns="urn:dslforum-org:device-1-0">
<specVersion><major>1</major><minor>0</minor></specVersion>
<systemVersion><HW>226</HW><Major>154</Major><Minor>8</Minor><Patch>2</Patch><Buildnumber>117000</Buildnumber><Display>154.08.02</Display></systemVersion>
<device>
<deviceType>urn:dslforum-org:device:InternetGatewayDevice:1</deviceType>
<friendlyName>FRITZ!Box 7590 AX</friendlyName>
<modelName>FRITZ!Box 7590 AX</modelName>
<serviceList>` +
	svc("DeviceInfo:1", "DeviceInfo-com:serviceId:DeviceInfo1", "deviceinfo") +
	svc("DeviceConfig:1", "DeviceConfig-com:serviceId:DeviceConfig1", "deviceconfig") +
	svc("Layer3Forwarding:1", "Layer3Forwarding-com:serviceId:Layer3Forwarding1", "layer3forwarding") +
	svc("UserInterface:1", "UserInterface-com:serviceId:UserInterface1", "userif") +
	svc("X_AVM-DE_OnTel:1", "X_AVM-DE_OnTel-com:serviceId:X_AVM-DE_OnTel1", "x_contact") +
	svc("Hosts:1", "LanDeviceHosts-com:serviceId:Hosts1", "hosts") +
	svc("LANConfigSecurity:1", "LANConfigSecurity-com:serviceId:LANConfigSecurity1", "lanconfigsecurity") +
	svc("X_AVM-DE_HostFilter:1", "X_AVM-DE_HostFilter-com:serviceId:X_AVM-DE_HostFilter1", "x_hostfilter") +
	`</serviceList>
<deviceList>
<device>
<deviceType>urn:dslforum-org:device:LANDevice:1</deviceType>
<serviceList>` +
	svc("WLANConfiguration:1", "WLANConfiguration-com:serviceId:WLANConfiguration1", "wlanconfig1") +
	svc("WLANConfiguration:2", "WLANConfiguration-com:serviceId:WLANConfiguration2", "wlanconfig2") +
	svc("WLANConfiguration:3", "WLANConfiguration-com:serviceId:WLANConfiguration3", "wlanconfig3") +
	`</serviceList>
</device>
<device>
<deviceType>urn:dslforum-org:device:WANDevice:1</deviceType>
<serviceList>` +
	svc("WANCommonInterfaceConfig:1", "WANCommonInterfaceConfig-com:serviceId:WANCommonInterfaceConfig1", "wancommonifconfig1") +
	`</serviceList>
<deviceList><device>
<deviceType>urn:dslforum-org:device:WANConnectionDevice:1</deviceType>
<serviceList>` +
	svc("WANPPPConnection:1", "WANPPPConnection-com:serviceId:WANPPPConnection1", "wanpppconn1") +
	`</serviceList>
</device></deviceList>
</device>
</deviceList>
</device>
</root>`

const hostList = `<?xml version="1.0" ?>
<List>
<Item><Index>1</Index><IPAddress>192.168.178.20</IPAddress><MACAddress>AA:BB:CC:00:00:01</MACAddress><Active>1</Active><HostName>laptop</HostName><InterfaceType>802.11</InterfaceType><X_AVM-DE_Port>0</X_AVM-DE_Port><X_AVM-DE_Speed>866</X_AVM-DE_Speed><X_AVM-DE_Guest>0</X_AVM-DE_Guest></Item>
<Item><Index>2</Index><IPAddress>192.168.178.3</IPAddress><MACAddress>AA:BB:CC:00:00:02</MACAddress><Active>1</Active><HostName>nas</HostName><InterfaceType>Ethernet</InterfaceType><X_AVM-DE_Port>1</X_AVM-DE_Port><X_AVM-DE_Speed>1000</X_AVM-DE_Speed><X_AVM-DE_Guest>0</X_AVM-DE_Guest></Item>
<Item><Index>3</Index><IPAddress>192.168.179.5</IPAddress><MACAddress>AA:BB:CC:00:00:03</MACAddress><Active>0</Active><HostName>visitor-phone</HostName><InterfaceType>802.11</InterfaceType><X_AVM-DE_Port>0</X_AVM-DE_Port><X_AVM-DE_Speed>0</X_AVM-DE_Speed><X_AVM-DE_Guest>1</X_AVM-DE_Guest></Item>
</List>`

const callList = `<?xml version="1.0" encoding="UTF-8"?>
<root>
<timestamp>1762952000</timestamp>
<Call><Id>3</Id><Type>2</Type><Caller>01701234567</Caller><Called>SIP: 030123456</Called><CalledNumber>030123456</CalledNumber><Name>Alice</Name><Numbertype>sip</Numbertype><Device></Device><Port>-1</Port><Date>12.11.26 14:03</Date><Duration>0:00</Duration><Count></Count><Path /></Call>
<Call><Id>2</Id><Type>3</Type><Caller>SIP: 030123456</Caller><CallerNumber>030123456</CallerNumber><Called>0891234</Called><Name></Name><Numbertype>sip</Numbertype><Device>Kitchen</Device><Port>10</Port><Date>11.11.26 09:15</Date><Duration>0:12</Duration><Count></Count><Path /></Call>
<Call><Id>1</Id><Type>1</Type><Caller>01519876543</Caller><Called>SIP: 030123456</Called><CalledNumber>030123456</CalledNumber><Name>Bob</Name><Numbertype>sip</Numbertype><Device>Kitchen</Device><Port>10</Port><Date>10.11.26 18:40</Date><Duration>1:05</Duration><Count></Count><Path /></Call>
</root>`

const phonebook = `<?xml version="1.0" encoding="utf-8"?>
<phonebooks><phonebook name="Telefonbuch">
<contact><category>0</category><person><realName>Alice</realName></person><telephony nid="2"><number type="mobile" prio="1" id="0">01701234567</number><number type="home" prio="0" id="1">030111222</number></telephony><uniqueid>1</uniqueid></contact>
<contact><category>0</category><person><realName>Bob</realName></person><telephony nid="1"><number type="work" prio="1" id="0">01519876543</number></telephony><uniqueid>2</uniqueid></contact>
</phonebook></phonebooks>`

// deviceList: %s = plug state, %d = thermostat target.
const deviceList = `<devicelist version="1" fwversion="8.02">
<device identifier="11657 0240192" id="16" functionbitmask="35712" fwversion="04.27" manufacturer="AVM" productname="FRITZ!DECT 200"><present>1</present><txbusy>0</txbusy><name>Desk lamp</name><switch><state>%s</state><mode>manuell</mode><lock>0</lock><devicelock>0</devicelock></switch><powermeter><voltage>230000</voltage><power>12500</power><energy>4321</energy></powermeter><temperature><celsius>215</celsius><offset>0</offset></temperature></device>
<device identifier="09995 0335100" id="17" functionbitmask="320" fwversion="05.16" manufacturer="AVM" productname="FRITZ!DECT 301"><present>1</present><txbusy>0</txbusy><name>Living room</name><temperature><celsius>205</celsius><offset>0</offset></temperature><hkr><tist>41</tist><tsoll>%d</tsoll><absenk>34</absenk><komfort>42</komfort><lock>0</lock><devicelock>0</devicelock><errorcode>0</errorcode><windowopenactiv>0</windowopenactiv><battery>80</battery></hkr></device>
</devicelist>`

const deviceLog = `<?xml version="1.0" encoding="utf-8"?>
<root>
<Event><id>1</id><group>net</group><date>12.11.26</date><time>14:05:00</time><msg>Internet connection established successfully. IP address: 203.0.113.7</msg></Event>
<Event><id>2</id><group>wlan</group><date>12.11.26</date><time>13:00:12</time><msg>WLAN device logged on (5 GHz), 866 Mbit/s, laptop, IP 192.168.178.20</msg></Event>
</root>`

// meshList follows AVM's mesh topology schema: router n-1, repeater n-2 (via 5 GHz), laptop on the
// repeater, nas on the router's LAN port. Each link appears under both interfaces, as on real boxes.
const meshList = `{"schema_version":"5.5","nodes":[
{"uid":"n-1","device_name":"fritz.box","device_model":"FRITZ!Box 7590 AX","device_mac_address":"48:5D:35:00:00:01","is_meshed":true,"mesh_role":"master",
 "node_interfaces":[
  {"uid":"ni-1","name":"AP:5G:0","type":"WLAN","mac_address":"48:5D:35:00:00:01","node_links":[
    {"uid":"nl-1","type":"WLAN","state":"CONNECTED","node_1_uid":"n-1","node_2_uid":"n-2","node_interface_1_uid":"ni-1","node_interface_2_uid":"ni-3","max_data_rate_rx":1200000,"max_data_rate_tx":1200000,"cur_data_rate_rx":10000,"cur_data_rate_tx":80000}]},
  {"uid":"ni-2","name":"LAN:1","type":"LAN","mac_address":"48:5D:35:00:00:01","node_links":[
    {"uid":"nl-2","type":"LAN","state":"CONNECTED","node_1_uid":"n-1","node_2_uid":"n-4","node_interface_1_uid":"ni-2","node_interface_2_uid":"ni-6","max_data_rate_rx":1000000,"max_data_rate_tx":1000000,"cur_data_rate_rx":2000,"cur_data_rate_tx":500}]}]},
{"uid":"n-2","device_name":"fritz.repeater","device_model":"FRITZ!Repeater 3000 AX","device_mac_address":"4E:5D:35:00:00:02","is_meshed":true,"mesh_role":"slave",
 "node_interfaces":[
  {"uid":"ni-3","name":"UPLINK:5G:0","type":"WLAN","mac_address":"4E:5D:35:00:00:02","node_links":[
    {"uid":"nl-1","type":"WLAN","state":"CONNECTED","node_1_uid":"n-1","node_2_uid":"n-2","node_interface_1_uid":"ni-1","node_interface_2_uid":"ni-3","max_data_rate_rx":1200000,"max_data_rate_tx":1200000,"cur_data_rate_rx":10000,"cur_data_rate_tx":80000}]},
  {"uid":"ni-4","name":"AP:2G:0","type":"WLAN","mac_address":"4E:5D:35:00:00:02","node_links":[
    {"uid":"nl-3","type":"WLAN","state":"CONNECTED","node_1_uid":"n-3","node_2_uid":"n-2","node_interface_1_uid":"ni-5","node_interface_2_uid":"ni-4","max_data_rate_rx":286000,"max_data_rate_tx":286000,"cur_data_rate_rx":40000,"cur_data_rate_tx":3000},
    {"uid":"nl-4","type":"WLAN","state":"DISCONNECTED","node_1_uid":"n-2","node_2_uid":"n-5","node_interface_1_uid":"ni-4","node_interface_2_uid":"ni-7","max_data_rate_rx":0,"max_data_rate_tx":0,"cur_data_rate_rx":0,"cur_data_rate_tx":0}]}]},
{"uid":"n-3","device_name":"laptop","device_model":"","device_mac_address":"AA:BB:CC:00:00:01","is_meshed":false,"mesh_role":"unknown",
 "node_interfaces":[{"uid":"ni-5","name":"","type":"WLAN","mac_address":"AA:BB:CC:00:00:01","node_links":[
    {"uid":"nl-3","type":"WLAN","state":"CONNECTED","node_1_uid":"n-3","node_2_uid":"n-2","node_interface_1_uid":"ni-5","node_interface_2_uid":"ni-4","max_data_rate_rx":286000,"max_data_rate_tx":286000,"cur_data_rate_rx":40000,"cur_data_rate_tx":3000}]}]},
{"uid":"n-4","device_name":"nas","device_model":"","device_mac_address":"AA:BB:CC:00:00:02","is_meshed":false,"mesh_role":"unknown",
 "node_interfaces":[{"uid":"ni-6","name":"eth0","type":"LAN","mac_address":"AA:BB:CC:00:00:02","node_links":[]}]},
{"uid":"n-5","device_name":"visitor-phone","device_model":"","device_mac_address":"AA:BB:CC:00:00:03","is_meshed":false,"mesh_role":"unknown",
 "node_interfaces":[{"uid":"ni-7","name":"","type":"WLAN","mac_address":"AA:BB:CC:00:00:03","node_links":[]}]}
]}`
