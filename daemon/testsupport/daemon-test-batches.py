#!/usr/bin/env python3
"""Run the entire daemon suite in bounded processes, accounting for every test.

Only the canonical, unfiltered -count=1 gate uses this runner. Custom Go flags
continue through the original gate. Run from daemon/ under the aggregate RSS
wrapper; no task state or verification evidence is changed here.
"""

import concurrent.futures
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

MAX_WORKERS = 4
BATCHES = 8
WALL_SECONDS = 3000
TEST_NAME = re.compile(r"(?:Test|Example|Fuzz)\w+$")


def worker_count():
    # Each worker reserves one Go CPU. Four workers on a two-CPU runner
    # starve the real workflow progress/deadline tests inside each process.
    # Honor affinity where available, retaining the existing ceiling and
    # per-worker memory/parallelism limits on larger hosts.
    cpus = os.cpu_count() or 1
    try:
        cpus = min(cpus, len(os.sched_getaffinity(0)))
    except (AttributeError, OSError):
        pass
    return max(1, min(MAX_WORKERS, cpus))


def partition(names):
    if not names or len(set(names)) != len(names):
        raise ValueError("empty or duplicate test inventory")
    groups = [[] for _ in range(BATCHES)]
    for name in names:
        groups[int.from_bytes(hashlib.sha256(name.encode()).digest()[:4], "big") % BATCHES].append(name)
    return [group for group in groups if group]


def audit_events(lines, package, expected):
    outcomes = {}
    package_passed = False
    failures = []
    for line in lines:
        event = json.loads(line)
        if event.get("Package") != package:
            raise ValueError("unexpected package in batch output")
        action, name = event.get("Action"), event.get("Test")
        if action == "fail":
            failures.append(name or package)
        if name and "/" not in name and action in ("pass", "fail", "skip"):
            if name in outcomes:
                raise ValueError("duplicate test outcome: " + name)
            outcomes[name] = action
        if not name and action == "pass":
            package_passed = True
    missing = sorted(set(expected) - set(outcomes))
    extra = sorted(set(outcomes) - set(expected))
    return {
        "passed": package_passed and not failures and not missing and not extra,
        "expected": len(expected), "observed": len(outcomes),
        "skipped": sum(value == "skip" for value in outcomes.values()),
        "missing": missing, "unexpected": extra, "failures": failures,
    }


