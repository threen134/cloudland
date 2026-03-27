# Introduction

CloudLand is a production-ready, open-source Cloud Infrastructure management platform (IaaS). It provides powerful APIs and a modern dashboard for managing virtual machines, networking, and storage resources.

## Architecture

```
Web UI (:5173) → CPGateway (:8000) → Cloudland Core Services
```

- **CPGateway** — Python FastAPI control plane: authentication, organization management, quota enforcement, and proxy to backends
- **Web UI** — Vue 3 + TypeScript frontend dashboard
- **API** — Go REST API (Gin + GORM + PostgreSQL)
- **Core** — C++ compute/network/storage orchestration

## Key Concepts

### Organizations
All resources belong to an **organization**. Users can be members of multiple organizations with different roles (Admin, Member, Viewer). Switch organizations from the dashboard header.

### Regions
Resources are provisioned in a specific **region**. Switch regions from the dashboard header. Each region is an independent Cloudland backend.

### Resource Quotas
Each organization has quotas for CPU, RAM, disk, and floating IPs. Quota usage is shown in the dashboard overview.

## Authentication

- Login at `/login` with your email and password
- Access tokens are valid for 120 minutes
- Enable **Remember Me** to persist your session

## Quick Links

- [Instances](/compute/instances) — Create and manage virtual machines
- [Volumes](/compute/volumes) — Block storage for your instances
- [VPCs](/network/vpcs) — Isolated virtual networks
- [Security Groups](/network/security-groups) — Firewall rules
- [Floating IPs](/network/floating-ips) — Public IP addresses
- [Load Balancers](/network/load-balancers) — Distribute traffic across instances
- [SSH Keys](/compute/ssh-keys) — Manage SSH key pairs
