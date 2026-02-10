// SPDX-License-Identifier: GPL-2.0+
package compare

import "strings"

// PathDeepCompare mirrors import.c:path_deep_compare.
func PathDeepCompare(a, b string) int {
	if a == b {
		return 0
	}
	compar := strings.Compare(a, b)
	if compar == 0 {
		return 0
	}

	aslash := strings.LastIndexByte(a, '/')
	bslash := strings.LastIndexByte(b, '/')

	if aslash == -1 && bslash == -1 {
		return compar
	}
	if aslash == -1 {
		return 1
	}
	if bslash == -1 {
		return -1
	}

	alen := len(a)
	blen := len(b)

	if aslash == -1 || bslash == -1 {
		return compar
	}

	if aslash < bslash {
		aidx := aslash
		if aidx < blen && b[aidx] == '/' && strings.HasPrefix(a, b[:aidx]) {
			return 1
		}
	} else {
		bidx := bslash
		if bidx < alen && a[bidx] == '/' && strings.HasPrefix(b, a[:bidx]) {
			return -1
		}
	}
	return compar
}
