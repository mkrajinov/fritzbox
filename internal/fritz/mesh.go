package fritz

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Mesh topology (JSON behind Hosts1.X_AVM-DE_GetMeshListPath, documented by AVM as "Mesh-Topologie").

type meshJSON struct {
	SchemaVersion string `json:"schema_version"`
	Nodes         []struct {
		UID        string `json:"uid"`
		Name       string `json:"device_name"`
		Model      string `json:"device_model"`
		MAC        string `json:"device_mac_address"`
		IsMeshed   bool   `json:"is_meshed"`
		MeshRole   string `json:"mesh_role"` // master, slave, unknown
		Interfaces []struct {
			UID   string `json:"uid"`
			Name  string `json:"name"`
			Type  string `json:"type"` // WLAN, LAN, PLC, DECT
			Links []struct {
				UID      string `json:"uid"`
				Type     string `json:"type"`
				State    string `json:"state"` // CONNECTED, DISCONNECTED
				Node1    string `json:"node_1_uid"`
				Node2    string `json:"node_2_uid"`
				CurRx    int64  `json:"cur_data_rate_rx"` // kbit/s, node 1 perspective
				CurTx    int64  `json:"cur_data_rate_tx"`
				MaxRx    int64  `json:"max_data_rate_rx"`
				MaxTx    int64  `json:"max_data_rate_tx"`
				Iface1   string `json:"node_interface_1_uid"`
				Iface2   string `json:"node_interface_2_uid"`
				LastSeen int64  `json:"last_connected"`
			} `json:"node_links"`
		} `json:"node_interfaces"`
	} `json:"nodes"`
}

// MeshLink is a connected device as seen from a mesh node.
type MeshLink struct {
	Name     string `json:"name"`
	MAC      string `json:"mac"`
	Type     string `json:"type"`      // WLAN, LAN, ...
	Iface    string `json:"interface"` // interface name on the mesh node, e.g. "AP:5G:0"
	RxKbit   int64  `json:"rx_kbit"`   // node → device direction is TX; these are from the device's view
	TxKbit   int64  `json:"tx_kbit"`
	MaxKbit  int64  `json:"max_kbit"`
	IsMeshed bool   `json:"is_mesh_node"`
}

// MeshNode is a FRITZ! mesh node (router or repeater) with its connected devices.
type MeshNode struct {
	Name    string     `json:"name"`
	Model   string     `json:"model"`
	MAC     string     `json:"mac"`
	Role    string     `json:"role"`             // master or slave
	Uplink  *MeshLink  `json:"uplink,omitempty"` // for repeaters: link to the parent node
	Parent  string     `json:"parent,omitempty"` // parent node name
	Clients []MeshLink `json:"clients"`          // devices connected to this node
}

// ParseMesh turns the topology JSON into mesh nodes with their clients.
func ParseMesh(data []byte) ([]MeshNode, error) {
	var t meshJSON
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("mesh topology: %w", err)
	}
	byUID := map[string]int{}
	for i, n := range t.Nodes {
		byUID[n.UID] = i
	}
	isMeshNode := func(uid string) bool {
		i, ok := byUID[uid]
		if !ok {
			return false
		}
		r := t.Nodes[i].MeshRole
		return r == "master" || r == "slave"
	}
	ifaceName := map[string]string{}
	for _, n := range t.Nodes {
		for _, f := range n.Interfaces {
			ifaceName[f.UID] = f.Name
		}
	}

	out := []MeshNode{}
	idx := map[string]int{} // node uid → index in out
	for _, n := range t.Nodes {
		if isMeshNode(n.UID) {
			idx[n.UID] = len(out)
			out = append(out, MeshNode{Name: n.Name, Model: n.Model, MAC: n.MAC, Role: n.MeshRole})
		}
	}

	seen := map[string]bool{}
	for _, n := range t.Nodes {
		for _, f := range n.Interfaces {
			for _, l := range f.Links {
				if l.State != "CONNECTED" || seen[l.UID] {
					continue
				}
				seen[l.UID] = true
				a, b := l.Node1, l.Node2
				ifaceA := l.Iface1
				// Orient the link so that "a" is the upstream mesh node.
				switch {
				case isMeshNode(a) && isMeshNode(b):
					// Master is upstream of a slave; between two slaves keep the given order.
					if t.Nodes[byUID[b]].MeshRole == "master" {
						a, b, ifaceA = b, a, l.Iface2
					}
				case isMeshNode(b):
					a, b, ifaceA = b, a, l.Iface2
				case !isMeshNode(a):
					continue // link between two non-mesh devices
				}
				// Rates in the JSON are from node 1's perspective; flip when we swapped.
				rx, tx, mx := l.CurRx, l.CurTx, max(l.MaxRx, l.MaxTx)
				if a != l.Node1 {
					rx, tx = tx, rx
				}
				dev := t.Nodes[byUID[b]]
				link := MeshLink{
					Name: dev.Name, MAC: dev.MAC, Type: l.Type, Iface: ifaceName[ifaceA],
					// From the device's view: what the node transmits, the device receives.
					RxKbit: tx, TxKbit: rx, MaxKbit: mx, IsMeshed: isMeshNode(b),
				}
				out[idx[a]].Clients = append(out[idx[a]].Clients, link)
				if isMeshNode(b) {
					l2 := link
					out[idx[b]].Uplink = &l2
					out[idx[b]].Parent = t.Nodes[byUID[a]].Name
				}
			}
		}
	}
	for i := range out {
		sort.SliceStable(out[i].Clients, func(x, y int) bool {
			cx, cy := out[i].Clients[x], out[i].Clients[y]
			if cx.IsMeshed != cy.IsMeshed {
				return cx.IsMeshed
			}
			return strings.ToLower(cx.Name) < strings.ToLower(cy.Name)
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Role == "master" && out[j].Role != "master" })
	return out, nil
}

// Mesh returns the mesh nodes (router first, then repeaters) and their connected devices.
func (b *Box) Mesh(ctx context.Context) ([]MeshNode, error) {
	r, err := b.call(ctx, "Hosts1", "X_AVM-DE_GetMeshListPath", nil)
	if err != nil {
		return nil, err
	}
	data, err := b.TR.Get(ctx, r["NewX_AVM-DE_MeshListPath"])
	if err != nil {
		return nil, err
	}
	return ParseMesh(data)
}
