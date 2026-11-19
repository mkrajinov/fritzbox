package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

func blockCmds(a *app) []*cobra.Command {
	var name string
	block := &cobra.Command{
		Use: "block <number>", Short: "Put a number on the call block list (calls from it are rejected)",
		Example: "  fb calls block 0900123456 --name \"Spam\"\n  fb calls block +4930987654",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := fritz.BlockableNumber(args[0]); err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				id, added, err := b.BlockNumber(ctx, args[0], name)
				if err != nil {
					return err
				}
				if !added {
					fmt.Fprintf(a.out, "%s is already blocked (entry %d)\n", fritz.NormalizeNumber(args[0]), id)
					return nil
				}
				fmt.Fprintf(a.out, "%s blocked (entry %d)\n", fritz.NormalizeNumber(args[0]), id)
				return nil
			})
		},
	}
	block.Flags().StringVar(&name, "name", "", "label for the entry (default: the number)")

	var byID bool
	unblock := &cobra.Command{
		Use: "unblock <number>", Short: "Remove a number from the call block list", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				var removed []fritz.Contact
				var err error
				if byID {
					id, perr := strconv.Atoi(args[0])
					if perr != nil {
						return errors.New("--id needs the entry number shown by `fb calls blocked`")
					}
					removed, err = b.UnblockID(ctx, id)
				} else {
					removed, err = b.UnblockNumber(ctx, args[0])
				}
				if err != nil {
					return err
				}
				for _, c := range removed {
					fmt.Fprintf(a.out, "unblocked %s\n", blockLabel(c))
				}
				return nil
			})
		},
	}
	unblock.Flags().BoolVar(&byID, "id", false, "the argument is an entry id, not a number")

	list := &cobra.Command{
		Use: "blocked", Short: "List the call block list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				list, err := b.BlockedNumbers(ctx)
				if err != nil {
					return err
				}
				if a.asJSON {
					return a.printJSON(list)
				}
				if len(list) == 0 {
					fmt.Fprintln(a.out, ui.Muted.Render("no blocked numbers"))
					return nil
				}
				rows := make([][]string, 0, len(list))
				for _, c := range list {
					nums := make([]string, len(c.Numbers))
					for i, n := range c.Numbers {
						nums[i] = n.Number
					}
					label := c.Name
					if label == strings.Join(nums, ", ") {
						label = ""
					}
					rows = append(rows, []string{strconv.Itoa(c.ID), strings.Join(nums, ", "), label})
				}
				a.printTable([]string{"ID", "NUMBER", "NAME"}, rows)
				return nil
			})
		},
	}
	return []*cobra.Command{block, unblock, list}
}

func blockLabel(c fritz.Contact) string {
	if len(c.Numbers) == 0 {
		return c.Name
	}
	if c.Name == "" || c.Name == c.Numbers[0].Number {
		return c.Numbers[0].Number
	}
	return c.Numbers[0].Number + " (" + c.Name + ")"
}
