#!/bin/bash
# sandbox tests of the Ceph node scripts changed for private registries and mon changes (shared-storage-design.md
# §8.1, §8.5): ceph_install.sh logs in to the registry before it pulls (the password only on stdin) and refuses an image
# of another registry; the bootstrap hands the login to cephadm in a file of mode 600 that is gone afterwards; the
# mon_addrs action reads the mons without their ports. Stubs stand in for apt-get, docker, cephadm and the cluster.
S=/opt/cloudland/scripts/kvm/storage
pass=0; fail=0
ok() { echo "PASS $*"; pass=$((pass+1)); }
ko() { echo "FAIL $*"; fail=$((fail+1)); }
uuid=cccccccc-1212-3434-5656-787878787878
fake=$(mktemp -d)
export FAKE=$fake

cat >$fake/apt-get <<'EOF'
#!/bin/bash
echo "apt-get $*" >>$FAKE/calls
EOF
cat >$fake/docker <<'EOF'
#!/bin/bash
case $1 in
    login) echo "docker $*" >>$FAKE/calls; cat >$FAKE/login-stdin ;;
    pull) echo "docker $*" >>$FAKE/calls ;;
    run) echo "ceph version 19.2.3 (abc) squid (stable)" ;;
    *) echo "docker $*" >>$FAKE/calls ;;
