package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mkrajinov/fritzbox/internal/aha"
	"github.com/mkrajinov/fritzbox/internal/fritz"
	"github.com/mkrajinov/fritzbox/internal/ui"
)

func homeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "home", Aliases: []string{"smarthome"}, Short: "Smart home plugs and thermostats (AHA)"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List smart home devices",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			devs, err := b.SmartDevices(ctx)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(devs)
			}
			rows := make([][]string, 0, len(devs))
			for _, d := range devs {
				rows = append(rows, []string{ui.OnOff(d.Present == 1), d.Name, d.AIN, d.Product, deviceState(d)})
			}
			a.printTable([]string{"", "NAME", "AIN", "PRODUCT", "STATE"}, rows)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "switch <ain|name> <on|off|toggle>", Short: "Switch a smart plug", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			devs, err := b.SmartDevices(ctx)
			if err != nil {
				return err
			}
			d, err := fritz.ResolveSmartDevice(devs, args[0])
			if err != nil {
				return err
			}
			if d.Switch == nil {
				return fmt.Errorf("%s is not a switchable device", d.Name)
			}
			if err := b.SetSwitch(ctx, d.AIN, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s: %s\n", d.Name, args[1])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "temp <ain|name> <°C|off|on>", Short: "Set a thermostat target (8–28 °C, 0.5 steps)", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := aha.EncodeTemp(args[1]); err != nil {
				return err
			}
			ctx, cancel := a.ctx(cmd)
			defer cancel()
			b, err := a.box(ctx)
			if err != nil {
				return err
			}
			devs, err := b.SmartDevices(ctx)
			if err != nil {
				return err
			}
			d, err := fritz.ResolveSmartDevice(devs, args[0])
			if err != nil {
				return err
			}
			if d.Thermostat == nil {
				return fmt.Errorf("%s is not a thermostat", d.Name)
			}
			if err := b.SetThermostat(ctx, d.AIN, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "%s: target %s\n", d.Name, args[1])
			return nil
		},
	})
	return cmd
}

func deviceState(d aha.Device) string {
	var parts []string
	if d.Switch != nil {
		parts = append(parts, map[string]string{"1": ui.OK.Render("on"), "0": ui.Muted.Render("off")}[d.Switch.State])
	}
	if d.PowerMeter != nil {
		parts = append(parts, fmt.Sprintf("%.1f W", float64(d.PowerMeter.Power)/1000))
	}
	if d.Thermostat != nil {
		parts = append(parts, fmt.Sprintf("%s → %s", aha.DecodeTemp(d.Thermostat.Actual), aha.DecodeTemp(d.Thermostat.Target)))
	} else if d.Temperature != nil {
		parts = append(parts, fmt.Sprintf("%.1f°C", float64(d.Temperature.Celsius)/10))
	}
	var out strings.Builder
	for i, p := range parts {
		if i > 0 {
			out.WriteString(ui.Muted.Render(" · "))
		}
		out.WriteString(p)
	}
	return out.String()
}
