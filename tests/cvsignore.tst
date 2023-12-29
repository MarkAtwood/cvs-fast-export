#!/usr/bin/env python3
## Verify that detection of .cvsignore forces cvs type

import sys, testlifter

testlifter.verbose += sys.argv[1:].count("-v")
repo = testlifter.CVSRepository("cvsignore.repo")
repo.init()
repo.module("module")
co = repo.checkout("module", "cvsconvert.checkout")

co.write("README", "The quick brown fox jumped over the lazy dog.")
co.add("README")
co.commit("This is a sample commit")
co.write(".cvsignore", "wibble wobble #ping")
co.add(".cvsignore")
co.commit("This is a .cvsignore commit")

repo.cleanup()

# end