esac
EOF
cat >$fake/ceph <<'EOF'
#!/bin/bash
[ "$1" = --version ] && echo "ceph version 19.2.3 (abc) squid (stable)"
EOF
cat >$fake/cephadm <<'EOF'
#!/bin/bash
echo "cephadm $*" >>$FAKE/calls
for ((i = 1; i <= $#; i++)); do
    if [ "${!i}" = --registry-json ]; then j=$((i + 1)); f=${!j}; stat -c %a "$f" >$FAKE/regmode; cp "$f" $FAKE/regjson; fi
done
touch $FAKE/bootstrapped
EOF
chmod +x $fake/*

# run <script> <function> <input>: a function of a node script with the stubs, input on stdin and in $input
run() {
    local file=$1 fn=$2
    (
        cd $S
        export PATH=$fake:$PATH
        source <(sed -e '/^stc_run "\$@"$/d' -e 's/^cd \$(dirname \$0)$/:/' $file)
        stc_progress() { echo "PROGRESS $*"; }
        stc_result() { echo "RESULT $*"; }
        stc_fail() { echo "FAILED $*"; exit 1; }
        stc_lock() { :; }
        stc_keys_update() { :; }
        ceph_unit_order() { :; }
        ceph_unit_oom() { :; }
        getent() { return 0; }
        ceph_admin_ready() { [ -f $FAKE/bootstrapped ]; }
        ceph_admin() {
            case "$2 $3" in
                "mon dump") echo '{"mons":[{"public_addrs":{"addrvec":[{"type":"v2","addr":"10.9.0.1:3300"},{"type":"v1","addr":"10.9.0.1:6789"}]}},{"public_addrs":{"addrvec":[{"type":"v2","addr":"10.9.0.2:3300"}]}}]}' ;;
                *) echo "ceph_admin $*" >>$FAKE/calls ;;
            esac
        }
        input=$3
        fsid=$uuid
        if [ "$fn" = stc_main ]; then stc_main <<<"$3"; else "$fn"; fi
    )
}

# ---- install: the login before the pull ----
in='{"cluster_uuid":"'$uuid'","image":"reg.example.com:5000/ceph/ceph:v19.2.3","orch":true,"registry":{"url":"reg.example.com:5000","username":"cl","password":"p@ss w0rd"}}'
: >$fake/calls
out=$(run ceph_install.sh stc_main "$in")
grep -q '^RESULT .*"image":"reg.example.com:5000/ceph/ceph:v19.2.3"' <<<"$out" && ok "install with a registry" || ko "install: $out"
login=$(grep -n 'docker login' $fake/calls | cut -d: -f1); pull=$(grep -n 'docker pull' $fake/calls | cut -d: -f1)
[ -n "$login" ] && [ -n "$pull" ] && [ "$login" -lt "$pull" ] && ok "logged in before the pull" || ko "order: $(cat $fake/calls)"
grep -qx 'docker login reg.example.com:5000 -u cl --password-stdin' $fake/calls && ok "the user on the command line, not the password" || ko "login: $(grep login $fake/calls)"
[ "$(cat $fake/login-stdin)" = "p@ss w0rd" ] && ok "the password through stdin" || ko "stdin: $(cat $fake/login-stdin)"
! grep -q 'p@ss' $fake/calls && ok "the password is on no command line" || ko "password leaked: $(cat $fake/calls)"
# An image of another registry
out=$(run ceph_install.sh stc_main "${in/reg.example.com:5000\/ceph/quay.io\/ceph}")
grep -q '^FAILED the image quay.io/ceph/ceph:v19.2.3 is not from registry' <<<"$out" && ok "an image of another registry is refused" || ko "other registry: $out"
# No registry: no login
: >$fake/calls
out=$(run ceph_install.sh stc_main '{"cluster_uuid":"'$uuid'","image":"quay.io/ceph/ceph:v19.2.3","orch":true}')
grep -q '^RESULT' <<<"$out" && ! grep -q 'docker login' $fake/calls && ok "no registry, no login" || ko "no registry: $out / $(cat $fake/calls)"
# A client host: no pull, no login
: >$fake/calls
out=$(run ceph_install.sh stc_main '{"cluster_uuid":"'$uuid'","image":"","orch":false}')
grep -q '^RESULT {"version":"19.2.3","image":""}' <<<"$out" && ! grep -q docker $fake/calls && ok "a client host pulls nothing" || ko "client: $out"

# ---- bootstrap: the login to cephadm in a file ----
dir=/opt/cloudland/run/storage/$uuid
mkdir -p $dir
rm -f $dir/id_ed25519 $dir/id_ed25519.pub $fake/bootstrapped
ssh-keygen -q -t ed25519 -N '' -f $dir/id_ed25519 >/dev/null
binp='{"cluster_uuid":"'$uuid'","fsid":"'$uuid'","mon_ip":"10.9.0.1","hostname":"'$(hostname)'","labels":["mon","_admin"],"image":"reg.example.com:5000/ceph/ceph:v19.2.3","cluster_network":"","single_host":true,"registry":{"url":"reg.example.com:5000","username":"cl","password":"p@ss w0rd"}}'
: >$fake/calls
out=$(run ceph_cluster.sh do_bootstrap "$binp")
grep -q '^RESULT' <<<"$out" && ok "bootstrap with a registry" || ko "bootstrap: $out"
grep -q -- '--registry-json' $fake/calls && ! grep -q 'p@ss' $fake/calls && ok "the login goes in a file, not on the command line" || ko "bootstrap args: $(cat $fake/calls)"
[ "$(cat $fake/regmode)" = 600 ] && ok "the login file is mode 600" || ko "mode $(cat $fake/regmode)"
[ "$(jq -c . $fake/regjson)" = '{"url":"reg.example.com:5000","username":"cl","password":"p@ss w0rd"}' ] && ok "cephadm gets url, username, password" || ko "json $(cat $fake/regjson)"
[ ! -e $dir/registry.json ] && ok "the login file is gone" || ko "registry.json left"
# Without a registry: no login file
rm -f $fake/bootstrapped; : >$fake/calls
out=$(run ceph_cluster.sh do_bootstrap "$(jq -c 'del(.registry)' <<<"$binp")")
grep -q '^RESULT' <<<"$out" && ! grep -q -- '--registry-json' $fake/calls && ok "no registry, no login file" || ko "bootstrap without registry: $(cat $fake/calls)"

# ---- the mons as they are ----
out=$(run ceph_cluster.sh do_mon_addrs '{"cluster_uuid":"'$uuid'","fsid":"'$uuid'"}')
grep -q '^RESULT {"mon_addrs":\["10.9.0.1","10.9.0.2"\]}' <<<"$out" && ok "mon addresses without ports: $(grep RESULT <<<"$out")" || ko "mon_addrs: $out"
rm -f $fake/bootstrapped
out=$(run ceph_cluster.sh do_mon_addrs '{"cluster_uuid":"'$uuid'","fsid":"'$uuid'"}')
grep -q '^FAILED this host has no working admin configuration' <<<"$out" && ok "no admin configuration: refused" || ko "no admin: $out"

rm -rf $fake $dir
echo "== $pass passed, $fail failed"
