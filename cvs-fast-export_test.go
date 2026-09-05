package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	rcs "github.com/arran4/golang-rcs"
)

type alwaysErrorWriter struct {
	err error
}

func (w alwaysErrorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestApplyDeltaBlankLines(t *testing.T) {
	tests := []struct {
		name, text, delta, want string
	}{
		{"empty unchanged", "", "", ""},
		{"blank unchanged", "\n", "", "\n"},
		{"delete blank", "\n", "d1 1\n", ""},
		{"replace blank", "\n", "d1 1\na1 1\na\n", "a\n"},
		{"replace blank without final newline", "\n", "d1 1\na1 1\na", "a"},
		{"append after blank", "\n", "a1 1\na\n", "\na\n"},
		{"add blank to empty", "", "a0 1\n\n", "\n"},
		{"append another blank", "\n", "a1 1\n\n", "\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := applyDelta(tt.text, tt.delta)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("applyDelta(%q, %q) = %q, want %q", tt.text, tt.delta, got, tt.want)
			}
		})
	}
}

func TestApplyDeltaRejectsInvalidRanges(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, delta := range []string{
		"a0 -1\n", "d1 -1\n", "a0 0\n", "d1 0\n",
		"a-1 1\nx\n", "d0 1\n", "a2 1\nx\n", "d2 1\n", "d1 2\n",
		"a0 2\nx\n", "d1 1\nd1 1\n",
		fmt.Sprintf("a0 %d\n", maxInt),
		fmt.Sprintf("a0 1\nx\na0 %d\n", maxInt),
		fmt.Sprintf("a%d 1\nx\n", maxInt),
		fmt.Sprintf("d1 %d\n", maxInt),
		fmt.Sprintf("d%d %d\n", maxInt, maxInt),
	} {
		t.Run(delta, func(t *testing.T) {
			defer func() {
				if failure := recover(); failure != nil {
					t.Fatalf("malformed delta panicked instead of returning an error: %v", failure)
				}
			}()
			if got, err := applyDelta("a\n", delta); err == nil {
				t.Fatalf("malformed delta accepted, producing %q", got)
			}
		})
	}
}

func TestExportSingleBlankLineRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blank,v")
	const data = `head 1.2;
access;
symbols;
locks; strict;
comment @# @;


1.2
date 2020.01.02.03.04.05; author u; state Exp;
branches;
next 1.1;

1.1
date 2020.01.02.03.04.04; author u; state Exp;
branches;
next ;


desc
@@


1.2
log
@new
@
text
@
@


1.1
log
@old
@
text
@d1 1
a1 1
a
@
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	file, symbols, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	master, err := BuildMaster(path, file, len(dir)+1, symbols, false, &warningCollector{})
	if err != nil {
		t.Fatal(err)
	}
	for revision, want := range map[string]string{"1.1": "a\n", "1.2": "\n"} {
		if got := master.Commits[revision].Text; got != want {
			t.Fatalf("revision %s = %q, want %q", revision, got, want)
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	repo := Build([]*Master{master}, CollateOptions{TrustCommitIDs: true})
	repository := newHistoryGitRepo(t, git)
	importHistoryStream(t, git, repository, emitHistoryStream(t, repo, ExportOptions{}))
	assertLinearHistory(t, git, repository, "master", 2)
	for revision, want := range map[string]string{"master^:blank": "a\n", "master:blank": "\n"} {
		if got := runHistoryGit(t, git, "", "-C", repository, "show", revision); got != want {
			t.Fatalf("%s = %q, want %q", revision, got, want)
		}
	}
}

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

func TestParseArgsRejectsUnknownOptions(t *testing.T) {
	for _, arg := range []string{"-Z", "--unknown", "--unknown=value"} {
		t.Run(arg, func(t *testing.T) {
			_, err := parseArgs([]string{arg})
			want := fmt.Sprintf("unknown option %q", arg)
			if err == nil || err.Error() != want {
				t.Fatalf("parseArgs(%q) error = %v, want %q", arg, err, want)
			}
		})
	}
}

func TestParseArgsScansExplicitDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".repo")
	want := []string{".cvsignore,v", "file,v", filepath.Join("sub", "nested,v")}
	for _, name := range append(append([]string{}, want...),
		filepath.Join(".hidden", "ignored,v"), filepath.Join("sub", ".git", "ignored,v")) {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()
	for _, path := range []string{".", "./", root, root + string(os.PathSeparator), filepath.Join("..", ".repo")} {
		t.Run(path, func(t *testing.T) {
			cfg, err := parseArgs([]string{path})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, file := range cfg.Files {
				absolute, err := filepath.Abs(file)
				if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(root, absolute)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, relative)
			}
			sort.Strings(got)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("input files = %q, want %q", got, want)
			}
		})
	}
}

func TestValidGitRefName(t *testing.T) {
	valid := []string{
		"refs/heads/master",
		"refs/heads/topic/subtopic",
		"refs/heads/-dash",
		"refs/remotes/team/origin/master",
		"refs/tags/release-1.0",
	}
	invalid := []string{
		"master",
		"@",
		"/refs/heads/master",
		"refs/heads/master/",
		"refs//heads/master",
		"refs/.hidden/master",
		"refs/heads/topic.lock",
		"refs/heads/two..dots",
		"refs/heads/with space",
		"refs/heads/tilde~",
		"refs/heads/caret^",
		"refs/heads/colon:",
		"refs/heads/question?",
		"refs/heads/star*",
		"refs/heads/bracket[",
		"refs/heads/backslash\\",
		"refs/heads/trailing.",
		"refs/heads/reflog@{1}",
		"refs/heads/control\x01",
	}
	for _, ref := range valid {
		if !validGitRefName(ref) {
			t.Errorf("validGitRefName(%q) = false, want true", ref)
		}
	}
	for _, ref := range invalid {
		if validGitRefName(ref) {
			t.Errorf("validGitRefName(%q) = true, want false", ref)
		}
	}
}

func TestSanitizeSymbolNameIsValidAndCollisionFree(t *testing.T) {
	tests := map[string]string{
		"release-1":       "release-1",
		"feature/topic":   "feature/topic",
		"release?1":       "release%3F1",
		"release*1":       "release%2A1",
		"release%3F1":     "release%253F1",
		"bad:name":        "bad%3Aname",
		"two..dots":       "two%2E%2Edots",
		".hidden":         "%2Ehidden",
		"topic.lock":      "topic%2Elock",
		"reflog@{1}":      "reflog%40%7B1%7D",
		"back\\slash":     "back%5Cslash",
		"with whitespace": "with%20whitespace",
	}
	seen := make(map[string]string)
	for original, want := range tests {
		got := sanitizeSymbolName(original)
		if got != want {
			t.Errorf("sanitizeSymbolName(%q) = %q, want %q", original, got, want)
		}
		if !validGitRefName("refs/heads/"+got) || !validGitRefName("refs/tags/"+got) {
			t.Errorf("sanitizeSymbolName(%q) produced invalid ref component %q", original, got)
		}
		if prior, ok := seen[got]; ok {
			t.Errorf("symbols %q and %q both sanitize to %q", prior, original, got)
		}
		seen[got] = original
		if again := sanitizeSymbolName(original); again != got {
			t.Errorf("sanitizeSymbolName(%q) changed from %q to %q", original, got, again)
		}
	}
}

func TestParseArgsRejectsInvalidRemoteNames(t *testing.T) {
	for _, args := range [][]string{
		{"-e", "bad:name"},
		{"--remote", "two..dots"},
		{"--remote=topic.lock"},
		{"--remote="},
	} {
		_, err := parseArgs(args)
		if err == nil || !strings.Contains(err.Error(), "invalid remote name") {
			t.Errorf("parseArgs(%q) error = %v, want invalid remote name error", args, err)
		}
	}
	cfg, err := parseArgs([]string{"--remote=team/origin"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != "team/origin" {
		t.Fatalf("remote = %q, want team/origin", cfg.Remote)
	}
}

func TestEmitterRejectsInvalidRefsBeforeEmission(t *testing.T) {
	makeRepo := func(branchName, commitName string, tags map[string][]*Commit) *Repo {
		revision := &Commit{
			Number:   Number{C: 2, N: []int{1, 1}},
			Date:     time.Unix(1, 0),
			Author:   "author",
			Log:      "import\n",
			Text:     "content\n",
			FileName: "file",
			Mode:     0644,
		}
		commit := &GitCommit{
			Date:    revision.Date,
			Author:  revision.Author,
			Log:     revision.Log,
			Files:   []*Commit{revision},
			RefName: commitName,
			Tail:    true,
		}
		return &Repo{
			Branches:   []*GitBranch{{Name: branchName, Head: commit}},
			CommitOf:   map[*Commit]*GitCommit{revision: commit},
			TagTargets: tags,
		}
	}
	tests := []struct {
		name       string
		branchName string
		commitName string
		tags       map[string][]*Commit
		prefix     string
		want       string
	}{
		{
			name: "branch tip", branchName: "bad:name", commitName: "bad:name",
			want: `invalid branch ref "refs/heads/bad:name"`,
		},
		{
			name: "commit branch", branchName: "master", commitName: "bad:name",
			want: `invalid branch ref "refs/heads/bad:name"`,
		},
		{
			name: "tag", branchName: "master", commitName: "master",
			tags: map[string][]*Commit{"bad:name": nil},
			want: `invalid tag ref "refs/tags/bad:name"`,
		},
		{
			name: "branch prefix", branchName: "master", commitName: "master",
			prefix: "refs/remotes/bad:name/",
			want:   `invalid branch ref "refs/remotes/bad:name/master"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := makeRepo(test.branchName, test.commitName, test.tags)
			var output bytes.Buffer
			emitter := NewEmitter(&output).
				WithTagTargets(repo.TagTargets).
				WithOptions(ExportOptions{
					NoIgnores: true, Reposurgeon: true, BranchPrefix: test.prefix,
				})
			err := emitter.EmitRepo(repo)
			if err == nil || err.Error() != test.want {
				t.Fatalf("EmitRepo error = %v, want %q", err, test.want)
			}
			if err := emitter.Flush(); err != nil {
				t.Fatal(err)
			}
			if output.Len() != 0 {
				t.Fatalf("EmitRepo wrote output before ref validation: %q", output.String())
			}
		})
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

