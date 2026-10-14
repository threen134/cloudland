#!/usr/bin/env python3
"""Check that every externally reachable API route is referenced by a regression test case.

External routes are the ones served by cpgateway: its own routes (cpgateway/src/apis/routes.go)
plus the proxy whitelist forwarded to clapi (cpgateway/src/apis/proxy_routes.go). clapi routes
that are not on the whitelist are not reachable from outside and are not checked.

A route counts as covered when some test-items/*.md file (except README.md) or the tracking CSV mentions its method
followed by its path. Path parameters may be written as :id, {id}, <id>, $ID or a literal value,
and the /api/v1 prefix is optional.

Two lists next to this script:
  route_coverage_exempt.txt    routes deliberately not tracked, one "METHOD PATH reason" per line
  route_coverage_baseline.txt  known gaps (routes without a test case yet); remove an entry once
                               a test case covers it. --update-baseline rewrites it.

It also checks the tracker's 设计依据 column (every reference must name a design document section
or a code path that exists) and the generated "回归用例对照" tables in the design documents the
tracker refers to: they must match the tracker. --update-design-maps regenerates those tables.

And every whitelist entry must be forwarded to a route clapi registers (api/src/apis/routes.go);
known mismatches are listed in proxy_unrouted_baseline.txt, one "METHOD PATH reason" per line.

Exit status 1 when a route is neither covered, exempt nor in the baseline (a new route without a
test case), when either list names a route that no longer exists or a baseline route that is
now covered, or on a bad design reference or an out-of-date regression map. With --report the
full table is printed and the exit status is always 0.
"""

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
GW_ROUTES = ROOT / "cpgateway/src/apis/routes.go"
PROXY_ROUTES = ROOT / "cpgateway/src/apis/proxy_routes.go"
TEST_DIR = ROOT / "test-items"
CLAPI_ROUTES = ROOT / "api/src/apis/routes.go"
UNROUTED = TEST_DIR / "scripts/proxy_unrouted_baseline.txt"
EXEMPT = TEST_DIR / "scripts/route_coverage_exempt.txt"
BASELINE = TEST_DIR / "scripts/route_coverage_baseline.txt"

METHODS = ("GET", "POST", "PUT", "PATCH", "DELETE")


GW_SOURCES = ROOT / "cpgateway"

# Any call that registers a gin route. Only the forms gateway_routes understands are allowed;
# anything else fails the check instead of being skipped, so a route cannot slip past the gate.
REGISTER_RE = re.compile(r"\b(\w+)\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Handle|Any|Match)\(")
# A gin route registration in any file: method call with a "/path" literal, or Handle("METHOD", ...)
ROUTE_SHAPE_RE = re.compile(
    r'\.(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any)\(\s*"/|\.Handle\(\s*"(?:GET|POST|PUT|PATCH|DELETE)"|\.Match\('
)
# The proxy whitelist loop in routes.go: its routes come from proxy_routes.go
PROXY_LOOP = "group.Handle(route.Method, route.Path, proxyHandler(route))"


class UnparsedRoute(Exception):
    pass


def gateway_routes():
    """Routes registered in routes.go, with their group prefixes resolved."""
    text = GW_ROUTES.read_text(encoding="utf-8")
    groups = {"r": ""}
    group_re = re.compile(r'(\w+)\s*:?=\s*(\w+)\.Group\("([^"]*)"')
    for name, parent, prefix in group_re.findall(text):
        if parent not in groups:
            raise UnparsedRoute(f"routes.go: group {name} derives from unknown group {parent}")
        groups[name] = groups[parent] + prefix
    routes = set()
    call_re = re.compile(r'^\s*(\w+)\.(%s)\("([^"]*)"' % "|".join(METHODS))
    for n, line in enumerate(text.splitlines(), 1):
        if not REGISTER_RE.search(line) or line.strip().startswith("//"):
            continue
        if line.strip() == PROXY_LOOP:
            continue
        m = call_re.match(line)
        if not m or m.group(1) not in groups:
            raise UnparsedRoute(f"routes.go:{n}: unrecognized route registration: {line.strip()}")
        var, method, path = m.groups()
        full = groups[var] + path
        if not full.startswith("/api/v1"):
            continue  # "/" and "/health" are not API routes
        routes.add((method, full[len("/api/v1"):] or "/"))
    return routes


