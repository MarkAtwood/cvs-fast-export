#!/usr/bin/env python3
"""Regression checks for the companion-tool hardening."""

# pylint: disable=consider-using-f-string,missing-function-docstring,too-many-locals

import ast
import contextlib
import io
import os
from pathlib import Path
import runpy
import shlex
import shutil
import subprocess
import sys
import tempfile
from unittest import mock


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


def assert_content_comparisons(root, temp_path, namespace):
    "Check actual converted bytes with ordinary and proxy checkout layouts."
    missed = []
    for layout in ("module", "bare", "whole"):
        repo = temp_path / ("comparison-" + layout)
        fixture = root / "tests" / "oldhead.testrepo"
        shutil.copytree(fixture / "module" if layout == "bare" else fixture, repo)
        module = repo if layout == "bare" else repo / "module"
        # This component must stay intact when matching the two checkouts.
        nested = module / "nested.checkout"
        nested.mkdir()
        shutil.copyfile(module / "Makefile.am,v", nested / "file,v")
        if layout == "whole":
            module.rename(repo / "one")
            shutil.copytree(repo / "one", repo / "two")

        with namespace["directory_context"](str(temp_path)):
            comparison = namespace["ConvertComparison"](
                srepo=str(repo),
                smodule="module" if layout == "module" else None,
                options=("-q",),
                whole_repo=layout == "whole",
            )
            try:
                with contextlib.redirect_stdout(io.StringIO()):
                    if not comparison.checkall():
                        raise AssertionError("%s clean conversion failed" % layout)

                relative = "nested.checkout/file"
                if layout == "whole":
                    relative = "one/" + relative
                git_repo = temp_path / comparison.gitRepoName
                # Comparison checks out the ref detached; corrupt the branch itself.
                run(["git", "checkout", "--quiet", "master"], cwd=git_repo)
                target = git_repo / relative
                content = target.read_bytes()
                # Keep the size unchanged; content comparison must read bytes.
                target.write_bytes(bytes([content[0] ^ 1]) + content[1:])
                run(["git", "add", "--", relative], cwd=git_repo)
                run(
                    [
                        "git", "-c", "user.name=Test", "-c", "user.email=test@example.org",
                        "commit", "--quiet", "--no-gpg-sign", "-m", "Corrupt converted content",
                    ],
                    cwd=git_repo,
                )
                output, errors = io.StringIO(), io.StringIO()
                with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
                    if comparison.checkall():
                        missed.append(layout)
                    elif "are different" not in errors.getvalue():
                        raise AssertionError("%s did not report the content mismatch" % layout)
                if "compares clean" in output.getvalue() and layout not in missed:
                    raise AssertionError("%s reported a corrupt conversion as clean" % layout)
            finally:
                comparison.cleanup()
    if missed:
        raise AssertionError("content comparisons skipped for %s" % ", ".join(missed))


def load_cvsconvert(root):
    "Keep the exporter path valid when comparisons change directories."
    with mock.patch.dict(os.environ, {"CVS_FAST_EXPORT": str(root / "cvs-fast-export")}):
        return runpy.run_path(str(root / "cvsconvert"))


def assert_checked_references(namespace):
    "Check that each comparison visits its own branch and tag lists."
    comparison = object.__new__(namespace["ConvertComparison"])
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


def assert_cli_exit_status(root, temp_path):
    "Comparison failures must survive cleanup and later successful conversions."
    work = temp_path / "exit-status"
    work.mkdir()
    for name in ("failing", "passing"):
        shutil.copytree(root / "tests" / "oldhead.testrepo", work / name)
    exporter = work / "exporter.py"
    exporter.write_text(
        "import subprocess, sys\n"
        "paths = sys.stdin.buffer.read()\n"
        "result = subprocess.run(sys.argv[1:], input=paths, stdout=subprocess.PIPE)\n"
        "data = result.stdout\n"
        "if b'/failing/' in paths:\n"
        "    data = data.replace(b' content for ', b' damaged for ')\n"
        "sys.stdout.buffer.write(data)\n"
        "sys.exit(result.returncode)\n",
        encoding="utf-8",
    )
    env = os.environ.copy()
    env["CVS_FAST_EXPORT"] = " ".join(
        shlex.quote(str(arg)) for arg in (sys.executable, exporter, root / "cvs-fast-export")
    )
    for names, expected in (
        (("failing",), 1),
        (("failing", "passing"), 1),
        (("passing", "failing"), 1),
        (("passing",), 0),
    ):
        result = subprocess.run(
            [str(root / "cvsconvert"), "-q", "-n"] + list(names),
            cwd=work, env=env, capture_output=True, text=True, check=False,
        )
        if result.returncode != expected:
            raise AssertionError("%s exited %d, expected %d\n%s" % (
                names, result.returncode, expected, result.stderr))
        if expected and "are different" not in result.stderr:
            raise AssertionError("content mismatch was not reported")
        if "passing" in names and "passing conversion compares clean" not in result.stdout:
            raise AssertionError("successful conversion was not checked")
        for name in names:
            for suffix in ("-module-git", "-module.checkout"):
                if (work / (name + suffix)).exists():
                    raise AssertionError("conversion cleanup was skipped")


def main():
    root = Path(__file__).resolve().parent.parent
    cvsconvert = root / "cvsconvert"
    cvssync = root / "cvssync"
    cvsstrip = root / "cvsstrip"

    assert_shell_free(cvsconvert)
    assert_shell_free(cvssync)

    namespace = load_cvsconvert(root)
    assert_checked_references(namespace)

    with tempfile.TemporaryDirectory(prefix="cfe-companion-") as temp:
        temp_path = Path(temp)
        assert_cli_exit_status(root, temp_path)
        assert_content_comparisons(root, temp_path, namespace)
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
