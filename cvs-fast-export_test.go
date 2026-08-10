package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

func TestEmitRepoIncrementalStreamImportsIntoExistingRepo(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	number := func(s string) Number {
		t.Helper()
		n, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	oldRevision := &Commit{
		Number:   number("1.1"),
		Date:     time.Unix(100, 0),
		Author:   "author",
		Log:      "old\n",
		Text:     "old contents\n",
		FileName: "file",
		Mode:     0644,
	}
	newRevision := &Commit{
		Number:   number("1.2"),
		Date:     time.Unix(200, 0),
		Author:   "author",
		Log:      "new\n",
		Text:     "new contents\n",
		FileName: "file",
		Mode:     0644,
	}
	oldCommit := &GitCommit{
		Date:    oldRevision.Date,
		Author:  oldRevision.Author,
		Log:     oldRevision.Log,
		Files:   []*Commit{oldRevision},
		RefName: "master",
		Tail:    true,
	}
	newCommit := &GitCommit{
		Parent:  oldCommit,
		Date:    newRevision.Date,
		Author:  newRevision.Author,
		Log:     newRevision.Log,
		Files:   []*Commit{newRevision},
		RefName: "master",
	}

	emit := func(repo *Repo, opts ExportOptions) string {
		t.Helper()
		var output bytes.Buffer
		emitter := NewEmitter(&output).WithOptions(opts)
		if err := emitter.EmitRepo(repo); err != nil {
			t.Fatal(err)
		}
		if err := emitter.Flush(); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}

	initial := emit(&Repo{
		Branches: []*GitBranch{{Name: "master", Head: oldCommit}},
		CommitOf: map[*Commit]*GitCommit{oldRevision: oldCommit},
	}, ExportOptions{NoIgnores: true})
	incremental := emit(&Repo{
		Branches: []*GitBranch{{Name: "master", Head: newCommit}},
		CommitOf: map[*Commit]*GitCommit{oldRevision: oldCommit, newRevision: newCommit},
	}, ExportOptions{Incremental: true, NoIgnores: true, FromTime: 150})

	commitAt := strings.Index(incremental, "commit refs/heads/master\n")
	parentAt := strings.Index(incremental, "from refs/heads/master^0\n")
	if commitAt < 0 || parentAt < commitAt {
		t.Fatalf("external parent is not inside the first emitted commit:\n%s", incremental)
	}
	if strings.Contains(incremental, "from :2\n") {
		t.Fatalf("incremental stream references an omitted commit mark:\n%s", incremental)
	}

	repoDir := t.TempDir()
	runGit := func(input string, args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		if input != "" {
			cmd.Stdin = strings.NewReader(input)
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return string(output)
	}
	runGit("", "init", "--quiet", repoDir)
	runGit(initial, "-C", repoDir, "fast-import", "--quiet")
	runGit(incremental, "-C", repoDir, "fast-import", "--quiet")
	if got := strings.TrimSpace(runGit("", "-C", repoDir, "rev-list", "--count", "master")); got != "2" {
		t.Fatalf("incremental import produced %s commits, want 2", got)
	}
	if got := runGit("", "-C", repoDir, "show", "master:file"); got != newRevision.Text {
		t.Fatalf("imported file = %q, want %q", got, newRevision.Text)
	}
}

func TestConvertDateAcceptsRFC3339AndRejectsInvalidInput(t *testing.T) {
	want := time.Date(2026, time.August, 10, 12, 34, 56, 0, time.UTC).Unix()
	for _, arg := range []string{
		"2026-08-10T12:34:56Z",
		"2026-08-10T08:34:56-04:00",
		"2026-08-10T12:34:56+0000",
		"1786365296",
	} {
		got, err := convertDate(arg)
		if err != nil {
			t.Errorf("convertDate(%q): %v", arg, err)
			continue
		}
		if got != want {
			t.Errorf("convertDate(%q) = %d, want %d", arg, got, want)
		}
	}
	if _, err := convertDate("definitely-not-a-date"); err == nil {
		t.Fatal("convertDate accepted an invalid date")
	}
	if _, err := parseArgs([]string{"-i", "definitely-not-a-date"}); err == nil {
		t.Fatal("parseArgs silently accepted an invalid incremental date")
	}
	cfg, err := parseArgs([]string{"--incremental=2026-08-10T12:34:56Z"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Incremental || cfg.FromTime != want {
		t.Fatalf("incremental config = %+v, want FromTime %d", cfg, want)
	}
}

func TestGitignoreEscapesUseExactFastImportDataLength(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	number, err := Parse("1.1")
	if err != nil {
		t.Fatal(err)
	}
	revision := &Commit{
		Number:   number,
		Date:     time.Unix(100, 0),
		Author:   "author",
		Log:      "add ignores\n",
		Text:     "wibble wobble #ping",
		FileName: ".gitignore",
		Mode:     0644,
	}
	commit := &GitCommit{
		Date:    revision.Date,
		Author:  revision.Author,
		Log:     revision.Log,
		Files:   []*Commit{revision},
		RefName: "master",
		Tail:    true,
	}
	repo := &Repo{
		Branches: []*GitBranch{{Name: "master", Head: commit}},
		CommitOf: map[*Commit]*GitCommit{revision: commit},
	}

	var output bytes.Buffer
	emitter := NewEmitter(&output).WithOptions(ExportOptions{NoIgnores: true})
	if err := emitter.EmitRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Flush(); err != nil {
		t.Fatal(err)
	}
	stream := output.String()
	wantBlob := normalizeGitignore(revision.Text)
	wantFrame := fmt.Sprintf("data %d\n%s\n", len(wantBlob), wantBlob)
	if !strings.Contains(stream, wantFrame) {
		t.Fatalf("gitignore blob does not declare its exact byte length:\n%s", stream)
	}

	repoDir := t.TempDir()
	initCmd := exec.Command(git, "init", "--quiet", repoDir)
	if imported, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, imported)
	}
	importCmd := exec.Command(git, "-C", repoDir, "fast-import", "--quiet")
	importCmd.Stdin = strings.NewReader(stream)
	if imported, err := importCmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import rejected stream: %v\n%s\n%s", err, imported, stream)
	}
	showCmd := exec.Command(git, "-C", repoDir, "show", "master:.gitignore")
	gotBlob, err := showCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(gotBlob); got != wantBlob {
		t.Fatalf("imported .gitignore = %q, want %q", got, wantBlob)
	}
}

func TestEmptyFileRemainsZeroByteBlob(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	number, err := Parse("1.1")
	if err != nil {
		t.Fatal(err)
	}
	revision := &Commit{
		Number:   number,
		Date:     time.Unix(100, 0),
		Author:   "author",
		Log:      "add empty file\n",
		Text:     "",
		FileName: "empty",
		Mode:     0644,
	}
	commit := &GitCommit{
		Date:    revision.Date,
		Author:  revision.Author,
		Log:     revision.Log,
		Files:   []*Commit{revision},
		RefName: "master",
		Tail:    true,
	}
	repo := &Repo{
		Branches: []*GitBranch{{Name: "master", Head: commit}},
		CommitOf: map[*Commit]*GitCommit{revision: commit},
	}

	var output bytes.Buffer
	emitter := NewEmitter(&output).WithOptions(ExportOptions{NoIgnores: true})
	if err := emitter.EmitRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Flush(); err != nil {
		t.Fatal(err)
	}
	stream := output.String()
	if !strings.Contains(stream, "blob\nmark :1\ndata 0\n\ncommit refs/heads/master\n") {
		t.Fatalf("zero-byte file was not emitted as a data 0 blob:\n%s", stream)
	}

	repoDir := t.TempDir()
	initCmd := exec.Command(git, "init", "--quiet", repoDir)
	if initialized, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, initialized)
	}
	importCmd := exec.Command(git, "-C", repoDir, "fast-import", "--quiet")
	importCmd.Stdin = strings.NewReader(stream)
	if imported, err := importCmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import rejected zero-byte stream: %v\n%s\n%s", err, imported, stream)
	}
	sizeCmd := exec.Command(git, "-C", repoDir, "cat-file", "-s", "master:empty")
	size, err := sizeCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(size)); got != "0" {
		t.Fatalf("imported empty file has size %s, want 0", got)
	}
}

