// SPDX-License-Identifier: GPL-2.0+
package export

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"thyrsus.com/cvs-fast-export/internal/authormap"
	"thyrsus.com/cvs-fast-export/internal/collate"
	"thyrsus.com/cvs-fast-export/internal/compare"
	"thyrsus.com/cvs-fast-export/internal/cvsnum"
	"thyrsus.com/cvs-fast-export/internal/symbols"
)

type Emitter struct {
	w          *bufio.Writer
	mark       int
	epoch      int64
	didIg      bool
	tagTargets map[string][]*symbols.Commit
	forceDates bool
	reposurgeon bool
	commitWindow int64
	authors    map[string]authormap.Author
	sourceType string
}

func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{
		w:     bufio.NewWriter(w),
		mark:  1,
		epoch: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		commitWindow: 300,
	}
}

type Options struct {
	ForceDates   bool
	Reposurgeon  bool
	CommitWindow time.Duration
	Authors      map[string]authormap.Author
	SourceType   string
}

func (e *Emitter) WithTagTargets(tags map[string][]*symbols.Commit) *Emitter {
	e.tagTargets = tags
	return e
}

func (e *Emitter) WithOptions(opts Options) *Emitter {
	e.forceDates = opts.ForceDates
	e.reposurgeon = opts.Reposurgeon
	if opts.CommitWindow > 0 {
		e.commitWindow = int64(opts.CommitWindow.Seconds())
	}
	e.authors = opts.Authors
	e.sourceType = opts.SourceType
	return e
}

func (e *Emitter) Flush() error {
	return e.w.Flush()
}

// EmitRepo emits a fast-import stream for all branches, ordered by commit date.
func (e *Emitter) EmitRepo(repo *collate.Repo) error {
	if repo == nil {
		return nil
	}
	if e.reposurgeon {
		st := e.sourceType
		if st == "" {
			st = "rcs"
		}
		fmt.Fprintf(e.w, "#reposurgeon sourcetype %s\n", st)
	}
	commits := collectCommits(repo)
	markMap := make(map[*collate.GitCommit]int)
	state := make(map[string]map[string]fileState)
	emittedTags := make(map[string]struct{})
	tagNames := append([]string(nil), repo.TagOrder...)
	if len(tagNames) == 0 {
		tagNames = make([]string, 0, len(e.tagTargets))
		for name := range e.tagTargets {
			tagNames = append(tagNames, name)
		}
		sort.Strings(tagNames)
	} else if len(tagNames) != len(e.tagTargets) {
		seen := make(map[string]struct{}, len(tagNames))
		for _, name := range tagNames {
			seen[name] = struct{}{}
		}
		var extra []string
		for name := range e.tagTargets {
			if _, ok := seen[name]; ok {
				continue
			}
			extra = append(extra, name)
		}
		sort.Strings(extra)
		tagNames = append(tagNames, extra...)
	}
	for i, c := range commits {
		prevState := state[c.RefName]
		if prevState == nil {
			prevState = map[string]fileState{}
		}
		parentMark := 0
		if c.Parent != nil {
			parentMark = markMap[c.Parent]
		}
		first := i == 0
		ops, next := buildOps(prevState, c.Files)
		if len(ops) == 0 {
			state[c.RefName] = next
			continue
		}
		mark, err := e.emitCommit(c.RefName, c, parentMark, first, ops)
		if err != nil {
			return err
		}
		state[c.RefName] = next
		markMap[c] = mark
		for _, name := range tagNames {
			targets := e.tagTargets[name]
			if _, ok := emittedTags[name]; ok {
				continue
			}
			if tagMatchesState(targets, next) {
				fmt.Fprintf(e.w, "reset refs/tags/%s\n", name)
				fmt.Fprintf(e.w, "from :%d\n\n", mark)
				emittedTags[name] = struct{}{}
			}
		}
	}
	fmt.Fprintln(e.w, "done")
	return nil
}

