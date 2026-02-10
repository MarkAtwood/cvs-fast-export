// SPDX-License-Identifier: GPL-2.0+
package rcsmodel

import (
	"fmt"
	"os"
	"sort"
	"time"

	rcs "github.com/arran4/golang-rcs"

	"thyrsus.com/cvs-fast-export/internal/cvsnum"
	"thyrsus.com/cvs-fast-export/internal/symbols"
)

type Master struct {
	Source   string
	Heads    []*symbols.Head
	Symbols  []symbols.Symbol
	Versions []symbols.Version
	Commits  map[string]*symbols.Commit
	Root     *symbols.Commit
}

func BuildMaster(source string, file *rcs.File) (*Master, error) {
	commits := make(map[string]*symbols.Commit, len(file.RevisionHeads))
	contents := make(map[string]*rcs.RevisionContent, len(file.RevisionContents))
	for _, rc := range file.RevisionContents {
		contents[rc.Revision] = rc
	}
	for _, rh := range file.RevisionHeads {
		num, err := cvsnum.Parse(rh.Revision)
		if err != nil {
			return nil, err
		}
		var log string
		if rc, ok := contents[rh.Revision]; ok {
			log = rc.Log
		}
		commits[rh.Revision] = &symbols.Commit{
			Number:   num,
			Dead:     rh.State == "dead",
			CommitID: rh.CommitID,
			Date:     rh.Date,
			Author:   rh.Author,
			Log:      log,
		}
	}

	var versions []symbols.Version
	for _, rh := range file.RevisionHeads {
		num, err := cvsnum.Parse(rh.Revision)
		if err != nil {
			return nil, err
		}
		ver := symbols.Version{Number: num}
		for _, br := range rh.Branches {
			bn, err := cvsnum.Parse(br)
			if err != nil {
				return nil, err
			}
			ver.Branches = append(ver.Branches, bn)
		}
		versions = append(versions, ver)
	}

	branchHeads, root := buildBranchHeads(commits, versions)

	var sym []symbols.Symbol
	for name, rev := range file.SymbolMap {
		n, err := cvsnum.Parse(rev)
		if err != nil {
			return nil, err
		}
		sym = append(sym, symbols.Symbol{Name: name, Number: n})
	}

	return &Master{
		Source:   source,
		Heads:    branchHeads,
		Symbols:  sym,
		Versions: versions,
		Commits:  commits,
		Root:     root,
	}, nil
}

func buildBranchHeads(commits map[string]*symbols.Commit, versions []symbols.Version) ([]*symbols.Head, *symbols.Commit) {
	trunkNum, ok := findTrunkStart(commits)
	var heads []*symbols.Head
	var root *symbols.Commit
	if ok {
		trunk := buildBranchChain(commits, trunkNum)
		if trunk != nil {
			root = lastCommit(trunk)
			heads = append(heads, &symbols.Head{
				RefName: "master",
				Commit:  trunk,
				Number:  trunk.Number,
				Degree:  2,
			})
		}
	}

	seen := make(map[string]struct{})
	for _, ver := range versions {
		for _, br := range ver.Branches {
			key := cvsnum.String(br)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			head := buildBranchChain(commits, br)
			if head == nil {
				continue
			}
			heads = append(heads, &symbols.Head{Commit: head})
		}
	}
	return heads, root
}

func findTrunkStart(commits map[string]*symbols.Commit) (cvsnum.Number, bool) {
	var min cvsnum.Number
	found := false
	for _, c := range commits {
		if !cvsnum.IsTrunk(c.Number) {
			continue
		}
		if !found || cvsnum.Compare(c.Number, min) < 0 {
			min = c.Number
			found = true
		}
	}
	return min, found
}

func buildBranchChain(commits map[string]*symbols.Commit, branch cvsnum.Number) *symbols.Commit {
	var chain []*symbols.Commit
	for _, c := range commits {
		if onBranch(c.Number, branch) {
			chain = append(chain, c)
		}
	}
	if len(chain) == 0 {
		return nil
	}
	sort.Slice(chain, func(i, j int) bool {
		return cvsnum.Compare(chain[i].Number, chain[j].Number) < 0
	})
	for i := len(chain) - 1; i > 0; i-- {
		chain[i].Parent = chain[i-1]
	}
	adjustDates(chain)
	return chain[len(chain)-1]
}

func adjustDates(chain []*symbols.Commit) {
	if len(chain) < 2 {
		return
	}
	var gc *symbols.Commit
	for i := len(chain) - 1; i > 0; i-- {
		c := chain[i]
		p := chain[i-1]
		if p.Date.After(c.Date) {
			if gc != nil && !p.Date.After(gc.Date) {
				c.Date = p.Date
			} else {
				p.Date = c.Date
			}
		}
		gc = c
	}
}

func lastCommit(head *symbols.Commit) *symbols.Commit {
	if head == nil {
		return nil
	}
	c := head
	for c.Parent != nil {
		c = c.Parent
	}
	return c
}

func onBranch(rev, branch cvsnum.Number) bool {
	bn := normalizeBranch(branch)
	rn := normalizeBranch(rev)
	if bn.C != rn.C {
		return false
	}
	for i := 0; i < bn.C-1; i++ {
		if bn.N[i] != rn.N[i] {
			return false
		}
	}
	return true
}

func normalizeBranch(n cvsnum.Number) cvsnum.Number {
	if n.C == 0 {
		return n
	}
	if n.C&1 == 1 {
		n = cvsnum.AppendZero(n)
	}
	return n
}

func LoadFile(path string) (*rcs.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rcsFile, err := rcs.ParseFile(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return rcsFile, nil
}
