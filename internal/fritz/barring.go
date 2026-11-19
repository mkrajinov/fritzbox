package fritz

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// BlockedNumbers lists the box's call barring list (Telephony > Call Handling > Call Blocks).
func (b *Box) BlockedNumbers(ctx context.Context) ([]Contact, error) {
	r, err := b.call(ctx, onTel, "GetCallBarringList", nil)
	if err != nil {
		return nil, err
	}
	u := r["NewPhonebookURL"]
	if u == "" {
		return nil, errors.New("box returned no call block list URL")
	}
	data, err := b.TR.Get(ctx, u)
	if err != nil {
		return nil, err
	}
	return ParsePhonebook(data)
}

// BlockableNumber checks a number before it goes on the block list.
func BlockableNumber(number string) (string, error) {
	n := NormalizeNumber(number)
	if n == "" || strings.EqualFold(n, "unknown") || strings.EqualFold(n, "anonymous") {
		return "", errors.New("the caller withheld their number; block anonymous calls in the web UI (Telephony > Call Handling)")
	}
	for i, r := range n {
		if !(r >= '0' && r <= '9' || r == '+' && i == 0) {
			return "", fmt.Errorf("can only block phone numbers (digits and a leading +), got %q", number)
		}
	}
	return n, nil
}

// BlockNumber puts a number on the call block list. It reports added=false when the number
// is already blocked (the box would otherwise store a duplicate).
func (b *Box) BlockNumber(ctx context.Context, number, name string) (id int, added bool, err error) {
	n, err := BlockableNumber(number)
	if err != nil {
		return 0, false, err
	}
	r, err := b.call(ctx, onTel, "GetCallBarringEntryByNum", tr064.Args{"NewNumber": n})
	switch {
	case err == nil:
		if c, perr := parseElement([]byte(r["NewPhonebookEntryData"]), "contact"); perr == nil {
			if ct, cerr := nodeContact(c); cerr == nil {
				return ct.ID, false, nil
			}
		}
		return 0, false, nil
	case !tr064.IsCode(err, 714) && !tr064.IsCode(err, 713):
		return 0, false, err
	}
	if strings.TrimSpace(name) == "" {
		name = n
	}
	c := Contact{Name: name, Numbers: []Number{{Type: "home", Number: n}}}
	r, err = b.call(ctx, onTel, "SetCallBarringEntry", tr064.Args{"NewPhonebookEntryData": newContactNode(c).String()})
	if err != nil {
		return 0, false, err
	}
	return atoi(r["NewPhonebookEntryUniqueID"]), true, nil
}

// UnblockNumber removes the block list entries for a number and returns them.
func (b *Box) UnblockNumber(ctx context.Context, number string) ([]Contact, error) {
	want := NormalizeNumber(number)
	return b.unblock(ctx, number, func(c Contact) bool {
		for _, n := range c.Numbers {
			if NormalizeNumber(n.Number) == want {
				return true
			}
		}
		return false
	})
}

// UnblockID removes the block list entry with this id.
func (b *Box) UnblockID(ctx context.Context, id int) ([]Contact, error) {
	return b.unblock(ctx, "entry "+strconv.Itoa(id), func(c Contact) bool { return c.ID == id })
}

func (b *Box) unblock(ctx context.Context, what string, match func(Contact) bool) ([]Contact, error) {
	list, err := b.BlockedNumbers(ctx)
	if err != nil {
		return nil, err
	}
	var hits []Contact
	for _, c := range list {
		if match(c) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("%s is not on the call block list", what)
	}
	for _, c := range hits {
		if _, err := b.call(ctx, onTel, "DeleteCallBarringEntryUID", tr064.Args{"NewPhonebookEntryUniqueID": itoa(c.ID)}); err != nil {
			return nil, err
		}
	}
	return hits, nil
}
