# Subnets

Subnets are subdivisions of a VPC. Instances are connected to the network through a subnet.

## Create a Subnet

1. Go to **Network → Subnets** and click **Create Subnet**
2. Select the parent **VPC**
3. Enter a name and CIDR (e.g. `192.168.1.0/24`)
4. Optionally configure a gateway IP and DNS servers
5. Click **Create**

## CIDR Guidelines

- The subnet CIDR must be within the VPC CIDR range
- The first and last IPs are reserved (network and broadcast)
- CloudLand reserves a few IPs for internal use (gateway, DHCP)

## Attach an Instance to a Subnet

When creating an instance, select the subnet in the network configuration step. The instance will receive an IP from the subnet's DHCP range.

## Delete a Subnet

A subnet can only be deleted when no instances or other resources are connected to it.
