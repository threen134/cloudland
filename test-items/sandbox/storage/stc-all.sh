#!/bin/bash
# Runs every sandbox test of the storage node scripts in turn (the scripts installed by stc-install.sh first) and
# prints the summary line of each. stc-test10-memlow.sh changes cgroups of the WSL machine and is left out
D="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash $D/stc-install.sh >/dev/null 2>&1
for t in stc-test1 stc-test2 stc-test3 stc-test4 stc-test5 stc-test6 stc-test7 stc-test8 stc-test9 stc-test9-oom stc-test11 stc-test12 \
    stc-test13 stc-test14 stc-test15 stc-test16 stc-test17 stc-test18; do
    sed 's/\r$//' $D/$t.sh >/tmp/$t.sh
    out=$(timeout 900 bash /tmp/$t.sh 2>&1)
    summary=$(grep -E "passed|failed" <<<"$out" | tail -1)
    fails=$(grep -E "^FAIL" <<<"$out" | head -5)
    echo "$t: ${summary:-no summary}"
    [ -n "$fails" ] && echo "$fails" | sed 's/^/    /'
done
