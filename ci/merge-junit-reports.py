#!/usr/bin/env python3
# Merge sharded e2e JUnit reports into a single deduplicated report.

import glob
import os
import shutil
import sys
from xml.etree import ElementTree as ET


def priority(testcase: ET.Element) -> int:
    """Higher = keep over lower-priority duplicates."""
    if testcase.find("failure") is not None or testcase.find("error") is not None:
        return 3
    if testcase.find("skipped") is not None:
        return 0
    return 2  # passed


def main() -> int:
    shard_files = sorted(glob.glob("e2e-report-shard-*/report/junit-report.xml"))
    if not shard_files:
        print("No e2e-report-shard-* reports found; nothing to merge.", file=sys.stderr)
        return 0

    by_name: dict[tuple[str, str], ET.Element] = {}
    template_suite: ET.Element | None = None

    for path in shard_files:
        tree = ET.parse(path)
        for suite in tree.getroot().iter("testsuite"):
            if template_suite is None:
                template_suite = suite
            for tc in suite.findall("testcase"):
                key = (tc.get("classname", ""), tc.get("name", ""))
                if key not in by_name or priority(tc) > priority(by_name[key]):
                    by_name[key] = tc

    if template_suite is None:
        print("No <testsuite> element found in any shard report.", file=sys.stderr)
        return 1

    out_root = ET.Element("testsuites")
    out_suite = ET.SubElement(out_root, "testsuite", attrib=dict(template_suite.attrib))

    tests = len(by_name)
    failures = sum(1 for tc in by_name.values() if tc.find("failure") is not None)
    errors = sum(1 for tc in by_name.values() if tc.find("error") is not None)
    skipped = sum(1 for tc in by_name.values() if tc.find("skipped") is not None)
    out_suite.set("tests", str(tests))
    out_suite.set("failures", str(failures))
    out_suite.set("errors", str(errors))
    out_suite.set("skipped", str(skipped))
    out_root.set("tests", str(tests))
    out_root.set("failures", str(failures))
    out_root.set("errors", str(errors))

    for tc in by_name.values():
        out_suite.append(tc)

    os.makedirs("e2e-report-merged/report", exist_ok=True)
    ET.ElementTree(out_root).write(
        "e2e-report-merged/report/junit-report.xml",
        encoding="utf-8",
        xml_declaration=True,
    )

    for d in glob.glob("e2e-report-shard-*"):
        shutil.rmtree(d)

    print(
        f"Merged {len(shard_files)} shard reports -> {tests} unique specs "
        f"(failures={failures}, errors={errors}, skipped={skipped})"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
