package fritz

import (
	"context"
	"encoding/xml"
	"errors"
	"strings"
	"time"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

const tamSvc = "X_AVM-DE_TAM1"

// TAM is an answering machine.
type TAM struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Display bool   `json:"display"`
}

// TAMs lists answering machines. Hidden (unconfigured) slots are skipped.
func (b *Box) TAMs(ctx context.Context) ([]TAM, error) {
	r, err := b.call(ctx, tamSvc, "GetList", nil)
	if err != nil {
		return nil, err
	}
	return ParseTAMList(r["NewTAMList"])
}

// ParseTAMList parses NewTAMList.
func ParseTAMList(s string) ([]TAM, error) {
	var l struct {
		Items []struct {
			Index   int    `xml:"Index"`
			Display string `xml:"Display"`
			Enable  string `xml:"Enable"`
			Name    string `xml:"Name"`
		} `xml:"Item"`
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	if err := xml.Unmarshal([]byte(s), &l); err != nil {
		return nil, err
	}
	var out []TAM
	for _, it := range l.Items {
		if it.Display == "0" {
			continue
		}
		out = append(out, TAM{Index: it.Index, Name: it.Name, Enabled: bool01(it.Enable), Display: true})
	}
	return out, nil
}

// SetTAM switches an answering machine on or off.
func (b *Box) SetTAM(ctx context.Context, index int, on bool) error {
	_, err := b.call(ctx, tamSvc, "SetEnable", tr064.Args{"NewIndex": itoa(index), "NewEnable": b01(on)})
	return err
}

// Message is an answering machine message.
type Message struct {
	TAM      int       `json:"tam"`
	Index    int       `json:"index"`
	Date     time.Time `json:"date"`
	Number   string    `json:"number"`
	Name     string    `json:"name"`
	Called   string    `json:"called"`
	Duration string    `json:"duration"`
	New      bool      `json:"new"`
}

// ParseMessages parses the message list XML behind GetMessageList.
func ParseMessages(data []byte, loc *time.Location) ([]Message, error) {
	var l struct {
		Messages []struct {
			Index    int    `xml:"Index"`
			Tam      int    `xml:"Tam"`
			Called   string `xml:"Called"`
			Date     string `xml:"Date"`
			Duration string `xml:"Duration"`
			Name     string `xml:"Name"`
			New      string `xml:"New"`
			Number   string `xml:"Number"`
		} `xml:"Message"`
	}
	if err := xml.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(l.Messages))
	for _, m := range l.Messages {
		d, _ := time.ParseInLocation(callDateLayout, strings.TrimSpace(m.Date), loc)
		out = append(out, Message{
			TAM: m.Tam, Index: m.Index, Date: d, Number: m.Number, Name: m.Name,
			Called: m.Called, Duration: m.Duration, New: bool01(m.New),
		})
	}
	return out, nil
}

// Messages lists the messages of all answering machines, newest first.
func (b *Box) Messages(ctx context.Context) ([]Message, error) {
	tams, err := b.TAMs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, t := range tams {
		r, err := b.call(ctx, tamSvc, "GetMessageList", tr064.Args{"NewIndex": itoa(t.Index)})
		if err != nil {
			return nil, err
		}
		data, err := b.TR.Get(ctx, r["NewURL"])
		if err != nil {
			return nil, err
		}
		ms, err := ParseMessages(data, time.Local)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	sortByDateDesc(out)
	return out, nil
}

func sortByDateDesc(ms []Message) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j].Date.After(ms[j-1].Date); j-- {
			ms[j], ms[j-1] = ms[j-1], ms[j]
		}
	}
}

// MarkMessage marks a message as heard (or new again).
func (b *Box) MarkMessage(ctx context.Context, tam, index int, read bool) error {
	_, err := b.call(ctx, tamSvc, "MarkMessage", tr064.Args{"NewIndex": itoa(tam), "NewMessageIndex": itoa(index), "NewMarkedAsRead": b01(read)})
	return err
}

// DeleteMessage deletes a message.
func (b *Box) DeleteMessage(ctx context.Context, tam, index int) error {
	_, err := b.call(ctx, tamSvc, "DeleteMessage", tr064.Args{"NewIndex": itoa(tam), "NewMessageIndex": itoa(index)})
	return err
}

