# Instances

Instances are virtual machines running on CloudLand. You can create, start, stop, reboot, resize, and delete instances from the dashboard or via the API.

## Create an Instance

1. Go to **Compute → Instances** and click **Create Instance**
2. Choose a **flavor** (CPU / RAM / disk configuration)
3. Select an **image** (OS template)
4. Select or create a **VPC** and **subnet**
5. Attach a **security group**
6. Optionally add an **SSH key** for remote access
7. Click **Create**

The instance will be provisioned in under 60 seconds.

## Instance States

| State | Description |
|-------|-------------|
| `running` | Instance is active and reachable |
| `stopped` | Instance is shut down, disk is preserved |
| `paused` | Instance is suspended in memory |
| `error` | Provisioning or runtime error occurred |

## Actions

- **Start / Stop / Reboot** — Power management
- **Console** — Open a browser-based VNC console
- **Resize** — Change the flavor (requires a stop/start cycle)
- **Attach Volume** — Add block storage
- **Manage Networks** — Add or remove network interfaces and security groups
- **Create Image** — Snapshot the instance disk as a reusable image
- **Delete** — Permanently destroy the instance

## Network Interfaces

Each instance can have multiple network interfaces, each attached to a subnet. You can assign security groups per interface and attach floating IPs for public access.

## Console Access

Click **Console** on the instance detail page to open a VNC session in your browser. No SSH required.
