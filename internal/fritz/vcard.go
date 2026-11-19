package fritz

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ParseVCards reads contacts from vCard 2.1, 3.0 or 4.0 data. Contacts without a name or
// without a phone number are skipped and counted in skipped.
func ParseVCards(r io.Reader) (out []Contact, skipped int, err error) {
	lines, err := unfoldVCard(r)
	if err != nil {
		return nil, 0, err
	}
	var cur *Contact
	var family, given string
	for _, line := range lines {
		name, params, value, ok := splitVCardLine(line)
		if !ok {
			continue
		}
		switch name {
		case "BEGIN":
			if strings.EqualFold(value, "VCARD") {
				cur, family, given = &Contact{}, "", ""
			}
		case "END":
			if cur == nil || !strings.EqualFold(value, "VCARD") {
				continue
			}
			if cur.Name == "" {
				cur.Name = strings.TrimSpace(given + " " + family)
			}
			if cur.Name == "" || len(cur.Numbers) == 0 {
				skipped++
			} else {
				out = append(out, *cur)
			}
			cur = nil
		case "FN":
			if cur != nil {
				cur.Name = strings.TrimSpace(unescapeVCard(value))
			}
		case "N":
			if cur != nil {
				parts := splitUnescaped(value, ';')
				if len(parts) > 0 {
					family = parts[0]
				}
				if len(parts) > 1 {
					given = parts[1]
				}
			}
		case "TEL":
			if cur != nil {
				num := NormalizeNumber(strings.TrimPrefix(unescapeVCard(value), "tel:"))
				if num != "" {
					cur.Numbers = append(cur.Numbers, Number{Type: vcardTelType(params), Number: num})
				}
			}
		case "EMAIL":
			if cur != nil {
				if e := strings.TrimSpace(unescapeVCard(value)); e != "" {
					cur.Emails = append(cur.Emails, e)
				}
			}
		}
	}
	return out, skipped, nil
}

// unfoldVCard joins continuation lines (starting with a space or tab) and handles the
// quoted-printable soft line breaks of vCard 2.1.
func unfoldVCard(r io.Reader) ([]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lines []string
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		if len(lines) > 0 && strings.HasSuffix(lines[len(lines)-1], "=") && strings.Contains(strings.ToUpper(lines[len(lines)-1]), "QUOTED-PRINTABLE") {
			lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "=") + l
			continue
		}
		lines = append(lines, l)
	}
	return lines, sc.Err()
}

// splitVCardLine splits "item1.TEL;TYPE=CELL:+49 170" into TEL, [TYPE=CELL], "+49 170".
func splitVCardLine(line string) (name string, params []string, value string, ok bool) {
	head, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", nil, "", false
	}
	parts := strings.Split(head, ";")
	name = strings.ToUpper(parts[0])
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	for _, p := range parts[1:] {
		params = append(params, strings.ToUpper(p))
	}
	if slices.ContainsFunc(params, func(p string) bool { return strings.Contains(p, "QUOTED-PRINTABLE") }) {
		value = decodeQuotedPrintable(value)
	}
	return name, params, value, true
}