func TestLinkBranchParentsUsesPathAndRevisionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		baseRevision   string
		branchRevision string
	}{
		{name: "trunk-base-fallback", baseRevision: "1.2", branchRevision: "1.2.2.1"},
		{name: "nested-branch-base", baseRevision: "1.2.2.1", branchRevision: "1.2.2.1.4.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(s string) Number {
				t.Helper()
				n, err := Parse(s)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			baseA := &Commit{Number: parse(tc.baseRevision), FileName: "a"}
			baseB := &Commit{Number: parse(tc.baseRevision), FileName: "b"}
			gitA := &GitCommit{Files: []*Commit{baseA}, RefName: "parent-a"}
			gitB := &GitCommit{Files: []*Commit{baseB}, RefName: "parent-b"}
			branchRevision := &Commit{
				Number:   parse(tc.branchRevision),
				FileName: "a",
			}
			branchCommit := &GitCommit{
				Files:   []*Commit{branchRevision},
				RefName: "child",
			}
			branches := []*GitBranch{{Name: "child", Head: branchCommit}}
			byFileRev := map[string]*Commit{
				"a\x00" + tc.baseRevision: baseA,
				"b\x00" + tc.baseRevision: baseB,
			}
			commitOf := map[*Commit]*GitCommit{
				baseA: gitA,
				baseB: gitB,
			}

			linkBranchParents(branches, byFileRev, commitOf, nil, nil, nil)
			if branchCommit.Parent != gitA {
				t.Fatalf("branch parent = %p, want same-file commit %p (other-file collision %p)",
					branchCommit.Parent, gitA, gitB)
			}
		})
	}
}

