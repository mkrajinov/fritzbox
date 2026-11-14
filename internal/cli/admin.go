package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mkrajinovic/fbctl/internal/fritz"
	"github.com/mkrajinovic/fbctl/internal/ui"
)

// run connects to the selected router and runs f with a request context.
func (a *app) run(cmd *cobra.Command, f func(ctx context.Context, b *fritz.Box) error) error {
	ctx, cancel := a.ctx(cmd)
	defer cancel()
	b, err := a.box(ctx)
	if err != nil {
		return err
	}
	return f(ctx, b)
}

// secondFactorPrompt tells the user how to confirm a write on the box.
func (a *app) secondFactorPrompt(p fritz.SecondFactorPrompt) {
	fmt.Fprintf(a.err, "%s %s wants a confirmation for %s:\n  %s\n  %s\n",
		ui.Warn.Render(ui.GlyphWarning), orDefault(p.Router, "the box"), p.Action, ui.Bold.Render(p.Instructions()),
		ui.Muted.Render(fmt.Sprintf("waiting up to %s, ctrl+c to cancel", fritz.SecondFactorTimeout)))
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "1", "true", "yes", "enable":
		return true, nil
	case "off", "0", "false", "no", "disable":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off, got %q", s)
}

var onOffArgs = []string{"on", "off"}

func onOff(v bool) string {
	if v {
		return ui.OK.Render("on")
	}
	return ui.Muted.Render("off")
}

// attach adds subcommands to an existing top-level command.
func attach(root *cobra.Command, parent string, cmds ...*cobra.Command) {
	for _, c := range root.Commands() {
		if c.Name() == parent {
			c.AddCommand(cmds...)
			return
		}
	}
	panic("no command " + parent)
}

func adminCmds(a *app, root *cobra.Command) {
	root.AddCommand(updateCmd(a), backupCmd(a), dhcpCmd(a), remoteCmd(a), tamCmd(a), forwardCmd(a), dialCmd(a), hangupCmd(a), profilesCmd(a))
	attach(root, "hosts", hostsInfoCmd(a), hostsPriorityCmd(a), hostsAutoWakeCmd(a), hostsProfileCmd(a), hostsTicketCmd(a))
	attach(root, "wlan", wlanWPSCmd(a), wlanAllCmd(a), wlanSetCmd(a))
	attach(root, "home", homeTemplatesCmd(a), homeApplyCmd(a))
}

// Firmware.

func updateCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use: "update", Short: "Firmware: show state, check, install, auto-update mode",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				u, err := b.UpdateInfo(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(u)
				}
				avail := ui.Muted.Render("none")
				if u.Available {
					avail = ui.Warn.Render(orDefault(u.Version, "yes"))
				}
				a.kv([2]string{"installed", orDefault(u.Current, "–")}, [2]string{"update", avail},
					[2]string{"state", u.State}, [2]string{"auto update", orDefault(u.AutoMode, "–")},
					[2]string{"last update", orDefault(u.LastOK, "–")}, [2]string{"info", orDefault(u.InfoURL, "–")})
				return nil
			})
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use: "check", Short: "Ask the box to look for new firmware now",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				if err := b.CheckUpdate(ctx); err != nil {
					return err
				}
				u, err := b.UpdateInfo(ctx)
				if err != nil {
					return err
				}
				if u.Available {
					fmt.Fprintf(a.out, "update available: %s (install with `fb update install`)\n", orDefault(u.Version, "new version"))
				} else {
					fmt.Fprintln(a.out, "no update available")
				}
				return nil
			})
		},
	})
	var yes bool
	install := &cobra.Command{
		Use: "install", Short: "Install available firmware (the box reboots)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				u, err := b.UpdateInfo(ctx)
				if err != nil {
					return err
				}
				if !u.Available {
					return errors.New("no update available (run `fb update check` first)")
				}
				if !confirm(a, yes, fmt.Sprintf("Install FRITZ!OS %s on %s? Internet and phones are down for several minutes.", u.Version, b.Name)) {
					return errors.New("aborted")
				}
				st, err := b.DoUpdate(ctx)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "update %s; the box reboots when it is done\n", strings.ToLower(orDefault(st, "started")))
				return nil
			})
		},
	}
	install.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	cmd.AddCommand(install)
	cmd.AddCommand(&cobra.Command{
		Use: "auto <notify|important|all>", Short: "Set the automatic update mode", Args: cobra.ExactArgs(1),
		ValidArgs: fritz.AutoUpdateModes,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				if err := b.SetAutoUpdate(ctx, args[0]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "auto update: %s\n", args[0])
				return nil
			})
		},
	})
	return cmd
}

