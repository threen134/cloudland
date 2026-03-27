# Floating IPs

Floating IPs are public IP addresses that can be dynamically assigned to instances, giving them internet access.

## Allocate a Floating IP

1. Go to **Network → Floating IPs** and click **Allocate IP**
2. A public IP is assigned to your organization

## Associate with an Instance

1. Open the Floating IP detail page
2. Click **Associate**
3. Select the target instance and network interface
4. Click **Confirm**

The instance is now reachable at that public IP.

## Disassociate

1. Open the Floating IP detail page
2. Click **Disassociate**

The IP is returned to your organization's pool and can be re-associated with another instance.

## Release a Floating IP

Releasing a floating IP returns it to the platform pool and removes it from your organization. This cannot be undone — you will receive a different IP if you allocate again.

## Quota

Each organization has a floating IP quota. The current usage is shown in the dashboard overview. Contact your administrator to increase the quota.
