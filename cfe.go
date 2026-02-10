// cfe.go is an experimental Goio port of cvs-fast-export
//
// SPDX-FileCopyrightText: Copyright Eric S. Raymond <esr@thyrsus.com>
// SPDX-License-Identifier: BSD-2-Clause
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"

	rcs "github.com/arran4/golang-rcs"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	var files []*rcs.File
	for scanner.Scan() {
		path := scanner.Text()
		if !strings.HasSuffix(path, ",v") {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			log.Fatalf("open %s: %v", path, err)
		}
		rcsFile, err := rcs.ParseFile(f)
		f.Close()
		if err != nil {
			log.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, rcsFile)
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("read stdin: %v", err)
	}

	// Smoke test to verify that we have in fact read data.
	//fmt.Printf("Files: %d\n", len(files))
	fmt.Printf("%#v", files)
}
