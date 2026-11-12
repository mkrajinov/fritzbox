// Package tr064 is a minimal TR-064 (SOAP over HTTP) client for AVM FRITZ!Box routers.
package tr064

import (
	"encoding/xml"
	"sort"
	"strings"
)

// Service is one TR-064 service as announced in tr64desc.xml.
type Service struct {
	Type       string `xml:"serviceType"` // urn:dslforum-org:service:WLANConfiguration:1
	ID         string `xml:"serviceId"`   // urn:WLANConfiguration-com:serviceId:WLANConfiguration1
	ControlURL string `xml:"controlURL"`
	SCPDURL    string `xml:"SCPDURL"`
}

// Name is the short service name used throughout fbctl and fritzconnection,
// e.g. "WLANConfiguration1" (last segment of the serviceId).
func (s Service) Name() string {
	if i := strings.LastIndex(s.ID, ":"); i >= 0 {
		return s.ID[i+1:]
	}
	return s.ID
}

type device struct {
	DeviceType   string    `xml:"deviceType"`
	FriendlyName string    `xml:"friendlyName"`
	ModelName    string    `xml:"modelName"`
	Services     []Service `xml:"serviceList>service"`
	Devices      []device  `xml:"deviceList>device"`
}

type rootDesc struct {
	XMLName       xml.Name `xml:"root"`
	SystemVersion struct {
		Display string `xml:"Display"`
	} `xml:"systemVersion"`
	Device device `xml:"device"`
}

// Description is the parsed tr64desc.xml.
type Description struct {
	FriendlyName string
	ModelName    string
	Firmware     string
	Services     map[string]Service
}

// ParseDescription parses tr64desc.xml, flattening nested devices.
func ParseDescription(data []byte) (*Description, error) {
	var r rootDesc
	if err := xml.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	d := &Description{
		FriendlyName: r.Device.FriendlyName,
		ModelName:    r.Device.ModelName,
		Firmware:     r.SystemVersion.Display,
		Services:     map[string]Service{},
	}
	var walk func(dev device)
	walk = func(dev device) {
		for _, s := range dev.Services {
			d.Services[s.Name()] = s
		}
		for _, c := range dev.Devices {
			walk(c)
		}
	}
	walk(r.Device)
	return d, nil
}

// ServiceNames returns all service names sorted.
func (d *Description) ServiceNames() []string {
	names := make([]string, 0, len(d.Services))
	for n := range d.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SCPD types: per-service action list.

// Argument is one in/out argument of an action.
type Argument struct {
	Name      string `xml:"name"`
	Direction string `xml:"direction"`
	StateVar  string `xml:"relatedStateVariable"`
}

// Action is a callable TR-064 action.
type Action struct {
	Name      string     `xml:"name"`
	Arguments []Argument `xml:"argumentList>argument"`
}

type scpd struct {
	Actions []Action `xml:"actionList>action"`
}

// ParseSCPD parses a service control protocol description.
func ParseSCPD(data []byte) ([]Action, error) {
	var s scpd
	if err := xml.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	sort.Slice(s.Actions, func(i, j int) bool { return s.Actions[i].Name < s.Actions[j].Name })
	return s.Actions, nil
}

// NormalizeServiceName mirrors fritzconnection: "WLANConfiguration" -> "WLANConfiguration1",
// "WLANConfiguration:2" -> "WLANConfiguration2".
func NormalizeServiceName(name string) string {
	name = strings.Replace(name, ":", "", 1)
	if name == "" {
		return name
	}
	if last := name[len(name)-1]; last < '0' || last > '9' {
		name += "1"
	}
	return name
}
