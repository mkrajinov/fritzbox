package fakebox

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Book is a fake phonebook. Entries are <contact> XML elements including <uniqueid>.
type Book struct {
	Name     string
	ReadOnly bool // online phonebook: writes fail with 820
	Entries  []string
}

var (
	uidRe    = regexp.MustCompile(`<uniqueid>\s*(\d+)\s*</uniqueid>`)
	numberRe = regexp.MustCompile(`<number[^>]*>([^<]*)</number>`)
)

func defaultBooks() map[int]*Book {
	return map[int]*Book{
		0: {Name: "Telefonbuch", Entries: []string{
			`<contact><category>0</category><person><realName>Alice</realName></person><telephony nid="2"><services><email classifier="private" id="0">alice@example.com</email></services><number type="mobile" prio="1" id="0" quickdial="7">01701234567</number><number type="home" prio="0" id="1">030111222</number></telephony><uniqueid>1</uniqueid></contact>`,
			`<contact><category>0</category><person><realName>Bob</realName></person><telephony nid="1"><number type="work" prio="1" id="0">01519876543</number></telephony><uniqueid>2</uniqueid></contact>`,
		}},
		1: {Name: "Google", ReadOnly: true, Entries: []string{
			`<contact><category>0</category><person><realName>Carol</realName></person><telephony nid="1"><number type="mobile" prio="1" id="0">01605550001</number></telephony><uniqueid>1</uniqueid></contact>`,
		}},
	}
}

func renderBook(name string, entries []string) string {
	return `<?xml version="1.0" encoding="utf-8"?><phonebooks><phonebook name="` + xmlEscape(name) + `">` + strings.Join(entries, "") + `</phonebook></phonebooks>`
}

func entryUID(e string) int {
	if m := uidRe.FindStringSubmatch(e); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func (b *Box) servePhonebook(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, _ := strconv.Atoi(r.URL.Query().Get("pbid"))
	bk := b.Books[id]
	if bk == nil {
		http.NotFound(w, r)
		return
	}
	io.WriteString(w, renderBook(bk.Name, bk.Entries))
}

func (b *Box) serveBarring(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	io.WriteString(w, renderBook("Sperrliste", b.Barring))
}

// setEntry stores data (a <contact> element) in entries: it replaces the entry with the same
// uniqueid or appends it with a new one. Returns the entries and the uniqueid.
func (b *Box) setEntry(entries []string, data string) ([]string, int) {
	if uid := entryUID(data); uid != 0 {
		for i, e := range entries {
			if entryUID(e) == uid {
				entries[i] = data
				return entries, uid
			}
		}
	}
	b.nextUID++
	uid := b.nextUID
	data = uidRe.ReplaceAllString(data, "")
	data = strings.Replace(data, "</contact>", fmt.Sprintf("<uniqueid>%d</uniqueid></contact>", uid), 1)
	return append(entries, data), uid
}

func deleteEntry(entries []string, uid int) ([]string, bool) {
	for i, e := range entries {
		if entryUID(e) == uid {
			return append(entries[:i], entries[i+1:]...), true
		}
	}
	return entries, false
}

func arg(body, name string) string {
	return html.UnescapeString(between(body, "<"+name+">", "</"+name+">"))
}

// contactAction simulates the phonebook and call barring actions of X_AVM-DE_OnTel. Called with
// b.mu held; returns the out arguments or a UPnP error code.
func (b *Box) contactAction(action, body string) (out map[string]string, code int, handled bool) {
	book := func() *Book {
		id, _ := strconv.Atoi(arg(body, "NewPhonebookID"))
		return b.Books[id]
	}
	switch action {
	case "GetPhonebookList":
		var ids []string
		for i := 0; i < 10; i++ {
			if b.Books[i] != nil {
				ids = append(ids, strconv.Itoa(i))
			}
		}
		return map[string]string{"NewPhonebookList": strings.Join(ids, ",")}, 0, true
	case "GetPhonebook":
		bk := book()
		if bk == nil {
			return nil, 713, true
		}
		id := arg(body, "NewPhonebookID")
		return map[string]string{"NewPhonebookName": bk.Name, "NewPhonebookExtraID": "", "NewPhonebookURL": "/phonebook.lua?sid=deadbeef&pbid=" + id}, 0, true
	case "GetPhonebookEntryUID":
		bk := book()
		if bk == nil {
			return nil, 600, true
		}
		uid, _ := strconv.Atoi(arg(body, "NewPhonebookEntryUniqueID"))
		for _, e := range bk.Entries {
			if entryUID(e) == uid {
				return map[string]string{"NewPhonebookEntryData": `<?xml version="1.0" encoding="utf-8"?>` + e}, 0, true
			}
		}
		return nil, 713, true
	case "SetPhonebookEntryUID":
		bk := book()
		if bk == nil {
			return nil, 600, true
		}
		if bk.ReadOnly {
			return nil, 820, true
		}
		var uid int
		bk.Entries, uid = b.setEntry(bk.Entries, arg(body, "NewPhonebookEntryData"))
		return map[string]string{"NewPhonebookEntryUniqueID": strconv.Itoa(uid)}, 0, true
	case "DeletePhonebookEntryUID":
		bk := book()
		if bk == nil {
			return nil, 600, true
		}
		if bk.ReadOnly {
			return nil, 820, true
		}
		uid, _ := strconv.Atoi(arg(body, "NewPhonebookEntryUniqueID"))
		var ok bool
		if bk.Entries, ok = deleteEntry(bk.Entries, uid); !ok {
			return nil, 713, true
		}
		return map[string]string{}, 0, true
	case "GetCallBarringList":
		return map[string]string{"NewPhonebookURL": "/callbarring.lua?sid=deadbeef"}, 0, true
	case "GetCallBarringEntryByNum":
		want := arg(body, "NewNumber")
		for _, e := range b.Barring {
			for _, m := range numberRe.FindAllStringSubmatch(e, -1) {
				if strings.TrimSpace(m[1]) == want {
					return map[string]string{"NewPhonebookEntryData": e}, 0, true
				}
			}
		}
		return nil, 714, true
	case "SetCallBarringEntry":
		var uid int
		b.Barring, uid = b.setEntry(b.Barring, arg(body, "NewPhonebookEntryData"))
		return map[string]string{"NewPhonebookEntryUniqueID": strconv.Itoa(uid)}, 0, true
	case "DeleteCallBarringEntryUID":
		uid, _ := strconv.Atoi(arg(body, "NewPhonebookEntryUniqueID"))
		b.Barring, _ = deleteEntry(b.Barring, uid)
		return map[string]string{}, 0, true
	}
	return nil, 0, false
}