func (e *Emitter) emitCommit(branch string, c *collate.GitCommit, parentMark int, first bool, ops []*fileOp) (int, error) {
	ref := branch
	if ref == "" {
		ref = "master"
	}

	files := append([]*symbols.Commit(nil), c.Files...)
	sort.Slice(files, func(i, j int) bool {
		return compare.PathDeepCompare(files[i].FileName, files[j].FileName) < 0
	})
	for _, op := range ops {
		if op.kind == 'M' {
			op.mark = e.emitBlob(op.path, op.text)
		}
	}

	mark := e.mark
	e.mark++

	when := c.Date.Unix()
	if e.forceDates {
		when = 100000 + int64(mark)*e.commitWindow*2
	} else if when == 0 {
		when = e.epoch
	}

	fmt.Fprintf(e.w, "commit refs/heads/%s\n", ref)
	fmt.Fprintf(e.w, "mark :%d\n", mark)
	full, email, tz := resolveAuthor(e.authors, c.Author)
	if e.forceDates {
		tz = "+0000"
	}
	fmt.Fprintf(e.w, "committer %s <%s> %d %s\n", full, email, when, tz)
	if c.Log == "" {
		c.Log = "import\n"
	}
	fmt.Fprintf(e.w, "data %d\n%s", len(c.Log), c.Log)
	if parentMark > 0 {
		fmt.Fprintf(e.w, "from :%d\n", parentMark)
	}
	for _, op := range ops {
		if op.kind == 'M' {
			fmt.Fprintf(e.w, "M 100%o :%d %s\n", op.mode, op.mark, op.path)
		} else if op.kind == 'D' {
			fmt.Fprintf(e.w, "D %s\n", op.path)
		}
	}
	if first && !e.didIg {
		hasGitignore := false
		for _, f := range files {
			if f.FileName == ".gitignore" {
				hasGitignore = true
				break
			}
		}
		if !hasGitignore {
			fmt.Fprintln(e.w, "M 100644 inline .gitignore")
			fmt.Fprintf(e.w, "data %d\n%s\n", len(cvsIgnores), cvsIgnores)
		}
		e.didIg = true
	}
	if e.reposurgeon {
		revpairs := buildRevpairs(ops)
		if revpairs != "" {
			fmt.Fprintf(e.w, "property cvs-revisions %d %s", len(revpairs), revpairs)
		}
	}
	fmt.Fprintln(e.w)
	return mark, nil
}

func (e *Emitter) emitBlob(path, text string) int {
	mark := e.mark
	e.mark++
	if text == "" {
		text = "\n"
	}
	if path == ".gitignore" && !strings.HasPrefix(text, cvsIgnores) {
		text = normalizeGitignore(text)
	}
	fmt.Fprintf(e.w, "blob\nmark :%d\n", mark)
	fmt.Fprintf(e.w, "data %d\n%s\n", len(text), text)
	return mark
}

type fileOp struct {
	kind byte
	path string
	text string
	mark int
	mode int
	commit *symbols.Commit
}

type fileState struct {
	text string
	mode int
}

func buildOps(prev map[string]fileState, files []*symbols.Commit) ([]*fileOp, map[string]fileState) {
	next := make(map[string]fileState, len(prev))
	byPath := make(map[string]*symbols.Commit)
	for k, v := range prev {
		next[k] = v
	}
	for _, f := range files {
		path := f.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		if f.Dead {
			delete(next, path)
			continue
		}
		byPath[path] = f
		text := f.Text
		if path == ".gitignore" && !strings.HasPrefix(text, cvsIgnores) {
			text = normalizeGitignore(text)
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0644
		}
		next[path] = fileState{text: text, mode: mode}
	}

	var paths []string
	for k := range prev {
		paths = append(paths, k)
	}
	for k := range next {
		if _, ok := prev[k]; !ok {
			paths = append(paths, k)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return compare.PathDeepCompare(paths[i], paths[j]) < 0
	})

	var ops []*fileOp
	seen := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		old, okOld := prev[p]
		newv, okNew := next[p]
		switch {
		case okOld && !okNew:
			ops = append(ops, &fileOp{kind: 'D', path: p})
		case !okOld && okNew:
			ops = append(ops, &fileOp{kind: 'M', path: p, text: newv.text, mode: newv.mode, commit: byPath[p]})
		case okOld && okNew && (old.text != newv.text || old.mode != newv.mode):
			ops = append(ops, &fileOp{kind: 'M', path: p, text: newv.text, mode: newv.mode, commit: byPath[p]})
		}
	}
	return ops, next
}

