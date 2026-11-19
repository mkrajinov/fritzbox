package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

// parseNumbers reads "type:number" specs; a bare number is a home number.
func parseNumbers(specs []string) ([]fritz.Number, error) {
	var out []fritz.Number
	for _, s := range specs {
		typ, num, ok := strings.Cut(s, ":")
		if !ok {
			typ, num = "home", s
		}
		typ = strings.ToLower(strings.TrimSpace(typ))
		if typ == "cell" {
			typ = "mobile"
		}
		if !slices.Contains(fritz.NumberTypes, typ) {
			return nil, fmt.Errorf("number type %q in %q: use one of %s", typ, s, strings.Join(fritz.NumberTypes, ", "))
		}
		out = append(out, fritz.Number{Type: typ, Number: fritz.NormalizeNumber(num)})
	}
	return out, nil
}

// resolveBook accepts a phonebook id or (part of) its name.
func resolveBook(ctx context.Context, b *fritz.Box, q string) (fritz.Phonebook, error) {
	pbs, err := b.Phonebooks(ctx)
	if err != nil {
		return fritz.Phonebook{}, err
	}
	if id, err := strconv.Atoi(q); err == nil {
		for _, p := range pbs {
			if p.ID == id {
				return p, nil
			}
		}
		return fritz.Phonebook{}, fmt.Errorf("no phonebook %d (see `fb phonebook list`)", id)
	}
	var hits []fritz.Phonebook
	for _, p := range pbs {
		if strings.Contains(strings.ToLower(p.Name), strings.ToLower(q)) {
			hits = append(hits, p)
		}
	}
	switch len(hits) {
	case 0:
		return fritz.Phonebook{}, fmt.Errorf("no phonebook matches %q (see `fb phonebook list`)", q)
	case 1:
		return hits[0], nil
	}
	return fritz.Phonebook{}, fmt.Errorf("%q matches several phonebooks; use the id", q)
}

// resolveContact accepts a contact id or (part of) its name.
func resolveContact(ctx context.Context, b *fritz.Box, book int, q string) (fritz.Contact, error) {
	_, cs, err := b.Contacts(ctx, book)
	if err != nil {
		return fritz.Contact{}, err
	}
	hits := fritz.FindContacts(cs, q)
	switch len(hits) {
	case 0:
		return fritz.Contact{}, fmt.Errorf("no contact matches %q", q)
	case 1:
		return hits[0], nil
	}
	names := make([]string, len(hits))
	for i, c := range hits {
		names[i] = fmt.Sprintf("%s (id %d)", c.Name, c.ID)
	}
	return fritz.Contact{}, fmt.Errorf("%q matches several contacts: %s; use the id", q, strings.Join(names, ", "))
}

func describeContact(c fritz.Contact) string {
	nums := make([]string, len(c.Numbers))
	for i, n := range c.Numbers {
		nums[i] = n.Number + ui.Muted.Render(" "+n.Type)
	}
	return fmt.Sprintf("%s · %s", ui.Bold.Render(c.Name), strings.Join(nums, ", "))
}

