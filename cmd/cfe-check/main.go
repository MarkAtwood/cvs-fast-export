// SPDX-License-Identifier: GPL-2.0+
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"thyrsus.com/cvs-fast-export/internal/compare"
)

type testCase struct {
	Name string
	Kind string // repo | testrepo
	Args []string
}

func main() {
	var cBin string
	var goBin string
	var testsDir string
	flag.StringVar(&cBin, "c-bin", "", "path to C cvs-fast-export binary")
	flag.StringVar(&goBin, "go-bin", "", "path to Go cvs-fast-export binary")
	flag.StringVar(&testsDir, "tests-dir", "tests", "path to tests directory")
	flag.Parse()

	if cBin == "" || goBin == "" {
		fmt.Fprintln(os.Stderr, "cfe-check: --c-bin and --go-bin are required")
		os.Exit(1)
	}

	if err := ensureNeutralizeMap(testsDir); err != nil {
		fmt.Fprintf(os.Stderr, "cfe-check: %v\n", err)
		os.Exit(1)
	}

	cases := []testCase{
		{
			Name: "vendor",
			Kind: "testrepo",
			Args: []string{"-T", "-A", "neutralize.map", "--reposurgeon"},
		},
		{
			Name: "tagbug",
			Kind: "repo",
			Args: []string{"-T", "-A", "neutralize.map", "--reposurgeon"},
		},
		{
			Name: "twobranch",
			Kind: "repo",
			Args: []string{"-T", "-A", "neutralize.map", "--reposurgeon"},
		},
		{
			Name: "oldhead",
			Kind: "testrepo",
			Args: []string{"-T", "-A", "neutralize.map", "--reposurgeon"},
		},
		{
			Name: "longrev",
			Kind: "repo",
			Args: []string{"-T", "-A", "neutralize.map", "--reposurgeon"},
		},
	}

	failures := 0
	for _, tc := range cases {
		root, err := caseRoot(testsDir, tc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cfe-check: %v\n", err)
			failures++
			continue
		}
		inputs, err := collectInputs([]string{root}, nil, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "cfe-check: %s: %v\n", tc.Name, err)
			failures++
			continue
		}

		res, err := compare.Run(cBin, goBin, tc.Args, inputs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cfe-check: %s: %v\n", tc.Name, err)
			failures++
			continue
		}
		if res.Match {
			fmt.Printf("%s: OK (%d bytes)\n", tc.Name, len(res.CStdout))
			continue
		}
		failures++
		fmt.Printf("%s: mismatch (C exit=%d Go exit=%d)\n", tc.Name, res.CExit, res.GoExit)
		if res.DiffOffset >= 0 {
			fmt.Printf("%s", res.Context)
		}
	}

	if failures > 0 {
		os.Exit(1)
	}
}

func ensureNeutralizeMap(testsDir string) error {
	path := filepath.Join(testsDir, "neutralize.map")
	user := os.Getenv("USER")
	if user == "" {
		user = "root"
	}
	line := fmt.Sprintf("%s = foo <foo> -0500\n", user)
	return os.WriteFile(path, []byte(line), 0644)
}

func caseRoot(testsDir string, tc testCase) (string, error) {
	switch tc.Kind {
	case "repo":
		repoDir := filepath.Join(testsDir, tc.Name+".repo")
		if _, err := os.Stat(repoDir); err == nil {
			return filepath.Join(repoDir, "module"), nil
		}
		tst := filepath.Join(testsDir, tc.Name+".tst")
		if _, err := os.Stat(tst); err == nil {
			cmd := exec.Command("python3", tc.Name+".tst")
			cmd.Dir = testsDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("build %s: %w", tc.Name, err)
			}
			return filepath.Join(repoDir, "module"), nil
		}
		return "", fmt.Errorf("missing repo or .tst for %s", tc.Name)
	case "testrepo":
		repoDir := filepath.Join(testsDir, tc.Name+".testrepo")
		if _, err := os.Stat(repoDir); err != nil {
			return "", fmt.Errorf("missing testrepo %s", repoDir)
		}
		return filepath.Join(repoDir, "module"), nil
	default:
		return "", fmt.Errorf("unknown case kind %q", tc.Kind)
	}
}

func collectInputs(roots, files []string, stdinList string) ([]string, error) {
	var inputs []string

	if stdinList != "" {
		data, err := os.ReadFile(stdinList)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			inputs = append(inputs, line)
		}
	}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ",v") {
				inputs = append(inputs, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	inputs = append(inputs, files...)

	uniq := make(map[string]struct{}, len(inputs))
	out := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if _, ok := uniq[in]; ok {
			continue
		}
		uniq[in] = struct{}{}
		out = append(out, in)
	}

	sort.Slice(out, func(i, j int) bool {
		return compare.PathDeepCompare(out[i], out[j]) < 0
	})
	return out, nil
}