func tagMatchesState(targets []*symbols.Commit, state map[string]fileState) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		path := t.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		want := t.Text
		if path == ".gitignore" && !strings.HasPrefix(want, cvsIgnores) {
			want = normalizeGitignore(want)
		}
		got, ok := state[path]
		if !ok {
			return false
		}
		mode := t.Mode
		if mode == 0 {
			mode = 0644
		}
		if got.text != want || got.mode != mode {
			return false
		}
	}
	return true
}

func collectCommits(repo *collate.Repo) []*collate.GitCommit {
	var out []*collate.GitCommit
	for _, b := range repo.Branches {
		out = append(out, collate.CommitsForBranch(b)...)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		if out[i].RefName != out[j].RefName {
			return out[i].RefName < out[j].RefName
		}
		return out[i].Log < out[j].Log
	})
	return out
}

func resolveAuthor(authors map[string]authormap.Author, name string) (string, string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "cvs-fast-export", "unknown", "+0000"
	}
	if authors != nil {
		if a, ok := authors[name]; ok {
			tz := a.Timezone
			if tz == "" {
				tz = "+0000"
			}
			return a.Full, a.Email, tz
		}
	}
	return name, name, "+0000"
}

func buildRevpairs(ops []*fileOp) string {
	var b strings.Builder
	for _, op := range ops {
		if op.kind != 'M' || op.commit == nil {
			continue
		}
		path := op.commit.OrigName
		if path == "" {
			path = op.path
		}
		b.WriteString(path)
		b.WriteString(" ")
		b.WriteString(cvsnum.String(op.commit.Number))
		b.WriteString("\n")
	}
	return b.String()
}

const cvsIgnores = "# CVS default ignores begin\n" +
	"tags\n" +
	"TAGS\n" +
	".make.state\n" +
	".nse_depinfo\n" +
	"*~\n" +
	"\\#*\n" +
	".#*\n" +
	",*\n" +
	"_$*\n" +
	"*$\n" +
	"*.old\n" +
	"*.bak\n" +
	"*.BAK\n" +
	"*.orig\n" +
	"*.rej\n" +
	".del-*\n" +
	"*.a\n" +
	"*.olb\n" +
	"*.o\n" +
	"*.obj\n" +
	"*.so\n" +
	"*.exe\n" +
	"*.Z\n" +
	"*.elc\n" +
	"*.ln\n" +
	"core\n" +
	"# CVS default ignores end\n"

func normalizeGitignore(text string) string {
	if text == "" {
		return cvsIgnores
	}
	buf := []byte(text)
	for i := range buf {
		if buf[i] == ' ' {
			buf[i] = '\n'
		}
	}
	suppressDefaults := false
	if len(buf) >= 2 && buf[0] == '!' && buf[1] == '\n' {
		suppressDefaults = true
		buf = buf[2:]
	}
	var b strings.Builder
	if !suppressDefaults {
		b.WriteString(cvsIgnores)
	}
	for i := 0; i < len(buf); i++ {
		if buf[i] == '#' && (i == 0 || buf[i-1] == '\n') {
			b.WriteByte('\\')
		}
		b.WriteByte(buf[i])
	}
	return b.String()
}
