package tr064

import (
	"strings"
	"testing"
)

func TestNormalizeServiceName(t *testing.T) {
	for in, want := range map[string]string{
		"WLANConfiguration":   "WLANConfiguration1",
		"WLANConfiguration:2": "WLANConfiguration2",
		"DeviceInfo1":         "DeviceInfo1",
		"X_AVM-DE_OnTel":      "X_AVM-DE_OnTel1",
	} {
		if got := NormalizeServiceName(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
}

func TestEnvelopeEscapesAndSorts(t *testing.T) {
	b := string(buildEnvelope("urn:dslforum-org:service:WLANConfiguration:1", "SetSSID", Args{"NewSSID": `a<b&"c"`, "A": "1"}))
	if !strings.Contains(b, "<NewSSID>a&lt;b&amp;&#34;c&#34;</NewSSID>") {
		t.Errorf("not escaped: %s", b)
	}
	if strings.Index(b, "<A>") > strings.Index(b, "<NewSSID>") {
		t.Errorf("args not sorted: %s", b)
	}
}

func TestParseResponse(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<u:GetInfoResponse xmlns:u="urn:dslforum-org:service:DeviceInfo:1">
<NewModelName>FRITZ!Box 7590</NewModelName><NewDescription></NewDescription><NewDeviceLog>a &amp; b</NewDeviceLog>
</u:GetInfoResponse></s:Body></s:Envelope>`)
	r, err := parseResponse(data, "GetInfo")
	if err != nil {
		t.Fatal(err)
	}
	if r["NewModelName"] != "FRITZ!Box 7590" || r["NewDeviceLog"] != "a & b" {
		t.Errorf("%v", r)
	}
	if v, ok := r["NewDescription"]; !ok || v != "" {
		t.Errorf("empty element should be present: %v", r)
	}
	if _, err := parseResponse(data, "Other"); err == nil {
		t.Error("expected error for missing response element")
	}
}

func TestParseFault(t *testing.T) {
	f := parseFault([]byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>866</errorCode><errorDescription>second factor authentication required</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`))
	if f == nil || f.Code != 866 {
		t.Fatalf("%+v", f)
	}
}

func TestRedact(t *testing.T) {
	in := `<NewKeyPassphrase>hunter2</NewKeyPassphrase> /calllist.lua?sid=abcdef0123`
	out := redact(in)
	if strings.Contains(out, "hunter2") || strings.Contains(out, "abcdef0123") {
		t.Errorf("secrets leaked: %s", out)
	}
}

func TestPinnedTLS(t *testing.T) {
	cfg := PinnedTLSConfig("AB:CD")
	if err := cfg.VerifyPeerCertificate([][]byte{[]byte("x")}, nil); err == nil {
		t.Error("mismatching fingerprint must be rejected")
	}
	fp := Fingerprint([]byte("x"))
	if err := PinnedTLSConfig(strings.ToUpper(fp)).VerifyPeerCertificate([][]byte{[]byte("x")}, nil); err != nil {
		t.Errorf("matching fingerprint rejected: %v", err)
	}
}
