# Volumes

Volumes are persistent block storage devices that can be attached to instances. Data on volumes is preserved even when instances are stopped or deleted.

## Create a Volume

1. Go to **Compute → Volumes** and click **Create Volume**
2. Enter a name and size (GB)
3. Optionally select a source image to pre-populate the volume
4. Click **Create**

## Attach to an Instance

1. Open the volume detail page
2. Click **Attach**
3. Select the target instance
4. The volume will appear as a new block device (e.g. `/dev/vdb`) inside the instance

## Detach a Volume

1. Open the volume detail page
2. Click **Detach**
3. The instance must not be actively writing to the volume

::: warning
Always unmount the volume inside the OS before detaching to avoid data corruption.
:::

## Volume States

| State | Description |
|-------|-------------|
| `available` | Ready to attach |
| `in-use` | Attached to an instance |
| `error` | Provisioning failed |

## Extend a Volume

Volumes can be extended (increased) in size while detached. Shrinking is not supported.
