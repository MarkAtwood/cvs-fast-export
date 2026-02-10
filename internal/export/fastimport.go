// SPDX-License-Identifier: GPL-2.0+
package export

import (
	"bufio"
	"fmt"
	"io"
	"time"

	"thyrsus.com/cvs-fast-export/internal/collate"
)

type Emitter struct {
	w     *bufio.Writer
	mark  int
	epoch int64
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
	for i := len(commits) - 1; i >= 0; i-- {
		if err := e.emitCommit(branch.Name, commits[i]); err != nil {
			return err
		}
	}
	return nil
}

func (e *Emitter) emitCommit(branch string, c *collate.GitCommit) error {
	mark := e.mark
	e.mark++
	ref := branch
	if ref == "" {
		ref = "master"
	}

	when := c.Date.Unix()
	if when == 0 {
		when = e.epoch
	}

	fmt.Fprintf(e.w, "commit refs/heads/%s\n", ref)
	fmt.Fprintf(e.w, "mark :%d\n", mark)
	if c.Author == "" {
		c.Author = "cvs-fast-export <unknown>"
	}
	fmt.Fprintf(e.w, "author %s %d +0000\n", c.Author, when)
	fmt.Fprintf(e.w, "committer %s %d +0000\n", c.Author, when)
	if c.Log == "" {
		c.Log = "import\n"
	}
	fmt.Fprintf(e.w, "data %d\n%s\n", len(c.Log), c.Log)
	for _, f := range c.Files {
		if f.Dead {
			continue
		}
		blobMark := e.emitBlob(f.Text)
		path := f.FileName
		if path == "" {
			path = "UNKNOWN"
		}
		fmt.Fprintf(e.w, "M 100644 :%d %s\n", blobMark, path)
	}
	return nil
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
