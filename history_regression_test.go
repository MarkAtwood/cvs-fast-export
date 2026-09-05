package main

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func historyRevision(t *testing.T, revision string, when int64, path, text string) *Commit {
	t.Helper()
	number, err := Parse(revision)
	if err != nil {
		t.Fatal(err)
	}
	return &Commit{
		Number:   number,
		Date:     time.Unix(when, 0),
		Author:   "author",
		Log:      "revision " + revision + "\n",
		Text:     text,
		FileName: path,
		Mode:     0644,
	}
}

func historyCommit(parent *GitCommit, revision *Commit) *GitCommit {
	commit := &GitCommit{
		Parent:  parent,
		Date:    revision.Date,
		Author:  revision.Author,
		Log:     revision.Log,
		Files:   []*Commit{revision},
		RefName: "master",
	}
	if parent == nil {
		commit.Tail = true
	}
	return commit
}

func emitHistoryStream(t *testing.T, repo *Repo, options ExportOptions) string {
	t.Helper()
	options.NoIgnores = true
	var output bytes.Buffer
	emitter := NewEmitter(&output).
		WithTagTargets(repo.TagTargets).
		WithOptions(options)
	if err := emitter.EmitRepo(repo); err != nil {
		t.Fatal(err)
	}
	if err := emitter.Flush(); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func runHistoryGit(t *testing.T, git, input string, args ...string) string {
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

func newHistoryGitRepo(t *testing.T, git string) string {
	t.Helper()
	repository := t.TempDir()
	runHistoryGit(t, git, "", "init", "--quiet", repository)
	return repository
}

func importHistoryStream(t *testing.T, git, repository, stream string) {
	t.Helper()
	runHistoryGit(t, git, stream, "-C", repository, "fast-import", "--quiet")
}

func assertLinearHistory(t *testing.T, git, repository, ref string, count int) {
	t.Helper()
	lines := strings.FieldsFunc(runHistoryGit(t, git, "", "-C", repository,
		"rev-list", "--reverse", "--parents", ref), func(r rune) bool { return r == '\n' })
	if len(lines) != count {
		t.Fatalf("%s has %d commits, want %d: %q", ref, len(lines), count, lines)
	}
	var previous string
	for i, line := range lines {
		fields := strings.Fields(line)
		if i == 0 {
			if len(fields) != 1 {
				t.Fatalf("root commit has parents: %q", line)
			}
		} else if len(fields) != 2 || fields[1] != previous {
			t.Fatalf("commit %d does not have the previous commit as its sole parent: %q", i+1, line)
		}
		previous = fields[0]
	}
}

func TestExportPreservesQuotedPaths(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	for _, path := range []string{
		`"quoted"`, `"unterminated`, "line\nbreak", "tab\tname", `back\slash`,
		"control-\x01\x7f", "utf8-café", "bytes-\x80\xff7", "space name", "sub/nested\nfile",
	} {
		t.Run(strconv.Quote(path), func(t *testing.T) {
			first := historyCommit(nil, historyRevision(t, "1.1", 100, path, "original\n"))
			second := historyCommit(first, historyRevision(t, "1.2", 200, path, "updated\n"))
			dead := historyRevision(t, "1.3", 300, path, "")
			dead.Dead = true
			last := historyCommit(second, dead)
			repo := &Repo{Branches: []*GitBranch{{Name: "master", Head: last}}}
			repository := newHistoryGitRepo(t, git)
			importHistoryStream(t, git, repository, emitHistoryStream(t, repo, ExportOptions{}))
			for _, state := range []struct{ ref, content string }{
				{"master~2", "original\n"}, {"master~1", "updated\n"},
			} {
				if got := runHistoryGit(t, git, "", "-C", repository,
					"ls-tree", "-rz", "--name-only", state.ref); got != path+"\x00" {
					t.Fatalf("%s paths = %q, want %q", state.ref, got, path+"\x00")
				}
				if got := runHistoryGit(t, git, "", "-C", repository,
					"show", state.ref+":"+path); got != state.content {
					t.Fatalf("%s content = %q, want %q", state.ref, got, state.content)
				}
			}
			if got := runHistoryGit(t, git, "", "-C", repository,
				"ls-tree", "-rz", "--name-only", "master"); got != "" {
				t.Fatalf("deleted path remains in final tree: %q", got)
			}
		})
	}
}

func TestIncrementalNewBranches(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	for _, tc := range []struct {
		name                         string
		older, noop, nested, advance bool
		forceDates                   bool
		prefix                       string
	}{
		{name: "trunk tip"},
		{name: "older trunk base", older: true},
		{name: "trunk advances in same import", older: true, advance: true},
		{name: "no-op base and ancestor", older: true, noop: true, advance: true},
		{name: "nested branch", older: true, nested: true, advance: true},
		{name: "forced dates", older: true, noop: true, advance: true, forceDates: true},
		{name: "remote refs", older: true, advance: true, prefix: "refs/remotes/cvs/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := tc.prefix
			if prefix == "" {
				prefix = "refs/heads/"
			}
			root := historyCommit(nil, historyRevision(t, "1.1", 100, "file", "base\n"))
			base, trunk := root, root
			if tc.noop {
				base = historyCommit(root, historyRevision(t, "1.2", 150, "file", "base\n"))
				trunk = base
			}
			if tc.older {
				trunk = historyCommit(trunk, historyRevision(t, "1.1", 200, "trunk-only", "old\n"))
			}
			baseline := &Repo{Branches: []*GitBranch{{Name: "master", Head: trunk}}}
			parentRef := prefix + "master"
			if tc.older {
				parentRef += "~1"
			}
			if tc.nested {
				base = historyCommit(root, historyRevision(t, "1.1.2.1", 250, "file", "release\n"))
				base.RefName, base.Tail = "release", true
				baseline.Branches = append(baseline.Branches, &GitBranch{Name: "release", Head: base})
				parentRef = prefix + "release"
			}
			options := ExportOptions{ForceDates: tc.forceDates, BranchPrefix: prefix}
			repository := newHistoryGitRepo(t, git)
			importHistoryStream(t, git, repository, emitHistoryStream(t, baseline, options))
			parentID := strings.TrimSpace(runHistoryGit(t, git, "", "-C", repository, "rev-parse", parentRef))
			cutoff := int64(350)
			if tc.forceDates {
				cutoff, err = strconv.ParseInt(strings.TrimSpace(runHistoryGit(t, git, "",
					"-C", repository, "log", "-1", "--format=%ct", prefix+"master")), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
			}
			update := &Repo{Branches: append([]*GitBranch(nil), baseline.Branches...)}
			if tc.advance {
				trunk = historyCommit(trunk, historyRevision(t, "1.2", 400, "trunk-only", "new\n"))
				update.Branches[0] = &GitBranch{Name: "master", Head: trunk}
			}
			branch := historyCommit(base, historyRevision(t, "1.1.2.1", 500, "file", "branch\n"))
			branch.RefName, branch.Tail = "newbranch", true
			update.Branches = append(update.Branches, &GitBranch{Name: "newbranch", Head: branch})
			options.Incremental, options.FromTime = true, cutoff
			stream := emitHistoryStream(t, update, options)
			importHistoryStream(t, git, repository, stream)
			if got := strings.TrimSpace(runHistoryGit(t, git, "", "-C", repository,
				"rev-parse", prefix+"newbranch^")); got != parentID {
				t.Fatalf("new branch parent = %s, want %s\n%s", got, parentID, stream)
			}
			if got := runHistoryGit(t, git, "", "-C", repository, "show", prefix+"newbranch:file"); got != "branch\n" {
				t.Fatalf("branch file = %q, want %q", got, "branch\n")
			}
			if got := runHistoryGit(t, git, "", "-C", repository,
				"ls-tree", "-r", "--name-only", prefix+"newbranch"); got != "file\n" {
				t.Fatalf("branch inherited files outside its base: %q", got)
			}
			// The incremental result must have the same commit identities as a full import.
			full := newHistoryGitRepo(t, git)
			options.Incremental, options.FromTime = false, 0
			importHistoryStream(t, git, full, emitHistoryStream(t, update, options))
			for _, b := range update.Branches {
				ref := prefix + b.Name
				got := runHistoryGit(t, git, "", "-C", repository, "rev-parse", ref)
				want := runHistoryGit(t, git, "", "-C", full, "rev-parse", ref)
				if got != want {
					t.Fatalf("incremental %s = %s, full import = %s", ref, got, want)
				}
			}
		})
	}
}

func TestHistoryRegressions(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}

	t.Run("empty file update", func(t *testing.T) {
		rev1 := historyRevision(t, "1.1", 100, "empty", "not empty\n")
		rev2 := historyRevision(t, "1.2", 200, "empty", "")
		git1 := historyCommit(nil, rev1)
		git2 := historyCommit(git1, rev2)
		repo := &Repo{
			Branches: []*GitBranch{{Name: "master", Head: git2}},
			CommitOf: map[*Commit]*GitCommit{rev1: git1, rev2: git2},
		}
		repository := newHistoryGitRepo(t, git)
		importHistoryStream(t, git, repository, emitHistoryStream(t, repo, ExportOptions{}))
		assertLinearHistory(t, git, repository, "master", 2)
		if got := runHistoryGit(t, git, "", "-C", repository, "show", "master^:empty"); got != rev1.Text {
			t.Fatalf("pre-update content = %q, want %q", got, rev1.Text)
		}
		if got := strings.TrimSpace(runHistoryGit(t, git, "", "-C", repository,
			"cat-file", "-s", "master:empty")); got != "0" {
			t.Fatalf("updated empty file has size %s, want 0", got)
		}
	})

	t.Run("escaped cvsignore update", func(t *testing.T) {
		path := fileopName(".cvsignore")
		rev1 := historyRevision(t, "1.1", 100, path, "#first\n")
		rev2 := historyRevision(t, "1.2", 200, path, "*.o #second\n")
		git1 := historyCommit(nil, rev1)
		git2 := historyCommit(git1, rev2)
		repo := &Repo{
			Branches: []*GitBranch{{Name: "master", Head: git2}},
			CommitOf: map[*Commit]*GitCommit{rev1: git1, rev2: git2},
		}
		repository := newHistoryGitRepo(t, git)
		importHistoryStream(t, git, repository, emitHistoryStream(t, repo, ExportOptions{}))
		assertLinearHistory(t, git, repository, "master", 2)
		if got, want := runHistoryGit(t, git, "", "-C", repository,
			"show", "master^:.gitignore"), normalizeGitignore(rev1.Text); got != want {
			t.Fatalf("initial .gitignore = %q, want %q", got, want)
		}
		if got, want := runHistoryGit(t, git, "", "-C", repository,
			"show", "master:.gitignore"), normalizeGitignore(rev2.Text); got != want {
			t.Fatalf("updated .gitignore = %q, want %q", got, want)
		}
	})

	t.Run("revert and tag", func(t *testing.T) {
		rev1 := historyRevision(t, "1.1", 100, "file", "original\n")
		rev2 := historyRevision(t, "1.2", 200, "file", "changed\n")
		rev3 := historyRevision(t, "1.3", 300, "file", rev1.Text)
		git1 := historyCommit(nil, rev1)
		git2 := historyCommit(git1, rev2)
		git3 := historyCommit(git2, rev3)
		repo := &Repo{
			Branches:   []*GitBranch{{Name: "master", Head: git3}},
			CommitOf:   map[*Commit]*GitCommit{rev1: git1, rev2: git2, rev3: git3},
			TagTargets: map[string][]*Commit{"after-revert": {rev3}},
			TagOrder:   []string{"after-revert"},
		}
		repository := newHistoryGitRepo(t, git)
		importHistoryStream(t, git, repository, emitHistoryStream(t, repo, ExportOptions{}))
		assertLinearHistory(t, git, repository, "master", 3)
		head := strings.TrimSpace(runHistoryGit(t, git, "", "-C", repository, "rev-parse", "master"))
		tag := strings.TrimSpace(runHistoryGit(t, git, "", "-C", repository, "rev-parse", "after-revert"))
		root := strings.TrimSpace(runHistoryGit(t, git, "", "-C", repository,
			"rev-list", "--max-parents=0", "master"))
		if tag != head {
			t.Fatalf("after-revert points to %s, want final commit %s", tag, head)
		}
		if tag == root {
			t.Fatal("after-revert attached to the original equal-content revision")
		}
	})

	t.Run("rolling timestamps", func(t *testing.T) {
		base := int64(1_000_000)
		revisions := []*Commit{
			historyRevision(t, "1.1", base, "file-0", "zero\n"),
			historyRevision(t, "1.1", base+299, "file-1", "one\n"),
			historyRevision(t, "1.1", base+598, "file-2", "two\n"),
		}
		for _, revision := range revisions {
			revision.Log = "one changeset\n"
		}
		index := &collateIndex{commitOf: make(map[*Commit]*GitCommit)}
		head := collateBranch("master", []*Head{
			{RefName: "master", Commit: revisions[0]},
			{RefName: "master", Commit: revisions[1]},
			{RefName: "master", Commit: revisions[2]},
		}, CollateOptions{CommitTimeWindow: 300 * time.Second}, index)
		repo := &Repo{
			Branches: []*GitBranch{{Name: "master", Head: head}},
			CommitOf: index.commitOf,
		}
		repository := newHistoryGitRepo(t, git)
		importHistoryStream(t, git, repository, emitHistoryStream(t, repo, ExportOptions{}))
		assertLinearHistory(t, git, repository, "master", 2)
		if got := runHistoryGit(t, git, "", "-C", repository,
			"ls-tree", "-r", "--name-only", "master^"); got != "file-0\nfile-1\n" {
			t.Fatalf("first changeset tree = %q", got)
		}
		if got := runHistoryGit(t, git, "", "-C", repository,
			"ls-tree", "-r", "--name-only", "master"); got != "file-0\nfile-1\nfile-2\n" {
			t.Fatalf("final changeset tree = %q", got)
		}
	})

	t.Run("incremental updates", func(t *testing.T) {
		rev1 := historyRevision(t, "1.1", 100, "file", "first\n")
		rev2 := historyRevision(t, "1.2", 200, "file", "second\n")
		rev3 := historyRevision(t, "1.3", 300, "file", "third\n")
		git1 := historyCommit(nil, rev1)
		git2 := historyCommit(git1, rev2)
		git3 := historyCommit(git2, rev3)
		baseline := &Repo{
			Branches: []*GitBranch{{Name: "master", Head: git1}},
			CommitOf: map[*Commit]*GitCommit{rev1: git1},
		}
		update := &Repo{
			Branches: []*GitBranch{{Name: "master", Head: git3}},
			CommitOf: map[*Commit]*GitCommit{rev1: git1, rev2: git2, rev3: git3},
		}
		repository := newHistoryGitRepo(t, git)
		importHistoryStream(t, git, repository, emitHistoryStream(t, baseline, ExportOptions{}))
		incremental := emitHistoryStream(t, update, ExportOptions{Incremental: true, FromTime: 150})
		if got := strings.Count(incremental, "commit refs/heads/master\n"); got != 2 {
			t.Fatalf("incremental stream contains %d commits, want 2\n%s", got, incremental)
		}
		if !strings.Contains(incremental, "from refs/heads/master^0\n") ||
			strings.Contains(incremental, "from :2\n") {
			t.Fatalf("incremental stream has invalid parent references:\n%s", incremental)
		}
		importHistoryStream(t, git, repository, incremental)
		assertLinearHistory(t, git, repository, "master", 3)
		if got := runHistoryGit(t, git, "", "-C", repository, "show", "master:file"); got != rev3.Text {
			t.Fatalf("updated content = %q, want %q", got, rev3.Text)
		}
	})
}