// Backup.

func backupCmd(a *app) *cobra.Command {
	var out string
	var force bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Save an encrypted configuration export (restore it in the web UI)",
		Long: "Downloads the box configuration, encrypted with a password you choose. You need the same\n" +
			"password to restore it under System > Backup in the web UI. The password is read from\n" +
			"FB_BACKUP_PASSWORD or asked for on the terminal.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pw, err := a.backupPassword()
			if err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				path := out
				if path == "" {
					path = fmt.Sprintf("fritzbox-%s-%s.export", b.Name, time.Now().Format("20060102-1504"))
				}
				flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
				if force {
					flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
				}
				data, err := b.Backup(ctx, pw)
				if err != nil {
					return err
				}
				f, err := os.OpenFile(path, flags, 0o600)
				if err != nil {
					return err
				}
				if _, err := f.Write(data); err != nil {
					f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
				abs, _ := filepath.Abs(path)
				fmt.Fprintf(a.out, "saved %s (%d KB)\n", abs, (len(data)+1023)/1024)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "file to write (default fritzbox-<router>-<date>.export)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

func (a *app) backupPassword() (string, error) {
	if pw := os.Getenv("FB_BACKUP_PASSWORD"); pw != "" {
		return pw, nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("set FB_BACKUP_PASSWORD (stdin is not a terminal)")
	}
	read := func(prompt string) (string, error) {
		fmt.Fprint(a.err, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(a.err)
		return string(b), err
	}
	p1, err := read("Backup password: ")
	if err != nil {
		return "", err
	}
	if len(p1) < 4 {
		return "", errors.New("use a backup password of at least 4 characters")
	}
	p2, err := read("Repeat: ")
	if err != nil {
		return "", err
	}
	if p1 != p2 {
		return "", errors.New("passwords differ")
	}
	return p1, nil
}

// DHCP.

func dhcpCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use: "dhcp", Short: "Show the DHCP server settings",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				d, err := b.DHCP(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(d)
				}
				a.kv([2]string{"dhcp server", onOff(d.Enabled)}, [2]string{"range", d.Min + " – " + d.Max},
					[2]string{"router", d.Router}, [2]string{"subnet mask", d.Subnet}, [2]string{"dns", d.DNS}, [2]string{"domain", d.Domain})
				return nil
			})
		},
	}
	var yes bool
	rng := &cobra.Command{
		Use: "range <first-ip> <last-ip>", Short: "Change the DHCP address range", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				if !confirm(a, yes, fmt.Sprintf("Set the DHCP range of %s to %s – %s? Devices get new addresses on their next lease.", b.Name, args[0], args[1])) {
					return errors.New("aborted")
				}
				if err := b.SetDHCPRange(ctx, args[0], args[1]); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "DHCP range: %s – %s\n", args[0], args[1])
				return nil
			})
		},
	}
	rng.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	cmd.AddCommand(rng)
	return cmd
}

// Remote access.

func remoteCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use: "remote", Short: "Internet access to the box: HTTPS, Let's Encrypt, DynDNS, MyFRITZ",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				r, err := b.Remote(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(r)
				}
				https := onOff(r.HTTPSEnabled)
				if r.HTTPSEnabled {
					https += ui.Muted.Render(" port " + r.HTTPSPort)
				}
				ddns := onOff(r.DDNSEnabled)
				if r.DDNSEnabled {
					ddns += ui.Muted.Render(fmt.Sprintf(" %s via %s · v4 %s · v6 %s", r.DDNSDomain, r.DDNSProvider, r.DDNSStatus4, r.DDNSStatus6))
				}
				mf := onOff(r.MyFritzEnabled)
				if r.MyFritzName != "" {
					mf += ui.Muted.Render(" " + r.MyFritzName + " · " + r.MyFritzState)
				}
				a.kv([2]string{"remote https", https}, [2]string{"let's encrypt", onOff(r.LetsEncrypt) + ui.Muted.Render(" "+r.LEState)},
					[2]string{"dyndns", ddns}, [2]string{"myfritz", mf})
				return nil
			})
		},
	}
	toggle := func(use, short string, risky bool, set func(context.Context, *fritz.Box, bool) (string, error)) *cobra.Command {
		var yes bool
		c := &cobra.Command{
			Use: use + " <on|off>", Short: short, Args: cobra.ExactArgs(1), ValidArgs: onOffArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				on, err := parseOnOff(args[0])
				if err != nil {
					return err
				}
				return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
					if on && risky && !confirm(a, yes, fmt.Sprintf("Make the %s web UI reachable from the internet?", b.Name)) {
						return errors.New("aborted")
					}
					note, err := set(ctx, b, on)
					if err != nil {
						return err
					}
					fmt.Fprintf(a.out, "%s %s%s\n", use, onOff(on), note)
					return nil
				})
			},
		}
		c.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
		return c
	}
	cmd.AddCommand(
		toggle("https", "Allow web UI access from the internet over HTTPS", true, func(ctx context.Context, b *fritz.Box, on bool) (string, error) {
			port, err := b.SetRemoteHTTPS(ctx, on)
			if on && port != "" {
				return ui.Muted.Render(" (port " + port + ")"), err
			}
			return "", err
		}),
		toggle("letsencrypt", "Use a Let's Encrypt certificate for remote access", false, func(ctx context.Context, b *fritz.Box, on bool) (string, error) {
			return "", b.SetLetsEncrypt(ctx, on)
		}),
		toggle("myfritz", "Turn MyFRITZ on or off", false, func(ctx context.Context, b *fritz.Box, on bool) (string, error) {
			return "", b.SetMyFritz(ctx, on)
		}),
	)
	return cmd
}

// Telephony.

func parseMsgRef(s string) (tam, idx int, err error) {
	t, i, ok := strings.Cut(s, ":")
	if !ok {
		return 0, 0, fmt.Errorf("message reference must be <tam>:<index> as shown by `fb tam`, got %q", s)
	}
	if tam, err = strconv.Atoi(t); err == nil {
		idx, err = strconv.Atoi(i)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("message reference must be <tam>:<index>, got %q", s)
	}
	return tam, idx, nil
}

func tamCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use: "tam", Aliases: []string{"voicemail"}, Short: "Answering machine messages",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				ms, err := b.Messages(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(ms)
				}
				rows := make([][]string, 0, len(ms))
				for _, m := range ms {
					mark := " "
					if m.New {
						mark = ui.Warn.Render(ui.GlyphOn)
					}
					rows = append(rows, []string{mark, fmt.Sprintf("%d:%d", m.TAM, m.Index), m.Date.Format("2006-01-02 15:04"), orDefault(m.Number, "unknown"), m.Name, m.Duration})
				}
				a.printTable([]string{"", "REF", "DATE", "FROM", "NAME", "LENGTH"}, rows)
				return nil
			})
		},
	}
	var unread bool
	mark := &cobra.Command{
		Use: "mark <tam:index>", Short: "Mark a message as heard (--unread: as new)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, i, err := parseMsgRef(args[0])
			if err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error { return b.MarkMessage(ctx, t, i, !unread) })
		},
	}
	mark.Flags().BoolVar(&unread, "unread", false, "mark as new instead")
	var yes bool
	del := &cobra.Command{
		Use: "delete <tam:index>", Short: "Delete a message", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, i, err := parseMsgRef(args[0])
			if err != nil {
				return err
			}
			if !confirm(a, yes, "Delete message "+args[0]+"?") {
				return errors.New("aborted")
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error { return b.DeleteMessage(ctx, t, i) })
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	list := &cobra.Command{
		Use: "machines", Short: "List answering machines",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				ts, err := b.TAMs(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(ts)
				}
				rows := make([][]string, 0, len(ts))
				for _, t := range ts {
					rows = append(rows, []string{strconv.Itoa(t.Index), t.Name, onOff(t.Enabled)})
				}
				a.printTable([]string{"TAM", "NAME", "STATE"}, rows)
				return nil
			})
		},
	}
	set := &cobra.Command{
		Use: "switch <tam> <on|off>", Short: "Switch an answering machine on or off", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("tam must be a number (see `fb tam machines`)")
			}
			on, err := parseOnOff(args[1])
			if err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error { return b.SetTAM(ctx, idx, on) })
		},
	}
	cmd.AddCommand(mark, del, list, set)
	return cmd
}

func forwardCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use: "forward", Aliases: []string{"deflections"}, Short: "Call forwarding rules (switch on/off; create them in the web UI)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				ds, err := b.Deflections(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(ds)
				}
				rows := make([][]string, 0, len(ds))
				for _, d := range ds {
					rows = append(rows, []string{strconv.Itoa(d.ID), onOff(d.Enabled), d.Describe()})
				}
				a.printTable([]string{"ID", "STATE", "RULE"}, rows)
				return nil
			})
		},
	}
	for _, on := range []bool{true, false} {
		use := map[bool]string{true: "on", false: "off"}[on]
		cmd.AddCommand(&cobra.Command{
			Use: use + " <id>", Short: "Switch a forwarding rule " + use, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := strconv.Atoi(args[0])
				if err != nil {
					return fmt.Errorf("id must be a number (see `fb forward`)")
				}
				return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
					if err := b.SetDeflection(ctx, id, on); err != nil {
						return err
					}
					fmt.Fprintf(a.out, "forwarding rule %d %s\n", id, onOff(on))
					return nil
				})
			},
		})
	}
	return cmd
}

func dialCmd(a *app) *cobra.Command {
	var phone string
	cmd := &cobra.Command{
		Use:   "dial [number|contact]",
		Short: "Ring your phone, then call a number (needs Dialing Help enabled on the box)",
		Long: "The box rings the phone set for dialing help; when you pick up, it calls the number.\n" +
			"A contact name from the phonebooks is resolved to its first number. Without arguments, shows\n" +
			"the phone used; --phone sets it (e.g. \"DECT: Mobilteil 1\").",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				if phone != "" {
					if err := b.SetDialPhone(ctx, phone); err != nil {
						return err
					}
					fmt.Fprintf(a.out, "dialing help rings: %s\n", phone)
				}
				if len(args) == 0 {
					if phone == "" {
						p, err := b.DialPhone(ctx)
						if err != nil {
							return err
						}
						fmt.Fprintf(a.out, "dialing help rings: %s\n", orDefault(p, "(not set)"))
					}
					return nil
				}
				number, label := args[0], args[0]
				if strings.IndexFunc(number, func(r rune) bool { return !strings.ContainsRune("0123456789+*# ", r) }) >= 0 {
					n, name, err := lookupContact(ctx, b, number)
					if err != nil {
						return err
					}
					number, label = n, name+" ("+n+")"
				}
				if err := b.Dial(ctx, strings.ReplaceAll(number, " ", "")); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "calling %s: pick up your phone · hang up with `fb hangup`\n", label)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&phone, "phone", "", "phone the box rings for dialing help")
	return cmd
}

