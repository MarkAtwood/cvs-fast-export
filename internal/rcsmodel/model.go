// SPDX-License-Identifier: GPL-2.0+
package rcsmodel

import (
	"fmt"
	"os"
	"sort"
	"strings"

	rcs "github.com/arran4/golang-rcs"

	"thyrsus.com/cvs-fast-export/internal/cvsnum"
	"thyrsus.com/cvs-fast-export/internal/symbols"
)

type Master struct {
	Source   string
	FileName string
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
	fileName := exportName(source)
	fulltext, err := reconstructTexts(file, contents)
	if err != nil {
		return nil, err
	}
	for _, rh := range file.RevisionHeads {
		num, err := cvsnum.Parse(rh.Revision)
		if err != nil {
			return nil, err
		}
		var log string
		var text string
		if rc, ok := contents[rh.Revision]; ok {
			log = rc.Log
			text = fulltext[rh.Revision]
		}
		commits[rh.Revision] = &symbols.Commit{
			Number:   num,
			Dead:     rh.State == "dead",
			CommitID: rh.CommitID,
			Date:     rh.Date,
			Author:   rh.Author,
			Log:      log,
			Text:     text,
			FileName: fileName,
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
		FileName: fileName,
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

func exportName(source string) string {
	path := source
	if idx := strings.Index(path, "/module/"); idx >= 0 {
		path = path[idx+len("/module/"):]
	}
	path = strings.ReplaceAll(path, "/Attic/", "/")
	path = strings.ReplaceAll(path, "/RCS/", "/")
	if strings.HasSuffix(path, ",v") {
		path = strings.TrimSuffix(path, ",v")
	}
	if strings.HasSuffix(path, ".cvsignore") {
		path = strings.TrimSuffix(path, ".cvsignore") + ".gitignore"
	}
	return path
}

func reconstructTexts(file *rcs.File, contents map[string]*rcs.RevisionContent) (map[string]string, error) {
	out := make(map[string]string, len(contents))
	if file.Head == "" {
		return out, nil
	}
	head := file.Head
	headContent, ok := contents[head]
	if !ok {
		return out, nil
	}
	current := headContent.Text
	out[head] = current

	next := nextRevisionOf(file, head)
	for next != "" {
		delta := contents[next].Text
		var err error
		current, err = applyDelta(current, delta)
		if err != nil {
			return nil, err
		}
		out[next] = current
		next = nextRevisionOf(file, next)
	}
	return out, nil
}

func nextRevisionOf(file *rcs.File, rev string) string {
	for _, rh := range file.RevisionHeads {
		if rh.Revision == rev {
			return rh.NextRevision
		}
	}
	return ""
}

// applyDelta applies an RCS reverse delta to the newer text, producing older text.
func applyDelta(text, delta string) (string, error) {
	lines, trailing := splitLinesForDelta(text)
	commands := splitLines(delta)
	i := 0
	for i < len(commands) {
		cmd := commands[i]
		if cmd == "" {
			i++
			continue
		}
		if cmd[0] != 'a' && cmd[0] != 'd' {
			return "", fmt.Errorf("invalid delta command: %q", cmd)
		}
		var start, count int
		var op byte
		_, err := fmt.Sscanf(cmd, "%c%d %d", &op, &start, &count)
		if err != nil {
			return "", fmt.Errorf("parse delta %q: %w", cmd, err)
		}
		switch op {
		case 'd':
			if start < 1 || start-1+count > len(lines) {
				return "", fmt.Errorf("delta delete out of range")
			}
			lines = append(lines[:start-1], lines[start-1+count:]...)
			i++
		case 'a':
			if start < 0 {
				return "", fmt.Errorf("delta add out of range")
			}
			if start > len(lines) {
				start = len(lines)
			}
			if i+count >= len(commands) {
				return "", fmt.Errorf("delta add missing lines")
			}
			add := commands[i+1 : i+1+count]
			pos := start
			lines = append(lines[:pos], append(add, lines[pos:]...)...)
			i += 1 + count
		}
	}
	return joinLinesWithTrailing(lines, trailing), nil
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, "\n")
	// Preserve trailing empty line if original ended with newline.
	if strings.HasSuffix(s, "\n") {
		return parts
	}
	return parts
}

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func splitLinesForDelta(s string) ([]string, bool) {
	trailing := strings.HasSuffix(s, "\n")
	if trailing {
		s = strings.TrimSuffix(s, "\n")
	}
	if s == "" {
		return []string{}, trailing
	}
	return strings.Split(s, "\n"), trailing
}

func joinLinesWithTrailing(lines []string, trailing bool) string {
	if len(lines) == 0 {
		if trailing {
			return "\n"
		}
		return ""
	}
	out := strings.Join(lines, "\n")
	if trailing {
		out += "\n"
	}
	return out
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