def check_no_other_registrations():
    """Routes must be registered in routes.go (or be whitelist entries); fail on any elsewhere."""
    for p in sorted(GW_SOURCES.rglob("*.go")):
        if p.name.endswith("_test.go") or p == GW_ROUTES:
            continue
        for n, line in enumerate(p.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
            if not line.strip().startswith("//") and ROUTE_SHAPE_RE.search(line):
                rel = p.relative_to(ROOT).as_posix()
                raise UnparsedRoute(f"{rel}:{n}: route registered outside routes.go: {line.strip()}")


def proxy_routes():
    text = PROXY_ROUTES.read_text(encoding="utf-8")
    entry_re = re.compile(r'\{"(%s)",\s*"([^"]+)"' % "|".join(METHODS))
    return set(entry_re.findall(text))


def proxy_targets():
    """Whitelist entries with the clapi path they are forwarded to: {(method, gateway path): target}."""
    text = PROXY_ROUTES.read_text(encoding="utf-8")
    entry_re = re.compile(r'\{"(%s)",\s*"([^"]+)",\s*"([^"]+)"' % "|".join(METHODS))
    return {(m, p): t for m, p, t in entry_re.findall(text)}


def clapi_routes():
    """Routes clapi registers in its routes.go, parameters normalized to ':'."""
    text = CLAPI_ROUTES.read_text(encoding="utf-8")
    consts = dict(re.findall(r'(\w+)\s*:=\s*"(/[^"]*)"', text))
    groups = {"r": ""}
    for name, parent, arg in re.findall(r'(\w+)\s*:=\s*(\w+)\.Group\(("[^"]*"|\w+)\)', text):
        if parent not in groups:
            raise UnparsedRoute(f"api routes.go: group {name} derives from unknown group {parent}")
        prefix = arg.strip('"') if arg.startswith('"') else consts.get(arg)
        if prefix is None:
            raise UnparsedRoute(f"api routes.go: group {name} uses unknown prefix {arg}")
        groups[name] = groups[parent] + prefix
    routes = set()
    for var, method, path in re.findall(r'\b(\w+)\.(%s)\("([^"]*)"' % "|".join(METHODS), text):
        if var not in groups:
            continue
        full = groups[var] + path
        if full.startswith("/api/v1"):
            routes.add((method, normalize(full[len("/api/v1"):])))
    return routes


def normalize(path):
    return re.sub(r"\{[^/}]+\}|:[^/]+", ":", path)


def check_proxy_targets():
    """Every whitelist entry must reach a route clapi registers, otherwise it answers 404 (or lands on
    a sibling such as /instances/:id). Known mismatches are listed in proxy_unrouted_baseline.txt."""
    known = load_list(UNROUTED, need_reason=True)
    targets = proxy_targets()
    served = clapi_routes()
    problems = []
    for (m, p), t in sorted(targets.items()):
        missing = (m, normalize(t)) not in served
        if missing and (m, p) not in known:
            problems.append(f"{m} {p}: forwarded to {t}, which clapi does not register")
        if not missing and (m, p) in known:
            problems.append(f"{m} {p}: clapi registers it now, remove it from {UNROUTED.name}")
    for r in known:
        if r not in targets:
            problems.append(f"{r[0]} {r[1]}: not on the whitelist, remove it from {UNROUTED.name}")
    return problems


def is_param(seg):
    return seg[:1] in (":", "*")


def path_shape(segs):
    """Segments with every parameter replaced by ':' (siblings share a shape prefix)."""
    return tuple(":" if is_param(s) else s for s in segs)


def literal_siblings(routes):
    """For each shape prefix, the literal segments routes use right after it.

    POST /floating_ips/batch_attach and POST /floating_ips/:id share the prefix (floating_ips,);
    a test case naming batch_attach must not count as covering :id.
    """
    siblings = {}
    for _, path in routes:
        segs = path.strip("/").split("/") if path.strip("/") else []
        for i, s in enumerate(segs):
            if not is_param(s):
                siblings.setdefault(path_shape(segs[:i]), set()).add(s)
    return siblings


PARAM_CHARS = r"[^/\s`|)'\"]+"


def route_regex(method, path, siblings=None):
    segs = path.strip("/").split("/") if path.strip("/") else []
    pats = []
    for i, s in enumerate(segs):
        if not is_param(s):
            pats.append(re.escape(s))
            continue
        taken = sorted((siblings or {}).get(path_shape(segs[:i]), ()), key=len, reverse=True)
        if taken:
            # a parameter is any value except a sibling route's literal segment at this position
            exclude = "|".join(re.escape(t) for t in taken)
            pats.append(r"(?!(?:%s)(?:[/\s`|)'\"?#]|$))%s" % (exclude, PARAM_CHARS))
        else:
            pats.append(PARAM_CHARS)
    alts = [r"(?:https?://\S*?)?(?:\$\{?\w+\}?)?(?:/api/v1)?/" + "/".join(pats)]
    # abbreviated forms such as ".../connections/:conn_id/restart": a suffix that still names
    # at least one literal segment
    for k in range(1, len(segs)):
        tail = segs[k:]
        if all(is_param(s) for s in tail):
            continue
        alts.append(r"(?:\.\.\.|…)/" + "/".join(pats[k:]))
    # method, a few separator characters (space, backtick, quote, table bar), the path, then
    # something that does not continue the path
    return re.compile(
        r"\b%s[\s`|*\"']{1,6}(?:%s)(?![A-Za-z0-9_\-]|/[A-Za-z0-9_:{<$])" % (method, "|".join(alts))
    )


PLAN_DIR = ROOT / "docs/architecture/plan"
TRACKER = TEST_DIR / "回归功能追踪.csv"
HEADING_RE = re.compile(r"^#{1,6}\s+(\d+(?:\.\d+)*)\.?\s+(.*?)\s*$")
DOC_REF_RE = re.compile(r"^(?:(?P<doc>[\w.-]+\.md)\s+)?§(?P<num>\d+(?:\.\d+)*)(?:\s+(?P<label>.+))?$")
CODE_REF_RE = re.compile(r"^代码：(?P<paths>.+?)(?:，无设计)?$")


def doc_sections(doc):
    """[(number, heading text, section text)] of a design document; numbers can repeat."""
    lines = (PLAN_DIR / doc).read_text(encoding="utf-8").splitlines()
    heads = [(i, HEADING_RE.match(l)) for i, l in enumerate(lines)]
    heads = [(i, m, len(lines[i]) - len(lines[i].lstrip("#"))) for i, m in heads if m]
    sections = []
    for k, (i, m, level) in enumerate(heads):
        end = next((j for j, _, lv in heads[k + 1 :] if lv <= level), len(lines))
        sections.append((m.group(1), m.group(2), "\n".join(lines[i:end])))
    return sections


def check_design_refs():
    """The 设计依据 column of the tracker must point at sections and code that exist.

    Formats, separated by "；": "<doc>.md §<n> <heading or entry>" (later items may omit the
    document, meaning the same one), or "代码：<path>[、<path>]，无设计". Section numbers repeat
    in some documents (a "3.1" in two chapters), so a label is required when the number is not
    unique, and the label must be part of the heading or appear in that section.
    """
    import csv

    problems = []
    cache = {}
    with open(TRACKER, encoding="utf-8-sig", newline="") as f:
        rows = list(csv.DictReader(f))
    if rows and "设计依据" not in rows[0]:
        return [f"{TRACKER.name}: no 设计依据 column"]
    for n, row in enumerate(rows, 2):
        ref = (row.get("设计依据") or "").strip()
        if not ref:
            continue
        where = f"{TRACKER.name} line {n} ({row['子功能']})"
        doc = None
        for item in (s.strip() for s in ref.split("；")):
            code = CODE_REF_RE.match(item)
            if code:
                for path in code.group("paths").split("、"):
                    if not (ROOT / path).exists():
                        problems.append(f"{where}: {path} does not exist")
                continue
            m = DOC_REF_RE.match(item)
            if not m:
                problems.append(f"{where}: cannot parse {item!r}")
                continue
            doc = m.group("doc") or doc
            if not doc:
                problems.append(f"{where}: {item!r} names no document")
                continue
            if doc not in cache:
                cache[doc] = doc_sections(doc) if (PLAN_DIR / doc).exists() else None
            if cache[doc] is None:
                problems.append(f"{where}: {doc} does not exist")
                continue
            cands = [s for s in cache[doc] if s[0] == m.group("num")]
            label = m.group("label")
            if not cands:
                problems.append(f"{where}: {doc} has no §{m.group('num')}")
            elif label:
                if not any(label in head or label in body for _, head, body in cands):
                    problems.append(f"{where}: {doc} §{m.group('num')} has nothing called {label!r}")
            elif len(cands) > 1:
                problems.append(f"{where}: {doc} §{m.group('num')} is ambiguous, add the heading")
    return problems


MAP_BEGIN = "<!-- regression-map:begin -->"
MAP_END = "<!-- regression-map:end -->"


def section_sort_key(ref):
    num = re.match(r"§(\d+(?:\.\d+)*)", ref).group(1)
    return [int(x) for x in num.split(".")], ref


def design_maps():
    """{doc: markdown table} built from the tracker's 设计依据 and 回归用例 columns."""
    import csv

    with open(TRACKER, encoding="utf-8-sig", newline="") as f:
        rows = list(csv.DictReader(f))
    by_doc = {}
    for row in rows:
        doc = None
        for item in (s.strip() for s in (row.get("设计依据") or "").split("；") if s.strip()):
            m = DOC_REF_RE.match(item)
            if not m:
                continue
            doc = m.group("doc") or doc
            ref = "§" + m.group("num") + (" " + m.group("label") if m.group("label") else "")
            feature = f"{row['功能域']} / {row['大功能']} / {row['子功能']}"
            by_doc.setdefault(doc, {}).setdefault(ref, []).append((feature, row["回归用例"]))
    tables = {}
    for doc, refs in by_doc.items():
        lines = [
            MAP_BEGIN,
            "",
            "> 由 `test-items/scripts/check_route_coverage.py --update-design-maps` 从回归追踪表"
            "（`test-items/回归功能追踪.csv` 的「设计依据」列）生成，**不要手改**：改追踪表后重新生成，"
            "CI 会检查两边是否一致。",
            "",
            "| 章节 | 功能（功能域 / 大功能 / 子功能） | 回归用例 |",
            "|---|---|---|",
        ]
        for ref in sorted(refs, key=section_sort_key):
            for feature, cases in refs[ref]:
                lines.append(f"| {ref} | {feature} | {cases} |")
        lines += ["", MAP_END]
        tables[doc] = "\n".join(lines)
    return tables


def sync_design_maps(write):
    """Check (or rewrite) the generated regression maps in the referenced design documents."""
    problems = []
    for doc, table in sorted(design_maps().items()):
        path = PLAN_DIR / doc
        if not path.exists():
            continue  # reported by check_design_refs
        raw = path.read_bytes().decode("utf-8")
        eol = "\r\n" if "\r\n" in raw else "\n"
        text = raw.replace("\r\n", "\n")
        if MAP_BEGIN not in text or MAP_END not in text:
            problems.append(f"{doc}: no regression map (add a section with {MAP_BEGIN} / {MAP_END})")
            continue
        start, end = text.index(MAP_BEGIN), text.index(MAP_END) + len(MAP_END)
        if text[start:end] == table:
            continue
        if write:
            path.write_bytes((text[:start] + table + text[end:]).replace("\n", eol).encode("utf-8"))
            print(f"updated the regression map in {doc}")
        else:
            problems.append(f"{doc}: regression map is out of date (run --update-design-maps)")
    return problems


def load_list(path, need_reason):
    entries = {}
    if not path.exists():
        return entries
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        fields = line.split(None, 2)
        if len(fields) < (3 if need_reason else 2):
            sys.exit(f"{path}: malformed entry {line!r}")
        entries[(fields[0], fields[1])] = fields[2] if len(fields) > 2 else ""
    return entries


BASELINE_HEADER = """\
# Known gaps: external routes that no regression test case mentions yet (METHOD PATH).
# Generated by check_route_coverage.py --update-baseline. Remove a line once a test case
# (test-items/TC-*.md) or the tracking CSV names the route as "METHOD /path"; the check fails
# while a covered route is still listed here. Do not add new routes here: write the test case.
"""


def corpus():
    texts = []
    # README.md describes the suite (and quotes example routes); only test case files count
    for p in sorted(TEST_DIR.glob("*.md")):
        if p.name == "README.md":
            continue
        texts.append(p.read_text(encoding="utf-8", errors="replace"))
    for p in sorted(TEST_DIR.glob("*.csv")):
        texts.append(p.read_text(encoding="utf-8-sig", errors="replace"))
    return "\n".join(texts)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--report", action="store_true", help="print the full table and always exit 0")
    ap.add_argument("--update-baseline", action="store_true", help="rewrite the known-gap list")
    ap.add_argument(
        "--update-design-maps", action="store_true",
        help="regenerate the regression maps in the design documents from the tracker",
    )
    args = ap.parse_args()

    if args.update_design_maps:
        problems = check_design_refs()
        for msg in problems:
            print(f"BAD-REF  {msg}")
        if problems:
            return 1
        errors = sync_design_maps(write=True)
        for msg in errors:
            print(f"ERROR    {msg}")
        return 1 if errors else 0

    try:
        routes = gateway_routes() | proxy_routes()
        check_no_other_registrations()
    except UnparsedRoute as e:
        print(f"ERROR {e}\n(teach check_route_coverage.py this form, or register the route in routes.go)")
        return 1
    siblings = literal_siblings(routes)
    exempt = load_list(EXEMPT, need_reason=True)
    baseline = load_list(BASELINE, need_reason=False)
    text = corpus()

    def order(r):
        return (r[1], r[0])

    covered, gaps = [], []
    for r in sorted(routes, key=order):
        if r in exempt:
            continue
        (covered if route_regex(*r, siblings).search(text) else gaps).append(r)

    if args.update_baseline:
        BASELINE.write_text(
            BASELINE_HEADER + "".join(f"{m} {p}\n" for m, p in gaps), encoding="utf-8", newline="\n"
        )
        print(f"wrote {len(gaps)} routes to {BASELINE.name}")
        return 0

    new = [r for r in gaps if r not in baseline]
    known = [r for r in gaps if r in baseline]
    problems = [(r, f"remove it from {EXEMPT.name}: no such route") for r in exempt if r not in routes]
    problems += [(r, f"remove it from {BASELINE.name}: no such route") for r in baseline if r not in routes]
    problems += [(r, f"remove it from {BASELINE.name}: now covered") for r in covered if r in baseline]

    if args.report:
        for m, p in covered:
            print(f"covered  {m:6} {p}")
        for m, p in known:
            print(f"gap      {m:6} {p}")
    for m, p in new:
        print(f"MISSING  {m:6} {p}  (new route without a test case)")
    for (m, p), why in sorted(problems, key=lambda x: order(x[0])):
        print(f"STALE    {m:6} {p}  ({why})")
    unrouted = check_proxy_targets()
    for msg in unrouted:
        print(f"UNROUTED {msg}")
    bad_refs = check_design_refs() + sync_design_maps(write=False)
    for msg in bad_refs:
        print(f"BAD-REF  {msg}")

    print(
        f"\n{len(routes)} external routes: {len(covered)} covered, {len(known)} known gaps, "
        f"{len(new)} new without test case, {len(exempt)} exempt; {len(bad_refs)} bad design references; "
        f"{len(unrouted)} whitelist entries clapi does not serve"
    )
    if args.report:
        return 0
    return 1 if new or problems or bad_refs or unrouted else 0


if __name__ == "__main__":
    sys.exit(main())
