#!/usr/bin/env python3
## Tricky tag corner case
# Tag should attach to the second commit,
# probably :4 in the emitted git stream,
# after :3 which is the first commit and
# blobs at :1 and :2.
# It's an error if the tag attaches to :3.

import sys, testlifter

testlifter.verbose += sys.argv[1:].count("-v")
repo = testlifter.CVSRepository("tagbug.repo")
repo.init()
repo.module("module")
co = repo.checkout("module", "tagbug.checkout")

co.write("foo.c", "The quick brown fox jumped over the lazy dog.\n")
co.add("foo.c")
co.write("bar.c", "Not an obfuscated C contest entry.\n")
co.add("bar.c")
co.commit("First commit")

co.remove("bar.c")
co.commit("Second commit")

co.tag("tag")

repo.cleanup()

# end
