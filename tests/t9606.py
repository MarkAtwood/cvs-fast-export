#!/usr/bin/env python3
## Test handling of stdin path length bounds
"""
Verify that a final line without a newline is accepted when short
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

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
