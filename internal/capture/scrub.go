package capture

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// Scrubber replaces personal data and secrets with placeholders. The same input always maps to
// the same placeholder, so a MAC in the host list still matches it in the WLAN device list, and a
// caller's name in the call list matches the phonebook.
type Scrubber struct {
	HideDeviceNames bool

	maps     map[string]map[string]string // kind → original → placeholder
	counters map[string]int
}

// NewScrubber returns an empty scrubber.
func NewScrubber(hideDeviceNames bool) *Scrubber {
	return &Scrubber{HideDeviceNames: hideDeviceNames, maps: map[string]map[string]string{}, counters: map[string]int{}}
}

func (s *Scrubber) alias(kind, orig string, make func(n int) string) string {
	m := s.maps[kind]
	if m == nil {
		m = map[string]string{}
		s.maps[kind] = m
	}
	if a, ok := m[orig]; ok {
		return a
	}
	s.counters[kind]++
	a := make(s.counters[kind])
	m[orig] = a
	return a
}

var (
	sidRe   = regexp.MustCompile(`(?i)(sid=)[0-9a-f]{4,}`)
	macRe   = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b`)
	ipv4Re  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re  = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}(?:/\d{1,3})?`)
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	uuidRe  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	fqdnRe  = regexp.MustCompile(`(?i)\b[a-z0-9-]+\.(?:myfritz\.net|dyndns\.org|duckdns\.org|ddns\.net|no-ip\.[a-z]+)\b`)
	// Long digit runs in free text (the event log): phone numbers.
	phoneTextRe = regexp.MustCompile(`(?:\+|\b0)\d[\d ]{5,}\d\b`)
)

// Text scrubs identifiers that can appear anywhere: session IDs, MAC, IP and email addresses,
// UUIDs and DynDNS/MyFRITZ names. Private IPv4 addresses are kept (they help reading the data).
func (s *Scrubber) Text(t string) string {
	t = sidRe.ReplaceAllString(t, "${1}0000000000000000")
	t = macRe.ReplaceAllStringFunc(t, func(m string) string {
		return s.alias("mac", strings.ToUpper(m), func(n int) string { return fmt.Sprintf("02:00:00:00:%02X:%02X", n>>8, n&0xff) })
	})
	t = uuidRe.ReplaceAllStringFunc(t, func(m string) string {
		return s.alias("uuid", strings.ToLower(m), func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) })
	})
	t = emailRe.ReplaceAllStringFunc(t, func(m string) string {
		return s.alias("email", strings.ToLower(m), func(n int) string { return fmt.Sprintf("user%d@example.com", n) })
	})
	t = fqdnRe.ReplaceAllStringFunc(t, func(m string) string {
		parts := strings.SplitN(m, ".", 2)
		return s.alias("fqdn", strings.ToLower(m), func(n int) string { return fmt.Sprintf("host%d.%s", n, parts[1]) })
	})
	t = ipv4Re.ReplaceAllStringFunc(t, func(m string) string {
		a, err := netip.ParseAddr(m)
		if err != nil || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || isMask(a) || a.IsMulticast() {
			return m
		}
		return s.alias("ipv4", m, func(n int) string { return fmt.Sprintf("203.0.113.%d", n%250+1) })
	})
	t = ipv6Re.ReplaceAllStringFunc(t, func(m string) string {
		addr, suffix, _ := strings.Cut(m, "/")
		a, err := netip.ParseAddr(addr)
		if err != nil || a.Is4() || a.IsLoopback() || a.IsUnspecified() {
			return m
		}
		if suffix != "" {
			suffix = "/" + suffix
		}
		if a.IsLinkLocalUnicast() {
			return s.alias("ipv6ll", strings.ToLower(addr), func(n int) string { return fmt.Sprintf("fe80::%x", n) }) + suffix
		}
		return s.alias("ipv6", strings.ToLower(addr), func(n int) string { return fmt.Sprintf("2001:db8::%x", n) }) + suffix
	})
	return t
}

func isMask(a netip.Addr) bool {
	b := a.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	return v != 0 && (^v)&((^v)+1) == 0 // contiguous ones followed by zeros
}

// Phone replaces a phone number, keeping its shape: a "SIP: " prefix, a leading + or 0, the
// length and internal numbers (**…). Empty values stay empty.
func (s *Scrubber) Phone(v string) string {
	t := strings.TrimSpace(v)
	if t == "" || strings.HasPrefix(t, "**") || strings.Trim(t, "0123456789+#* -/().") != "" && !strings.HasPrefix(t, "SIP") {
		return v
	}
	prefix := ""
	if i := strings.IndexAny(t, "+0123456789"); i > 0 {
		prefix, t = t[:i], t[i:]
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, t)
	if len(digits) < 3 {
		return v
	}
	return prefix + s.alias("phone", digits, func(n int) string {
		lead := ""
		switch {
		case strings.HasPrefix(t, "+"):
			lead = "+49"
		case strings.HasPrefix(t, "0"):
			lead = "0"
		}
		num := fmt.Sprintf("%d", 5550000+n)
		for len(lead)+len(num) < len(digits) {
			num = "0" + num
		}
		return lead + num
	})
}

// Person replaces a contact or caller name.
func (s *Scrubber) Person(v string) string {
	if strings.TrimSpace(v) == "" {
		return v
	}
	return s.alias("person", strings.TrimSpace(v), func(n int) string { return fmt.Sprintf("Contact %d", n) })
}

