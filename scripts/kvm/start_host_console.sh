#!/bin/bash

# Expose a root shell of this node on a one-time TCP port for the console proxy (host console, system admins only).
# The shell (host_console_shell.sh) runs on a pty behind console_bridge.sh, which accepts a single connection
# from the control plane only, ends the session after idle_seconds without traffic and removes its firewall rules.
# stdout is the callback protocol: print nothing but the |:-COMMAND-:| line

cd $(dirname $0)
source ../cloudrc

[ $# -lt 3 ] && die "$0 <session> <operator> <idle_seconds> [console_proxy_source] [rows] [cols]"

# Chosen by the clapi request waiting for this console, returned in the callback
session=$1
operator=$2
idle_timeout=$3
proxy_source=$4
# Initial terminal size, 0 for the default
rows=${5:-0}
cols=${6:-0}
[[ "$session" =~ ^[0-9a-f]{16,64}$ ]] || die "Invalid session"
[[ "$operator" =~ ^[A-Za-z0-9._@-]{1,64}$ ]] || die "Invalid operator"
[[ "$idle_timeout" =~ ^[0-9]{1,6}$ ]] && [ "$idle_timeout" -gt 0 ] || die "Invalid idle timeout"
[[ "$rows" =~ ^[0-9]{1,3}$ ]] && [[ "$cols" =~ ^[0-9]{1,4}$ ]] || die "Invalid terminal size"
command -v script >/dev/null || die "script is not installed"
console_bridge_addrs "$proxy_source"

# Session records: terminal output only, readable by root only
record_dir=$log_dir/host_console
mkdir -p $record_dir
chmod 700 $record_dir
find $record_dir -name '*.log' -mtime +180 -delete 2>/dev/null
record=$record_dir/$(date -u +%Y%m%dT%H%M%SZ)-$operator-${session:0:8}.log

# socat splits the EXEC command line on spaces; no argument contains spaces, commas or colons
address="EXEC:/opt/cloudland/scripts/backend/host_console_shell.sh $record $operator $rows $cols,pty,setsid,ctty,stderr,sane"
port=$(start_console_bridge host-${session:0:16} $idle_timeout "$address" $log_dir/host_console.log) || \
    die "Unable to start the host console bridge"
log_debug host-console "host console for $operator on $console_listen_ip:$port, record $record"
echo "|:-COMMAND-:| $(basename $0) '$session' '$port' '$console_listen_ip'"
