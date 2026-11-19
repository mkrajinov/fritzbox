// Package capture records a box's responses to a fixed list of read-only TR-064 actions, scrubs
// personal data and secrets, and checks them with fb's parsers. The result is meant to be
// reviewed and then used as test data.
package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// Step is one read-only action. Doc names the out argument holding a URL to download and
// DocKind how to scrub it.
type Step struct {
	Service, Action string
	Args            tr064.Args
	Doc, DocKind    string
	DocFile         string
}

// Steps lists only actions that read. Never add writes or actions that reveal secrets
// (GetSecurityKeys, X_AVM-DE_GetConfigFile, X_AVM-DE_CreateUrlSID), start something
// (X_AVM-DE_CheckUpdate, X_AVM-DE_Auth) or change state.
func Steps(wlans []int) []Step {
	steps := []Step{
		{Service: "DeviceInfo1", Action: "GetInfo"},
		{Service: "Hosts1", Action: "GetHostNumberOfEntries"},
		{Service: "Hosts1", Action: "X_AVM-DE_GetHostListPath", Doc: "NewX_AVM-DE_HostListPath", DocKind: "hosts", DocFile: "hostlist.xml"},
		{Service: "Hosts1", Action: "X_AVM-DE_GetMeshListPath", Doc: "NewX_AVM-DE_MeshListPath", DocKind: "mesh", DocFile: "meshlist.json"},
	}
	for _, i := range wlans {
		svc := "WLANConfiguration" + strconv.Itoa(i)
		steps = append(steps,
			Step{Service: svc, Action: "GetInfo"},
			Step{Service: svc, Action: "GetTotalAssociations"},
			Step{Service: svc, Action: "GetGenericAssociatedDeviceInfo", Args: tr064.Args{"NewAssociatedDeviceIndex": "0"}},
			Step{Service: svc, Action: "X_AVM-DE_GetWLANDeviceListPath", Doc: "NewX_AVM-DE_WLANDeviceListPath", DocKind: "wlandevices", DocFile: "wlandevicelist" + strconv.Itoa(i) + ".xml"},
			Step{Service: svc, Action: "GetChannelInfo"},
			Step{Service: svc, Action: "GetBeaconAdvertisement"},
			Step{Service: svc, Action: "X_AVM-DE_GetWLANExtInfo"},
		)
	}
	return append(steps,
		Step{Service: "DeviceInfo1", Action: "X_AVM-DE_GetDeviceLogPath", Doc: "NewDeviceLogPath", DocKind: "log", DocFile: "devicelog.xml"},
		Step{Service: "WANCommonInterfaceConfig1", Action: "GetCommonLinkProperties"},
		Step{Service: "WANCommonInterfaceConfig1", Action: "GetTotalBytesSent"},
		Step{Service: "WANCommonInterfaceConfig1", Action: "GetTotalBytesReceived"},
		Step{Service: "WANCommonInterfaceConfig1", Action: "X_AVM-DE_GetOnlineMonitor", Args: tr064.Args{"NewSyncGroupIndex": "0"}},
		Step{Service: "Layer3Forwarding1", Action: "GetDefaultConnectionService"},
		Step{Service: "WANIPConnection1", Action: "GetStatusInfo"},
		Step{Service: "WANIPConnection1", Action: "GetExternalIPAddress"},
		Step{Service: "WANIPConnection1", Action: "X_AVM_DE_GetExternalIPv6Address"},
		Step{Service: "WANPPPConnection1", Action: "GetStatusInfo"},
		Step{Service: "X_AVM-DE_HostFilter1", Action: "GetFilterProfiles"},
		Step{Service: "X_AVM-DE_OnTel1", Action: "GetCallList", Doc: "NewCallListURL", DocKind: "calls", DocFile: "calllist.xml"},
		Step{Service: "X_AVM-DE_OnTel1", Action: "GetPhonebookList"},
		Step{Service: "X_AVM-DE_OnTel1", Action: "GetPhonebook", Args: tr064.Args{"NewPhonebookID": "0"}, Doc: "NewPhonebookURL", DocKind: "phonebook", DocFile: "phonebook0.xml"},
		Step{Service: "X_AVM-DE_OnTel1", Action: "GetCallBarringList", Doc: "NewPhonebookURL", DocKind: "barring", DocFile: "callbarring.xml"},
		Step{Service: "X_AVM-DE_OnTel1", Action: "GetDeflections"},
		Step{Service: "X_AVM-DE_TAM1", Action: "GetList"},
		Step{Service: "X_AVM-DE_TAM1", Action: "GetMessageList", Args: tr064.Args{"NewIndex": "0"}, Doc: "NewURL", DocKind: "tam", DocFile: "tammessages0.xml"},
		Step{Service: "X_VoIP1", Action: "X_AVM-DE_DialGetConfig"},
		Step{Service: "UserInterface1", Action: "GetInfo"},
		Step{Service: "UserInterface1", Action: "X_AVM-DE_GetInfo"},
		Step{Service: "LANHostConfigManagement1", Action: "GetInfo"},
		Step{Service: "X_AVM-DE_RemoteAccess1", Action: "GetInfo"},
		Step{Service: "X_AVM-DE_RemoteAccess1", Action: "GetDDNSInfo"},
		Step{Service: "X_AVM-DE_MyFritz1", Action: "GetInfo"},
	)
}

