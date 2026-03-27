# Flavors

Flavors define the hardware configuration of an instance: vCPU count, RAM, and disk size.

## Viewing Flavors

Go to **Compute → Flavors** to see all available flavors and their specifications.

| Field | Description |
|-------|-------------|
| Name | Flavor identifier |
| vCPUs | Number of virtual CPU cores |
| RAM | Memory in MB |
| Disk | Root disk size in GB |

## Choosing a Flavor

Select a flavor when creating an instance. Consider:

- **CPU-bound workloads** — Choose flavors with higher vCPU count
- **Memory-bound workloads** — Choose flavors with higher RAM
- **Storage-bound workloads** — Attach additional volumes rather than relying on root disk

## Resize an Instance

To change the flavor of a running instance:

1. Stop the instance
2. Open the instance detail page
3. Click **Resize**
4. Select the new flavor
5. Start the instance

::: warning
Resizing to a smaller flavor may fail if the current root disk is larger than what the new flavor allows.
:::
