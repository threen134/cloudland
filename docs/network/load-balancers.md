# Load Balancers

Load balancers distribute incoming traffic across multiple instances, improving availability and scalability.

## Create a Load Balancer

1. Go to **Network → Load Balancers** and click **Create**
2. Enter a name and optional description
3. Select the **VPC** and **subnet**
4. Click **Create**

## Listeners

A listener defines the protocol and port the load balancer accepts traffic on.

1. Open the load balancer detail page
2. Click **Add Listener**
3. Configure:
   - **Protocol**: `HTTP`, `HTTPS`, or `TCP`
   - **Port**: e.g. `80` or `443`
4. Click **Save**

## Backend Pools

Backend pools are groups of instances that receive traffic from a listener.

1. On the listener, click **Add Backend**
2. Select the target instance and port
3. Optionally set a **weight** (higher weight = more traffic)

## Health Checks

Load balancers periodically check if backends are healthy. Unhealthy backends are automatically removed from rotation.

| Field | Description |
|-------|-------------|
| Protocol | `HTTP` or `TCP` |
| Path | HTTP path to check (e.g. `/health`) |
| Interval | Seconds between checks |
| Timeout | Seconds to wait for a response |
| Healthy Threshold | Consecutive successes to mark healthy |
| Unhealthy Threshold | Consecutive failures to mark unhealthy |

## Floating IP

Associate a floating IP with the load balancer to expose it publicly. See [Floating IPs](/network/floating-ips).
