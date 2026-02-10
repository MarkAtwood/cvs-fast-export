// SPDX-License-Identifier: GPL-2.0+
package rcsmodel

import (
	"fmt"
	"os"
	"sort"

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
}

func BuildMaster(source string, file *rcs.File) (*Master, error) {
	commits := make(map[string]*symbols.Commit, len(file.RevisionHeads))
	for _, rh := range file.RevisionHeads {
		num, err := cvsnum.Parse(rh.Revision)
		if err != nil {
			return nil, err
		}
		commits[rh.Revision] = &symbols.Commit{
			Number:   num,
			Dead:     rh.State == "dead",
			CommitID: rh.CommitID,
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

	branchHeads := buildBranchHeads(commits, versions)

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
	}, nil
}

func buildBranchHeads(commits map[string]*symbols.Commit, versions []symbols.Version) []*symbols.Head {
	trunkNum, ok := findTrunkStart(commits)
	var heads []*symbols.Head
	if ok {
		trunk := buildBranchChain(commits, trunkNum)
		if trunk != nil {
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
	return heads
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
		if cvsnum.SameBranch(c.Number, branch) {
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
	return chain[len(chain)-1]
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
