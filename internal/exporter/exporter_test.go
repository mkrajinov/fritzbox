package exporter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mkrajinov/fritzbox/internal/config"
	"github.com/mkrajinov/fritzbox/internal/fakebox"
	"github.com/mkrajinov/fritzbox/internal/fritz"
)

func TestCollect(t *testing.T) {
	fb := fakebox.New()
	defer fb.Close()
	host, port := fb.HostPort()
	ok, err := fritz.Connect(context.Background(), "home", &config.Router{Host: host, Port: port, WebPort: port, User: fakebox.User}, fakebox.Password, fritz.ConnectOptions{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dead, _ := fritz.Connect(context.Background(), "dead", &config.Router{Host: "127.0.0.1", Port: 1, User: "x"}, "x", fritz.ConnectOptions{Timeout: time.Second})

	reg := prometheus.NewRegistry()
	reg.MustRegister(New([]Target{{"home", ok}, {"dead", dead}}, Options{Timeout: 3 * time.Second, SmartHome: true}))

	want := `
# HELP fritzbox_up 1 if the router answered this scrape
# TYPE fritzbox_up gauge
fritzbox_up{router="dead"} 0
fritzbox_up{router="home"} 1
# HELP fritzbox_wan_rate_bytes_per_second current throughput
# TYPE fritzbox_wan_rate_bytes_per_second gauge
fritzbox_wan_rate_bytes_per_second{direction="down",router="home"} 50000
fritzbox_wan_rate_bytes_per_second{direction="up",router="home"} 1000
# HELP fritzbox_hosts known hosts
# TYPE fritzbox_hosts gauge
fritzbox_hosts{router="home",state="active"} 2
fritzbox_hosts{router="home",state="inactive"} 1
# HELP fritzbox_smarthome_power_watts current power draw
# TYPE fritzbox_smarthome_power_watts gauge
fritzbox_smarthome_power_watts{ain="11657 0240192",name="Desk lamp",router="home"} 12.5
# HELP fritzbox_smarthome_thermostat_target_celsius thermostat target (absent when off/on)
# TYPE fritzbox_smarthome_thermostat_target_celsius gauge
fritzbox_smarthome_thermostat_target_celsius{ain="09995 0335100",name="Living room",router="home"} 21
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"fritzbox_up", "fritzbox_wan_rate_bytes_per_second", "fritzbox_hosts",
		"fritzbox_smarthome_power_watts", "fritzbox_smarthome_thermostat_target_celsius"); err != nil {
		t.Fatal(err)
	}
	n, err := testutil.GatherAndCount(reg, "fritzbox_wlan_clients")
	if err != nil || n != 3 {
		t.Fatalf("wlan_clients series = %d, %v", n, err)
	}
}