func lookupContact(ctx context.Context, b *fritz.Box, q string) (number, name string, err error) {
	pbs, err := b.Phonebooks(ctx)
	if err != nil {
		return "", "", err
	}
	var hits []fritz.Contact
	seen := map[string]bool{} // the same contact can be in several phonebooks
	for _, pb := range pbs {
		_, cs, err := b.Contacts(ctx, pb.ID)
		if err != nil {
			continue
		}
		for _, c := range cs {
			key := c.Name + "\x00" + firstNumber(c)
			if len(c.Numbers) > 0 && !seen[key] && strings.Contains(strings.ToLower(c.Name), strings.ToLower(q)) {
				seen[key] = true
				hits = append(hits, c)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", "", fmt.Errorf("no contact matches %q", q)
	case 1:
		return hits[0].Numbers[0].Number, hits[0].Name, nil
	}
	names := make([]string, 0, len(hits))
	for _, h := range hits {
		names = append(names, h.Name)
	}
	return "", "", fmt.Errorf("%q matches %d contacts: %s", q, len(hits), strings.Join(names, ", "))
}

func hangupCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "hangup", Short: "End a call started with fb dial",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error { return b.Hangup(ctx) })
		},
	}
}

// Hosts: info, priority, auto wake, parental control.

func (a *app) withHost(cmd *cobra.Command, q string, f func(ctx context.Context, b *fritz.Box, h fritz.Host) error) error {
	return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
		hosts, err := b.Hosts(ctx)
		if err != nil {
			return err
		}
		h, err := fritz.FindHost(hosts, q)
		if err != nil {
			return err
		}
		return f(ctx, b, h)
	})
}

func hostsInfoCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "info <mac|ip|name>", Short: "Everything the box knows about a host", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withHost(cmd, args[0], func(ctx context.Context, b *fritz.Box, h fritz.Host) error {
				hi, err := b.HostInfo(ctx, h.IP)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(hi)
				}
				profile := hi.ProfileID
				if ps, err := b.FilterProfiles(ctx); err == nil {
					for _, p := range ps {
						if p.ID == hi.ProfileID {
							profile = p.Name + ui.Muted.Render(" ("+p.ID+")")
						}
					}
				}
				a.kv([2]string{"name", hi.Name}, [2]string{"ip / mac", hi.IP + "  " + hi.MAC}, [2]string{"active", onOff(hi.Active)},
					[2]string{"model", orDefault(hi.Model, "–")}, [2]string{"class", orDefault(hi.DeviceClass, "–")},
					[2]string{"internet", hi.WANAccess}, [2]string{"profile", orDefault(profile, "–")},
					[2]string{"online time", onlineTime(hi)}, [2]string{"priority", onOff(hi.Priority)}, [2]string{"auto wake", onOff(hi.AutoWOL)})
				return nil
			})
		},
	}
}

func onlineTime(h fritz.HostInfo) string {
	if h.TimeMax == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d of %d min used today", h.TimeUsed, h.TimeMax)
}

func hostsPriorityCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "priority <host> <on|off>", Short: "Prioritise a host's traffic", Args: cobra.ExactArgs(2), ValidArgs: onOffArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			on, err := parseOnOff(args[1])
			if err != nil {
				return err
			}
			return a.withHost(cmd, args[0], func(ctx context.Context, b *fritz.Box, h fritz.Host) error {
				if err := b.SetPriority(ctx, h.IP, on); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "priority %s for %s\n", onOff(on), orDefault(h.Name, h.IP))
				return nil
			})
		},
	}
}

func hostsAutoWakeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "autowake <host> <on|off>", Short: "Wake the host when it is accessed from the internet", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			on, err := parseOnOff(args[1])
			if err != nil {
				return err
			}
			return a.withHost(cmd, args[0], func(ctx context.Context, b *fritz.Box, h fritz.Host) error {
				if err := b.SetAutoWOL(ctx, h.MAC, on); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "auto wake %s for %s\n", onOff(on), orDefault(h.Name, h.MAC))
				return nil
			})
		},
	}
}

func profilesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "profiles", Short: "List parental-control access profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				ps, err := b.FilterProfiles(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(ps)
				}
				rows := make([][]string, 0, len(ps))
				for _, p := range ps {
					rows = append(rows, []string{p.ID, p.Name})
				}
				a.printTable([]string{"ID", "NAME"}, rows)
				return nil
			})
		},
	}
}

