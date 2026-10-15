#!/bin/bash
# sandbox: install the repository's node scripts at the paths a compute node uses
set -e
src="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../scripts" && pwd)"
mkdir -p /opt/cloudland/scripts /opt/cloudland/run /opt/cloudland/log /opt/cloudland/cache
rsync -a --delete --exclude backend $src/ /opt/cloudland/scripts/
ln -sfn kvm /opt/cloudland/scripts/backend
touch /opt/cloudland/scripts/cloudrc.local
# Windows checkout: make sure the scripts run as on a node
find /opt/cloudland/scripts -name '*.sh' -exec sed -i 's/\r$//' {} +
sed -i 's/\r$//' /opt/cloudland/scripts/cloudrc
chmod +x /opt/cloudland/scripts/kvm/*.sh /opt/cloudland/scripts/kvm/storage/*.sh
command -v jq >/dev/null || apt-get install -y jq >/dev/null
ls /opt/cloudland/scripts/backend/storage/
