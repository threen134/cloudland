#!/bin/bash

# 创建一个带当前时间戳的日志文件
datetime=$(date +'%Y-%m-%d-%H:%M:%S')
logfile=/tmp/allinone-deploy-$datetime.log
echo "Install is in progress... Log file is $logfile"
exec &> >(tee $logfile)

# 设置 cloudland 根目录
cland_root_dir=/opt/cloudland
# 获取当前目录
cd $(dirname $0)
# 检查当前目录是否为 cloudland/deploy 目录
[ $PWD != "$cland_root_dir/deploy" ] && echo "Please clone cloudland into /opt" && exit 1
net_conf=$cland_root_dir/deploy/netconf.yml

# Create cland user 
useradd -m -s /bin/bash cland
echo 'cland ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/cland
# 改变 cland 用户的密码
chown -R cland.cland $cland_root_dir
# 初始化网络配置文件 netconf.yml（若不存在则从示例文件复制）；
mkdir $cland_root_dir/{bin,deploy,etc,lib6,log,run,sci,scripts,src,web,cache} $cland_root_dir/cache/{image,instance,dnsmasq,meta,router,volume,xml} 2>/dev/null
[ ! -s "$net_conf" ] && cp ${net_conf}.example $net_conf

# Install packages
apt -y update
apt install -y make g++ libssl-dev libjsoncpp-dev ansible jq wget mkisofs network-manager net-tools python3-pip qemu-system-x86 qemu-utils bridge-utils ipcalc ipset iputils-arping libvirt-daemon libvirt-daemon-system libvirt-daemon-system-systemd libvirt-clients dnsmasq-base keepalived dnsmasq-utils conntrack
go version
if [ $? -ne 0 ]; then
    cd /tmp
    wget https://go.dev/dl/go1.21.13.linux-amd64.tar.gz
    tar -zxvf go1.21.13.linux-amd64.tar.gz
    mv go /usr/local/go
    export PATH=$PATH:/usr/local/go/bin:/root/go/bin
    echo 'PATH=$PATH:/usr/local/go/bin:/root/go/bin' >> $HOME/.bashrc
    go install github.com/swaggo/swag/cmd/swag@latest
    cd -
fi

# Build SCI
function build_sci() 
{
    cd $cland_root_dir/sci
    ./configure
    make
    make install
}

# Build web
function build_web()
{
    cd $cland_root_dir/web
    make all
}

# Build cloudland
function build_cland()
{
    cd $cland_root_dir/src
    make clean
    make
    make install
}

# Build libvirt console proxy
function build_console_proxy()
{
    cd /opt
    git clone https://github.com/libvirt/libvirt-console-proxy.git
    chown cland.cland libvirt-console-proxy
    cd libvirt-console-proxy
    go build -o build/virtconsoleproxyd cmd/virtconsoleproxyd/virtconsoleproxyd.go
    cd $cland_root_dir/deploy
}

# Generate host file
function gen_hosts()
{
    cland_ssh_dir=$cland_root_dir/deploy/.ssh
    mkdir -p $cland_ssh_dir
    chmod 700 $cland_ssh_dir
    if [ ! -f $cland_ssh_dir/cland.key ]; then
        yes y | ssh-keygen -t rsa -N "" -f $cland_ssh_dir/cland.key
        mkdir -p ~/.ssh
        chmod 700 ~/.ssh
        touch ~/.ssh/authorized_keys
        chmod 600 ~/.ssh/authorized_keys
        cat $cland_ssh_dir/cland.key.pub >> ~/.ssh/authorized_keys
    fi

    net_dev=$(cat $net_conf | grep 'network_device:' | cut -d: -f2)
    myip=$(ifconfig $net_dev | grep 'inet ' | awk '{print $2}')
    # 获取并输出系统主机名的 “短名称”（short hostname），仅返回主机名的核心部分，会自动剔除可能存在的域名后缀
    hname=$(hostname -s)
    # 把本机的 host ip 对写入host 文件,尾部追加
    bash -c "echo '$myip $hname' >> /etc/hosts"
    # 写入host.list文件
    echo $hname > $cland_root_dir/etc/host.list
    mkdir -p $cland_root_dir/deploy/hosts
    virt_type=kvm-x86_64
    # 写入ansible hosts文件
    cat > $cland_root_dir/deploy/hosts/hosts <<EOF
[hyper]
$hname ansible_host=$myip ansible_ssh_private_key_file=$cland_ssh_dir/cland.key client_id=0 zone_name=zone0 virt_type=$virt_type

[cland]
$hname ansible_host=$myip ansible_ssh_private_key_file=$cland_ssh_dir/cland.key

[web]
$hname ansible_host=$myip ansible_ssh_private_key_file=$cland_ssh_dir/cland.key

[database]
$hname ansible_host=$myip ansible_ssh_private_key_file=$cland_ssh_dir/cland.key

[monitor]
$hname ansible_host=$myip ansible_ssh_private_key_file=$cland_ssh_dir/cland.key
EOF
}

# 构建所有组件
git config --global --add safe.directory /opt/cloudland
#   构建SCI
diff /opt/sci/lib64/libsci.so.0.0.0 $cland_root_dir/sci/libsci/.libs/libsci.so.0.0.0
[ $? -ne 0 ] && build_sci
#   构建cloudland
diff $cland_root_dir/bin/cloudland $cland_root_dir/src/cloudland
[ $? -ne 0 ] && build_cland

# 产生host file
gen_hosts
cd $cland_root_dir/deploy
#   构建web
build_web
#   构建libvirt console proxy
build_console_proxy
chown -R cland.cland $cland_root_dir

echo "Build completes. Log file is /tmp/allinone-deploy-2021-01-10-10:42:09.log"
