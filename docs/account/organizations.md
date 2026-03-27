# Organizations

Organizations are the top-level tenancy unit in CloudLand. All resources (instances, volumes, networks, etc.) belong to an organization.

## Switch Organization

Click the organization name in the dashboard header to switch between organizations you belong to.

## Roles

| Role | Permissions |
|------|-------------|
| Admin | Full access: create/delete resources, manage members |
| Member | Create and manage resources |
| Viewer | Read-only access |

## Invite Members

1. Go to **Account → Users**
2. Click **Invite User**
3. Enter the email address and select a role
4. Click **Send Invitation**

The invitee will receive an email with an activation link.

## Resource Quotas

Each organization has quotas:

| Resource | Default |
|----------|---------|
| vCPUs | 20 |
| RAM | 51200 MB |
| Disk | 1000 GB |
| Floating IPs | 10 |

Quota usage is shown in the **Dashboard Overview**. Contact a superadmin to increase quotas.