// SSID replaces a network name.
func (s *Scrubber) SSID(v string) string {
	if strings.TrimSpace(v) == "" {
		return v
	}
	return s.alias("ssid", v, func(n int) string { return fmt.Sprintf("SSID-%d", n) })
}

// Device replaces a device name when HideDeviceNames is set.
func (s *Scrubber) Device(v string) string {
	if !s.HideDeviceNames || strings.TrimSpace(v) == "" {
		return v
	}
	return s.alias("device", v, func(n int) string { return fmt.Sprintf("device-%d", n) })
}

// Known replaces every name seen so far (in other documents) and phone numbers inside free text.
func (s *Scrubber) Known(t string) string {
	return phoneTextRe.ReplaceAllStringFunc(s.knownNames(t), s.Phone)
}

// knownNames replaces every person, SSID and device name seen so far.
func (s *Scrubber) knownNames(t string) string {
	type pair struct{ from, to string }
	var pairs []pair
	for _, kind := range []string{"person", "ssid", "device"} {
		for from, to := range s.maps[kind] {
			if len(from) >= 3 {
				pairs = append(pairs, pair{from, to})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return len(pairs[i].from) > len(pairs[j].from) }) // longest first
	for _, p := range pairs {
		t = strings.ReplaceAll(t, p.from, p.to)
	}
	return t
}

// Element-level scrubbing of XML documents and SOAP values.

var (
	phoneTags  = []string{"Caller", "Called", "CallerNumber", "CalledNumber", "Number", "number", "DeflectionToNumber", "Outgoing", "PhoneNumbers"}
	personTags = []string{"Name", "realName"}
	ssidTags   = []string{"SSID", "ssid"}
	deviceTags = []string{"HostName", "hostname", "name", "Device"}
)

func tagRe(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(<` + tag + `(?:\s[^>]*)?>)([^<]*)(</` + tag + `>)`)
}

var tagRes = map[string]*regexp.Regexp{}

func tagFor(tag string) *regexp.Regexp {
	re, ok := tagRes[tag]
	if !ok {
		re = tagRe(tag)
		tagRes[tag] = re
	}
	return re
}

func (s *Scrubber) replaceTags(doc string, tags []string, f func(string) string) string {
	for _, t := range tags {
		re := tagFor(t)
		doc = re.ReplaceAllStringFunc(doc, func(m string) string {
			p := re.FindStringSubmatch(m)
			return p[1] + xmlEscapeText(f(xmlUnescapeText(p[2]))) + p[3]
		})
	}
	return doc
}

var jsonDeviceName = regexp.MustCompile(`("device_name"\s*:\s*")([^"]*)(")`)

// Doc scrubs a downloaded document (XML or JSON). kind selects which tags hold people or devices:
// in host and WLAN device lists <Name>/<name> is a device, in call lists and phonebooks a person.
func (s *Scrubber) Doc(kind, doc string) string {
	switch kind {
	case "hosts", "wlandevices", "mesh", "tr64desc":
		doc = s.replaceTags(doc, deviceTags, s.Device)
		doc = s.replaceTags(doc, []string{"Name"}, s.Device)
		doc = jsonDeviceName.ReplaceAllStringFunc(doc, func(m string) string {
			p := jsonDeviceName.FindStringSubmatch(m)
			return p[1] + s.Device(p[2]) + p[3]
		})
		doc = s.replaceTags(doc, []string{"serialNumber", "UDN", "presentationURL"}, func(v string) string { return "REDACTED" })
	case "calls", "phonebook", "tam", "barring", "deflections":
		doc = s.replaceTags(doc, phoneTags, s.Phone)
		doc = s.replaceTags(doc, personTags, s.Person)
		doc = s.replaceTags(doc, []string{"Device"}, s.Device)
	case "log":
		doc = s.Known(doc)
	}
	doc = s.replaceTags(doc, ssidTags, s.SSID)
	// Names learned from earlier documents may appear in elements not listed above.
	return s.Text(s.knownNames(doc))
}

// secretKey reports whether a SOAP argument holds a secret.
func secretKey(k string) bool {
	l := strings.ToLower(k)
	for _, w := range []string{"password", "passphrase", "presharedkey", "wepkey", "pin", "token", "secret", "keypassphrase"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

// Value scrubs one SOAP out argument.
func (s *Scrubber) Value(key, v string) string {
	l := strings.ToLower(key)
	switch {
	case v == "":
		return v
	case secretKey(key):
		return "REDACTED"
	case strings.Contains(l, "serialnumber"), strings.Contains(l, "username") && l != "newx_avm-de_userlist":
		return "REDACTED"
	case l == "newssid":
		return s.SSID(v)
	case strings.Contains(l, "phonenumber") || l == "newphonenumbers":
		var out []string
		for p := range strings.SplitSeq(v, ",") {
			out = append(out, s.Phone(p))
		}
		return strings.Join(out, ",")
	case l == "newhostname" || l == "newx_avm-de_phonename":
		return s.Device(v)
	case strings.HasPrefix(strings.TrimSpace(v), "<"):
		// Embedded XML lists (TAM list, deflections, filter profiles, user list).
		v = s.replaceTags(v, phoneTags, s.Phone)
		v = s.replaceTags(v, ssidTags, s.SSID)
		if !strings.Contains(v, "<Username") {
			v = s.replaceTags(v, []string{"Name"}, s.Device)
		} else {
			v = tagRe("Username").ReplaceAllString(v, "${1}user${3}")
		}
	}
	return s.Text(v)
}

func xmlEscapeText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func xmlUnescapeText(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&").Replace(s)
}
