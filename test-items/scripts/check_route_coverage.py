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

Exit status 1 when a route is neither covered, exempt nor in the baseline (a new route without a
test case), or when either list names a route that no longer exists or a baseline route that is
now covered. With --report the full table is printed and the exit status is always 0.
"""

import argparse
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
GW_ROUTES = ROOT / "cpgateway/src/apis/routes.go"
PROXY_ROUTES = ROOT / "cpgateway/src/apis/proxy_routes.go"
TEST_DIR = ROOT / "test-items"
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
    args = ap.parse_args()

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

    print(
        f"\n{len(routes)} external routes: {len(covered)} covered, {len(known)} known gaps, "
        f"{len(new)} new without test case, {len(exempt)} exempt"
    )
    if args.report:
        return 0
    return 1 if new or problems else 0


if __name__ == "__main__":
    sys.exit(main())
