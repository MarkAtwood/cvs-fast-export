// SPDX-License-Identifier: GPL-2.0+
package collate

import (
	"sort"
	"time"

	"thyrsus.com/cvs-fast-export/internal/compare"
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
	Branches   []*GitBranch
	CommitOf   map[*symbols.Commit]*GitCommit
	ByNumber   map[string]*GitCommit
	TagTargets map[string][]*symbols.Commit
	TagOrder   []string
}

func Build(masters []*rcsmodel.Master, opts Options) *Repo {
	byName := collectBranches(masters)
	branches := toposortBranches(byName, masters)

	repo := &Repo{
		Branches:   branches,
		CommitOf:   make(map[*symbols.Commit]*GitCommit),
		ByNumber:   make(map[string]*GitCommit),
		TagTargets: make(map[string][]*symbols.Commit),
	}
	for _, b := range branches {
		heads := gatherHeads(masters, b.Name)
		if len(heads) == 0 {
			continue
		}
		b.Head = collateBranch(b.Name, heads, opts, repo.CommitOf, repo.ByNumber)
	}
	linkBranchParents(branches, repo.ByNumber)
	collectTags(masters, repo)
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

func collateBranch(branch string, heads []*symbols.Head, opts Options, commitOf map[*symbols.Commit]*GitCommit, byNumber map[string]*GitCommit) *GitCommit {
	seen := make(map[*symbols.Commit]struct{})
	var commits []*symbols.Commit
	for _, h := range heads {
		for c := h.Commit; c != nil; c = c.Parent {
			if _, ok := seen[c]; ok {
				continue
			}
			seen[c] = struct{}{}
			commits = append(commits, c)
			if c.Tail {
				break
			}
		}
	}
	sort.Slice(commits, func(i, j int) bool {
		if !commits[i].Date.Equal(commits[j].Date) {
			return commits[i].Date.Before(commits[j].Date)
		}
		if commits[i].FileName != commits[j].FileName {
			return compare.PathDeepCompare(commits[i].FileName, commits[j].FileName) < 0
		}
		return cvsnum.Compare(commits[i].Number, commits[j].Number) < 0
	})

	if opts.TrustCommitIDs {
		group := make(map[string]*GitCommit)
		var order []*GitCommit
		var noID []*symbols.Commit
		for _, c := range commits {
			if c.CommitID == "" {
				noID = append(noID, c)
				continue
			}
			g := group[c.CommitID]
			if g == nil {
				g = &GitCommit{
					Date:    c.Date,
					Author:  c.Author,
					Log:     c.Log,
					Files:   []*symbols.Commit{c},
					RefName: branch,
				}
				group[c.CommitID] = g
				order = append(order, g)
			} else {
				g.Files = append(g.Files, c)
				if c.Date.After(g.Date) {
					g.Date = c.Date
				}
			}
			commitOf[c] = g
			byNumber[cvsnum.String(c.Number)] = g
		}
		if len(noID) > 0 {
			sort.Slice(noID, func(i, j int) bool {
				if !noID[i].Date.Equal(noID[j].Date) {
					return noID[i].Date.Before(noID[j].Date)
				}
				if noID[i].FileName != noID[j].FileName {
					return compare.PathDeepCompare(noID[i].FileName, noID[j].FileName) < 0
				}
				return cvsnum.Compare(noID[i].Number, noID[j].Number) < 0
			})
			var prev *GitCommit
			for _, c := range noID {
				if prev != nil && commitsMatch(prev, c, opts) {
					prev.Files = append(prev.Files, c)
					commitOf[c] = prev
					byNumber[cvsnum.String(c.Number)] = prev
					continue
				}
				g := &GitCommit{
					Date:    c.Date,
					Author:  c.Author,
					Log:     c.Log,
					Files:   []*symbols.Commit{c},
					RefName: branch,
				}
				prev = g
				order = append(order, g)
				commitOf[c] = g
				byNumber[cvsnum.String(c.Number)] = g
			}
		}
		sort.Slice(order, func(i, j int) bool {
			if !order[i].Date.Equal(order[j].Date) {
				return order[i].Date.Before(order[j].Date)
			}
			return order[i].Log < order[j].Log
		})
		var prev *GitCommit
		for _, g := range order {
			g.Parent = prev
			prev = g
		}
		return prev
	}

	var prev *GitCommit
	for _, c := range commits {
		if prev != nil && commitsMatch(prev, c, opts) {
			prev.Files = append(prev.Files, c)
			commitOf[c] = prev
			continue
		}
		g := &GitCommit{
			Date:    c.Date,
			Author:  c.Author,
			Log:     c.Log,
			Files:   []*symbols.Commit{c},
			RefName: branch,
		}
		g.Parent = prev
		prev = g
		commitOf[c] = g
		byNumber[cvsnum.String(c.Number)] = g
	}
	return prev
}

func collectTags(masters []*rcsmodel.Master, repo *Repo) {
	seen := make(map[string]map[*symbols.Commit]struct{})
	seenOrder := make(map[string]struct{})
	for _, m := range masters {
		for _, t := range m.Tags {
			if t.Commit == nil {
				continue
			}
			if _, ok := seenOrder[t.Name]; !ok {
				repo.TagOrder = append([]string{t.Name}, repo.TagOrder...)
				seenOrder[t.Name] = struct{}{}
			}
			set := seen[t.Name]
			if set == nil {
				set = make(map[*symbols.Commit]struct{})
				seen[t.Name] = set
			}
			set[t.Commit] = struct{}{}
		}
	}
	for name, set := range seen {
		for c := range set {
			repo.TagTargets[name] = append(repo.TagTargets[name], c)
		}
	}
}

func linkBranchParents(branches []*GitBranch, byNumber map[string]*GitCommit) {
	var master *GitBranch
	for _, b := range branches {
		if b.Name == "master" {
			master = b
			break
		}
	}
	var masterCommits []*GitCommit
	if master != nil {
		for c := master.Head; c != nil; c = c.Parent {
			masterCommits = append(masterCommits, c)
		}
	}
	for _, b := range branches {
		if b.Name == "master" || b.Head == nil {
			continue
		}
		// Find oldest commit on branch
		var tail *GitCommit
		for c := b.Head; c != nil; c = c.Parent {
			tail = c
		}
		if tail == nil || len(tail.Files) == 0 {
			continue
		}
		base := tail.Files[0].Number
		if base.C < 4 {
			continue
		}
		base.C -= 2
		base.N = base.N[:base.C]
		if gc := byNumber[cvsnum.String(base)]; gc != nil {
			tail.Parent = gc
			continue
		}
		if len(masterCommits) > 0 {
			var pick *GitCommit
			for _, mc := range masterCommits {
				if !mc.Date.After(tail.Date) {
					pick = mc
					break
				}
			}
			if pick != nil {
				tail.Parent = pick
			}
		}
	}
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
