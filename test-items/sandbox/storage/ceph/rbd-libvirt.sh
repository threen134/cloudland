#!/bin/bash
# Manual check on a running sandbox cluster: a volume made by create_volume_shared.sh, attached to a TCG domain by
# attach_volume_shared.sh, grown online by resize_volume_shared.sh, detached, deleted by delete_volume_shared.sh.
# <cluster uuid> <pool uuid> <ceph pool>
cluster=$1; pool=$2; cpool=$3; vol=999
source /opt/cloudland/scripts/cloudrc
args=$(jq -cn --arg c "$cluster" --arg p "$pool" --arg cp "$cpool" --argjson v $vol \
    '{driver: "ceph_rbd", pool: $p, cluster: $c, conf: ("/etc/ceph/" + $c + ".conf"), user: "cloudland", secret_uuid: $c, ceph_pool: $cp,
      quota_bytes: 0, volume_id: $v, image: ("volume-" + ($v | tostring)), size_gb: 1, old_gb: 1, instance_id: 990001}')
rbd="rbd --conf /etc/ceph/$cluster.conf --id cloudland"
dom=inst-990001
cd /opt/cloudland/scripts/backend
echo "CREATE: $(./create_volume_shared.sh $vol x <<<"$args")"
virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom; mkdir -p $xml_dir/$dom
cat >$xml_dir/$dom/$dom.xml <<'XML'
<domain type='qemu'><name>inst-990001</name><memory unit='MiB'>96</memory><vcpu>1</vcpu>
<os><type arch='x86_64' machine='q35'>hvm</type></os><devices><emulator>/usr/bin/qemu-system-x86_64</emulator></devices></domain>
XML
virsh define $xml_dir/$dom/$dom.xml >/dev/null && virsh start $dom || { echo "DOMAIN FAILED"; exit 1; }
echo "ATTACH: $(./attach_volume_shared.sh 990001 $vol x <<<"$args")"
cat $xml_dir/$dom/disk-$vol.xml
echo "BLK: $(virsh domblklist $dom --details | awk '$1 == "network" {print $3, $4}')"
echo "WATCHERS: $($rbd status --format json $cpool/volume-$vol | jq '.watchers | length')"
args2=$(jq -c '.size_gb = 2' <<<"$args")
echo "RESIZE: $(./resize_volume_shared.sh $vol x <<<"$args2")"
echo "CAPACITY: $(virsh domblkinfo $dom vdb | awk '/Capacity/ {print $2}') $($rbd info --format json $cpool/volume-$vol | jq .size)"
echo "DELETE OPEN: $(./delete_volume_shared.sh $vol x <<<"$args")"
virsh detach-device $dom $xml_dir/$dom/disk-$vol.xml --live --config >/dev/null; sleep 3
echo "AFTER: $($rbd status --format json $cpool/volume-$vol | jq '.watchers | length')"
virsh destroy $dom >/dev/null 2>&1; virsh undefine $dom >/dev/null 2>&1; rm -rf $xml_dir/$dom
echo "DELETE: $(./delete_volume_shared.sh $vol x <<<"$args")"
echo "LEFT: $($rbd ls -p $cpool | wc -l)"