func phonebookEditCmds(a *app) []*cobra.Command {
	var name string
	var numbers, emails []string
	var vip bool
	add := &cobra.Command{
		Use: "add <phonebook>", Short: "Add a contact",
		Example: "  fb phonebook add 0 --name \"Jane Doe\" --number mobile:0170123456 --number work:030123\n" +
			"  fb phonebook add Telefonbuch --name Plumber --number 0301234 --vip",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			nums, err := parseNumbers(numbers)
			if err != nil {
				return err
			}
			c := fritz.Contact{Name: name, Numbers: nums, Emails: emails, VIP: vip}
			if err := fritz.ValidateContact(c); err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				pb, err := resolveBook(ctx, b, args[0])
				if err != nil {
					return err
				}
				id, err := b.AddContact(ctx, pb.ID, c)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "added %s to %s (id %d)\n", describeContact(c), pb.Name, id)
				return nil
			})
		},
	}
	addFlags := func(c *cobra.Command) {
		c.Flags().StringVar(&name, "name", "", "contact name")
		c.Flags().StringArrayVar(&numbers, "number", nil, "phone number as type:number (types: "+strings.Join(fritz.NumberTypes, ", ")+"; default home), repeatable")
		c.Flags().StringArrayVar(&emails, "email", nil, "email address, repeatable")
		c.Flags().BoolVar(&vip, "vip", false, "mark as VIP (rings even when ringing is blocked)")
	}
	addFlags(add)

	edit := &cobra.Command{
		Use: "edit <phonebook> <contact>", Short: "Change a contact's name, numbers, emails or VIP flag",
		Long: "Changes only the given fields. --number and --email replace all numbers or addresses.\n" +
			"The contact is found by id or name (see `fb phonebook show`).",
		Example: "  fb phonebook edit 0 \"Jane Doe\" --number mobile:0170999 --number home:030555\n  fb phonebook edit 0 12 --name \"Jane Smith\" --vip=false",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var e fritz.ContactEdit
			if cmd.Flags().Changed("name") {
				e.Name = &name
			}
			if cmd.Flags().Changed("number") {
				nums, err := parseNumbers(numbers)
				if err != nil {
					return err
				}
				e.Numbers = &nums
			}
			if cmd.Flags().Changed("email") {
				e.Emails = &emails
			}
			if cmd.Flags().Changed("vip") {
				e.VIP = &vip
			}
			if e == (fritz.ContactEdit{}) {
				return errors.New("nothing to change: pass --name, --number, --email or --vip")
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				pb, err := resolveBook(ctx, b, args[0])
				if err != nil {
					return err
				}
				c, err := resolveContact(ctx, b, pb.ID, args[1])
				if err != nil {
					return err
				}
				updated, err := b.UpdateContact(ctx, pb.ID, c.ID, e)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "updated %s\n", describeContact(updated))
				return nil
			})
		},
	}
	addFlags(edit)

	var yes bool
	del := &cobra.Command{
		Use: "delete <phonebook> <contact>", Aliases: []string{"rm"}, Short: "Delete a contact", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				pb, err := resolveBook(ctx, b, args[0])
				if err != nil {
					return err
				}
				c, err := resolveContact(ctx, b, pb.ID, args[1])
				if err != nil {
					return err
				}
				if !confirm(a, yes, fmt.Sprintf("Delete %s from %s?", c.Name, pb.Name)) {
					return errors.New("aborted")
				}
				if err := b.DeleteContact(ctx, pb.ID, c.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "deleted %s\n", c.Name)
				return nil
			})
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")

	var dryRun bool
	var format string
	imp := &cobra.Command{
		Use: "import <phonebook> <file.vcf|file.csv>", Short: "Add contacts from a vCard or CSV file",
		Long: "Adds every contact of the file. Contacts that already exist with the same name and one of\n" +
			"the same numbers are skipped. CSV: one row per number with the columns name,type,number\n" +
			"(and optionally email,vip), with or without a header row; - reads standard input.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cs, skipped, err := readContactsFile(args[1], format, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if skipped > 0 {
				fmt.Fprintf(a.err, "%s\n", ui.Muted.Render(fmt.Sprintf("%d entries without a name or number skipped", skipped)))
			}
			for _, c := range cs {
				if err := fritz.ValidateContact(c); err != nil {
					return fmt.Errorf("%s: %w", c.Name, err)
				}
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				pb, err := resolveBook(ctx, b, args[0])
				if err != nil {
					return err
				}
				_, existing, err := b.Contacts(ctx, pb.ID)
				if err != nil {
					return err
				}
				added, dup := 0, 0
				for _, c := range cs {
					if fritz.HasContact(existing, c) {
						dup++
						continue
					}
					if dryRun {
						fmt.Fprintf(a.out, "would add %s\n", describeContact(c))
						added++
						continue
					}
					if _, err := b.AddContact(ctx, pb.ID, c); err != nil {
						return fmt.Errorf("%s: %w (%d added before the error)", c.Name, err, added)
					}
					existing = append(existing, c)
					added++
				}
				verb := "added"
				if dryRun {
					verb = "would add"
				}
				fmt.Fprintf(a.out, "%s %d contacts to %s, %d already there\n", verb, added, pb.Name, dup)
				return nil
			})
		},
	}
	imp.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "only show what would be added")
	imp.Flags().StringVar(&format, "format", "", "vcf or csv (default: from the file extension)")

	var out string
	exp := &cobra.Command{
		Use: "export <phonebook>", Short: "Write a phonebook as vCard, CSV or JSON",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f := strings.ToLower(format)
			if f == "" {
				f = strings.TrimPrefix(strings.ToLower(filepath.Ext(out)), ".")
			}
			if f == "" || f == "vcard" {
				f = "vcf"
			}
			if f != "vcf" && f != "csv" && f != "json" {
				return fmt.Errorf("--format must be vcf, csv or json")
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				pb, err := resolveBook(ctx, b, args[0])
				if err != nil {
					return err
				}
				_, cs, err := b.Contacts(ctx, pb.ID)
				if err != nil {
					return err
				}
				w := a.out
				if out != "" && out != "-" {
					file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
					if err != nil {
						return err
					}
					defer file.Close()
					w = file
				}
				switch f {
				case "csv":
					err = fritz.WriteCSV(w, cs)
				case "json":
					saved := a.out
					a.out = w
					err = a.printJSON(cs)
					a.out = saved
				default:
					err = fritz.WriteVCards(w, cs)
				}
				if err == nil && out != "" && out != "-" {
					fmt.Fprintf(a.err, "wrote %d contacts to %s\n", len(cs), out)
				}
				return err
			})
		},
	}
	exp.Flags().StringVarP(&out, "output", "o", "", "file to write (default: standard output)")
	exp.Flags().StringVar(&format, "format", "", "vcf, csv or json (default: from the file extension, else vcf)")
	return []*cobra.Command{add, edit, del, imp, exp}
}

func readContactsFile(path, format string, stdin io.Reader) ([]fritz.Contact, int, error) {
	var r io.Reader = stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, 0, err
		}
		defer f.Close()
		r = f
	}
	if format == "" {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	}
	switch strings.ToLower(format) {
	case "vcf", "vcard":
		return fritz.ParseVCards(r)
	case "csv":
		cs, err := fritz.ParseCSV(r)
		return cs, 0, err
	}
	return nil, 0, fmt.Errorf("unknown file type %q: use a .vcf or .csv file, or --format", format)
}
