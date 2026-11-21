package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/backups"
	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

// profileName resolves the selected router profile without connecting.
func (a *app) profileName() (string, error) {
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	name, _, err := cfg.Resolve(a.routerName)
	return name, err
}

func (a *app) backupDir(flag string) (dir, router string, err error) {
	router, err = a.profileName()
	if err != nil {
		return "", "", err
	}
	if flag != "" {
		return flag, router, nil
	}
	dir, err = backups.DefaultDir(router)
	return dir, router, err
}

func backupSubCmds(a *app) []*cobra.Command {
	var dir string
	var keep int
	var always bool
	run := &cobra.Command{
		Use: "run", Short: "Save a backup into the backup folder and delete old ones",
		Long: "Saves an export as <router>-<date>-<time>.export in the backup folder (default:\n" +
			"the user config folder/fbctl/backups/<router>), unless the configuration is unchanged since\n" +
			"the last backup, and keeps the newest --keep exports. Store the password first with\n" +
			"`fb backup password set`; schedule it with `fb service install backup --every 24h`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if keep < 1 {
				return errors.New("--keep must be at least 1")
			}
			d, router, err := a.backupDir(dir)
			if err != nil {
				return err
			}
			pw, err := a.backupPassword()
			if err != nil {
				return err
			}
			return a.run(cmd, func(ctx context.Context, b *fritz.Box) error {
				data, err := b.Backup(ctx, pw)
				if err != nil {
					return err
				}
				path, unchanged, err := backups.Save(d, router, data, time.Now(), !always)
				if err != nil {
					return err
				}
				if unchanged {
					fmt.Fprintf(a.out, "configuration unchanged since %s, nothing saved\n", filepath.Base(path))
				} else {
					fmt.Fprintf(a.out, "saved %s (%d KB)\n", path, (len(data)+1023)/1024)
				}
				removed, err := backups.Prune(d, router, keep)
				for _, r := range removed {
					fmt.Fprintf(a.out, "%s %s\n", ui.Muted.Render("removed"), filepath.Base(r))
				}
				return err
			})
		},
	}
	run.Flags().StringVar(&dir, "dir", "", "backup folder (default: user config folder/fbctl/backups/<router>)")
	run.Flags().IntVar(&keep, "keep", 30, "number of backups to keep")
	run.Flags().BoolVar(&always, "always", false, "save even if the configuration did not change")

	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List saved backups", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, router, err := a.backupDir(dir)
			if err != nil {
				return err
			}
			l, err := backups.List(d, router)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(l)
			}
			if len(l) == 0 {
				fmt.Fprintf(a.out, "no backups in %s (create one with `fb backup run`)\n", d)
				return nil
			}
			rows := make([][]string, 0, len(l))
			for i, e := range l {
				rows = append(rows, []string{strconv.Itoa(i + 1), e.Time.Format("2006-01-02 15:04"), fmt.Sprintf("%d KB", (e.Size+1023)/1024), filepath.Base(e.Path)})
			}
			a.printTable([]string{"#", "DATE", "SIZE", "FILE"}, rows)
			fmt.Fprintln(a.out, ui.Muted.Render(d))
			return nil
		},
	}
	list.Flags().StringVar(&dir, "dir", "", "backup folder")

	var context int
	diff := &cobra.Command{
		Use: "diff [old] [new]", Short: "Show what changed between two backups (default: the last two)",
		Long: "Compares two exports line by line, grouped by config file. Old and new are a list\n" +
			"number (1 = oldest, -1 = newest), part of a file name (e.g. a date) or a path.\n" +
			"Timestamps, checksums and encrypted values change with every export and are ignored.",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, router, err := a.backupDir(dir)
			if err != nil {
				return err
			}
			l, err := backups.List(d, router)
			if err != nil {
				return err
			}
			refs := []string{"-2", "-1"}
			switch len(args) {
			case 0:
				if len(l) < 2 {
					return fmt.Errorf("need two backups to compare, %s has %d", d, len(l))
				}
			case 1:
				refs = []string{args[0], "-1"}
			case 2:
				refs = args
			}
			var data [2][]byte
			var names [2]string
			for i, ref := range refs {
				e, err := backups.Resolve(l, ref)
				if err != nil {
					return err
				}
				if data[i], err = os.ReadFile(e.Path); err != nil {
					return err
				}
				names[i] = filepath.Base(e.Path)
			}
			hunks := backups.Diff(data[0], data[1], context)
			if a.asJSON {
				return a.printJSON(hunks)
			}
			if len(hunks) == 0 {
				fmt.Fprintf(a.out, "no configuration changes between %s and %s\n", names[0], names[1])
				return nil
			}
			fmt.Fprintf(a.out, "%s %s\n%s %s\n", ui.Bad.Render("---"), names[0], ui.OK.Render("+++"), names[1])
			for _, h := range hunks {
				sec := h.Section
				if sec == "" {
					sec = "header"
				}
				fmt.Fprintln(a.out, ui.Muted.Render(fmt.Sprintf("@@ %s · line %d → %d @@", sec, h.OldLine, h.NewLine)))
				for _, op := range h.Ops {
					switch op.Kind {
					case '-':
						fmt.Fprintln(a.out, ui.Bad.Render("-"+op.Text))
					case '+':
						fmt.Fprintln(a.out, ui.OK.Render("+"+op.Text))
					default:
						fmt.Fprintln(a.out, " "+op.Text)
					}
				}
			}
			return nil
		},
	}
	diff.Flags().StringVar(&dir, "dir", "", "backup folder")
	diff.Flags().IntVarP(&context, "context", "U", 3, "lines of context around each change")

	pw := &cobra.Command{Use: "password", Short: "Store the backup password in the keychain (for scheduled backups)"}
	pw.AddCommand(&cobra.Command{
		Use: "set", Short: "Ask for the backup password and store it in the keychain", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			router, err := a.profileName()
			if err != nil {
				return err
			}
			p := os.Getenv("FB_BACKUP_PASSWORD")
			if p == "" {
				if p, err = a.promptBackupPassword(); err != nil {
					return err
				}
			}
			file, err := config.StoreBackupPassword(router, p)
			if err != nil {
				return fmt.Errorf("store backup password: %w", err)
			}
			if file != "" {
				fmt.Fprintf(a.out, "no keychain available: backup password for %s stored in %s (readable only by you)\n", router, file)
			} else {
				fmt.Fprintf(a.out, "backup password for %s stored in the keychain\n", router)
			}
			return nil
		},
	}, &cobra.Command{
		Use: "clear", Short: "Remove the stored backup password", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			router, err := a.profileName()
			if err != nil {
				return err
			}
			if err := config.DeleteBackupPassword(router); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "backup password for %s removed\n", router)
			return nil
		},
	})
	return []*cobra.Command{run, list, diff, pw}
}
