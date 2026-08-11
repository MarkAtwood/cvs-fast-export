#!/usr/bin/env python3
"""Regression checks for the companion-tool hardening."""

# pylint: disable=consider-using-f-string,missing-function-docstring,too-many-locals

import ast
import contextlib
import io
import os
from pathlib import Path
import runpy
import shutil
import subprocess
import sys
import tempfile


def run(command, **kwargs):
    "Run a command and fail this test on a nonzero exit."
    result = subprocess.run(command, check=False, **kwargs)
    if result.returncode:
        raise RuntimeError("%r returned %d" % (command, result.returncode))
    return result


def assert_shell_free(path):
    "Ensure subprocess calls in a script do not request a shell."
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        for keyword in node.keywords:
            if (
                keyword.arg == "shell"
                and isinstance(keyword.value, ast.Constant)
                and keyword.value.value is True
            ):
                raise AssertionError("%s still invokes a shell" % path.name)


def main():
    root = Path(__file__).resolve().parent.parent
    cvsconvert = root / "cvsconvert"
    cvssync = root / "cvssync"
    cvsstrip = root / "cvsstrip"

    assert_shell_free(cvsconvert)
    assert_shell_free(cvssync)

    namespace = runpy.run_path(str(cvsconvert))
    comparison_type = namespace["ConvertComparison"]
    comparison = object.__new__(comparison_type)
    comparison.branches = ["main"]
    comparison.tags = ["release"]
    comparison.repo = type("Repo", (), {"name": "sample"})()
    compared = []
    comparison.compare_tree = lambda legend, ref: compared.append((legend, ref)) or True
    with contextlib.redirect_stdout(io.StringIO()):
        if not comparison.checkall():
            raise AssertionError("independent comparison unexpectedly failed")
    if compared != [("branch", "main"), ("tag", "release")]:
        raise AssertionError("comparison used state outside its instance")

    with tempfile.TemporaryDirectory(prefix="cfe-companion-") as temp:
        temp_path = Path(temp)
        fake_rsync = temp_path / "rsync"
        fake_rsync.symlink_to(shutil.which("true"))
        marker = temp_path / "shell-injection"
        malicious_module = "module';touch %s;#" % marker
        child_env = os.environ.copy()
        child_env["PATH"] = temp + os.pathsep + child_env["PATH"]
        run(
            [str(cvssync), "host:/repository", malicious_module],
            env=child_env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        if marker.exists():
            raise AssertionError("cvssync executed text from a module name")

        repo = temp_path / "whole repo;touch PWNED"
        shutil.copytree(root / "tests" / "oldhead.testrepo", repo)
        (repo / "module").rename(repo / "one")
        shutil.copytree(repo / "one", repo / "two")
        result = subprocess.run(
            [str(cvsconvert), str(repo)],
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            universal_newlines=True,
        )
        if result.returncode == 0 or "choose one of" not in result.stderr:
            raise AssertionError("cvsconvert did not autodetect multiple modules")

        convert_env = os.environ.copy()
        convert_env["CVS_FAST_EXPORT"] = str(root / "cvs-fast-export")
        run(
            [str(cvsconvert), "-w", "-q", repo.name],
            cwd=temp,
            env=convert_env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        if (temp_path / "PWNED").exists():
            raise AssertionError("cvsconvert executed text from a repository name")
        git_repo = temp_path / (repo.name + "-proxy-module-git")
        tree = run(
            ["git", "-C", str(git_repo), "ls-tree", "-r", "--name-only", "master"],
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            universal_newlines=True,
        ).stdout.splitlines()
        if "one/ChangeLog" not in tree or "two/ChangeLog" not in tree:
            raise AssertionError("whole-repository conversion omitted a module")

        with open(root / "tests" / "empty,v", "rb") as master:
            run(
                [str(cvsstrip), "-vv"],
                stdin=master,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )

    print("ok - companion-tool hardening")
    return 0


if __name__ == "__main__":
    sys.exit(main())
