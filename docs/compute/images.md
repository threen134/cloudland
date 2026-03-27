# Images

Images are OS templates used to create instances. CloudLand supports public images provided by the platform and private images created from your own instances.

## Image Types

| Type | Description |
|------|-------------|
| Public | Provided by the platform, available to all organizations |
| Private | Created from your instances, visible only to your organization |

## Create a Private Image

You can create a private image by snapshotting a stopped instance:

1. Stop the instance
2. Open the instance detail page
3. Click **Create Image**
4. Enter a name and description
5. Click **Confirm**

The image will appear in **Compute → Images** once ready.

## Use an Image

When creating an instance, select the image in the **Image** step. Both public and your organization's private images are listed.

## Delete an Image

Private images can be deleted from the Images page. Public images cannot be deleted.

::: tip
Images are stored as disk snapshots. Creating an image from a large instance may take several minutes.
:::