func decodeQuotedPrintable(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+2 < len(s) {
			var c byte
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &c); err == nil {
				b.WriteByte(c)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// vcardTelType maps TEL parameters (TYPE=CELL, CELL, type="work,voice", …) to the box's types.
func vcardTelType(params []string) string {
	var types []string
	for _, p := range params {
		p = strings.TrimPrefix(p, "TYPE=")
		for t := range strings.SplitSeq(strings.Trim(p, `"`), ",") {
			types = append(types, t)
		}
	}
	has := func(t string) bool { return slices.Contains(types, t) }
	switch {
	case has("FAX"):
		return "fax_work"
	case has("CELL"), has("MOBILE"), has("IPHONE"):
		return "mobile"
	case has("WORK"):
		return "work"
	case has("HOME"):
		return "home"
	}
	return "other"
}

func unescapeVCard(s string) string {
	r := strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\:`, ":", `\\`, `\`)
	return r.Replace(s)
}

// splitUnescaped splits on sep, ignoring escaped separators, and unescapes the parts.
func splitUnescaped(s string, sep byte) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == sep {
			parts = append(parts, unescapeVCard(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(s[i])
	}
	return append(parts, unescapeVCard(cur.String()))
}

func escapeVCard(s string) string {
	return strings.NewReplacer(`\`, `\\`, ",", `\,`, ";", `\;`, "\n", `\n`).Replace(s)
}

var vcardTypes = map[string]string{"home": "HOME,VOICE", "mobile": "CELL,VOICE", "work": "WORK,VOICE", "fax_work": "WORK,FAX", "other": "VOICE"}

// WriteVCards writes contacts as vCard 3.0.
func WriteVCards(w io.Writer, cs []Contact) error {
	var b bytes.Buffer
	for _, c := range cs {
		b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\n")
		writeFolded(&b, "FN:"+escapeVCard(c.Name))
		writeFolded(&b, "N:;"+escapeVCard(c.Name)+";;;")
		for _, n := range c.Numbers {
			t := vcardTypes[n.Type]
			if t == "" {
				t = "VOICE"
			}
			writeFolded(&b, "TEL;TYPE="+t+":"+n.Number)
		}
		for _, e := range c.Emails {
			writeFolded(&b, "EMAIL;TYPE=INTERNET:"+escapeVCard(e))
		}
		if c.VIP {
			b.WriteString("CATEGORIES:VIP\r\n")
		}
		b.WriteString("END:VCARD\r\n")
	}
	_, err := w.Write(b.Bytes())
	return err
}

// writeFolded writes a content line, folding it at 75 bytes as RFC 6350 asks, without
// splitting a UTF-8 character.
func writeFolded(b *bytes.Buffer, line string) {
	for len(line) > 75 {
		cut := 75
		for cut > 0 && line[cut]&0xC0 == 0x80 {
			cut--
		}
		b.WriteString(line[:cut] + "\r\n ")
		line = line[cut:]
	}
	b.WriteString(line + "\r\n")
}

// CSVHeader is the column order WriteCSV uses and ParseCSV expects when there is no header.
var CSVHeader = []string{"name", "type", "number", "email", "vip"}

// WriteCSV writes one row per number; the email and vip columns are filled on the first row of a contact.
func WriteCSV(w io.Writer, cs []Contact) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	for _, c := range cs {
		for i, n := range c.Numbers {
			email, vip := "", ""
			if i == 0 {
				email = strings.Join(c.Emails, " ")
				if c.VIP {
					vip = "1"
				}
			}
			if err := cw.Write([]string{c.Name, n.Type, n.Number, email, vip}); err != nil {
				return err
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

// ParseCSV reads contacts from CSV. With a header row, columns are found by name (name,
// number, type, email, vip; extra columns are ignored); without one they are name,type,number.
// Rows with the same name become one contact with several numbers.
func ParseCSV(r io.Reader) ([]Contact, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	col := map[string]int{"name": 0, "type": 1, "number": 2, "email": -1, "vip": -1}
	if slices.ContainsFunc(rows[0], func(h string) bool { return strings.EqualFold(strings.TrimSpace(h), "number") }) {
		col = map[string]int{"name": -1, "type": -1, "number": -1, "email": -1, "vip": -1}
		for i, h := range rows[0] {
			if _, ok := col[strings.ToLower(strings.TrimSpace(h))]; ok {
				col[strings.ToLower(strings.TrimSpace(h))] = i
			}
		}
		if col["name"] < 0 {
			return nil, errors.New("CSV header has a number column but no name column")
		}
		rows = rows[1:]
	}
	get := func(row []string, k string) string {
		if i := col[k]; i >= 0 && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	var out []Contact
	index := map[string]int{}
	for n, row := range rows {
		name, num := get(row, "name"), NormalizeNumber(get(row, "number"))
		if name == "" && num == "" {
			continue
		}
		if name == "" || num == "" {
			return nil, fmt.Errorf("CSV row %d needs both a name and a number", n+1)
		}
		typ := strings.ToLower(get(row, "type"))
		if typ == "" {
			typ = "home"
		}
		i, ok := index[strings.ToLower(name)]
		if !ok {
			i = len(out)
			index[strings.ToLower(name)] = i
			out = append(out, Contact{Name: name})
		}
		out[i].Numbers = append(out[i].Numbers, Number{Type: typ, Number: num})
		for e := range strings.FieldsSeq(get(row, "email")) {
			out[i].Emails = append(out[i].Emails, e)
		}
		if v := get(row, "vip"); v == "1" || strings.EqualFold(v, "yes") || strings.EqualFold(v, "true") {
			out[i].VIP = true
		}
	}
	return out, nil
}
