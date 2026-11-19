package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mkrajinov/fritzbox/internal/fritz"
)

// formatNumbers renders numbers as the editable "type:number, …" list.
func formatNumbers(ns []fritz.Number) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = n.Type + ":" + n.Number
	}
	return strings.Join(parts, ", ")
}

// parseNumberList reads "mobile:0170 123, home:030" (a bare number is a home number).
func parseNumberList(s string) ([]fritz.Number, error) {
	var out []fritz.Number
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		typ, num, ok := strings.Cut(part, ":")
		if !ok {
			typ, num = "home", part
		}
		typ = strings.ToLower(strings.TrimSpace(typ))
		if !slices.Contains(fritz.NumberTypes, typ) {
			return nil, fmt.Errorf("type %q: use %s", typ, strings.Join(fritz.NumberTypes, ", "))
		}
		out = append(out, fritz.Number{Type: typ, Number: fritz.NormalizeNumber(num)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("enter at least one number")
	}
	return out, nil
}

// contactForm asks for the name, then the numbers, then saves. c == nil adds a new contact.
func (m *Model) contactForm(c *fritz.Contact) tea.Cmd {
	if m.book >= len(m.books) {
		m.flash, m.flashOK = "no phonebook loaded", false
		return nil
	}
	book := m.books[m.book]
	title, name, nums := "New contact in "+book.Name, "", ""
	if c != nil {
		title, name, nums = "Edit "+c.Name, c.Name, formatNumbers(c.Numbers)
	}
	m.input = newInput(title+" · name", "enter = next · esc = cancel", name, func(m *Model, name string) (tea.Cmd, error) {
		if name == "" {
			return nil, fmt.Errorf("a contact needs a name")
		}
		m.input = newInput(title+" · numbers", "type:number, separated by commas (types: "+strings.Join(fritz.NumberTypes, ", ")+") · enter = save", nums,
			func(m *Model, v string) (tea.Cmd, error) {
				ns, err := parseNumberList(v)
				if err != nil {
					return nil, err
				}
				if err := fritz.ValidateContact(fritz.Contact{Name: name, Numbers: ns}); err != nil {
					return nil, err
				}
				if c == nil {
					return m.action("added "+name, tabContacts, func(ctx context.Context, b Backend) error {
						_, err := b.AddContact(ctx, book.ID, fritz.Contact{Name: name, Numbers: ns})
						return err
					}), nil
				}
				id := c.ID
				return m.action("saved "+name, tabContacts, func(ctx context.Context, b Backend) error {
					_, err := b.UpdateContact(ctx, book.ID, id, fritz.ContactEdit{Name: &name, Numbers: &ns})
					return err
				}), nil
			})
		m.input.ti.CharLimit = 200
		return textinput.Blink, nil // m.input now holds the next step
	})
	return textinput.Blink
}

func (m Model) contactKey(key string, cs []fritz.Contact, cur int) (Model, tea.Cmd, bool) {
	switch key {
	case "n":
		cmd := m.contactForm(nil)
		return m, cmd, true
	case "e":
		if cur < len(cs) {
			c := cs[cur]
			cmd := m.contactForm(&c)
			return m, cmd, true
		}
		return m, nil, true
	case "X":
		if cur < len(cs) && m.book < len(m.books) {
			c, book := cs[cur], m.books[m.book]
			m.confirm = &confirmation{
				prompt: fmt.Sprintf("Delete %s from %s?", c.Name, book.Name),
				run: m.action("deleted "+c.Name, tabContacts, func(ctx context.Context, b Backend) error {
					return b.DeleteContact(ctx, book.ID, c.ID)
				}),
			}
		}
		return m, nil, true
	}
	return m, nil, false
}

// askBlock confirms putting a caller on the call block list.
func (m *Model) askBlock(c fritz.Call) {
	num, err := fritz.BlockableNumber(c.Number)
	if err != nil {
		m.flash, m.flashOK = err.Error(), false
		return
	}
	label := num
	if c.Name != "" {
		label = c.Name + " (" + num + ")"
	}
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("Block %s? The box rejects calls from this number (call block list).", label),
		run: m.action("blocked "+label, tabCalls, func(ctx context.Context, b Backend) error {
			_, added, err := b.BlockNumber(ctx, num, c.Name)
			if err == nil && !added {
				return fmt.Errorf("%s was already blocked", num)
			}
			return err
		}),
	}
}
