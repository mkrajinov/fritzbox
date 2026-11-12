// Package exporter exposes FRITZ!Box metrics for Prometheus.
//
// Every scrape polls all routers concurrently, each bounded by a timeout, and reports
// fritzbox_up=0 for routers that fail instead of failing the whole scrape.
package exporter

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mkrajinovic/fbctl/internal/fritz"
)

const ns = "fritzbox"

// Target is one router to scrape. Box is reused across scrapes so the digest
// challenge and AHA session survive (fewer round trips, no login per scrape).
type Target struct {
	Name string
	Box  *fritz.Box
}

// Options tunes what is collected.
type Options struct {
	Timeout   time.Duration
	SmartHome bool
}

type collector struct {
	targets []Target
	opt     Options
	mu      sync.Mutex // one scrape at a time: overlapping scrapes would double the load on the box
}

// New returns a Prometheus collector for the targets.
func New(targets []Target, o Options) prometheus.Collector {
	if o.Timeout == 0 {
		o.Timeout = 20 * time.Second
	}
	return &collector{targets: targets, opt: o}
}

func d(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(ns+"_"+name, help, append([]string{"router"}, labels...), nil)
}

var (
	descUp           = d("up", "1 if the router answered this scrape")
	descScrape       = d("scrape_duration_seconds", "time spent polling the router")
	descInfo         = d("info", "router model and firmware", "model", "firmware")
	descUptime       = d("uptime_seconds", "time since the router booted")
	descUpdate       = d("update_available", "1 if a FRITZ!OS update is available", "version")
	descWANConnected = d("wan_connected", "1 if the internet connection is up")
	descWANLink      = d("wan_link_up", "1 if the physical WAN link is up", "access_type")
	descWANUptime    = d("wan_connection_uptime_seconds", "time since the current internet connection was established")
	descWANMax       = d("wan_max_bits_per_second", "line rate", "direction")
	descWANRate      = d("wan_rate_bytes_per_second", "current throughput", "direction")
	descWANBytes     = d("wan_bytes_total", "transferred bytes (32-bit counter on some firmware, may wrap)", "direction")
	descHosts        = d("hosts", "known hosts", "state")
	descWLANEnabled  = d("wlan_enabled", "1 if the WLAN is enabled", "index", "band", "ssid")
	descWLANClients  = d("wlan_clients", "associated clients", "index", "band", "ssid")
	descSHPresent    = d("smarthome_present", "1 if the device is reachable", "ain", "name", "product")
	descSHSwitch     = d("smarthome_switch_on", "1 if the plug is switched on", "ain", "name")
	descSHPower      = d("smarthome_power_watts", "current power draw", "ain", "name")
	descSHEnergy     = d("smarthome_energy_wh_total", "energy since reset", "ain", "name")
	descSHTemp       = d("smarthome_temperature_celsius", "measured temperature", "ain", "name")
	descSHTarget     = d("smarthome_thermostat_target_celsius", "thermostat target (absent when off/on)", "ain", "name")
)

// Describe is intentionally empty: label sets depend on what each router reports (unchecked collector).
func (c *collector) Describe(chan<- *prometheus.Desc) {}

// Collect polls all routers concurrently.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range c.targets {
		wg.Go(func() { c.collectOne(t, ch) })
	}
	wg.Wait()
}

func gauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, v float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labels...)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *collector) collectOne(t Target, ch chan<- prometheus.Metric) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), c.opt.Timeout)
	defer cancel()
	r := t.Name
	defer func() { gauge(ch, descScrape, time.Since(start).Seconds(), r) }()

	st, err := t.Box.Status(ctx)
	if err != nil && st.Model == "" {
		gauge(ch, descUp, 0, r)
		return
	}
	gauge(ch, descUp, 1, r)
	gauge(ch, descInfo, 1, r, st.Model, st.Firmware)
	gauge(ch, descUptime, st.Uptime.Seconds(), r)
	gauge(ch, descUpdate, b2f(st.UpdateAvailable != ""), r, st.UpdateAvailable)
	if st.HasWAN {
		gauge(ch, descWANConnected, b2f(st.Connected), r)
		gauge(ch, descWANLink, b2f(st.LinkUp), r, st.AccessType)
		gauge(ch, descWANUptime, st.ConnUptime.Seconds(), r)
		gauge(ch, descWANMax, float64(st.MaxUpBits), r, "up")
		gauge(ch, descWANMax, float64(st.MaxDownBits), r, "down")
		gauge(ch, descWANRate, float64(st.Rates.Up), r, "up")
		gauge(ch, descWANRate, float64(st.Rates.Down), r, "down")
		gauge(ch, descWANBytes, float64(st.TotalSent), r, "up")
		gauge(ch, descWANBytes, float64(st.TotalReceived), r, "down")
	}

	if hosts, err := t.Box.Hosts(ctx); err == nil {
		active := 0
		for _, h := range hosts {
			if h.Active {
				active++
			}
		}
		gauge(ch, descHosts, float64(active), r, "active")
		gauge(ch, descHosts, float64(len(hosts)-active), r, "inactive")
	}

	if ws, err := t.Box.WLANs(ctx); err == nil {
		for _, w := range ws {
			idx, band := strconv.Itoa(w.Index), w.Label()
			gauge(ch, descWLANEnabled, b2f(w.Enabled), r, idx, band, w.SSID)
			gauge(ch, descWLANClients, float64(w.Clients), r, idx, band, w.SSID)
		}
	}

	if !c.opt.SmartHome {
		return
	}
	devs, err := t.Box.SmartDevices(ctx)
	if err != nil {
		return
	}
	for _, dv := range devs {
		gauge(ch, descSHPresent, b2f(dv.Present == 1), r, dv.AIN, dv.Name, dv.Product)
		if dv.Present != 1 {
			continue
		}
		if dv.Switch != nil && dv.Switch.State != "" {
			gauge(ch, descSHSwitch, b2f(dv.Switch.State == "1"), r, dv.AIN, dv.Name)
		}
		if dv.PowerMeter != nil {
			gauge(ch, descSHPower, float64(dv.PowerMeter.Power)/1000, r, dv.AIN, dv.Name)
			gauge(ch, descSHEnergy, float64(dv.PowerMeter.Energy), r, dv.AIN, dv.Name)
		}
		if dv.Temperature != nil {
			gauge(ch, descSHTemp, float64(dv.Temperature.Celsius)/10, r, dv.AIN, dv.Name)
		}
		if dv.Thermostat != nil && dv.Thermostat.Target >= 16 && dv.Thermostat.Target <= 56 {
			gauge(ch, descSHTarget, float64(dv.Thermostat.Target)/2, r, dv.AIN, dv.Name)
		}
	}
}
