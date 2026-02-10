// SPDX-License-Identifier: GPL-2.0+
package symbols

import (
	"fmt"
	"time"

	"thyrsus.com/cvs-fast-export/internal/cvsnum"
)

type Commit struct {
	Number   cvsnum.Number
	Parent   *Commit
	Dead     bool
	CommitID string
	Date     time.Time
	Author   string
	Log      string
	Text     string
	FileName string
	Tail     bool
}

type Head struct {
	RefName string
	Number  cvsnum.Number
	Commit  *Commit
	Parent  *Head
	Degree  int
	Tail    bool
}

type Symbol struct {
	Name   string
	Number cvsnum.Number
}

type Tag struct {
	Name   string
	Commit *Commit
}

type Version struct {
	Number   cvsnum.Number
	Branches []cvsnum.Number
}

// SetRefs applies symbol names to heads and extracts tags.
// This mirrors the core of revcvs.c:cvs_master_set_refs.
func SetRefs(heads []*Head, symbols []Symbol, findRevision func(cvsnum.Number) *Commit) ([]*Head, []Tag) {
	var tags []Tag
	for _, s := range symbols {
		if cvsnum.IsHead(s.Number) {
			h := findHeadByBranch(heads, s.Number)
			if h != nil {
				if h.RefName == "" {
					h.RefName = s.Name
					h.Degree = cvsnum.Degree(s.Number)
				} else {
					h = &Head{
						RefName: s.Name,
						Commit:  h.Commit,
						Degree:  cvsnum.Degree(s.Number),
					}
					heads = append(heads, h)
				}
			} else {
				n := s.Number
				for n.C >= 4 {
					n.C -= 2
					n.N = n.N[:n.C]
					c := findRevision(n)
					if c != nil {
						h = &Head{
							RefName: s.Name,
							Commit:  c,
							Degree:  cvsnum.Degree(s.Number),
						}
						heads = append(heads, h)
						break
					}
				}
			}
			if h != nil {
				h.Number = s.Number
			}
		} else {
			if c := findRevision(s.Number); c != nil {
				tags = append(tags, Tag{Name: s.Name, Commit: c})
			}
		}
	}

	heads = fixUnnamedHeads(heads)
	heads = discardZeroHeads(heads)
	linkParents(heads)
	return heads, tags
}

// PatchVendorBranches mirrors cvs_master_patch_vendor_branch.
// It mutates heads in place and returns whether any vendor branch was found.
func PatchVendorBranches(heads []*Head) ([]*Head, bool) {
	trunk := findHeadByName(heads, "master")
	if trunk == nil {
		return heads, false
	}

	var nvendor *Head
	foundVendor := false
	for _, vendor := range heads {
		if vendor.Commit == nil || !cvsnum.IsVendor(vendor.Commit.Number) {
			continue
		}
		foundVendor = true
		nvendor = vendor

		if vendor.RefName == "" {
			vlast := vendor.Commit
			for vlast != nil && vlast.Parent != nil {
				vlast = vlast.Parent
			}
			if vlast != nil {
				branch := cloneNumber(vlast.Number)
				if branch.C > 0 {
					branch.C--
					branch.N = branch.N[:branch.C]
				}
				name := fmt.Sprintf("import-%s", cvsnum.String(branch))
				vendor.RefName = name
			}
		}
		vendor.Parent = trunk
		vendor.Degree = vendor.Commit.Number.C
		vendor.Number = vendor.Commit.Number
	}

	if nvendor != nil && trunk.Commit != nil && trunk.Commit.Parent == nil {
		oldtip := trunk.Commit
		trunk.Commit = nvendor.Commit
		trunk.Degree = nvendor.Commit.Number.C
		trunk.Number = nvendor.Commit.Number
		vlast := trunk.Commit
		for vlast != nil && vlast.Parent != nil {
			vlast = vlast.Parent
		}
		if vlast != nil {
			vlast.Parent = oldtip
		}
		heads = removeHead(heads, nvendor)
	}

	return heads, foundVendor
}

// GraftBranches mirrors cvs_master_graft_branches.
func GraftBranches(heads []*Head, versions []Version, findRevision func(cvsnum.Number) *Commit) {
	trunk := findHeadByName(heads, "master")
	for _, h := range heads {
		if h == trunk || h.Tail {
			continue
		}
		c := h.Commit
		for c != nil && c.Parent != nil {
			if c.Tail {
				c = nil
				break
			}
			c = c.Parent
		}
		if c == nil {
			continue
		}
		for _, cv := range versions {
			for _, cb := range cv.Branches {
				if cvsnum.Compare(cb, c.Number) == 0 {
					c.Parent = findRevision(cv.Number)
					c.Tail = true
					break
				}
			}
			if c.Parent != nil {
				break
			}
		}
	}
}

func fixUnnamedHeads(heads []*Head) []*Head {
	for _, h := range heads {
		if h.RefName != "" {
			continue
		}
		c := firstLiveCommit(h.Commit)
		if c == nil {
			h.Number = cvsnum.Zero
			continue
		}
		n := c.Number
		if n.C >= 2 {
			n = cloneNumber(n)
			n.N[n.C-1] = n.N[n.C-2]
			n.N[n.C-2] = 0
		}
		h.Number = n
		h.Degree = cvsnum.Degree(n)
	}
	return heads
}

func discardZeroHeads(heads []*Head) []*Head {
	out := heads[:0]
	for _, h := range heads {
		if !cvsnum.Equal(h.Number, cvsnum.Zero) {
			out = append(out, h)
		}
	}
	return out
}

func linkParents(heads []*Head) {
	for _, h := range heads {
		if h.Number.C < 4 {
			continue
		}
		n := cloneNumber(h.Number)
		n.C -= 2
		n.N = n.N[:n.C]
		p := findBranch(heads, n)
		h.Parent = p
		if p != nil && h.RefName == "" {
			if h.Commit != nil && h.Commit.CommitID != "" {
				h.RefName = fmt.Sprintf("%s-UNNAMED-BRANCH-%s", p.RefName, h.Commit.CommitID)
			} else {
				h.RefName = fmt.Sprintf("%s-UNNAMED-BRANCH", p.RefName)
			}
		}
	}
}

func findHeadByBranch(heads []*Head, number cvsnum.Number) *Head {
	for _, h := range heads {
		if h.Commit != nil && cvsnum.SameBranch(h.Commit.Number, number) {
			return h
		}
	}
	return nil
}

func findBranch(heads []*Head, number cvsnum.Number) *Head {
	n := cloneNumber(number)
	for n.C >= 2 {
		for _, h := range heads {
			if cvsnum.SameBranch(h.Number, n) {
				return h
			}
		}
		n.C -= 2
		if n.C > 0 {
			n.N = n.N[:n.C]
		}
	}
	return nil
}

func firstLiveCommit(c *Commit) *Commit {
	for c != nil {
		if !c.Dead {
			return c
		}
		c = c.Parent
	}
	return nil
}

func cloneNumber(n cvsnum.Number) cvsnum.Number {
	out := cvsnum.Number{C: n.C, N: make([]int, n.C)}
	copy(out.N, n.N[:n.C])
	return out
}

func findHeadByName(heads []*Head, name string) *Head {
	for _, h := range heads {
		if h.RefName == name {
			return h
		}
	}
	return nil
}

func removeHead(heads []*Head, target *Head) []*Head {
	out := heads[:0]
	for _, h := range heads {
		if h != target {
			out = append(out, h)
		}
	}
	return out
}
