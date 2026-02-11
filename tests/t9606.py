#!/usr/bin/env python3
## Test handling of stdin path length bounds
"""
Verify that a final line without a newline is accepted when short,
and that truncated lines are rejected as too long.
"""

# pylint: disable=missing-function-docstring,consider-using-f-string

import os
import subprocess
import sys


def run_with_input(data):
    return subprocess.run(
        os.getenv("CVS_FAST_EXPORT", "../cvs-fast-export"),
        input=data,
        text=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        check=False,
    )


def main():
    short_input = "empty,v"
    ok = run_with_input(short_input)
    if ok.returncode != 0:
        sys.stderr.write("short input failed:\n%s" % ok.stderr)
        return 1

    try:
        path_max = os.pathconf(".", "PC_PATH_MAX")
    except (AttributeError, ValueError):
        path_max = 4096

    if path_max < 2:
        sys.stderr.write("unexpected PATH_MAX: %d\n" % path_max)
        return 1

    long_input = "a" * (path_max - 1)
    bad = run_with_input(long_input)
    if bad.returncode == 0:
        sys.stderr.write("long input unexpectedly succeeded\n")
        return 1
    if "too long" not in bad.stderr:
        sys.stderr.write("missing 'too long' error:\n%s" % bad.stderr)
        return 1

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