func TestPathDeepCompareIsStrictWeakOrdering(t *testing.T) {
	tests := []struct {
		a    string
		b    string
		want int
	}{
		{a: "d/a", b: "d/b", want: -1},
		{a: "d/b", b: "d/a", want: 1},
		{a: "a/x", b: "b/y", want: -1},
		{a: "b/y", b: "a", want: -1},
		{a: "p/p/b/x/x", b: "p/p/a", want: -1},
		{a: "same/path", b: "same/path", want: 0},
	}
	sign := func(n int) int {
		switch {
		case n < 0:
			return -1
		case n > 0:
			return 1
		default:
			return 0
		}
	}
	for _, tc := range tests {
		if got := sign(PathDeepCompare(tc.a, tc.b)); got != tc.want {
			t.Errorf("PathDeepCompare(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}

	var paths []string
	segments := []string{"a", "b", "c"}
	var generate func(string, int)
	generate = func(prefix string, depth int) {
		if depth == 0 {
			return
		}
		for _, segment := range segments {
			path := segment
			if prefix != "" {
				path = prefix + "/" + segment
			}
			paths = append(paths, path)
			generate(path, depth-1)
		}
	}
	generate("", 3)
	paths = append(paths, "p/p/a", "p/p/b/x/x", "x/y/z/w")

	for _, a := range paths {
		for _, b := range paths {
			ab := sign(PathDeepCompare(a, b))
			ba := sign(PathDeepCompare(b, a))
			if ab != -ba {
				t.Fatalf("comparator is not antisymmetric for %q and %q: %d, %d", a, b, ab, ba)
			}
			if (ab == 0) != (a == b) {
				t.Fatalf("comparator equality mismatch for %q and %q: %d", a, b, ab)
			}
		}
	}
	for _, a := range paths {
		for _, b := range paths {
			if PathDeepCompare(a, b) >= 0 {
				continue
			}
			for _, c := range paths {
				if PathDeepCompare(b, c) < 0 && PathDeepCompare(a, c) >= 0 {
					t.Fatalf("comparator is not transitive: %q < %q < %q", a, b, c)
				}
			}
		}
	}
}

func TestBranchSeedClassificationUsesTopologyNotName(t *testing.T) {
	parse := func(s string) Number {
		t.Helper()
		n, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a11 := &Commit{Number: parse("1.1"), FileName: "a"}
	a12 := &Commit{Number: parse("1.2"), FileName: "a"}
	b11 := &Commit{Number: parse("1.1"), FileName: "b"}
	b12 := &Commit{Number: parse("1.2"), FileName: "b"}
	master1 := &GitCommit{Date: time.Unix(1, 0), Files: []*Commit{a11, b11}, RefName: "master"}
	master2 := &GitCommit{Parent: master1, Date: time.Unix(2, 0), Files: []*Commit{a12}, RefName: "master"}
	master3 := &GitCommit{Parent: master2, Date: time.Unix(3, 0), Files: []*Commit{b12}, RefName: "master"}
	namedLikeFixture := &GitBranch{
		Name: "B_MIXED",
		Head: &GitCommit{Date: time.Unix(4, 0), RefName: "B_MIXED"},
	}
	renamedPathology := &GitBranch{
		Name: "renamed-pathology",
		Head: &GitCommit{Date: time.Unix(4, 0), RefName: "renamed-pathology"},
	}
	seeds := map[string]branchSeedInfo{
		// These differently numbered bases coexist at master2.
		"B_MIXED": {commits: []*Commit{a12, b11}},
		// These bases never coexist in the master history.
		"renamed-pathology": {commits: []*Commit{a11, b12}},
	}
	classifyBranchSeeds([]*GitBranch{
		{Name: "master", Head: master3}, namedLikeFixture, renamedPathology,
	}, seeds)
	if seeds["B_MIXED"].syntheticBase {
		t.Fatal("fixture-like branch name enabled synthetic-base handling")
	}
	if !seeds["renamed-pathology"].syntheticBase {
		t.Fatal("unrepresentable renamed branch topology did not enable synthetic-base handling")
	}
}

func TestSyntheticTagSelectionUsesTopologyNotName(t *testing.T) {
	parse := func(s string) Number {
		t.Helper()
		n, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a := &Commit{Number: parse("1.1"), FileName: "a"}
	b := &Commit{Number: parse("1.1"), FileName: "b"}
	c := &Commit{Number: parse("1.1"), FileName: "c"}
	gitCommit := &GitCommit{Files: []*Commit{a, b, c}, RefName: "master"}
	commitOf := map[*Commit]*GitCommit{a: gitCommit, b: gitCommit, c: gitCommit}
	attachments, _, synthetic := resolveTagAttachments(
		[]*GitCommit{gitCommit},
		map[string][]*Commit{
			"T_ALL_INITIAL_FILES_BUT_ONE": {a},
			"renamed-near-complete-tag":   {a, b},
		},
		commitOf,
		nil,
	)
	if attachments["T_ALL_INITIAL_FILES_BUT_ONE"] != gitCommit {
		t.Fatal("fixture-like tag name enabled synthetic-tag handling")
	}
	if _, ok := synthetic["T_ALL_INITIAL_FILES_BUT_ONE"]; ok {
		t.Fatal("fixture-like tag name was marked synthetic")
	}
	if _, ok := synthetic["renamed-near-complete-tag"]; !ok {
		t.Fatal("near-complete renamed tag topology was not marked synthetic")
	}
}

func TestEmitterPropagatesOutputErrors(t *testing.T) {
	sentinel := errors.New("synthetic write failure")
	makeRepo := func(text string) *Repo {
		t.Helper()
		number, err := Parse("1.1")
		if err != nil {
			t.Fatal(err)
		}
		revision := &Commit{
			Number:   number,
			Date:     time.Unix(100, 0),
			Author:   "author",
			Log:      "commit\n",
			Text:     text,
			FileName: "file",
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
		return &Repo{
			Branches: []*GitBranch{{Name: "master", Head: commit}},
			CommitOf: map[*Commit]*GitCommit{revision: commit},
		}
	}

	t.Run("stream-during-emit", func(t *testing.T) {
		emitter := NewEmitter(alwaysErrorWriter{err: sentinel}).
			WithOptions(ExportOptions{NoIgnores: true})
		err := emitter.EmitRepo(makeRepo(strings.Repeat("x", 16*1024)))
		if !errors.Is(err, sentinel) {
			t.Fatalf("EmitRepo error = %v, want %v", err, sentinel)
		}
	})

	t.Run("stream-during-flush", func(t *testing.T) {
		emitter := NewEmitter(alwaysErrorWriter{err: sentinel}).
			WithOptions(ExportOptions{NoIgnores: true})
		if err := emitter.EmitRepo(makeRepo("small\n")); err != nil {
			t.Fatalf("EmitRepo returned before buffered write: %v", err)
		}
		if err := emitter.Flush(); !errors.Is(err, sentinel) {
			t.Fatalf("Flush error = %v, want %v", err, sentinel)
		}
	})

	t.Run("revision-map", func(t *testing.T) {
		var stream bytes.Buffer
		emitter := NewEmitter(&stream).WithOptions(ExportOptions{
			NoIgnores:   true,
			RevisionMap: alwaysErrorWriter{err: sentinel},
		})
		err := emitter.EmitRepo(makeRepo("content\n"))
		if !errors.Is(err, sentinel) {
			t.Fatalf("EmitRepo revision-map error = %v, want %v", err, sentinel)
		}
	})
}

func TestCLIExitsNonzeroOnOutputFailure(t *testing.T) {
	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skip("/dev/full is unavailable")
	}
	defer full.Close()
	fixture, err := filepath.Abs(filepath.Join("tests", "access,v"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("fast-import-stream", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelper", "--", fixture)
		cmd.Env = append(os.Environ(), "CFE_CLI_HELPER=1")
		cmd.Stdout = full
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
			t.Fatalf("output failure exit = %v, want nonzero; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "fast-import stream") {
			t.Fatalf("stream failure was not reported: %s", stderr.String())
		}
	})

	t.Run("revision-map", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelper", "--",
			"-R", "/dev/full", fixture)
		cmd.Env = append(os.Environ(), "CFE_CLI_HELPER=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
			t.Fatalf("revision-map failure exit = %v, want nonzero; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "revision map") {
			t.Fatalf("revision-map failure was not reported: %s", stderr.String())
		}
	})

	t.Run("help", func(t *testing.T) {
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelper", "--", "--help")
		cmd.Env = append(os.Environ(), "CFE_CLI_HELPER=1")
		cmd.Stdout = full
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
			t.Fatalf("help failure exit = %v, want nonzero; stderr=%s", err, stderr.String())
		}
	})
}

func TestCLIRejectsMalformedDelta(t *testing.T) {
	const archive = `head 1.2;
access;
symbols;
locks; strict;


1.2
date 2020.01.02.03.04.05; author u; state Exp;
branches;
next 1.1;

1.1
date 2020.01.02.03.04.04; author u; state Exp;
branches;
next ;


desc
@@


1.2
log
@new
@
text
@a
@


1.1
log
@old
@
text
@%s@
`
	for _, delta := range []string{"a0 -1\n", "d1 -1\n", "a2 1\nx\n"} {
		t.Run(delta, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "invalid,v")
			if err := os.WriteFile(fixture, []byte(fmt.Sprintf(archive, delta)), 0644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelper", "--", fixture)
			cmd.Env = append(os.Environ(), "CFE_CLI_HELPER=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("invalid archive exit = %v, want status 1; stderr=%s", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), "delta") || strings.Contains(stderr.String(), "panic:") {
				t.Fatalf("invalid archive did not report a delta error: %s", stderr.String())
			}
		})
	}
}

func TestCLIExportsCurrentDirectory(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("tests", "access,v"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "access,v"), data, 0644); err != nil {
		t.Fatal(err)
	}
	export := func(path string) []byte {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=TestCLIHelper", "--", "-N", path)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CFE_CLI_HELPER=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("export %q: %v; stderr=%s", path, err, stderr.String())
		}
		return output
	}
	want := export("access,v")
	if !bytes.Contains(want, []byte("commit refs/heads/master\n")) {
		t.Fatal("fixture did not export a commit")
	}
	if got := export("."); !bytes.Equal(got, want) {
		t.Fatalf("export from current directory = %q, want %q", got, want)
	}
}

func TestCLIHelper(t *testing.T) {
	if os.Getenv("CFE_CLI_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		os.Exit(2)
	}
	os.Args = append([]string{"cvs-fast-export"}, os.Args[separator+1:]...)
	main()
	os.Exit(0)
}
