#!/usr/bin/env python3
## Sub-branch omits parent-branch files that lack its symbol

# CVS checks out a branch or tag only from files carrying that symbol.
# Branch "parent" forks from trunk; "child" forks from "parent" and is
# tagged on two files only.  Two files on "parent" have no "child" symbol:
# "untagged" was never tagged for the sub-branch, and "removed" was killed
# on "parent" after the sub-branch base revision of README was committed.
# Neither may appear on "child" or on the tags child_root and child_tag.

import testlifter

repo = testlifter.CVSRepository("subbranch.repo")
repo.init()
repo.module("module")
co = repo.checkout("module", "subbranch.checkout")

co.write("README", "README on trunk\n")
co.write("common", "common on trunk\n")
co.write("untagged", "untagged on trunk\n")
co.write("removed", "removed on trunk\n")
co.add("README", "common", "untagged", "removed")
co.commit("Initial trunk files")

co.branch("parent")

co.write("README", "README on parent\n")
co.commit("README on parent")

co.remove("removed")
co.commit("Remove a file on parent")

# Fork the sub-branch from two files only.
co.do("tag", "child_root", "README", "common")
co.do("tag", "-r", "child_root", "-b", "child", "README", "common")
co.switch("child")

co.write("README", "README on child\n")
co.commit("README on child")
co.tag("child_tag")

co.switch("parent")
co.write("untagged", "untagged on parent\n")
co.commit("untagged on parent")

repo.cleanup()

# end