func TestCollateBranchEnforcesWindowAcrossWholeChangeset(t *testing.T) {
	for _, trustCommitIDs := range []bool{true, false} {
		name := "content-only"
		if trustCommitIDs {
			name = "trust-commitids"
		}
		t.Run(name, func(t *testing.T) {
			number, err := Parse("1.1")
			if err != nil {
				t.Fatal(err)
			}
			base := time.Unix(1_000_000, 0)
			offsets := []time.Duration{0, 299 * time.Second, 598 * time.Second}
			revisions := make([]*Commit, 0, len(offsets))
			heads := make([]*Head, 0, len(offsets))
			for i, offset := range offsets {
				revision := &Commit{
					Number:   number,
					Date:     base.Add(offset),
					Author:   "author",
					Log:      "one changeset\n",
					Text:     fmt.Sprintf("file %d\n", i),
					FileName: fmt.Sprintf("file-%d", i),
					Mode:     0644,
				}
				revisions = append(revisions, revision)
				heads = append(heads, &Head{RefName: "master", Commit: revision})
			}
			index := &collateIndex{
				commitOf: make(map[*Commit]*GitCommit),
				byNumber: make(map[string]*GitCommit),
			}
			head := collateBranch("master", heads, CollateOptions{
				CommitTimeWindow: 300 * time.Second,
				TrustCommitIDs:   trustCommitIDs,
			}, index)
			if head == nil || head.Parent == nil || head.Parent.Parent != nil {
				t.Fatalf("collation did not produce exactly two changesets: %#v", head)
			}
			if got := len(head.Parent.Files); got != 2 {
				t.Fatalf("first changeset contains %d revisions, want 2", got)
			}
			if got := len(head.Files); got != 1 {
				t.Fatalf("second changeset contains %d revisions, want 1", got)
			}
			if index.commitOf[revisions[0]] != index.commitOf[revisions[1]] {
				t.Fatal("revisions 299 seconds apart were not coalesced")
			}
			if index.commitOf[revisions[1]] == index.commitOf[revisions[2]] {
				t.Fatal("598-second revision span was coalesced through a rolling window")
			}
		})
	}
}
