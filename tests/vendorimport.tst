#!/usr/bin/env python3
## Trunk follows vendor imports until the first trunk commit

# CVS shows the newest vendor revision on trunk while a file has no 1.2,
# so a's trunk goes 1.1 (v1), 1.1.1.2 (v2), 1.2.  b is never committed on
# trunk, so its trunk follows every import.  v3 arrives after a's 1.2 and
# stays on the vendor branch for a.

# pylint: disable=multiple-imports
import sys, os, shutil, tempfile, time, testlifter

testlifter.verbose += sys.argv[1:].count("-v")
repo = testlifter.CVSRepository("vendorimport.repo")
repo.init()
src = tempfile.mkdtemp()


def vendor_import(tag, contents):
    "Import a vendor release onto vendor branch VENDOR."
    for name, text in contents.items():
        with open(os.path.join(src, name), "w", encoding="ascii") as fp:
            fp.write(text)
    with testlifter.directory_context(src):
        repo.do("import", "-m", "'Vendor release %s'" % tag, "module", "VENDOR", tag)
    # Keep each step in its own second so that cvs export -D separates them.
    time.sleep(2)


vendor_import("v1", {"a": "one\n", "b": "bee one\n"})
vendor_import("v2", {"a": "two\n", "b": "bee two\n"})

co = repo.checkout("module", "vendorimport.checkout")
co.append("a", "three\n")
co.commit("Trunk edit of a")
time.sleep(2)

vendor_import("v3", {"a": "four\n", "b": "bee three\n"})

shutil.rmtree(src)
repo.cleanup()

# end
