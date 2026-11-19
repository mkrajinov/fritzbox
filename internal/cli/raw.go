package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// rawCmd is the escape hatch equivalent to `fritzconnection -s / -S / -a`.
func rawCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "raw", Short: "Inspect and call any TR-064 service/action"}
	cmd.AddCommand(&cobra.Command{
		Use: "services", Short: "List TR-064 services announced by the box",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			d, err := b.TR.Description(ctx)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(d)
			}
			for _, n := range d.ServiceNames() {
				fmt.Fprintln(a.out, n)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "actions <service>", Short: "List actions and arguments of a service", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			acts, err := b.TR.Actions(ctx, args[0])
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(acts)
			}
			for _, act := range acts {
				var in, out []string
				for _, arg := range act.Arguments {
					if arg.Direction == "in" {
						in = append(in, arg.Name)
					} else {
						out = append(out, arg.Name)
					}
				}
				fmt.Fprintf(a.out, "%s\n", act.Name)
				if len(in) > 0 {
					fmt.Fprintf(a.out, "  in:  %s\n", strings.Join(in, ", "))
				}
				if len(out) > 0 {
					fmt.Fprintf(a.out, "  out: %s\n", strings.Join(out, ", "))
				}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "call <service> <action> [Name=value ...]",
		Short: "Call an action, e.g. `fb raw call WLANConfiguration1 GetInfo`",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in := tr064.Args{}
			for _, kv := range args[2:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("argument %q must be Name=value", kv)
				}
				in[k] = v
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			res, err := b.TR.Call(ctx, args[0], args[1], in)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(res)
			}
			keys := make([]string, 0, len(res))
			for k := range res {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(a.out, "%-40s %s\n", k, res[k])
			}
			return nil
		},
	})
	return cmd
}
