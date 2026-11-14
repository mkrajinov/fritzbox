package fritz

import (
	"context"
	"fmt"
	"strings"

	"github.com/mkrajinovic/fbctl/internal/aha"
)

// SmartDevices lists DECT/Zigbee smart home devices via the AHA interface.
func (b *Box) SmartDevices(ctx context.Context) ([]aha.Device, error) {
	return b.ahaClient().Devices(ctx)
}

// ResolveSmartDevice matches an AIN (spaces optional) or a device name (case-insensitive).
func ResolveSmartDevice(devs []aha.Device, q string) (aha.Device, error) {
	norm := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	var matches []aha.Device
	for _, d := range devs {
		if norm(d.AIN) == norm(q) || strings.EqualFold(d.Name, q) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 0:
		return aha.Device{}, fmt.Errorf("no smart home device matches %q", q)
	case 1:
		return matches[0], nil
	}
	return aha.Device{}, fmt.Errorf("%q matches %d devices; use the AIN", q, len(matches))
}

// SetSwitch switches a plug on/off/toggle.
func (b *Box) SetSwitch(ctx context.Context, ain, state string) error {
	return b.ahaClient().SetSwitch(ctx, ain, state)
}

// SetThermostat sets a target temperature ("21.5", "off", "on").
func (b *Box) SetThermostat(ctx context.Context, ain, value string) error {
	v, err := aha.EncodeTemp(value)
	if err != nil {
		return err
	}
	return b.ahaClient().SetTarget(ctx, ain, v)
}

// SetThermostatRaw sets an already encoded target value.
func (b *Box) SetThermostatRaw(ctx context.Context, ain string, v int) error {
	return b.ahaClient().SetTarget(ctx, ain, v)
}

// Templates lists smart home templates.
func (b *Box) Templates(ctx context.Context) ([]aha.Template, error) {
	return b.ahaClient().Templates(ctx)
}

// ApplyTemplate runs a smart home template.
func (b *Box) ApplyTemplate(ctx context.Context, id string) error {
	return b.ahaClient().ApplyTemplate(ctx, id)
}

// ResolveTemplate matches a template identifier or name (case-insensitive).
func ResolveTemplate(ts []aha.Template, q string) (aha.Template, error) {
	var m []aha.Template
	for _, t := range ts {
		if t.ID == q || t.Num == q || strings.EqualFold(t.Name, q) {
			m = append(m, t)
		}
	}
	switch len(m) {
	case 0:
		return aha.Template{}, fmt.Errorf("no template matches %q", q)
	case 1:
		return m[0], nil
	}
	return aha.Template{}, fmt.Errorf("%q matches %d templates; use the id", q, len(m))
}