// forbidden actions are refused even if someone adds them to Steps.
func forbidden(action string) bool {
	for _, p := range []string{"Set", "Add", "Delete", "Do", "Force", "Reboot", "Request", "Wake", "Dial", "Create", "Start", "Stop", "Disallow", "Mark", "Apply", "Factory", "Update", "Upload"} {
		if strings.HasPrefix(action, p) || strings.HasPrefix(action, "X_AVM-DE_"+p) || strings.HasPrefix(action, "X_AVM_DE_"+p) {
			return action != "X_AVM-DE_DialGetConfig"
		}
	}
	switch action {
	case "GetSecurityKeys", "X_AVM-DE_GetConfigFile", "X_AVM-DE_CreateUrlSID", "X_AVM-DE_CheckUpdate":
		return true
	}
	return false
}

// Result summarises a capture.
type Result struct {
	Dir      string
	Calls    int
	Failed   []string // actions the box refused (missing service, no permission)
	Docs     int
	Findings []string // parser check results
}

type record struct {
	Service string            `json:"service"`
	Action  string            `json:"action"`
	Args    tr064.Args        `json:"args,omitempty"`
	Result  map[string]string `json:"result,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// Run executes the steps against the box and writes the scrubbed results to dir.
func Run(ctx context.Context, c *tr064.Client, dir string, s *Scrubber, progress func(string)) (Result, error) {
	res := Result{Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "soap"), 0o700); err != nil {
		return res, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o700); err != nil {
		return res, err
	}
	var wlans []int
	for i := 1; i <= 4; i++ {
		if c.HasService(ctx, "WLANConfiguration"+strconv.Itoa(i)) {
			wlans = append(wlans, i)
		}
	}
	docs := map[string][]byte{}
	// The host list goes first, so device names are known when other documents mention them.
	for _, st := range Steps(wlans) {
		if forbidden(st.Action) {
			return res, fmt.Errorf("refusing %s: not a read-only action", st.Action)
		}
		if !c.HasService(ctx, st.Service) {
			res.Failed = append(res.Failed, st.Service+" (service not offered)")
			continue
		}
		progress(st.Service + " " + st.Action)
		out, err := c.Call(ctx, st.Service, st.Action, st.Args)
		rec := record{Service: st.Service, Action: st.Action, Args: st.Args}
		res.Calls++
		if err != nil {
			rec.Error = s.Text(err.Error())
			res.Failed = append(res.Failed, st.Service+" "+st.Action+": "+rec.Error)
		} else {
			rec.Result = map[string]string{}
			for k, v := range out {
				rec.Result[k] = s.Value(k, v)
			}
		}
		name := fmt.Sprintf("%s.%s", st.Service, st.Action)
		if len(st.Args) > 0 {
			keys := make([]string, 0, len(st.Args))
			for k := range st.Args {
				keys = append(keys, st.Args[k])
			}
			sort.Strings(keys)
			name += "." + strings.Join(keys, ".")
		}
		if err := writeJSON(filepath.Join(dir, "soap", name+".json"), rec); err != nil {
			return res, err
		}
		if err != nil || st.Doc == "" || out[st.Doc] == "" {
			continue
		}
		data, gerr := c.Get(ctx, out[st.Doc])
		if gerr != nil {
			res.Failed = append(res.Failed, "download "+st.DocFile+": "+s.Text(gerr.Error()))
			continue
		}
		clean := []byte(s.Doc(st.DocKind, string(data)))
		docs[st.DocFile] = clean
		if err := os.WriteFile(filepath.Join(dir, "docs", st.DocFile), clean, 0o600); err != nil {
			return res, err
		}
		res.Docs++
	}
	res.Findings = Check(docs, dir)
	return res, writeReadme(dir, res)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Check runs fb's parsers over the captured documents and SOAP results and reports what they
// found, so formats fb only guessed can be confirmed or fixed.
func Check(docs map[string][]byte, dir string) []string {
	var out []string
	note := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	if d, ok := docs["hostlist.xml"]; ok {
		hs, err := fritz.ParseHostList(d)
		note("host list: %d hosts, err=%v", len(hs), err)
	}
	if d, ok := docs["meshlist.json"]; ok {
		ns, err := fritz.ParseMesh(d)
		note("mesh: %d nodes, err=%v", len(ns), err)
	}
	for name, d := range docs {
		if !strings.HasPrefix(name, "wlandevicelist") {
			continue
		}
		cs, err := fritz.ParseWLANDeviceList(d)
		lo, hi := 1<<30, -1<<30
		for _, c := range cs {
			lo, hi = min(lo, c.Signal), max(hi, c.Signal)
		}
		if len(cs) == 0 {
			lo, hi = 0, 0
		}
		note("%s: %d clients, signal %d…%d (fb assumes 0–100), err=%v", name, len(cs), lo, hi, err)
	}
	if d, ok := docs["calllist.xml"]; ok {
		cs, err := fritz.ParseCallList(d, time.Local)
		note("call list: %d calls, err=%v", len(cs), err)
	}
	for _, name := range []string{"phonebook0.xml", "callbarring.xml"} {
		if d, ok := docs[name]; ok {
			cs, err := fritz.ParsePhonebook(d)
			ids := 0
			for _, c := range cs {
				if c.ID != 0 {
					ids++
				}
			}
			note("%s: %d entries, %d with uniqueid, err=%v", name, len(cs), ids, err)
		}
	}
	if d, ok := docs["tammessages0.xml"]; ok {
		ms, err := fritz.ParseMessages(d, time.Local)
		paths := 0
		for _, m := range ms {
			if m.Path != "" {
				paths++
			}
		}
		note("answering machine 0: %d messages, %d with a recording path, err=%v", len(ms), paths, err)
	}
	if d, ok := docs["devicelog.xml"]; ok {
		evs, err := fritz.ParseDeviceLog(d, time.Local)
		note("event log: %d events, err=%v", len(evs), err)
	}
	if rec, ok := readRecord(dir, "X_AVM-DE_HostFilter1.GetFilterProfiles"); ok && rec.Error == "" {
		ps, err := fritz.ParseFilterProfiles(rec.Result["NewFilterProfileList"])
		note("access profiles: %d, err=%v", len(ps), err)
	}
	sort.Strings(out)
	return out
}

func readRecord(dir, name string) (record, bool) {
	var r record
	data, err := os.ReadFile(filepath.Join(dir, "soap", name+".json"))
	if err != nil || json.Unmarshal(data, &r) != nil {
		return r, false
	}
	return r, true
}

func writeReadme(dir string, r Result) error {
	var b strings.Builder
	b.WriteString("fritzbox capture, " + time.Now().Format("2006-01-02 15:04") + "\n\n")
	b.WriteString("Responses of read-only TR-064 actions (soap/) and the documents they point to (docs/).\n")
	b.WriteString("Session IDs, passwords, keys, serial numbers, MAC addresses, public IPs, phone numbers,\n")
	b.WriteString("caller and contact names, email addresses and SSIDs were replaced by placeholders.\n")
	b.WriteString("Review the files before sharing them; device names are kept unless --hide-device-names.\n\n")
	fmt.Fprintf(&b, "%d actions, %d documents\n\nParser checks:\n", r.Calls, r.Docs)
	for _, f := range r.Findings {
		b.WriteString("  " + f + "\n")
	}
	if len(r.Failed) > 0 {
		b.WriteString("\nNot captured:\n")
		for _, f := range r.Failed {
			b.WriteString("  " + f + "\n")
		}
	}
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(b.String()), 0o600)
}
