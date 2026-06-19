#!/usr/bin/env python3
## Branch-only file must not hide earlier commits on the same branch

import os, stat, sys, testlifter


def strip_commitids(top):
    "Remove modern CVS commitids to exercise the no-commitid coalescing path."
    for root, _, files in os.walk(top):
        for filename in files:
            if not filename.endswith(",v"):
                continue
            path = os.path.join(root, filename)
            mode = os.stat(path).st_mode
            with open(path, encoding=testlifter.binary_encoding) as fp:
                lines = fp.readlines()
            os.chmod(path, mode | stat.S_IWUSR)
            with open(path, "w", encoding=testlifter.binary_encoding) as fp:
                for line in lines:
                    if not line.startswith("commitid"):
                        fp.write(line)
            os.chmod(path, mode)


testlifter.verbose += sys.argv[1:].count("-v")
repo = testlifter.CVSRepository("branchbug.repo")
repo.init()
repo.module("module")
co = repo.checkout("module", "branchbug.checkout")

co.write("a.c", "a: initial\n")
co.add("a.c")
co.commit("Initial revision")

co.write("a.c", "a: trunk_v2\n")
co.commit("T2: update a.c")

co.outdo("cvs -Q tag -b MYBRANCH")
co.outdo("cvs -Q update -r MYBRANCH")

co.write("a.c", "a: branch_v1\n")
co.commit("B1: early work")

co.write("a.c", "a: branch_v2\n")
co.commit("B2: more early work")

co.write("a.c", "a: branch_v3\n")
co.write("newfile.c", "newfile: v1\n")
co.add("newfile.c")
co.commit("B3: add newfile.c")

co.write("newfile.c", "newfile: v2\n")
co.commit("B4: update newfile.c")

strip_commitids(os.path.join(repo.directory, "module"))

repo.cleanup()

# end
