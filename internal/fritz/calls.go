package fritz

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mkrajinovic/fbctl/internal/tr064"
)

// Call types as used by the AVM call list.
const (
	CallIn           = 1
	CallMissed       = 2
	CallOut          = 3
	CallActiveIn     = 9
	CallRejected     = 10
	CallActiveOut    = 11
	onTel            = "X_AVM-DE_OnTel1"
	callDateLayout   = "02.01.06 15:04"
	callListMaxItems = 999
)

// CallTypeName renders a call type.
func CallTypeName(t int) string {
	switch t {
	case CallIn:
		return "in"
	case CallMissed:
		return "missed"
	case CallOut:
		return "out"
	case CallActiveIn:
		return "active-in"
	case CallRejected:
		return "rejected"
	case CallActiveOut:
		return "active-out"
	}
	return strconv.Itoa(t)
}

// Call is one call list entry.
type Call struct {
	ID       int           `json:"id"`
	Type     int           `json:"type"`
	TypeName string        `json:"type_name"`
	Number   string        `json:"number"` // the remote party
	Name     string        `json:"name,omitempty"`
	Own      string        `json:"own_number,omitempty"`
	Device   string        `json:"device,omitempty"`
	Date     time.Time     `json:"date"`
	Duration time.Duration `json:"duration_ns"`
}

type callList struct {
	Calls []struct {
		ID           string `xml:"Id"`
		Type         string `xml:"Type"`
		Caller       string `xml:"Caller"`
		Called       string `xml:"Called"`
		CallerNumber string `xml:"CallerNumber"`
		CalledNumber string `xml:"CalledNumber"`
		Name         string `xml:"Name"`
		Device       string `xml:"Device"`
		Date         string `xml:"Date"`
		Duration     string `xml:"Duration"`
	} `xml:"Call"`
}

// ParseCallList parses the XML behind GetCallList.
func ParseCallList(data []byte, loc *time.Location) ([]Call, error) {
	var l callList
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	out := make([]Call, 0, len(l.Calls))
	for _, c := range l.Calls {
		t := atoi(c.Type)
		call := Call{ID: atoi(c.ID), Type: t, TypeName: CallTypeName(t), Name: c.Name, Device: c.Device}
		if t == CallOut || t == CallActiveOut {
			call.Number, call.Own = c.Called, c.CallerNumber
		} else {
			call.Number, call.Own = c.Caller, c.CalledNumber
		}
		call.Date, _ = time.ParseInLocation(callDateLayout, c.Date, loc)
		if h, m, ok := strings.Cut(c.Duration, ":"); ok {
			call.Duration = time.Duration(atoi(h))*time.Hour + time.Duration(atoi(m))*time.Minute
		}
		out = append(out, call)
	}
	return out, nil
}

// Calls returns the call list. days > 0 limits to the last N days; types filters (empty = all).
func (b *Box) Calls(ctx context.Context, days int, types ...int) ([]Call, error) {
	r, err := b.call(ctx, onTel, "GetCallList", nil)
	if err != nil {
		return nil, err
	}
	u := r["NewCallListURL"]
	if days > 0 {
		u += "&days=" + strconv.Itoa(days)
	} else {
		u += "&max=" + strconv.Itoa(callListMaxItems)
	}
	data, err := b.TR.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	calls, err := ParseCallList(data, time.Local)
	if err != nil || len(types) == 0 {
		return calls, err
	}
	keep := map[int]bool{}
	for _, t := range types {
		keep[t] = true
	}
	filtered := calls[:0]
	for _, c := range calls {
		if keep[c.Type] {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

// Phonebook metadata.
type Phonebook struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	url  string
}

// Number is a contact phone number.
type Number struct {
	Type   string `json:"type"` // home, mobile, work, fax_work, ...
	Number string `json:"number"`
}

// Contact is a phonebook entry.
type Contact struct {
	Name    string   `json:"name"`
	Numbers []Number `json:"numbers"`
}

// Phonebooks lists phonebooks.
func (b *Box) Phonebooks(ctx context.Context) ([]Phonebook, error) {
	r, err := b.call(ctx, onTel, "GetPhonebookList", nil)
	if err != nil {
		return nil, err
	}
	var out []Phonebook
	for s := range strings.SplitSeq(r["NewPhonebookList"], ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		id, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		pb, err := b.phonebook(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, pb)
	}
	return out, nil
}

func (b *Box) phonebook(ctx context.Context, id int) (Phonebook, error) {
	r, err := b.call(ctx, onTel, "GetPhonebook", tr064.Args{"NewPhonebookId": strconv.Itoa(id)})
	if err != nil {
		return Phonebook{}, err
	}
	return Phonebook{ID: id, Name: r["NewPhonebookName"], url: r["NewPhonebookURL"]}, nil
}

type phonebookXML struct {
	Contacts []struct {
		Name    string `xml:"person>realName"`
		Numbers []struct {
			Type   string `xml:"type,attr"`
			Number string `xml:",chardata"`
		} `xml:"telephony>number"`
	} `xml:"phonebook>contact"`
}

// ParsePhonebook parses a phonebook XML export.
func ParsePhonebook(data []byte) ([]Contact, error) {
	var p phonebookXML
	if err := xml.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	out := make([]Contact, 0, len(p.Contacts))
	for _, c := range p.Contacts {
		ct := Contact{Name: c.Name}
		for _, n := range c.Numbers {
			ct.Numbers = append(ct.Numbers, Number{Type: n.Type, Number: strings.TrimSpace(n.Number)})
		}
		out = append(out, ct)
	}
	return out, nil
}

// Contacts returns the entries of a phonebook.
func (b *Box) Contacts(ctx context.Context, id int) (Phonebook, []Contact, error) {
	pb, err := b.phonebook(ctx, id)
	if err != nil {
		return pb, nil, err
	}
	if _, err := url.Parse(pb.url); err != nil || pb.url == "" {
		return pb, nil, fmt.Errorf("phonebook %d has no download URL", id)
	}
	data, err := b.TR.Get(ctx, pb.url)
	if err != nil {
		return pb, nil, err
	}
	c, err := ParsePhonebook(data)
	return pb, c, err
}
