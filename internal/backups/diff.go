package backups

import (
	"regexp"
	"strings"
)

// Lines that change with every export although the configuration did not: the export
// password hash, the timestamp comments of each file and the checksum line. Encrypted values
// ($$$$…) are re-encrypted every time, so they are compared as placeholders.
var (
	volatileLine = regexp.MustCompile(`^(Password=|\*\*\*\* END OF EXPORT|\s*\* (Mon|Tue|Wed|Thu|Fri|Sat|Sun) )`)
	encrypted    = regexp.MustCompile(`\$\$\$\$[A-Za-z0-9+/=]+`)
	sectionLine  = regexp.MustCompile(`^\*\*\*\* (CFGFILE|BINFILE|B64FILE|CRYPTEDBINFILE|CRYPTEDB64FILE):(\S+)`)
)

// normalize returns the lines used for comparing: volatile lines are dropped and encrypted
// values replaced. orig maps each kept line to its line number in the export (1-based).
func normalize(data []byte) (lines []string, orig []int) {
	for i, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if volatileLine.MatchString(l) {
			continue
		}
		lines = append(lines, encrypted.ReplaceAllString(l, "$$$$(encrypted)"))
		orig = append(orig, i+1)
	}
	return lines, orig
}

// Same reports whether two exports hold the same configuration.
func Same(a, b []byte) bool {
	la, _ := normalize(a)
	lb, _ := normalize(b)
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if la[i] != lb[i] {
			return false
		}
	}
	return true
}

// Op is one diff line.
type Op struct {
	Kind byte // ' ' context, '-' only in the old export, '+' only in the new one
	Text string
}

// Hunk is a group of changes with context, inside one config file of the export.
type Hunk struct {
	Section string // e.g. ar7.cfg ("" before the first file)
	OldLine int    // first line of the hunk in the old export (1-based)
	NewLine int
	Ops     []Op
}

// Diff compares two exports and returns the changed regions with context lines around them.
func Diff(a, b []byte, context int) []Hunk {
	la, oa := normalize(a)
	lb, ob := normalize(b)
	script := myers(la, lb)
	secA, secB := sections(la), sections(lb)

	// Positions in a and b before each script step.
	ia, jb := make([]int, len(script)+1), make([]int, len(script)+1)
	for k, op := range script {
		ia[k+1], jb[k+1] = ia[k], jb[k]
		if op != '+' {
			ia[k+1]++
		}
		if op != '-' {
			jb[k+1]++
		}
	}
	// Script ranges to show: every change plus context, merged when they touch.
	type span struct{ from, to int } // [from, to)
	var spans []span
	for k, op := range script {
		if op == ' ' {
			continue
		}
		from, to := max(0, k-context), min(len(script), k+context+1)
		if n := len(spans); n > 0 && from <= spans[n-1].to {
			spans[n-1].to = max(spans[n-1].to, to)
			continue
		}
		spans = append(spans, span{from, to})
	}
	hunks := make([]Hunk, 0, len(spans))
	for _, sp := range spans {
		i, j := ia[sp.from], jb[sp.from]
		h := Hunk{OldLine: lineAt(oa, i), NewLine: lineAt(ob, j)}
		labelled := false
		for _, op := range script[sp.from:sp.to] {
			if !labelled && op != ' ' {
				// The hunk is labelled with the config file of its first change.
				if op == '+' {
					h.Section = secB[j]
				} else {
					h.Section = secA[i]
				}
				labelled = true
			}
			switch op {
			case ' ':
				h.Ops = append(h.Ops, Op{' ', la[i]})
				i++
				j++
			case '-':
				h.Ops = append(h.Ops, Op{'-', la[i]})
				i++
			case '+':
				h.Ops = append(h.Ops, Op{'+', lb[j]})
				j++
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

func lineAt(orig []int, i int) int {
	if i < len(orig) {
		return orig[i]
	}
	if len(orig) > 0 {
		return orig[len(orig)-1] + 1
	}
	return 1
}

func sections(lines []string) []string {
	out := make([]string, len(lines))
	cur := ""
	for i, l := range lines {
		if m := sectionLine.FindStringSubmatch(l); m != nil {
			cur = m[2]
		}
		out[i] = cur
	}
	return out
}

// myers returns the shortest edit script from a to b as a sequence of ' ', '-' and '+'.
func myers(a, b []string) []byte {
	n, m := len(a), len(b)
	maxD := n + m
	if maxD == 0 {
		return nil
	}
	off := maxD
	v := make([]int, 2*maxD+2)
	var trace [][]int
	for d := 0; d <= maxD; d++ {
		snap := make([]int, len(v))
		copy(snap, v)
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, d, off)
			}
		}
	}
	return nil
}

func backtrack(trace [][]int, a, b []string, d, off int) []byte {
	x, y := len(a), len(b)
	var rev []byte
	for ; d > 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[off+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, ' ')
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, '+')
			y--
		} else {
			rev = append(rev, '-')
			x--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, ' ')
		x--
		y--
	}
	out := make([]byte, len(rev))
	for i, c := range rev {
		out[len(rev)-1-i] = c
	}
	return out
}
