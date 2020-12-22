#!/bin/bash

cd $(dirname $0)

[ $# -lt 8 ] && echo "$0 <cluster_id> <cluster_name> <base_domain> <endpoint> <cookie> <ha_flag> <nworkers> <version> <lb_ip> <host_record>" && exit 1

cluster_id=$1
cluster_name=$2
base_domain=$3
endpoint=$4
cookie=$5
haflag=$6
nworkers=$7
version=$8
lb_ip=$9
host_rec=${10}
seq_max=100
cloud_user=rhel
public_ip=""
cat /etc/redhat-release | grep -q CentOS
[ $? -eq 0 ] && cloud_user=centos

#master_0_ip=""
#master_1_ip=""
#master_2_ip=""
#bstrap_ip=""
declare -a workers_ip
#declare -a workers_res_ip
#for i in $(seq 0 $seq_max); do
#    let suffix=$i+20
#    workers_ip[$i]="192.168.91.$suffix"
#done

function  get_lb_ip()
{
public_ip=$(grep '^IPADDR' /etc/sysconfig/network-scripts/ifcfg-eth0)
public_ip=${public_ip##*=}
echo "public_ip is $public_ip" >> /tmp/jinlings_ip.log
}

function setup_dns()
{
    instID=$(cat /var/lib/cloud/data/instance-id | cut -d'-' -f2)
    echo "insID IS $instID">> /tmp/jinlings.log
    count=0
    while [ -z "$public_ip" -a $count -lt 10 ]; do
        echo "instID is $instID" >> /tmp/jinlings.log
        echo "~~~~~~~+++++~~~~~~" >> /tmp/jinlings.log
        #data=$(curl -k -XPOST $endpoint/floatingips/assign --cookie "$cookie" --data "instance=$instID" --data "floatingIP=$lb_ip")
        #data=$(curl -k -XPOST $endpoint/floatingips/assign --cookie "$cookie" --data "instance=$instID")
        #working_dir=/tmp/inst-$instID
        #latest_dir=$working_dir/openstack/latest
        #public_ip=$(grep '^IPADDR' /etc/sysconfig/network-scripts/ifcfg-eth0)
        #data=$(cat $latest_dir/network_data.json)
        #echo $data >> /tmp/jinlings_data.log
        #echo "lb_ip is $lb_ip" >> /tmp/jinlings.log
        #LB_IP=$(jq  -r .networks[0].ip_address <<< $data)
        #public_ip=$LB_IP
        #public_ip=${public_ip##*=}
        #echo "public_ip is $public_ip" >> /tmp/jinlings_ip.log
        let count=$count+1
        sleep 1
    done
    #[ -z "$public_ip" ] && public_ip=192.168.91.8
    #[ -z "$public_ip" ] && public_ip=9.115.78.84
    dns_server=$(grep '^nameserver' /etc/resolv.conf | head -1 | awk '{print $2}')
    if [ -z "$dns_server" -o "$dns_server" = "127.0.0.1" ]; then
        dns_server=8.8.8.8
    fi
    #dns_server=9.115.78.223
    [ -n "$host_rec" ] && host_rec="$(echo $host_rec | tr ':' ' ')"
    yum install -y dnsmasq
    cp /etc/dnsmasq.conf /etc/dnsmasq.conf.bak
    cat > /etc/dnsmasq.conf <<EOF
no-resolv
server=$dns_server
local=/${cluster_name}.${base_domain}/
address=/apps.${cluster_name}.${base_domain}/$public_ip
srv-host=_etcd-server-ssl._tcp.${cluster_name}.${base_domain},etcd-0.${cluster_name}.${base_domain},2380,0,10
EOF
    if [ "$haflag" = "yes" ]; then
        cat >> /etc/dnsmasq.conf <<EOF
srv-host=_etcd-server-ssl._tcp.${cluster_name}.${base_domain},etcd-1.${cluster_name}.${base_domain},2380,0,10
srv-host=_etcd-server-ssl._tcp.${cluster_name}.${base_domain},etcd-2.${cluster_name}.${base_domain},2380,0,10
EOF
    fi
    cat >> /etc/dnsmasq.conf <<EOF
no-hosts
addn-hosts=/etc/dnsmasq.openshift.addnhosts
conf-dir=/etc/dnsmasq.d,.rpmnew,.rpmsave,.rpmorig
EOF

    cat > /etc/dnsmasq.openshift.addnhosts <<EOF
$host_rec
$public_ip dns.${cluster_name}.${base_domain}
$public_ip loadbalancer.${cluster_name}.${base_domain}  api.${cluster_name}.${base_domain}  lb.${cluster_name}.${base_domain}
$public_ip api-int.${cluster_name}.${base_domain}
$bstrap_ip bootstrap.${cluster_name}.${base_domain}
$master_0_ip master-0.${cluster_name}.${base_domain}  etcd-0.${cluster_name}.${base_domain}
$master_1_ip master-1.${cluster_name}.${base_domain}  etcd-1.${cluster_name}.${base_domain}
$master_2_ip master-2.${cluster_name}.${base_domain}  etcd-2.${cluster_name}.${base_domain}
EOF

    for i in $(seq 0 $seq_max); do  
       if [  -n "${workers_ip[$i]}" ]; then
            cat >> /etc/dnsmasq.openshift.addnhosts <<EOF
${workers_ip[$i]} worker-$i.${cluster_name}.${base_domain}
EOF
       fi
    done

    echo -e "nameserver 127.0.0.1\nsearch ${cluster_name}.${base_domain}" > /etc/resolv.conf
    systemctl restart dnsmasq
    systemctl enable dnsmasq
}

function setup_lb()
{
    yum install -y haproxy
    haconf=/etc/haproxy/haproxy.cfg
    cp $haconf ${haconf}.bak
    cat > $haconf <<EOF
global
    log         127.0.0.1 local2 info
    chroot      /var/lib/haproxy
    pidfile     /var/run/haproxy.pid
    maxconn     4000
    user        haproxy
    group       haproxy
    daemon

defaults
    timeout connect         5s
    timeout client          30s
    timeout server          30s
    log                     global

frontend kubernetes_api
    bind 0.0.0.0:6443
    default_backend kubernetes_api

frontend machine_config
    bind 0.0.0.0:22623
    default_backend machine_config

frontend router_https
    bind 0.0.0.0:443
    default_backend router_https

frontend router_http
    mode http
    option httplog
    bind 0.0.0.0:80
    default_backend router_http

backend kubernetes_api
    balance roundrobin
    option ssl-hello-chk
    server bootstrap bootstrap.${cluster_name}.${base_domain}:6443 check
    server master-0 master-0.${cluster_name}.${base_domain}:6443 check
    server master-1 master-1.${cluster_name}.${base_domain}:6443 check
    server master-2 master-2.${cluster_name}.${base_domain}:6443 check

backend machine_config
    balance roundrobin
    option ssl-hello-chk
    server bootstrap bootstrap.${cluster_name}.${base_domain}:22623 check
    server master-0 master-0.${cluster_name}.${base_domain}:22623 check
    server master-1 master-1.${cluster_name}.${base_domain}:22623 check
    server master-2 master-2.${cluster_name}.${base_domain}:22623 check

backend router_https
    balance roundrobin
    option ssl-hello-chk
EOF

    for i in $(seq 0 $seq_max); do
        if [ $i -lt $nworkers ]; then
            cat >> $haconf <<EOF
    server worker-$i worker-$i.${cluster_name}.${base_domain}:443 check
EOF
        fi
    done
    cat >> $haconf <<EOF

backend router_http

EOF
    for i in $(seq 0 $seq_max); do
        if [ $i -lt $nworkers ]; then
            cat >> $haconf <<EOF
    server worker-$i worker-$i.${cluster_name}.${base_domain}:80 check
EOF

        fi
    done

    systemctl restart haproxy
    systemctl enable haproxy
}

function setup_nginx()
{
    yum install -y nginx
    cp /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak
    cat > /etc/nginx/nginx.conf <<EOF
user nginx;
worker_processes auto;
error_log /var/log/nginx/error.log;
pid /run/nginx.pid;

# Load dynamic modules. See /usr/share/nginx/README.dynamic.
include /usr/share/nginx/modules/*.conf;

events {
    worker_connections 1024;
}

http {
    log_format  main  '\$remote_addr - \$remote_user [\$time_local] "\$request" '
                      '\$status \$body_bytes_sent "\$http_referer" '
                      '"\$http_user_agent" "\$http_x_forwarded_for"';

    access_log  /var/log/nginx/access.log  main;

    sendfile            on;
    tcp_nopush          on;
    tcp_nodelay         on;
    keepalive_timeout   65;
    types_hash_max_size 2048;

    include             /etc/nginx/mime.types;
    default_type        application/octet-stream;

    # Load modular configuration files from the /etc/nginx/conf.d directory.
    # See http://nginx.org/en/docs/ngx_core_module.html#include
    # for more information.
    include /etc/nginx/conf.d/*.conf;

    server {
        listen       8080 default_server;
        listen       [::]:8080 default_server;
        server_name  _;
        root         /usr/share/nginx/html;

        # Load configuration files for the default server block.
        include /etc/nginx/default.d/*.conf;

        location / {
        }

        error_page 404 /404.html;
            location = /40x.html {
        }

        error_page 500 502 503 504 /50x.html;
            location = /50x.html {
        }
    }
}
EOF

    systemctl restart nginx
    systemctl enable nginx
}

function download_pkgs()
{
    cd /opt
    conf_url=$endpoint/misc/openshift/ocd.conf
    [ -n "$version" ] && conf_url=${conf_url}.${version}
    wget --no-check-certificate $conf_url -O ocd.conf
    source ocd.conf
    wget --no-check-certificate -O /usr/share/nginx/html/rhcos.raw.gz $coreos_image_url
    wget --no-check-certificate -O openshift-install-linux.tgz $openshift_installer
    wget --no-check-certificate -O openshift-client-linux.tgz $openshift_client
    tar -zxf openshift-install-linux.tgz
    tar -zxf openshift-client-linux.tgz
    cp kubectl oc /usr/bin/
}

function ignite_files()
{
    echo "~~~~~~~~~+++++~~~~~~~~~"
    #pwd
    parts=$(cat | base64 -d | sed -s 's/\r//')
    ssh_key=$(cat /home/$cloud_user/.ssh/authorized_keys | tail -1)
    rm -rf $cluster_name
    mkdir $cluster_name
    #cd $cluster_name
    mreplica=1
    [ "$haflag" = "yes" ] && mreplica=3
    cat > install-config.yaml <<EOF
apiVersion: v1
baseDomain: $base_domain
compute:
- hyperthreading: Enabled
  name: worker
  replicas: 0
controlPlane:
  hyperthreading: Enabled
  name: master
  replicas: $mreplica
metadata:
  name: $cluster_name
networking:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  networkType: OpenShiftSDN
  serviceNetwork:
  - 172.30.0.0/16
platform:
  none: {}
fips: false
sshKey: '$ssh_key'
$parts
EOF
    sed -i "/^$/d" install-config.yaml
    mkdir /opt/backup
    cp install-config.yaml /opt/backup
    cd /opt/$cluster_name
    echo " create manifests now" >> /tmp/jinlings.log
    ../openshift-install create manifests
    sed -i "s/mastersSchedulable: true/mastersSchedulable: false/" manifests/cluster-scheduler-02-config.yml
    echo " manifests running completed "  >> /tmp/jinlings.log
    cp -rf ../$cluster_name /opt/backup
    pwd
    echo " starting to create ignition-configs file " >> /tmp/jinlings.log
    ../openshift-install create ignition-configs
    echo " ignition-configs file completed" >> /tmp/jinlings.log
    cp -rf ../$cluster_name /opt/backup
    ignite_dir=/usr/share/nginx/html/ignition
    rm -rf $ignite_dir
    mkdir $ignite_dir
    cp *.ign $ignite_dir
    chmod a+r $ignite_dir/*
    echo "copy ignition file completed " >> /tmp/jinlings.log
    cat >>/root/.bashrc <<EOF
export KUBECONFIG=/opt/$cluster_name/auth/kubeconfig
export PS1='[\u@\h.$cluster_name \w]\\$ '
EOF
    cat >>/home/$cloud_user/.bashrc <<EOF
export PS1='[\u@\h.$cluster_name \w]\\$ '
EOF
}

function setup_nfs_pv()
{
    cd /opt/$cluster_name
    mkdir data
    chmod a+rw data
    yum -y install nfs-utils nfs-utils-lib
    systemctl start  rpcbind
    systemctl start nfs
    systemctl start  nfslock
    systemctl enable rpcbind
    systemctl enable nfs
    systemctl enable  nfslock
    cat >/etc/exports <<EOF
/opt/$cluster_name/data 192.168.91.0/24(rw,sync,no_root_squash,no_subtree_check,insecure)
EOF
    exportfs -a

    cat >nfs-pv.yaml <<EOF
apiVersion: v1
kind: PersistentVolume
metadata:
  name: nfs-pv
spec:
  capacity:
    storage: 100Gi
  accessModes:
  - ReadWriteMany
  nfs:
    path: /opt/$cluster_name/data
    server: 192.168.91.8
  persistentVolumeReclaimPolicy: Recycle
EOF
    ../oc create -f nfs-pv.yaml
    ../oc patch configs.imageregistry/cluster --type merge --patch '{"spec":{"storage":{"pvc":{"claim":""}}}}'
    cd -
}

function create_storage()
{
    cd /opt/$cluster_name
    cat >storage.yaml <<EOF
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: gp2
provisioner: kubernetes.io/glusterfs
parameters:
  resturl: 'http://192.168.91.199:8080'
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF
    ../oc create -f storage.yaml
    cd -
}

function set_autocsr()
{
    cat >/etc/cron.hourly/50autocsr <<EOF
#!/bin/bash
export KUBECONFIG=/opt/$cluster_name/auth/kubeconfig
/opt/oc get csr -ojson | jq -r '.items[] | select(.status == {} ) | .metadata.name' | xargs /opt/oc adm certificate approve
EOF
    chmod +x /etc/cron.hourly/50autocsr
}

function launch_cluster()
{
    if [ ! -d $cluster_name  ];then
      mkdir $cluster_name
    fi
    cd /opt/$cluster_name
    bstrap_res=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=bootstrap.${cluster_name}.${base_domain}&ipaddr=${public_ip}")
    date >> /tmp/jinlings.log
    echo $bstrap_res >> /tmp/jinlings.log
    bstrap_ID=$(jq -r .ID <<< $bstrap_res)
    bstrap_ip=$(jq -r .'Interfaces[0].Address.Address' <<< $bstrap_res)
    bstrap_ip=${bstrap_ip%/*}
    echo "~~~~~~~++++~~~~~~"
    curl -k -XPOST $endpoint/openshifts/$cluster_id/state --cookie $cookie --data "status=bootstrap"
    curl -k -XPOST $endpoint/openshifts/$cluster_id/state --cookie $cookie --data "status=masters"
    master_0_res=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=master-0.${cluster_name}.${base_domain}&ipaddr=${public_ip}")
    master_0_ip=$(jq -r .'Interfaces[0].Address.Address' <<< $master_0_res)
    master_0_ip=${master_0_ip%/*}
    echo " masterip is  $master_0_ip" >> /tmp/jinlings.log
    sleep 3
    if [ "$haflag" = "yes" ]; then
        master_1_res=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=master-1.${cluster_name}.${base_domain}&ipaddr=${public_ip}")
        master_1_ip=$(jq -r .'Interfaces[0].Address.Address' <<< $master_1_res)
        master_1_ip=${master_1_ip%/*}
        sleep 3
        master_2_res=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=master-2.${cluster_name}.${base_domain}&ipaddr=${public_ip}")
        master_2_ip=$( jq -r .'Interfaces[0].Address.Address' <<< $master_2_res)
        master_2_ip=${master_2_ip%/*}
        sleep 3
    fi
    curl -k -XPOST $endpoint/openshifts/$cluster_id/state --cookie $cookie --data "status=workers"
    workers_ip[0]=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=worker-0.${cluster_name}.${base_domain}&ipaddr=${public_ip}" | jq -r .'Interfaces[0].Address.Address')
    workers_ip[0]=${workers_ip[0]%/*}
    sleep 3
    workers_ip[1]=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=worker-1.${cluster_name}.${base_domain}&ipaddr=${public_ip}" | jq -r .'Interfaces[0].Address.Address')
    workers_ip[1]=${workers_ip[1]%/*}
    echo "~~~~~~~~+++++++~~~~~~~~"
    echo "workerip is $workers_ip[1]" >> /tmp/jinlings.log
    let more=$nworkers-2
    for i in $(seq 1 $more); do
        let index=$i+1
        let last=$index+20
        workers_ip[$index]=$(curl -k -XPOST $endpoint/openshifts/$cluster_id/launch --cookie $cookie --data "hostname=worker-$index.${cluster_name}.${base_domain}&ipaddr=${public_ip}" | jq -r .'Interfaces[0].Address.Address')
        workers_ip[$index]=${workers_ip[$index]%/*}
        sleep 3
    done
}

function wait_ocd()
{
    while true; do
        sleep 5
        nc -zv $bstrap_ip 6443
        [ $? -eq 0 ] && break
    done
    ../openshift-install wait-for bootstrap-complete --log-level debug
    echo "bootstrap-complete runnning completed " >> /tmp/jinlings.log
    curl -k -XDELETE $endpoint/instances/$bstrap_ID --cookie $cookie
    nodes=3
    [ "$haflag" = "yes" ] && nodes=5
    export KUBECONFIG=auth/kubeconfig
    while true; do
        ../oc get csr -ojson | jq -r '.items[] | select(.status == {} ) | .metadata.name' | xargs ../oc adm certificate approve
        sleep 5
        count=$(../oc get nodes | grep -c Ready)
        [ "$count" -ge "$nodes" ] && break
    done
    sleep 60
    while true; do
        sleep 5
        ../oc get csr -ojson | jq -r '.items[] | select(.status == {} ) | .metadata.name' | xargs ../oc adm certificate approve
        ../oc get clusteroperators image-registry
        [ $? -eq 0 ] && break
    done
    #setup_nfs_pv
    ../openshift-install wait-for install-complete
    echo "install-complete" >>  /tmp/jinlings.log
    curl -k -XPOST $endpoint/openshifts/$cluster_id/state --cookie $cookie --data "status=complete"
    let nodes=$nodes+$more
    while true; do
        sleep 5
        ../oc get csr -ojson | jq -r '.items[] | select(.status == {} ) | .metadata.name' | xargs ../oc adm certificate approve
        count=$(../oc get nodes | grep -c Ready)
        [ "$count" -ge "$nodes" ] && break
    done
    set_autocsr
    # create_storage
}

setenforce Permissive
sed -i 's/^SELINUX=enforcing/SELINUX=permissive/' /etc/selinux/config
[ $(uname -m) != s390x ] && yum -y install epel-release
[ "$(uname -m)" = "s390x" ] && yum -y install rng-tools && systemctl start rngd
systemctl stop firewalld
systemctl disable firewalld
systemctl mask firewalld
yum -y install wget jq nc
download_pkgs
get_lb_ip
launch_cluster
setup_dns
setup_lb
setup_nginx
ignite_files
wait_ocd