func resolveProfile(ps []fritz.FilterProfile, q string) (fritz.FilterProfile, error) {
	for _, p := range ps {
		if p.ID == q || strings.EqualFold(p.Name, q) {
			return p, nil
		}
	}
	return fritz.FilterProfile{}, fmt.Errorf("no access profile %q (see `fb profiles`)", q)
}

func hostsProfileCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "profile <host> [profile]", Short: "Show or set a host's access profile", Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withHost(cmd, args[0], func(ctx context.Context, b *fritz.Box, h fritz.Host) error {
				ps, err := b.FilterProfiles(ctx)
				if err != nil {
					return err
				}
				if len(args) == 1 {
					hi, err := b.HostInfo(ctx, h.IP)
					if err != nil {
						return err
					}
					name := hi.ProfileID
					if p, err := resolveProfile(ps, hi.ProfileID); err == nil {
						name = p.Name
					}
					fmt.Fprintf(a.out, "%s: %s · %s\n", orDefault(h.Name, h.IP), orDefault(name, "–"), onlineTime(hi))
					return nil
				}
				p, err := resolveProfile(ps, args[1])
				if err != nil {
					return err
				}
				if err := b.SetFilterProfile(ctx, h.IP, p.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s now uses profile %s\n", orDefault(h.Name, h.IP), p.Name)
				return nil
			})
		},
	}
}

func hostsTicketCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "ticket <host>", Short: "Give a host extra online time (one ticket)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withHost(cmd, args[0], func(ctx context.Context, b *fritz.Box, h fritz.Host) error {
				used, mx, err := b.AddTicket(ctx, h.IP)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "ticket added for %s: %d of %d min used today\n", orDefault(h.Name, h.IP), used, mx)
				return nil
			})
		},
	}
}

// WLAN: WPS, all radios, SSID/channel/hidden.

func (a *app) withWLAN(cmd *cobra.Command, sel string, f func(ctx context.Context, b *fritz.Box, w fritz.WLAN) error) error {
	return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
		ws, err := b.WLANs(ctx)
		if err != nil {
			return err
		}
		w, err := fritz.ResolveWLAN(ws, sel)
		if err != nil {
			return err
		}
		return f(ctx, b, w)
	})
}

func wlanWPSCmd(a *app) *cobra.Command {
	var stop, wait bool
	cmd := &cobra.Command{
		Use: "wps <2.4|5|6|guest|index>", Short: "Start push-button WPS so a device can join without the password", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.withWLAN(cmd, args[0], func(ctx context.Context, b *fritz.Box, w fritz.WLAN) error {
				st, err := b.StartWPS(ctx, w.Index, stop)
				if err != nil {
					return err
				}
				if stop {
					fmt.Fprintln(a.out, "WPS stopped")
					return nil
				}
				fmt.Fprintf(a.out, "WPS %s on %s (%s): press WPS on the device within 2 minutes\n", orDefault(st, "started"), w.Label(), w.SSID)
				if !wait {
					return nil
				}
				// Poll with a context of its own: the default request timeout is shorter than WPS.
				wctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute+10*time.Second)
				defer cancel()
				for {
					select {
					case <-wctx.Done():
						return errors.New("no device joined in time")
					case <-time.After(2 * time.Second):
					}
					st, err := b.WPSStatus(wctx, w.Index)
					if err != nil {
						continue
					}
					switch {
					case st == fritz.WPSSuccess:
						fmt.Fprintln(a.out, ui.OK.Render("device joined"))
						return nil
					case strings.HasPrefix(st, "err"), st == "off", st == "inactive":
						return fmt.Errorf("WPS ended: %s", st)
					}
				}
			})
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", false, "stop a running WPS session")
	cmd.Flags().BoolVar(&wait, "wait", true, "wait until a device joins")
	return cmd
}

