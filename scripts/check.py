"""Small, dependency-free CI checks. Run with Python 3."""

import os
from pathlib import Path
import subprocess
import sys


def check_format():
    files = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.go"]
    )
    paths = [os.fsdecode(path) for path in files.split(b"\0") if path]
    if not paths:
        raise ValueError("no Go files found")
    unformatted = []
    for path in paths:
        result = subprocess.check_output(["gofmt", "-l", path], text=True)
        if result.strip():
            unformatted.append(path)
    if unformatted:
        raise ValueError("run gofmt on: " + ", ".join(unformatted))
    print("All Go files are formatted.")


def check_coverage(profile, minimum):
    # -coverpkg can repeat each block in profiles from multiple test packages.
    # Count each unique block once, summing its execution counts across packages.
    blocks = {}
    lines = Path(profile).read_text(encoding="utf-8").splitlines()
    if not lines or not lines[0].startswith("mode: "):
        raise ValueError("invalid coverage profile header")
    for line in lines[1:]:
        location, statements, count = line.rsplit(" ", 2)
        statements, count = int(statements), int(count)
        if statements < 0 or count < 0:
            raise ValueError("negative statement or execution count")
        if location in blocks and blocks[location][0] != statements:
            raise ValueError("inconsistent statement counts for " + location)
        previous = blocks.get(location, (statements, 0))
        blocks[location] = (statements, previous[1] + count)
    total = sum(statements for statements, _ in blocks.values())
    covered = sum(statements for statements, count in blocks.values() if count)
    if not total:
        raise ValueError("coverage profile contains no statements")
    percent = 100 * covered / total
    message = f"Statement coverage: {percent:.2f}% ({covered}/{total}); required: {minimum:g}%"
    print(message)
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as output:
            output.write("## Test coverage\n\n" + message + "\n")
    if percent < minimum:
        raise ValueError("statement coverage is below the required threshold")


def main():
    if sys.argv[1:] == ["format"]:
        check_format()
    elif len(sys.argv) == 4 and sys.argv[1] == "coverage":
        minimum = float(sys.argv[3])
        if not 0 <= minimum <= 100:
            raise ValueError("coverage threshold must be between 0 and 100")
        check_coverage(sys.argv[2], minimum)
    else:
        raise ValueError("usage: check.py format | coverage PROFILE MINIMUM_PERCENT")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
