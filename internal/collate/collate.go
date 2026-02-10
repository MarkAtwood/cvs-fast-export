// SPDX-License-Identifier: GPL-2.0+
package collate

import (
	"sort"
	"time"

	"thyrsus.com/cvs-fast-export/internal/cvsnum"
	"thyrsus.com/cvs-fast-export/internal/rcsmodel"
	"thyrsus.com/cvs-fast-export/internal/symbols"
)

type Options struct {
	CommitTimeWindow time.Duration
	TrustCommitIDs   bool
}

type GitCommit struct {
	Parent  *GitCommit
	Date    time.Time
	Author  string
	Log     string
	Files   []*symbols.Commit
	RefName string
}

type GitBranch struct {
	Name   string
	Head   *GitCommit
	Parent *GitBranch
	Depth  int
}

type Repo struct {
	Branches []*GitBranch
}

func Build(masters []*rcsmodel.Master, opts Options) *Repo {
	byName := collectBranches(masters)
	branches := toposortBranches(byName, masters)

	repo := &Repo{Branches: branches}
	for _, b := range branches {
		heads := gatherHeads(masters, b.Name)
		if len(heads) == 0 {
			continue
		}
		b.Head = collateBranch(heads, opts)
	}
	return repo
}

// CommitsForBranch flattens commits from newest to oldest for a branch.
func CommitsForBranch(branch *GitBranch) []*GitCommit {
	var out []*GitCommit
	for c := branch.Head; c != nil; c = c.Parent {
		out = append(out, c)
	}
	return out
}

func collectBranches(masters []*rcsmodel.Master) map[string]*GitBranch {
	byName := make(map[string]*GitBranch)
	for _, m := range masters {
		for _, h := range m.Heads {
			name := h.RefName
			if name == "" {
				continue
			}
			if _, ok := byName[name]; !ok {
				byName[name] = &GitBranch{Name: name}
			}
		}
	}
	return byName
}

func toposortBranches(byName map[string]*GitBranch, masters []*rcsmodel.Master) []*GitBranch {
	var branches []*GitBranch
	for _, b := range byName {
		branches = append(branches, b)
	}
	sort.Slice(branches, func(i, j int) bool {
		return branches[i].Name < branches[j].Name
	})
	// parent relationships are resolved lazily in this minimal skeleton
	return branches
}

func gatherHeads(masters []*rcsmodel.Master, name string) []*symbols.Head {
	var heads []*symbols.Head
	for _, m := range masters {
		for _, h := range m.Heads {
			if h.RefName == name {
				heads = append(heads, h)
			}
		}
	}
	return heads
}

func collateBranch(heads []*symbols.Head, opts Options) *GitCommit {
	var commits []*symbols.Commit
	for _, h := range heads {
		for c := h.Commit; c != nil; c = c.Parent {
			commits = append(commits, c)
		}
	}
	sort.Slice(commits, func(i, j int) bool {
		return cvsnum.Compare(commits[i].Number, commits[j].Number) < 0
	})

	var prev *GitCommit
	for _, c := range commits {
		if c.Dead {
			continue
		}
		if prev != nil && commitsMatch(prev, c, opts) {
			prev.Files = append(prev.Files, c)
			continue
		}
		g := &GitCommit{
			Date:   c.Date,
			Author: c.Author,
			Log:    c.Log,
			Files:  []*symbols.Commit{c},
		}
		g.Parent = prev
		prev = g
	}
	return prev
}

func commitsMatch(g *GitCommit, c *symbols.Commit, opts Options) bool {
	if opts.TrustCommitIDs && g.Files != nil && len(g.Files) > 0 {
		if g.Files[0].CommitID != "" || c.CommitID != "" {
			return g.Files[0].CommitID == c.CommitID && g.Files[0].CommitID != ""
		}
	}
	if g.Author != c.Author {
		return false
	}
	if g.Log != c.Log {
		return false
	}
	delta := g.Date.Sub(c.Date)
	if delta < 0 {
		delta = -delta
	}
	return delta < opts.CommitTimeWindow
}