func wlanAllCmd(a *app) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use: "all <on|off>", Short: "Switch all WLAN radios on or off (like the WLAN button)", Args: cobra.ExactArgs(1), ValidArgs: onOffArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			on, err := parseOnOff(args[0])
			if err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				if !on && !confirm(a, yes, fmt.Sprintf("Switch off all WLANs of %s? Wireless devices, including this one if it is on WLAN, lose the connection.", b.Name)) {
					return errors.New("aborted")
				}
				if err := b.SetWLANGlobal(ctx, on); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "all WLANs %s\n", onOff(on))
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	return cmd
}

func wlanSetCmd(a *app) *cobra.Command {
	var ssid, channel, hidden string
	var yes bool
	cmd := &cobra.Command{
		Use: "set <2.4|5|6|guest|index>", Short: "Change SSID, channel or SSID visibility", Args: cobra.ExactArgs(1),
		Example: "  fb wlan set guest --ssid Visitors\n  fb wlan set 5 --channel 36\n  fb wlan set 2.4 --channel auto --hidden=false",
		RunE: func(cmd *cobra.Command, args []string) error {
			if ssid == "" && channel == "" && hidden == "" {
				return a.withWLAN(cmd, args[0], func(ctx context.Context, b *fritz.Box, w fritz.WLAN) error {
					ci, err := b.Channels(ctx, w.Index)
					if err != nil {
						return err
					}
					if a.asJSON {
						return a.printJSON(ci)
					}
					cur := strconv.Itoa(ci.Channel)
					if ci.Auto {
						cur += " (auto)"
					}
					a.kv([2]string{"ssid", w.SSID}, [2]string{"hidden", onOff(w.Hidden)}, [2]string{"channel", cur}, [2]string{"possible", fmt.Sprint(ci.Possible)})
					return nil
				})
			}
			return a.withWLAN(cmd, args[0], func(ctx context.Context, b *fritz.Box, w fritz.WLAN) error {
				if !confirm(a, yes, fmt.Sprintf("Change %s (%s)? Devices on it reconnect.", w.Label(), w.SSID)) {
					return errors.New("aborted")
				}
				if ssid != "" {
					if err := b.SetSSID(ctx, w.Index, ssid); err != nil {
						return err
					}
					fmt.Fprintf(a.out, "SSID: %s\n", ssid)
				}
				if channel != "" {
					ch := 0
					if channel != "auto" {
						n, err := strconv.Atoi(channel)
						if err != nil || n <= 0 {
							return fmt.Errorf("channel must be a number or auto")
						}
						ch = n
					}
					if err := b.SetChannel(ctx, w.Index, ch); err != nil {
						return err
					}
					fmt.Fprintf(a.out, "channel: %s\n", channel)
				}
				if hidden != "" {
					h, err := parseOnOff(hidden)
					if err != nil {
						return err
					}
					if err := b.SetHidden(ctx, w.Index, h); err != nil {
						return err
					}
					fmt.Fprintf(a.out, "hidden: %s\n", onOff(h))
				}
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&ssid, "ssid", "", "new network name")
	f.StringVar(&channel, "channel", "", "channel number or auto")
	f.StringVar(&hidden, "hidden", "", "hide the SSID (true/false)")
	f.BoolVarP(&yes, "yes", "y", false, "don't ask")
	return cmd
}

// Smart home templates.

func homeTemplatesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "templates", Short: "List smart home templates",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				ts, err := b.Templates(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(ts)
				}
				rows := make([][]string, 0, len(ts))
				for _, t := range ts {
					rows = append(rows, []string{t.Name, t.ID, strconv.Itoa(len(t.Devices))})
				}
				a.printTable([]string{"NAME", "ID", "DEVICES"}, rows)
				return nil
			})
		},
	}
}

func homeApplyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "apply <template>", Short: "Run a smart home template", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				ts, err := b.Templates(ctx)
				if err != nil {
					return err
				}
				t, err := fritz.ResolveTemplate(ts, args[0])
				if err != nil {
					return err
				}
				if err := b.ApplyTemplate(ctx, t.ID); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "applied %s\n", t.Name)
				return nil
			})
		},
	}
}

func firstNumber(c fritz.Contact) string {
	if len(c.Numbers) == 0 {
		return ""
	}
	return c.Numbers[0].Number
}
