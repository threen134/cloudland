# SSH Keys

SSH key pairs allow you to securely log in to your instances without a password.

## Add an SSH Key

1. Go to **Compute → SSH Keys** and click **Add Key**
2. Enter a name
3. Paste your **public key** (e.g. contents of `~/.ssh/id_rsa.pub`)
4. Click **Save**

To generate a new key pair locally:

```bash
ssh-keygen -t ed25519 -C "your@email.com"
```

Then paste the contents of `~/.ssh/id_ed25519.pub` into CloudLand.

## Use an SSH Key

When creating an instance, select your SSH key in the creation wizard. The public key will be injected into the instance via `cloud-init`.

Connect to your instance:

```bash
ssh -i ~/.ssh/id_ed25519 ubuntu@<floating-ip>
```

## Delete an SSH Key

SSH keys can be deleted from the SSH Keys page. Deleting a key does not affect already-running instances that were provisioned with that key.
