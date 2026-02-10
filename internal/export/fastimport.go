// SPDX-License-Identifier: GPL-2.0+
package export

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"thyrsus.com/cvs-fast-export/internal/collate"
	"thyrsus.com/cvs-fast-export/internal/compare"
	"thyrsus.com/cvs-fast-export/internal/symbols"
)

type Emitter struct {
	w     *bufio.Writer
	mark  int
	epoch int64
	didIg bool
}

func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{
		w:     bufio.NewWriter(w),
		mark:  1,
		epoch: time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
	}
}

func (e *Emitter) Flush() error {
	return e.w.Flush()
}

// EmitBranch emits a minimal fast-import stream for a single branch.
// This is a temporary scaffold for output comparisons.
func (e *Emitter) EmitBranch(branch *collate.GitBranch) error {
	if branch == nil || branch.Head == nil {
		return nil
	}
	commits := collate.CommitsForBranch(branch)
	var parentMark int
	for i := len(commits) - 1; i >= 0; i-- {
		mark, err := e.emitCommit(branch.Name, commits[i], parentMark)
		if err != nil {
			return err
		}
		parentMark = mark
	}
	fmt.Fprintln(e.w, "done")
	return nil
}

func (e *Emitter) emitCommit(branch string, c *collate.GitCommit, parentMark int) (int, error) {
	ref := branch
	if ref == "" {
		ref = "master"
	}

	when := c.Date.Unix()
	if when == 0 {
		when = e.epoch
	}

	files := append([]*symbols.Commit(nil), c.Files...)
	sort.Slice(files, func(i, j int) bool {
		return compare.PathDeepCompare(files[i].FileName, files[j].FileName) < 0
	})
	blobMarks := make([]int, len(files))
	for i, f := range files {
		if f.Dead {
			continue
		}
		blobMarks[i] = e.emitBlob(f.Text)
	}

	mark := e.mark
	e.mark++
	fmt.Fprintf(e.w, "commit refs/heads/%s\n", ref)
	fmt.Fprintf(e.w, "mark :%d\n", mark)
	ident := formatIdent(c.Author)
	fmt.Fprintf(e.w, "committer %s %d +0000\n", ident, when)
	if c.Log == "" {
		c.Log = "import\n"
	}
	fmt.Fprintf(e.w, "data %d\n%s", len(c.Log), c.Log)
	if parentMark > 0 {
		fmt.Fprintf(e.w, "from :%d\n", parentMark)
	}
	for i, f := range files {
		if f.Dead {
			continue
		}
		path := f.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		fmt.Fprintf(e.w, "M 100644 :%d %s\n", blobMarks[i], path)
	}
	if !e.didIg {
		fmt.Fprintln(e.w, "M 100644 inline .gitignore")
		fmt.Fprintf(e.w, "data %d\n%s\n", len(cvsIgnores), cvsIgnores)
		e.didIg = true
	}
	fmt.Fprintln(e.w)
	return mark, nil
}

func (e *Emitter) emitBlob(text string) int {
	mark := e.mark
	e.mark++
	if text == "" {
		text = "\n"
	}
	fmt.Fprintf(e.w, "blob\nmark :%d\n", mark)
	fmt.Fprintf(e.w, "data %d\n%s\n", len(text), text)
	return mark
}

func formatIdent(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "cvs-fast-export <unknown>"
	}
	if strings.Contains(name, "<") {
		return name
	}
	return fmt.Sprintf("%s <%s>", name, name)
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
