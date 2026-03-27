# VPCs

A Virtual Private Cloud (VPC) is an isolated virtual network within CloudLand. All instances must be deployed inside a VPC subnet.

## Create a VPC

1. Go to **Network → VPCs** and click **Create VPC**
2. Enter a name and CIDR block (e.g. `192.168.0.0/16`)
3. Click **Create**

## Subnets

Each VPC contains one or more subnets. Subnets define a smaller IP range within the VPC CIDR.

See [Subnets](/network/subnets) for details.

## Default VPC

A default VPC is created automatically for new organizations. You can use it immediately or create additional VPCs for network isolation.

## Delete a VPC

A VPC can only be deleted when all its subnets and attached resources have been removed.

::: tip
Use multiple VPCs to isolate different environments (e.g. production vs. staging).
:::
