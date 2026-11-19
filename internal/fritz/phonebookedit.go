package fritz

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// NumberTypes are the number types the box's phonebook knows.
var NumberTypes = []string{"home", "mobile", "work", "fax_work", "other"}

// ContactEdit lists the fields to change; nil fields stay as they are.
type ContactEdit struct {
	Name    *string
	Numbers *[]Number
	Emails  *[]string
	VIP     *bool
}

// ValidateContact checks a contact before it is written to the box.
func ValidateContact(c Contact) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("a contact needs a name")
	}
	if len(c.Numbers) == 0 {
		return errors.New("a contact needs at least one number")
	}
	for _, n := range c.Numbers {
		if err := validNumber(n.Number); err != nil {
			return err
		}
		if !slices.Contains(NumberTypes, n.Type) {
			return fmt.Errorf("number type %q: use one of %s", n.Type, strings.Join(NumberTypes, ", "))
		}
	}
	return nil
}

func validNumber(s string) error {
	if s == "" {
		return errors.New("empty phone number")
	}
	for i, r := range s {
		if !(unicode.IsDigit(r) || r == '*' || r == '#' || (r == '+' && i == 0)) {
			return fmt.Errorf("phone number %q may only contain digits, a leading + and * or #", s)
		}
	}
	return nil
}

