package estate

// A port of Python's difflib.SequenceMatcher (with its autojunk heuristic) and unified_diff, so the "vs prev" backup
// difference reads exactly as the Python service writes it.

import (
	"fmt"
	"sort"
	"strings"
)

type seqMatcher struct {
	a, b    []string
	b2j     map[string][]int
	matches [][3]int
}

func newSeqMatcher(a, b []string) *seqMatcher {
	s := &seqMatcher{a: a, b: b}
	s.chainB()
	return s
}

func (s *seqMatcher) chainB() {
	b := s.b
	s.b2j = map[string][]int{}
	for i, elt := range b {
		s.b2j[elt] = append(s.b2j[elt], i)
	}
	// no isjunk; autojunk: popular elements of a long sequence are junk
	n := len(b)
	if n >= 200 {
		ntest := n/100 + 1
		for elt, idxs := range s.b2j {
			if len(idxs) > ntest {
				delete(s.b2j, elt)
			}
		}
	}
}

func (s *seqMatcher) findLongestMatch(alo, ahi, blo, bhi int) (int, int, int) {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range s.b2j[s.a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	// no junk: the "bjunk" extension loops do nothing; popular elements are handled like junk only through b2j
	isPopular := func(x string) bool { _, ok := s.b2j[x]; return !ok }
	for besti > alo && bestj > blo && !isPopularJunk(isPopular, s.b[bestj-1], false) && s.a[besti-1] == s.b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && !isPopularJunk(isPopular, s.b[bestj+bestsize], false) && s.a[besti+bestsize] == s.b[bestj+bestsize] {
		bestsize++
	}
	// Python then extends with junk elements (bjunk); bjunk is empty without isjunk, so nothing more
	return besti, bestj, bestsize
}

// isPopularJunk mirrors "not isbjunk(b[j])": with no isjunk function bjunk is empty, so it is always false.
func isPopularJunk(_ func(string) bool, _ string, _ bool) bool { return false }

func (s *seqMatcher) getMatchingBlocks() [][3]int {
	if s.matches != nil {
		return s.matches
	}
	la, lb := len(s.a), len(s.b)
	queue := [][4]int{{0, la, 0, lb}}
	var mb [][3]int
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		alo, ahi, blo, bhi := q[0], q[1], q[2], q[3]
		i, j, k := s.findLongestMatch(alo, ahi, blo, bhi)
		if k > 0 {
			mb = append(mb, [3]int{i, j, k})
			if alo < i && blo < j {
				queue = append(queue, [4]int{alo, i, blo, j})
			}
			if i+k < ahi && j+k < bhi {
				queue = append(queue, [4]int{i + k, ahi, j + k, bhi})
			}
		}
	}
	sort.Slice(mb, func(x, y int) bool {
		if mb[x][0] != mb[y][0] {
			return mb[x][0] < mb[y][0]
		}
		if mb[x][1] != mb[y][1] {
			return mb[x][1] < mb[y][1]
		}
		return mb[x][2] < mb[y][2]
	})
	i1, j1, k1 := 0, 0, 0
	var nonAdj [][3]int
	for _, m := range mb {
		i2, j2, k2 := m[0], m[1], m[2]
		if i1+k1 == i2 && j1+k1 == j2 {
			k1 += k2
		} else {
			if k1 > 0 {
				nonAdj = append(nonAdj, [3]int{i1, j1, k1})
			}
			i1, j1, k1 = i2, j2, k2
		}
	}
	if k1 > 0 {
		nonAdj = append(nonAdj, [3]int{i1, j1, k1})
	}
	nonAdj = append(nonAdj, [3]int{la, lb, 0})
	s.matches = nonAdj
	return nonAdj
}

type opcode struct {
	tag            byte // r e d i
	i1, i2, j1, j2 int
}

func (s *seqMatcher) getOpcodes() []opcode {
	i, j := 0, 0
	var ans []opcode
	for _, m := range s.getMatchingBlocks() {
		ai, bj, size := m[0], m[1], m[2]
		var tag byte
		if i < ai && j < bj {
			tag = 'r'
		} else if i < ai {
			tag = 'd'
		} else if j < bj {
			tag = 'i'
		}
		if tag != 0 {
			ans = append(ans, opcode{tag, i, ai, j, bj})
		}
		i, j = ai+size, bj+size
		if size > 0 {
			ans = append(ans, opcode{'e', ai, i, bj, j})
		}
	}
	return ans
}

func (s *seqMatcher) getGroupedOpcodes(n int) [][]opcode {
	codes := s.getOpcodes()
	if len(codes) == 0 {
		codes = []opcode{{'e', 0, 1, 0, 1}}
	}
	if codes[0].tag == 'e' {
		c := codes[0]
		codes[0] = opcode{'e', maxi(c.i1, c.i2-n), c.i2, maxi(c.j1, c.j2-n), c.j2}
	}
	if last := codes[len(codes)-1]; last.tag == 'e' {
		codes[len(codes)-1] = opcode{'e', last.i1, mini(last.i2, last.i1+n), last.j1, mini(last.j2, last.j1+n)}
	}
	nn := n + n
	var groups [][]opcode
	var group []opcode
	for _, c := range codes {
		i1, i2, j1, j2 := c.i1, c.i2, c.j1, c.j2
		if c.tag == 'e' && i2-i1 > nn {
			group = append(group, opcode{'e', i1, mini(i2, i1+n), j1, mini(j2, j1+n)})
			groups = append(groups, group)
			group = nil
			i1, j1 = maxi(i1, i2-n), maxi(j1, j2-n)
		}
		group = append(group, opcode{c.tag, i1, i2, j1, j2})
	}
	if len(group) > 0 && !(len(group) == 1 && group[0].tag == 'e') {
		groups = append(groups, group)
	}
	return groups
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mini(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func formatRangeUnified(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return fmt.Sprintf("%d", beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}

// unifiedDiff is difflib.unified_diff(a, b, fromfile, tofile, lineterm="", n=n) as a list of lines.
func unifiedDiff(a, b []string, fromfile, tofile string, n int) []string {
	var out []string
	started := false
	for _, group := range newSeqMatcher(a, b).getGroupedOpcodes(n) {
		if !started {
			started = true
			out = append(out, "--- "+fromfile, "+++ "+tofile)
		}
		first, last := group[0], group[len(group)-1]
		out = append(out, fmt.Sprintf("@@ -%s +%s @@", formatRangeUnified(first.i1, last.i2), formatRangeUnified(first.j1, last.j2)))
		for _, c := range group {
			if c.tag == 'e' {
				for _, line := range a[c.i1:c.i2] {
					out = append(out, " "+line)
				}
				continue
			}
			if c.tag == 'r' || c.tag == 'd' {
				for _, line := range a[c.i1:c.i2] {
					out = append(out, "-"+line)
				}
			}
			if c.tag == 'r' || c.tag == 'i' {
				for _, line := range b[c.j1:c.j2] {
					out = append(out, "+"+line)
				}
			}
		}
	}
	return out
}

var _ = strings.Join
