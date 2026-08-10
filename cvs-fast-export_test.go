package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rcs "github.com/arran4/golang-rcs"
)

func TestLoadFilePreservesCarriageReturnsInDeltaText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary,v")
	data := "" +
		"head\t1.2;\n" +
		"access;\n" +
		"symbols;\n" +
		"locks; strict;\n" +
		"comment\t@# @;\n" +
		"\n" +
		"\n" +
		"1.2\n" +
		"date\t2020.01.02.03.04.05;\tauthor u;\tstate Exp;\n" +
		"branches;\n" +
		"next\t1.1;\n" +
		"\n" +
		"1.1\n" +
		"date\t2020.01.02.03.04.04;\tauthor u;\tstate Exp;\n" +
		"branches;\n" +
		"next\t;\n" +
		"\n" +
		"\n" +
		"desc\n" +
		"@@\n" +
		"\n" +
		"\n" +
		"1.2\n" +
		"log\n" +
		"@new\n" +
		"@\n" +
		"text\n" +
		"@base\n" +
		"@\n" +
		"\n" +
		"\n" +
		"1.1\n" +
		"log\n" +
		"@old\n" +
		"@\n" +
		"text\n" +
		"@d1 1\n" +
		"a1 1\n" +
		"old\rwithcr\n" +
		"@\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	file, _, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := make(map[string]*rcs.RevisionContent, len(file.RevisionContents))
	for _, rc := range file.RevisionContents {
		contents[rc.Revision] = rc
	}
	fulltext, err := reconstructTexts(file, contents)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fulltext["1.1"], "old\rwithcr\n"; got != want {
		t.Fatalf("fulltext[1.1] = %q, want %q", got, want)
	}
}

func TestResolveTagAttachmentsUsesRevisionIdentity(t *testing.T) {
	number := func(s string) Number {
		t.Helper()
		n, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	rev1 := &Commit{
		Number:   number("1.1"),
		Date:     time.Unix(1, 0),
		Text:     "same content\n",
		FileName: "file",
		Mode:     0644,
	}
	rev2 := &Commit{
		Number:   number("1.2"),
		Date:     time.Unix(2, 0),
		Text:     "different content\n",
		FileName: "file",
		Mode:     0644,
	}
	rev3 := &Commit{
		Number:   number("1.3"),
		Date:     time.Unix(3, 0),
		Text:     rev1.Text,
		FileName: "file",
		Mode:     0644,
	}

	git1 := &GitCommit{Date: rev1.Date, Files: []*Commit{rev1}, RefName: "master"}
	git2 := &GitCommit{Parent: git1, Date: rev2.Date, Files: []*Commit{rev2}, RefName: "master"}
	git3 := &GitCommit{Parent: git2, Date: rev3.Date, Files: []*Commit{rev3}, RefName: "master"}
	commitOf := map[*Commit]*GitCommit{
		rev1: git1,
		rev2: git2,
		rev3: git3,
	}

	attachments, warnings, synthetic := resolveTagAttachments(
		[]*GitCommit{git1, git2, git3},
		map[string][]*Commit{"late": {rev3}},
		commitOf,
		false,
		nil,
	)
	if got := attachments["late"]; got != git3 {
		t.Fatalf("late tag attached to %p, want final revision commit %p", got, git3)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(synthetic) != 0 {
		t.Fatalf("unexpected synthetic tags: %v", synthetic)
	}
}

func TestEmitRepoPreservesTagOnNoOpRevision(t *testing.T) {
	number := func(s string) Number {
		t.Helper()
		n, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	rev1 := &Commit{
		Number:   number("1.1"),
		Date:     time.Unix(1, 0),
		Author:   "author",
		Log:      "first\n",
		Text:     "unchanged\n",
		FileName: "file",
		Mode:     0644,
	}
	rev2 := &Commit{
		Number:   number("1.2"),
		Date:     time.Unix(2, 0),
		Author:   "author",
		Log:      "no-op\n",
		Text:     rev1.Text,
		FileName: rev1.FileName,
		Mode:     rev1.Mode,
	}

	git1 := &GitCommit{
		Date:    rev1.Date,
		Author:  rev1.Author,
		Log:     rev1.Log,
		Files:   []*Commit{rev1},
		RefName: "master",
		Tail:    true,
	}
	git2 := &GitCommit{
		Parent:  git1,
		Date:    rev2.Date,
		Author:  rev2.Author,
		Log:     rev2.Log,
		Files:   []*Commit{rev2},
		RefName: "master",
	}
	repo := &Repo{
		Branches:   []*GitBranch{{Name: "master", Head: git2}},
		CommitOf:   map[*Commit]*GitCommit{rev1: git1, rev2: git2},
		TagTargets: map[string][]*Commit{"late": {rev2}},
		TagOrder:   []string{"late"},
	}

	var output bytes.Buffer
	emitter := NewEmitter(&output).
		WithTagTargets(repo.TagTargets).
		WithOptions(ExportOptions{NoIgnores: true})
	if err := emitter.EmitRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Flush(); err != nil {
		t.Fatal(err)
	}
	stream := output.String()
	if got := strings.Count(stream, "commit refs/heads/master\n"); got != 1 {
		t.Fatalf("emitted %d commits for a content no-op, want 1\n%s", got, stream)
	}
	if !strings.Contains(stream, "reset refs/tags/late\nfrom :2\n") {
		t.Fatalf("late tag was not aliased to the emitted parent commit\n%s", stream)
	}
}
