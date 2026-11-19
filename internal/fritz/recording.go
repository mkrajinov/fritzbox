package fritz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mkrajinov/fritzbox/internal/tr064"
)

// Recording is a downloaded answering machine message.
type Recording struct {
	Message Message
	Data    []byte
	Ext     string // ".wav" when the data has a RIFF header, else ".bin"
}

// recordingURL builds the download URL for a message: the server and session of the message
// list URL, path /download.lua?path=<recording>. Paths that already point at download.lua are kept.
func recordingURL(listURL, path string) (string, error) {
	lu, err := url.Parse(listURL)
	if err != nil {
		return "", err
	}
	sid := lu.Query().Get("sid")
	if path == "" {
		return "", errors.New("the box lists no recording for this message")
	}
	var u *url.URL
	if strings.HasPrefix(path, "/download.lua") {
		if u, err = url.Parse(path); err != nil {
			return "", err
		}
	} else {
		u = &url.URL{Path: "/download.lua", RawQuery: url.Values{"path": {path}}.Encode()}
	}
	q := u.Query()
	if sid != "" && q.Get("sid") == "" {
		q.Set("sid", sid)
	}
	u.RawQuery = q.Encode()
	u.Scheme, u.Host = lu.Scheme, lu.Host
	return u.String(), nil
}

// Recording downloads the audio of an answering machine message.
func (b *Box) Recording(ctx context.Context, tam, index int) (Recording, error) {
	r, err := b.call(ctx, tamSvc, "GetMessageList", tr064.Args{"NewIndex": itoa(tam)})
	if err != nil {
		return Recording{}, err
	}
	listURL := r["NewURL"]
	data, err := b.TR.Get(ctx, listURL)
	if err != nil {
		return Recording{}, err
	}
	ms, err := ParseMessages(data, time.Local)
	if err != nil {
		return Recording{}, err
	}
	for _, m := range ms {
		if m.Index != index {
			continue
		}
		u, err := recordingURL(listURL, m.Path)
		if err != nil {
			return Recording{}, err
		}
		audio, err := b.TR.Get(ctx, u)
		if err != nil {
			return Recording{}, fmt.Errorf("download recording: %w", err)
		}
		rec := Recording{Message: m, Data: audio, Ext: ".bin"}
		if bytes.HasPrefix(audio, []byte("RIFF")) {
			rec.Ext = ".wav"
		}
		return rec, nil
	}
	return Recording{}, fmt.Errorf("answering machine %d has no message %d", tam, index)
}
