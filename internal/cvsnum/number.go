// SPDX-License-Identifier: GPL-2.0+
package cvsnum

import (
	"fmt"
	"strconv"
	"strings"
)

type Number struct {
	C int
	N []int
}

var Zero = Number{C: 0, N: nil}

func Parse(s string) (Number, error) {
	if s == "" {
		return Zero, fmt.Errorf("empty CVS number")
	}
	parts := strings.Split(s, ".")
	n := Number{C: len(parts), N: make([]int, len(parts))}
	for i, p := range parts {
		if p == "" {
			return Zero, fmt.Errorf("invalid CVS number %q", s)
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return Zero, fmt.Errorf("invalid CVS number %q", s)
		}
		n.N[i] = v
	}
	return n, nil
}

func IsHead(n Number) bool {
	if n.C <= 2 || (n.C&1) != 0 {
		return false
	}
	return n.N[n.C-2] == 0
}

func SameBranch(a, b Number) bool {
	if a.C&1 == 1 {
		a = appendZero(a)
	}
	if b.C&1 == 1 {
		b = appendZero(b)
	}
	if a.C != b.C {
		return false
	}
	if a.C == 2 {
		return true
	}
	for i := 0; i < a.C-1; i++ {
		an := a.N[i]
		bn := b.N[i]
		if i == a.C-2 {
			if an == 0 {
				an = a.N[i+1]
			}
			if bn == 0 {
				bn = b.N[i+1]
			}
		}
		if an != bn {
			return false
		}
	}
	return true
}

func Equal(a, b Number) bool {
	if a.C != b.C {
		return false
	}
	for i := 0; i < a.C; i++ {
		if a.N[i] != b.N[i] {
			return false
		}
	}
	return true
}

// Compare gives a total ordering; parent always < child.
func Compare(a, b Number) int {
	n := min(a.C, b.C)
	for i := 0; i < n; i++ {
		if a.N[i] < b.N[i] {
			return -1
		}
		if a.N[i] > b.N[i] {
			return 1
		}
	}
	if a.C < b.C {
		return -1
	}
	if a.C > b.C {
		return 1
	}
	return 0
}

func Degree(n Number) int {
	if n.C < 4 {
		return n.C
	}
	four := n
	four.C = 4
	four.N = append([]int(nil), n.N[:4]...)
	if IsVendor(four) {
		return n.C - 1
	}
	return n.C
}

func IsTrunk(n Number) bool { return n.C == 2 }

// Vendor branches are 1.1.x with odd x.
func IsVendor(n Number) bool {
	if n.C != 4 {
		return false
	}
	return n.N[0] == 1 && n.N[1] == 1 && (n.N[2]&1) == 1
}

func String(n Number) string {
	if n.C == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < n.C; i++ {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(n.N[i]))
	}
	return b.String()
}

func AppendZero(n Number) Number {
	return appendZero(n)
}

func appendZero(n Number) Number {
	out := Number{C: n.C + 1, N: make([]int, n.C+1)}
	copy(out.N, n.N)
	out.N[n.C] = 0
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
