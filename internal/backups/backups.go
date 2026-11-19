// Package backups keeps rotating configuration exports of a FRITZ!Box and compares them.
package backups

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const stamp = "20060102-150405"

// DefaultDir is os.UserConfigDir()/fbctl/backups/<router>.
func DefaultDir(router string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fbctl", "backups", router), nil
}

// Entry is one stored export.
type Entry struct {
	Path string    `json:"path"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

func namePattern(router string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(router) + `-(\d{8}-\d{6})\.export$`)
}

// List returns the router's exports in dir, oldest first. Other files are ignored.
func List(dir, router string) ([]Entry, error) {
	des, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	re := namePattern(router)
	var out []Entry
	for _, de := range des {
		m := re.FindStringSubmatch(de.Name())
		if m == nil || !de.Type().IsRegular() {
			continue
		}
		t, err := time.ParseInLocation(stamp, m[1], time.Local)
		if err != nil {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{Path: filepath.Join(dir, de.Name()), Time: t, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// Save writes data as <router>-<time>.export (mode 0600, folder 0700). If the newest existing
// export has the same configuration (see Same), nothing is written and unchanged is true.
func Save(dir, router string, data []byte, now time.Time, skipUnchanged bool) (path string, unchanged bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	if skipUnchanged {
		list, err := List(dir, router)
		if err != nil {
			return "", false, err
		}
		if len(list) > 0 {
			prev, err := os.ReadFile(list[len(list)-1].Path)
			if err == nil && Same(prev, data) {
				return list[len(list)-1].Path, true, nil
			}
		}
	}
	path = filepath.Join(dir, fmt.Sprintf("%s-%s.export", router, now.Format(stamp)))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", false, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return "", false, err
	}
	return path, false, f.Close()
}

// Prune deletes all but the newest keep exports of the router and returns the removed paths.
// Only files matching the export name pattern are touched.
func Prune(dir, router string, keep int) ([]string, error) {
	if keep < 1 {
		return nil, errors.New("keep at least one backup")
	}
	list, err := List(dir, router)
	if err != nil || len(list) <= keep {
		return nil, err
	}
	var removed []string
	for _, e := range list[:len(list)-keep] {
		if err := os.Remove(e.Path); err != nil {
			return removed, err
		}
		removed = append(removed, e.Path)
	}
	return removed, nil
}

// Resolve finds an export by file path, by position in List (1 = oldest, -1 = newest) or by
// a part of its file name such as a date (20261119).
func Resolve(list []Entry, ref string) (Entry, error) {
	if strings.ContainsRune(ref, os.PathSeparator) {
		info, err := os.Stat(ref)
		if err != nil {
			return Entry{}, err
		}
		return Entry{Path: ref, Time: info.ModTime(), Size: info.Size()}, nil
	}
	if n, err := strconv.Atoi(ref); err == nil && len(strings.TrimPrefix(ref, "-")) <= 4 {
		if n < 0 {
			n = len(list) + 1 + n
		}
		if n >= 1 && n <= len(list) {
			return list[n-1], nil
		}
		return Entry{}, fmt.Errorf("no backup #%s (there are %d)", ref, len(list))
	}
	var hits []Entry
	for _, e := range list {
		if strings.Contains(filepath.Base(e.Path), ref) {
			hits = append(hits, e)
		}
	}
	switch len(hits) {
	case 0:
		return Entry{}, fmt.Errorf("no backup matches %q", ref)
	case 1:
		return hits[0], nil
	}
	return Entry{}, fmt.Errorf("%q matches %d backups; be more specific", ref, len(hits))
}
