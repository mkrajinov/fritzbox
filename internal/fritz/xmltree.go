package fritz

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// node is a generic XML element. Phonebook entries are edited as trees so that fields fb does
// not know (images, quickdial, vanity, doorphone, …) survive an edit unchanged.
type node struct {
	Name     string
	Attrs    []xml.Attr
	Text     string
	Children []*node
}

// parseElement returns the first element called name in data (at any depth).
func parseElement(data []byte, name string) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, errors.New("no <" + name + "> element")
		}
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == name {
			return decodeNode(dec, se)
		}
	}
}

func decodeNode(dec *xml.Decoder, se xml.StartElement) (*node, error) {
	n := &node{Name: se.Name.Local, Attrs: se.Attr}
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			c, err := decodeNode(dec, t)
			if err != nil {
				return nil, err
			}
			n.Children = append(n.Children, c)
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			if len(n.Children) == 0 {
				n.Text = text.String()
			}
			return n, nil
		}
	}
}

func (n *node) encode(enc *xml.Encoder) error {
	attrs := make([]xml.Attr, len(n.Attrs))
	for i, a := range n.Attrs {
		attrs[i] = xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: a.Value}
	}
	se := xml.StartElement{Name: xml.Name{Local: n.Name}, Attr: attrs}
	if err := enc.EncodeToken(se); err != nil {
		return err
	}
	if len(n.Children) == 0 && n.Text != "" {
		if err := enc.EncodeToken(xml.CharData(n.Text)); err != nil {
			return err
		}
	}
	for _, c := range n.Children {
		if err := c.encode(enc); err != nil {
			return err
		}
	}
	return enc.EncodeToken(se.End())
}

func (n *node) String() string {
	var b bytes.Buffer
	enc := xml.NewEncoder(&b)
	_ = n.encode(enc)
	_ = enc.Flush()
	return b.String()
}

// child returns the first direct child called name, or nil.
func (n *node) child(name string) *node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ensure returns the first child called name, appending an empty one if there is none.
func (n *node) ensure(name string) *node {
	if c := n.child(name); c != nil {
		return c
	}
	c := &node{Name: name}
	n.Children = append(n.Children, c)
	return c
}

// all returns the direct children called name.
func (n *node) all(name string) []*node {
	var out []*node
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// remove drops every direct child called name.
func (n *node) remove(name string) {
	kept := n.Children[:0]
	for _, c := range n.Children {
		if c.Name != name {
			kept = append(kept, c)
		}
	}
	n.Children = kept
}

func (n *node) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *node) setAttr(name, value string) {
	for i, a := range n.Attrs {
		if a.Name.Local == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
}
