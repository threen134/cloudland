# Security Groups

Security groups act as virtual firewalls, controlling inbound and outbound traffic for your instances.

## Create a Security Group

1. Go to **Network → Security Groups** and click **Create**
2. Enter a name and optional description
3. Click **Create**

## Manage Rules

Open the security group detail page and click **Add Rule**.

| Field | Description |
|-------|-------------|
| Direction | `Ingress` (inbound) or `Egress` (outbound) |
| Protocol | `TCP`, `UDP`, `ICMP`, or `All` |
| Port Range | e.g. `22` for SSH, `80-443` for HTTP/HTTPS |
| Remote | Source/destination CIDR (e.g. `0.0.0.0/0` for anywhere) |

### Common Rules

| Purpose | Direction | Protocol | Port |
|---------|-----------|----------|------|
| SSH | Ingress | TCP | 22 |
| HTTP | Ingress | TCP | 80 |
| HTTPS | Ingress | TCP | 443 |
| Ping | Ingress | ICMP | — |
| All outbound | Egress | All | — |

## Attach to an Instance

Security groups are attached per network interface. You can manage them from the instance detail page under **Network Interfaces**.

## Default Security Group

A default security group is created for each organization. It allows all outbound traffic and no inbound traffic by default.

::: warning
Opening port `0.0.0.0/0` exposes your instance to the entire internet. Restrict source IPs where possible.
:::
