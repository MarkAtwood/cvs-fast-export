// SPDX-License-Identifier: GPL-2.0+
package rcsmodel

import (
	"bytes"
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
	Tags     []symbols.Tag
	Versions []symbols.Version
	Commits  map[string]*symbols.Commit
	Root     *symbols.Commit
}

func BuildMaster(source string, file *rcs.File, striplen int, symbolOrder []string) (*Master, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	fileMode := 0644
	if info.Mode()&0100 != 0 {
		fileMode = 0755
	}
	commits := make(map[string]*symbols.Commit, len(file.RevisionHeads))
	contents := make(map[string]*rcs.RevisionContent, len(file.RevisionContents))
	for _, rc := range file.RevisionContents {
		contents[rc.Revision] = rc
	}
	fileName := exportName(source, striplen)
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
			log = normalizeLog(rc.Log, file.Description)
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
			Mode:     fileMode,
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
	seen := make(map[string]struct{}, len(file.SymbolMap))
	if len(symbolOrder) > 0 {
		for i := len(symbolOrder) - 1; i >= 0; i-- {
			name := symbolOrder[i]
			rev, ok := file.SymbolMap[name]
			if !ok {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			name = sanitizeSymbolName(name)
			if name == "" {
				return nil, fmt.Errorf("symbol name empty after sanitization")
			}
			n, err := cvsnum.Parse(rev)
			if err != nil {
				return nil, err
			}
			sym = append(sym, symbols.Symbol{Name: name, Number: n})
		}
	}
	for name, rev := range file.SymbolMap {
		if _, ok := seen[name]; ok {
			continue
		}
		name = sanitizeSymbolName(name)
		if name == "" {
			return nil, fmt.Errorf("symbol name empty after sanitization")
		}
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

func exportName(source string, striplen int) string {
	path := source
	if striplen > 0 && striplen < len(path) {
		path = path[striplen:]
	} else if striplen >= len(path) {
		path = ""
	}
	parts := strings.Split(path, "/")
	cleaned := make([]string, 0, len(parts))
	for i, part := range parts {
		if part == "" {
			continue
		}
		isLast := i == len(parts)-1
		if !isLast {
			if part == "Attic" || part == "RCS" {
				continue
			}
		} else {
			if strings.HasSuffix(part, ",v") {
				part = strings.TrimSuffix(part, ",v")
			}
			if strings.HasSuffix(part, ".cvsignore") {
				part = strings.TrimSuffix(part, ".cvsignore") + ".gitignore"
			}
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, "/")
}

func ExportName(source string, striplen int) string {
	return exportName(source, striplen)
}

func sanitizeSymbolName(s string) string {
	const bad = "~^\\*?"
	var b strings.Builder
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			continue
		}
		if strings.ContainsRune(bad, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
	out[head] = headContent.Text

	newerOf := make(map[string]string)
	heads := make(map[string]*rcs.RevisionHead)
	for _, rh := range file.RevisionHeads {
		heads[rh.Revision] = rh
		if rh.NextRevision != "" {
			newerOf[rh.NextRevision] = rh.Revision
		}
	}

	var compute func(string) error
	compute = func(rev string) error {
		if _, ok := out[rev]; ok {
			return nil
		}
		rc, ok := contents[rev]
		if !ok {
			return nil
		}
		if newer := newerOf[rev]; newer != "" {
			if err := compute(newer); err != nil {
				return err
			}
			base := out[newer]
			text, err := applyDelta(base, rc.Text)
			if err != nil {
				return err
			}
			out[rev] = text
			return nil
		}
		if parent := branchParent(rev); parent != "" {
			if err := compute(parent); err != nil {
				return err
			}
			base := out[parent]
			text, err := applyDelta(base, rc.Text)
			if err != nil {
				return err
			}
			out[rev] = text
			return nil
		}
		out[rev] = rc.Text
		return nil
	}

	for rev := range contents {
		if err := compute(rev); err != nil {
			return nil, err
		}
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
	type deltaOp struct {
		op    byte
		start int
		count int
		lines []string
	}
	var ops []deltaOp
	for i := 0; i < len(commands); {
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
			ops = append(ops, deltaOp{op: 'd', start: start, count: count})
			i++
		case 'a':
			if i+count >= len(commands) {
				return "", fmt.Errorf("delta add missing lines")
			}
			add := commands[i+1 : i+1+count]
			ops = append(ops, deltaOp{op: 'a', start: start, count: count, lines: add})
			i += 1 + count
		}
	}
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		switch op.op {
		case 'd':
			if op.start < 1 || op.start-1+op.count > len(lines) {
				return "", fmt.Errorf("delta delete out of range")
			}
			lines = append(lines[:op.start-1], lines[op.start-1+op.count:]...)
		case 'a':
			if op.start < 0 {
				return "", fmt.Errorf("delta add out of range")
			}
			pos := op.start
			if pos > len(lines) {
				pos = len(lines)
			}
			lines = append(lines[:pos], append(op.lines, lines[pos:]...)...)
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

func normalizeLog(log, desc string) string {
	if log != "Initial revision\n" {
		return log
	}
	if len(desc) == 0 {
		return "*** empty log message ***\n"
	}
	return desc
}

func branchParent(rev string) string {
	n, err := cvsnum.Parse(rev)
	if err != nil {
		return ""
	}
	if n.C < 4 {
		return ""
	}
	n.C -= 2
	n.N = n.N[:n.C]
	return cvsnum.String(n)
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

func LoadFile(path string) (*rcs.File, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	rcsFile, err := rcs.ParseFile(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return rcsFile, parseSymbolOrder(string(data)), nil
}

func parseSymbolOrder(data string) []string {
	lines := strings.Split(data, "\n")
	inSymbols := false
	var names []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inSymbols {
			if strings.HasPrefix(trimmed, "symbols") {
				rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "symbols"))
				if rest != "" {
					names = append(names, parseSymbolTokens(rest)...)
					if strings.Contains(rest, ";") {
						break
					}
					inSymbols = true
					continue
				}
				inSymbols = true
			}
			continue
		}
		names = append(names, parseSymbolTokens(trimmed)...)
		if strings.Contains(line, ";") {
			break
		}
	}
	return names
}

func parseSymbolTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ';'
	})
	var names []string
	for _, f := range fields {
		if idx := strings.IndexByte(f, ':'); idx > 0 {
			names = append(names, f[:idx])
		}
	}
	return names
}
