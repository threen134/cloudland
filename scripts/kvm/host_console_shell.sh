#!/bin/bash

# Shell of a host console session, run by console_bridge.sh on the pty of the connection (see start_host_console.sh).
# The terminal output is appended to the session record; keyboard input is not recorded, so passwords typed at
# prompts that do not echo stay out of it.

[ $# -lt 2 ] && exit 1
record=$1
operator=$2
rows=${3:-0}
cols=${4:-0}

umask 077
echo "=== host console of $(hostname) opened by $operator at $(date -u +%Y-%m-%dT%H:%M:%SZ) ===" >>"$record"
# script sizes its own pty after this one
[ "$rows" -gt 0 ] && [ "$cols" -gt 0 ] && stty rows $rows cols $cols
printf '\033[33mCloudLand host console on %s (root). This session is recorded.\033[0m\r\n' "$(hostname)"

cd /root
# A clean environment: the agent's (GRPC_AUTH_TOKEN, TRACEPARENT, NODE_ID) is not passed to the shell
exec env -i HOME=/root USER=root LOGNAME=root SHELL=/bin/bash TERM=xterm-256color LANG=C.UTF-8 \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    CLOUDLAND_CONSOLE_OPERATOR="$operator" \
    script -q -f -a "$record" -c "bash -l"
