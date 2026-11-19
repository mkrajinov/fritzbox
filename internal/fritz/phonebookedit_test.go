package fritz_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mkrajinov/fritzbox/internal/fakebox"
	"github.com/mkrajinov/fritzbox/internal/fritz"
)

func TestPhonebookEditRoundTrip(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()

	// Special characters must survive the trip through the SOAP string argument.
	name := `Smith & Sons <"Ltd">`
	id, err := b.AddContact(ctx, 0, fritz.Contact{Name: name, Numbers: []fritz.Number{{Type: "work", Number: "+4930123"}}, Emails: []string{"a&b@example.com"}, VIP: true})
	if err != nil || id == 0 {
		t.Fatalf("add: %d %v", id, err)
	}
	_, cs, err := b.Contacts(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := fritz.FindContacts(cs, strings.ToLower(name))
	if len(got) != 1 || got[0].Name != name || got[0].ID != id || !got[0].VIP || got[0].Emails[0] != "a&b@example.com" || got[0].Numbers[0].Number != "+4930123" {
		t.Fatalf("added contact: %+v", got)
	}

	// Editing the name keeps fields fb doesn't model (quickdial on Alice's number).
	newName := "Alice Liddell"
	c, err := b.UpdateContact(ctx, 0, 1, fritz.ContactEdit{Name: &newName})
	if err != nil || c.Name != newName || len(c.Numbers) != 2 || c.Emails[0] != "alice@example.com" {
		t.Fatalf("update: %+v %v", c, err)
	}
	set := fb.CallsTo("x_contact", "SetPhonebookEntryUID")
	last := set[len(set)-1].Body
	if !strings.Contains(last, "quickdial=&#34;7&#34;") && !strings.Contains(last, `quickdial="7"`) && !strings.Contains(last, "quickdial=&quot;7&quot;") {
		t.Errorf("edit dropped quickdial: %s", last)
	}
	if !strings.Contains(last, "&lt;uniqueid&gt;1&lt;/uniqueid&gt;") {
		t.Errorf("edit must send the uniqueid: %s", last)
	}

	nums := []fritz.Number{{Type: "mobile", Number: "0170 999"}}
	if _, err := b.UpdateContact(ctx, 0, 1, fritz.ContactEdit{Numbers: &nums}); err == nil {
		t.Error("number with a space should be rejected")
	}
	nums[0].Number = "0170999"
	c, err = b.UpdateContact(ctx, 0, 1, fritz.ContactEdit{Numbers: &nums})
	if err != nil || len(c.Numbers) != 1 || c.Numbers[0].Number != "0170999" {
		t.Fatalf("numbers: %+v %v", c, err)
	}

	if err := b.DeleteContact(ctx, 0, id); err != nil {
		t.Fatal(err)
	}
	_, cs, _ = b.Contacts(ctx, 0)
	if len(fritz.FindContacts(cs, "sons")) != 0 {
		t.Error("contact not deleted")
	}
	if _, err := b.UpdateContact(ctx, 0, 999, fritz.ContactEdit{Name: &newName}); err == nil || !strings.Contains(err.Error(), "no contact with id 999") {
		t.Errorf("missing contact: %v", err)
	}
	// Book 1 is an online phonebook in the fake.
	if _, err := b.AddContact(ctx, 1, fritz.Contact{Name: "X", Numbers: []fritz.Number{{Type: "home", Number: "1"}}}); err == nil || !strings.Contains(err.Error(), "online phonebooks") {
		t.Errorf("read-only book: %v", err)
	}
}

func TestCallBlockList(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	id, added, err := b.BlockNumber(ctx, "0900 123-456", "Spam")
	if err != nil || !added || id == 0 {
		t.Fatalf("block: %d %v %v", id, added, err)
	}
	id2, added, err := b.BlockNumber(ctx, "0900123456", "")
	if err != nil || added || id2 != id {
		t.Fatalf("second block should find the entry: %d %v %v", id2, added, err)
	}
	if n := len(fb.CallsTo("x_contact", "SetCallBarringEntry")); n != 1 {
		t.Errorf("SetCallBarringEntry called %d times", n)
	}
	list, err := b.BlockedNumbers(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "Spam" || list[0].Numbers[0].Number != "0900123456" {
		t.Fatalf("list: %+v %v", list, err)
	}
	for _, bad := range []string{"", "unknown", "abc"} {
		if _, _, err := b.BlockNumber(ctx, bad, ""); err == nil {
			t.Errorf("blocking %q should fail", bad)
		}
	}
	if _, err := b.UnblockNumber(ctx, "0900 123456"); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.BlockedNumbers(ctx); len(list) != 0 {
		t.Errorf("still blocked: %+v", list)
	}
	if _, err := b.UnblockNumber(ctx, "0900123456"); err == nil {
		t.Error("unblocking twice should fail")
	}
	_, _, _ = b.BlockNumber(ctx, "0800", "")
	list, _ = b.BlockedNumbers(ctx)
	if _, err := b.UnblockID(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestVCardAndCSV(t *testing.T) {
	in := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Müller\\, Hans\r\nN:Müller;Hans;;;\r\nTEL;TYPE=CELL,VOICE:+49 170 1234\r\n" +
		"item1.TEL;type=WORK:030/123 45\r\nEMAIL;TYPE=INTERNET:hans@ex\r\n ample.com\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:2.1\r\nN:Doe;Jane\r\nTEL;HOME:0301\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nFN:No Number\r\nEND:VCARD\r\n"
	cs, skipped, err := fritz.ParseVCards(strings.NewReader(in))
	if err != nil || skipped != 1 || len(cs) != 2 {
		t.Fatalf("parse: %+v %d %v", cs, skipped, err)
	}
	if cs[0].Name != "Müller, Hans" || cs[0].Numbers[0] != (fritz.Number{Type: "mobile", Number: "+491701234"}) ||
		cs[0].Numbers[1] != (fritz.Number{Type: "work", Number: "03012345"}) || cs[0].Emails[0] != "hans@example.com" {
		t.Errorf("vcard 3: %+v", cs[0])
	}
	if cs[1].Name != "Jane Doe" || cs[1].Numbers[0].Type != "home" {
		t.Errorf("vcard 2.1: %+v", cs[1])
	}

	var buf bytes.Buffer
	if err := fritz.WriteVCards(&buf, cs); err != nil {
		t.Fatal(err)
	}
	back, _, err := fritz.ParseVCards(&buf)
	if err != nil || len(back) != 2 || back[0].Name != cs[0].Name || !slices.Equal(back[0].Numbers, cs[0].Numbers) {
		t.Fatalf("vcard round trip: %+v %v", back, err)
	}

	buf.Reset()
	cs[0].VIP = true
	if err := fritz.WriteCSV(&buf, cs); err != nil {
		t.Fatal(err)
	}
	csvBack, err := fritz.ParseCSV(&buf)
	if err != nil || len(csvBack) != 2 || !csvBack[0].VIP || len(csvBack[0].Numbers) != 2 || csvBack[0].Emails[0] != "hans@example.com" {
		t.Fatalf("csv round trip: %+v %v", csvBack, err)
	}
	plain, err := fritz.ParseCSV(strings.NewReader("Ann,mobile,0170 1\nAnn,home,030 2\n"))
	if err != nil || len(plain) != 1 || len(plain[0].Numbers) != 2 || plain[0].Numbers[0].Number != "01701" {
		t.Fatalf("csv without header: %+v %v", plain, err)
	}
	if _, err := fritz.ParseCSV(strings.NewReader("number,type\n0301,home\n")); err == nil {
		t.Error("header without name column should fail")
	}
	if !fritz.HasContact(cs, fritz.Contact{Name: "müller, hans", Numbers: []fritz.Number{{Number: "030/12345"}}}) {
		t.Error("HasContact should match name case-insensitively and numbers normalized")
	}
}

func TestRecordingDownload(t *testing.T) {
	b, fb := connect(t, fakebox.User, fakebox.Password)
	ctx := context.Background()
	for _, idx := range []int{0, 1} { // message 0 has a bare path, message 1 a download.lua path
		rec, err := b.Recording(ctx, 0, idx)
		if err != nil || rec.Ext != ".wav" || string(rec.Data) != fakebox.Recording || rec.Message.Index != idx {
			t.Fatalf("recording %d: %+v %v", idx, rec.Message, err)
		}
	}
	dl := fb.CallsTo("http", "download")
	if len(dl) != 2 || !strings.Contains(dl[0].Body, "path=%2Fdata%2Ftam%2Frec%2Frec.0.000") || !strings.Contains(dl[0].Body, "sid=deadbeef") {
		t.Fatalf("download requests: %+v", dl)
	}
	if _, err := b.Recording(ctx, 0, 7); err == nil || !strings.Contains(err.Error(), "no message 7") {
		t.Errorf("missing message: %v", err)
	}
}