// Deflection is a call forwarding rule. Rules are created in the web UI; TR-064 can only switch them.
type Deflection struct {
	ID       int    `json:"id"`
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`   // fromAll, fromNumber, fromAnonymous, fromPB, toMSN, ...
	Number   string `json:"number"` // the number the rule matches, if any
	To       string `json:"to"`
	Mode     string `json:"mode"` // eImmediately, eShortDelayed, eLongDelayed, eParallelCall, eBusy, ...
	Outgoing string `json:"outgoing,omitempty"`
}

// Describe renders a rule in words.
func (d Deflection) Describe() string {
	from := map[string]string{
		"fromAll": "all calls", "fromAnonymous": "anonymous calls", "fromNotInPhonebook": "callers not in phonebook",
		"fromPB": "callers in phonebook", "fromVIP": "VIP callers", "toMSN": "calls to " + d.Number,
		"fromNumber": "calls from " + d.Number, "toPOTS": "calls to the landline", "toVoIP": "calls to " + d.Number,
	}[d.Type]
	if from == "" {
		from = strings.TrimSpace(d.Type + " " + d.Number)
	}
	when := map[string]string{
		"eImmediately": "immediately", "eShortDelayed": "after a short delay", "eLongDelayed": "after a long delay",
		"eBusy": "when busy", "eDelayedOrBusy": "when busy or not answered", "eParallelCall": "ringing in parallel",
		"eNoSignal": "when unreachable",
	}[d.Mode]
	if when == "" {
		when = d.Mode
	}
	return from + " → " + d.To + " " + when
}

// Deflections lists call forwarding rules.
func (b *Box) Deflections(ctx context.Context) ([]Deflection, error) {
	r, err := b.call(ctx, onTel, "GetDeflections", nil)
	if err != nil {
		return nil, err
	}
	return ParseDeflections(r["NewDeflectionList"])
}

// ParseDeflections parses NewDeflectionList.
func ParseDeflections(s string) ([]Deflection, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var l struct {
		Items []struct {
			ID       int    `xml:"DeflectionId"`
			Enable   string `xml:"Enable"`
			Type     string `xml:"Type"`
			Number   string `xml:"Number"`
			To       string `xml:"DeflectionToNumber"`
			Mode     string `xml:"Mode"`
			Outgoing string `xml:"Outgoing"`
		} `xml:"Item"`
	}
	if err := xml.Unmarshal([]byte(s), &l); err != nil {
		return nil, err
	}
	out := make([]Deflection, 0, len(l.Items))
	for _, it := range l.Items {
		out = append(out, Deflection{ID: it.ID, Enabled: bool01(it.Enable), Type: it.Type, Number: it.Number, To: it.To, Mode: it.Mode, Outgoing: it.Outgoing})
	}
	return out, nil
}

// SetDeflection switches a forwarding rule on or off.
func (b *Box) SetDeflection(ctx context.Context, id int, on bool) error {
	_, err := b.call(ctx, onTel, "SetDeflectionEnable", tr064.Args{"NewDeflectionId": itoa(id), "NewEnable": b01(on)})
	return err
}

const voipSvc = "X_VoIP1"

// Dial makes the box ring the configured phone and, once picked up, call number. Needs
// "Dialing help" (Wählhilfe) enabled under Telephony > Calls > Dialing Help.
func (b *Box) Dial(ctx context.Context, number string) error {
	number = strings.TrimSpace(number)
	if number == "" {
		return errors.New("no number to dial")
	}
	for _, r := range number {
		if !strings.ContainsRune("0123456789+*#", r) {
			return errors.New("a phone number may only contain digits, +, * and #")
		}
	}
	_, err := b.call(ctx, voipSvc, "X_AVM-DE_DialNumber", tr064.Args{"NewX_AVM-DE_PhoneNumber": number})
	return err
}

// Hangup ends a call started with Dial.
func (b *Box) Hangup(ctx context.Context) error {
	_, err := b.call(ctx, voipSvc, "X_AVM-DE_DialHangup", nil)
	return err
}

// DialPhone returns the phone the box rings for Dial.
func (b *Box) DialPhone(ctx context.Context) (string, error) {
	r, err := b.call(ctx, voipSvc, "X_AVM-DE_DialGetConfig", nil)
	if err != nil {
		return "", err
	}
	return r["NewX_AVM-DE_PhoneName"], nil
}

// SetDialPhone chooses the phone the box rings for Dial (e.g. "FON1: Telefon", "DECT: Mobilteil 1").
func (b *Box) SetDialPhone(ctx context.Context, name string) error {
	_, err := b.call(ctx, voipSvc, "X_AVM-DE_DialSetConfig", tr064.Args{"NewX_AVM-DE_PhoneName": name})
	return err
}