// NormalizeNumber removes spaces, dashes, slashes, dots and brackets from a phone number.
func NormalizeNumber(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(" -/(). ", r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// newContactNode builds the entry XML for a new contact (no uniqueid: the box assigns one).
func newContactNode(c Contact) *node {
	n := &node{Name: "contact"}
	applyEdit(n, ContactEdit{Name: &c.Name, Numbers: &c.Numbers, Emails: &c.Emails, VIP: &c.VIP})
	return n
}

// applyEdit changes a contact tree in place, keeping everything it does not touch.
func applyEdit(n *node, e ContactEdit) {
	if e.VIP != nil {
		n.ensure("category").Text = map[bool]string{true: "1", false: "0"}[*e.VIP]
	}
	if e.Name != nil {
		n.ensure("person").ensure("realName").Text = strings.TrimSpace(*e.Name)
	}
	tel := n.ensure("telephony")
	if e.Emails != nil {
		svc := tel.ensure("services")
		old := svc.all("email")
		svc.remove("email")
		for i, addr := range *e.Emails {
			em := &node{Name: "email", Attrs: nil, Text: strings.TrimSpace(addr)}
			if i < len(old) {
				em.Attrs = old[i].Attrs
			} else {
				em.setAttr("classifier", "private")
			}
			em.setAttr("id", strconv.Itoa(i))
			svc.Children = append(svc.Children, em)
		}
	}
	if e.Numbers != nil {
		old := map[string]*node{}
		for _, o := range tel.all("number") {
			old[NormalizeNumber(o.Text)] = o
		}
		// Numbers go after <services>, like in the box's own export.
		kept := tel.Children[:0]
		for _, c := range tel.Children {
			if c.Name != "number" {
				kept = append(kept, c)
			}
		}
		tel.Children = kept
		for i, num := range *e.Numbers {
			nn := &node{Name: "number", Text: num.Number}
			if o := old[NormalizeNumber(num.Number)]; o != nil {
				nn.Attrs = o.Attrs // keep quickdial, vanity
			}
			nn.setAttr("type", num.Type)
			nn.setAttr("prio", map[bool]string{true: "1", false: "0"}[i == 0])
			nn.setAttr("id", strconv.Itoa(i))
			tel.Children = append(tel.Children, nn)
		}
		tel.setAttr("nid", strconv.Itoa(len(*e.Numbers)))
	}
}

// writeHint explains failures of phonebook writes.
func writeHint(err error) error {
	var u *tr064.UPnPError
	if errors.As(err, &u) {
		return fmt.Errorf("%w (online phonebooks such as Google or CardDAV can't be changed through the box's interface)", err)
	}
	return err
}

// AddContact adds a contact to a phonebook and returns its new id.
func (b *Box) AddContact(ctx context.Context, book int, c Contact) (int, error) {
	if err := ValidateContact(c); err != nil {
		return 0, err
	}
	r, err := b.call(ctx, onTel, "SetPhonebookEntryUID", tr064.Args{
		"NewPhonebookID": itoa(book), "NewPhonebookEntryData": newContactNode(c).String(),
	})
	if err != nil {
		return 0, writeHint(err)
	}
	return atoi(r["NewPhonebookEntryUniqueID"]), nil
}

// contactNode reads one entry as an XML tree.
func (b *Box) contactNode(ctx context.Context, book, id int) (*node, error) {
	r, err := b.call(ctx, onTel, "GetPhonebookEntryUID", tr064.Args{"NewPhonebookID": itoa(book), "NewPhonebookEntryUniqueID": itoa(id)})
	if err != nil {
		if tr064.IsCode(err, 713) || tr064.IsCode(err, 714) {
			return nil, fmt.Errorf("phonebook %d has no contact with id %d", book, id)
		}
		return nil, err
	}
	return parseElement([]byte(r["NewPhonebookEntryData"]), "contact")
}

// UpdateContact changes a contact. It reads the entry from the box and writes it back with only
// the requested fields changed, so data fb doesn't show (photos, quickdial, …) is kept.
func (b *Box) UpdateContact(ctx context.Context, book, id int, e ContactEdit) (Contact, error) {
	n, err := b.contactNode(ctx, book, id)
	if err != nil {
		return Contact{}, err
	}
	applyEdit(n, e)
	n.ensure("uniqueid").Text = itoa(id)
	c, err := nodeContact(n)
	if err != nil {
		return c, err
	}
	if err := ValidateContact(c); err != nil {
		return c, err
	}
	if _, err := b.call(ctx, onTel, "SetPhonebookEntryUID", tr064.Args{
		"NewPhonebookID": itoa(book), "NewPhonebookEntryData": n.String(),
	}); err != nil {
		return c, writeHint(err)
	}
	return c, nil
}

func nodeContact(n *node) (Contact, error) {
	var p phonebookXML
	data := "<phonebooks><phonebook>" + n.String() + "</phonebook></phonebooks>"
	if err := xml.Unmarshal([]byte(data), &p); err != nil || len(p.Contacts) != 1 {
		return Contact{}, fmt.Errorf("cannot read contact: %v", err)
	}
	return p.Contacts[0].contact(), nil
}

// DeleteContact removes a contact from a phonebook.
func (b *Box) DeleteContact(ctx context.Context, book, id int) error {
	_, err := b.call(ctx, onTel, "DeletePhonebookEntryUID", tr064.Args{"NewPhonebookID": itoa(book), "NewPhonebookEntryUniqueID": itoa(id)})
	return writeHint(err)
}

// FindContacts returns the contacts whose id equals q or whose name contains q (case-insensitive).
// An exact name match wins over partial ones.
func FindContacts(cs []Contact, q string) []Contact {
	if id, err := strconv.Atoi(q); err == nil {
		for _, c := range cs {
			if c.ID == id {
				return []Contact{c}
			}
		}
	}
	lq := strings.ToLower(strings.TrimSpace(q))
	var exact, partial []Contact
	for _, c := range cs {
		l := strings.ToLower(c.Name)
		switch {
		case l == lq:
			exact = append(exact, c)
		case strings.Contains(l, lq):
			partial = append(partial, c)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// HasContact reports whether cs already holds a contact with this name and one of its numbers.
func HasContact(cs []Contact, c Contact) bool {
	for _, x := range cs {
		if !strings.EqualFold(strings.TrimSpace(x.Name), strings.TrimSpace(c.Name)) {
			continue
		}
		for _, a := range x.Numbers {
			for _, b := range c.Numbers {
				if NormalizeNumber(a.Number) == NormalizeNumber(b.Number) {
					return true
				}
			}
		}
	}
	return false
}