class Runner:
    def __init__(self, directory, race):
        self.directory = Path(directory)
        self.flags = ["-race"] if race else []
        self.binary = self.directory / "daemon.test"
        self.deadline = time.monotonic() + WALL_SECONDS
        self.lock = threading.RLock()
        self.children = set()
        self.stopping = False
        self.workers = worker_count()

    def stop(self):
        with self.lock:
            self.stopping = True
            for child in self.children:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass

    def command(self, args, output, worker=False, separate_stderr=False):
        env = dict(os.environ)
        if worker:
            # At most four processes retain aggregate GOMAXPROCS<=4,
            # parallel<=8 and GOMEMLIMIT<=2500MiB. Fewer visible CPUs reduce
            # workers without increasing any worker's resource budget.
            env.update(GOMAXPROCS="1", GOMEMLIMIT="625MiB")
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("full daemon gate exceeded its 50-minute wall budget")
        with open(output, "w") as stream, open(str(output) + ".stderr", "w") as diagnostics:
            with self.lock:
                if self.stopping:
                    raise RuntimeError("gate interrupted")
                child = subprocess.Popen(args, env=env, stdout=stream,
                                         stderr=diagnostics if separate_stderr else subprocess.STDOUT,
                                         start_new_session=True)
                self.children.add(child)
            try:
                while True:
                    try:
                        return child.wait(timeout=min(30, max(0.001, self.deadline - time.monotonic())))
                    except subprocess.TimeoutExpired as error:
                        if time.monotonic() >= self.deadline:
                            self.stop()
                            child.wait()
                            raise TimeoutError("full daemon gate exceeded its 50-minute wall budget") from error
                        # Keep workflow idle detection informed without renewing
                        # either deadline. Byte counts distinguish actual output
                        # growth from a compiler/test that remains silent.
                        print(f"daemon-test progress: {Path(output).name}; "
                              f"{Path(output).stat().st_size} output bytes; "
                              f"{math.ceil(self.deadline - time.monotonic())}s gate budget left", flush=True)
            finally:
                with self.lock:
                    self.children.discard(child)

    def checked_output(self, args, name):
        path = self.directory / name
        if self.command(args, path, separate_stderr=True):
            raise RuntimeError("command failed; see " + str(path) + " and its .stderr file")
        return path.read_text()

    def batch(self, index, package, names):
        path = self.directory / f"batch-{index}.jsonl"
        pattern = "^(" + "|".join(re.escape(name) for name in names) + ")$"
        if len(pattern.encode()) > 120000:
            raise ValueError("batch pattern exceeds safe argument length")
        timeout = max(1, math.floor(self.deadline - time.monotonic()))
        started = time.monotonic()
        status = self.command(["go", "tool", "test2json", "-p", package, "-t",
                               str(self.binary), "-test.v=test2json", "-test.count=1",
                               "-test.parallel=2", f"-test.timeout={timeout}s",
                               "-test.run", pattern], path, worker=True)
        with path.open() as stream:
            result = audit_events(stream, package, names)
        result.update(batch=index, exitCode=status, seconds=round(time.monotonic() - started, 2))
        result["passed"] = result["passed"] and status == 0
        if not result["passed"]:
            # Preserve actionable failure text for the normal verification
            # classifier; JSON evidence remains available alongside it.
            with path.open() as stream:
                for line in stream:
                    event = json.loads(line)
                    if event.get("Output"):
                        print(event["Output"], end="", flush=True)
        display = dict(result)
        for key in ("missing", "unexpected", "failures"):
            display[key + "Count"] = len(result[key])
            display[key] = result[key][:20]
        print("daemon-test batch: " + json.dumps(display), flush=True)
        return result

    def run(self):
        # This enumerates import paths, not release binaries. Avoid Go's VCS
        # stamping probe walking out of an isolated worktree into a parent
        # fixture repository. Test compilation and production builds are unchanged.
        package = self.checked_output(["go", "list", "-buildvcs=false", "."], "root-package.txt").strip()
        packages = self.checked_output(["go", "list", "-buildvcs=false", "./..."], "packages.txt").splitlines()
        if not package or package not in packages:
            raise ValueError("root package missing from full package inventory")
        # Compile and vet once before starting workers. Running `go test` in
        # each worker can duplicate expensive compilation under different
        # compiler concurrency settings, consuming the resources batching saves.
        self.checked_output(["go", "test", "-p", "2", *self.flags, "-c",
                             "-o", str(self.binary), "."], "compile.log")
        listing = self.checked_output([str(self.binary), "-test.list=.",
                                       "-test.timeout=50m"], "inventory.txt")
        names = [line for line in listing.splitlines() if TEST_NAME.fullmatch(line)]
        groups = partition(names)
        inventory = {"package": package, "packages": packages, "tests": names,
                     "batches": groups, "workers": self.workers, "wallSeconds": WALL_SECONDS}
        (self.directory / "inventory.json").write_text(json.dumps(inventory, indent=2))
        print(f"daemon-test inventory: {len(names)} tests/examples/fuzz targets; "
              f"{len(packages)} packages; {len(groups)} batches; {self.workers} workers", flush=True)
        # Other packages run in their original, unfiltered form, outside the
        # root workers so their concurrency does not add to the resource caps.
        others = [name for name in packages if name != package]
        if others:
            path = self.directory / "other-packages.log"
            status = self.command(["go", "test", "-p", "2", *self.flags,
                                   "-count=1", "-parallel=8", "-timeout=50m", *others], path)
            print(path.read_text(), end="", flush=True)
            if status:
                raise RuntimeError("non-root package tests failed")
        with concurrent.futures.ThreadPoolExecutor(max_workers=self.workers) as pool:
            futures = [pool.submit(self.batch, i + 1, package, group) for i, group in enumerate(groups)]
            results = [future.result() for future in futures]
        (self.directory / "results.json").write_text(json.dumps(results, indent=2))
        if not all(result["passed"] for result in results):
            raise RuntimeError("full suite failed or inventory coverage incomplete")
        print(f"daemon-test coverage: PASS ({len(names)} inventoried root tests; "
              f"{len(others)} additional packages)", flush=True)


def main():
    if sys.argv[1:] not in ([], ["-race"]):
        raise SystemExit("usage: daemon-test-batches.py [-race]")
    directory = tempfile.mkdtemp(prefix="solu-daemon-test-batches-")
    runner = Runner(directory, "-race" in sys.argv)

    def interrupted(signum, _frame):
        runner.stop()
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    try:
        runner.run()
    except Exception as error:
        print(f"FAIL: {error}; evidence: {directory}", file=sys.stderr, flush=True)
        return 1
    finally:
        runner.stop()
    shutil.rmtree(directory)
    return 0


if __name__ == "__main__":
    sys.exit(main())
