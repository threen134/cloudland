# -*- mode: sh -*-
# VNC password helpers of the scripts that define or start instances. Source it after ../cloudrc.
#
# Nothing here may print to stdout: every stdout line of a node script is sent to clapi as a callback.
#
# QEMU accepts a VNC password only when it was started with VNC password authentication, which libvirt does
# when the <graphics> element of the definition carries passwd. A domain started without one serves VNC
# without authentication until its next cold start, and set_vnc_passwd.sh can not give it a password. So
# every definition carries a random password; the console sets its own one-time password over it.

# A random VNC password (VNC authentication uses 8 characters)
function vnc_random_passwd()
{
    local pass
    pass=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom 2>/dev/null | head -c 8)
    [ ${#pass} -eq 8 ] || return 1
    echo "$pass"
}

# Give the VNC <graphics> of the domain XML file $1 the password $2 (a new random one by default), replacing
# the one it has. Nothing to do when the definition has no VNC graphics.
function vnc_xml_set_passwd()
{
    local xml=$1 pass=${2:-}
    grep -q "<graphics type=['\"]vnc['\"]" "$xml" || return 0
    if [ -z "$pass" ]; then
        pass=$(vnc_random_passwd) || return 1
    fi
    [[ "$pass" =~ ^[A-Za-z0-9]+$ ]] || return 1
    sed -i -E "/<graphics type=['\"]vnc['\"]/ { s/ passwd=(\"[^\"]*\"|'[^']*')//; s/<graphics type=(['\"])vnc(['\"])/<graphics type=\1vnc\2 passwd='$pass'/ }" "$xml"
}

# Give the VNC <graphics> of the domain XML file $1 a random password when it has none. The XML has to be
# dumped with --security-info, the password is left out otherwise.
function vnc_xml_ensure_passwd()
{
    local xml=$1
    grep "<graphics type=['\"]vnc['\"]" "$xml" | grep -q " passwd=" && return 0
    vnc_xml_set_passwd "$xml"
}

# Make sure the persistent definition of domain $1 carries a VNC password before it is started. Domains
# defined before the definitions carried one have none; this gives them one at their next cold start.
# Also called from the heartbeat (try_start_instance): every virsh call is bounded.
function vnc_ensure_domain_passwd()
{
    local dom=$1 graphics pass dev_xml rc
    graphics=$(timeout 30 virsh dumpxml --inactive --security-info "$dom" 2>/dev/null | grep -o "<graphics type='vnc'[^>]*>")
    [ -z "$graphics" ] && return 0
    grep -q " passwd=" <<< "$graphics" && return 0
    pass=$(vnc_random_passwd) || return 1
    dev_xml=$(mktemp) || return 1
    echo "<graphics type='vnc' listen='0.0.0.0' passwd='$pass'/>" > "$dev_xml"
    timeout 30 virsh update-device "$dom" "$dev_xml" --config >/dev/null 2>&1
    rc=$?
    rm -f "$dev_xml"
    [ $rc -eq 0 ] || log_debug "$dom" "vnc_ensure_domain_passwd: failed to give $dom a VNC password"
    return $rc
}
