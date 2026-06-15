# Permanent apt signing key

The public apt repository must be signed with one stable Meerkat key. Do not
publish public builds with a key generated inside GitHub Actions, because that
forces users to refresh `/usr/share/keyrings/meerkat-agent.gpg` after the key
rotates.

Create and back up the real key on a trusted workstation, not in an AI session.
Keep the primary secret key and revocation certificate offline. GitHub Actions
only needs a signing-capable exported secret key or subkey in repository
secrets.

## Create the key

Run these commands on the trusted workstation:

```sh
gpg --quick-generate-key "Meerkat Agent Apt <meerkat-agent@tnisoft.ro>" ed25519 cert 5y
gpg --list-secret-keys --keyid-format long "Meerkat Agent Apt"
```

Record the long primary key id, shown after `sec ed25519/`. The examples below
use `KEY_ID`.

Add the signing subkey that GitHub Actions will use:

```sh
gpg --quick-add-key KEY_ID ed25519 sign 5y
```

Create a revocation certificate and store it offline with the primary secret
key backup:

```sh
gpg --output meerkat-agent-apt-revoke.asc --gen-revoke KEY_ID
gpg --armor --export-secret-keys KEY_ID > meerkat-agent-apt-primary-secret.asc
gpg --armor --export KEY_ID > meerkat-agent-apt-public.asc
```

## Configure GitHub Actions

Export the secret material that CI will use for signing:

```sh
gpg --armor --export-secret-subkeys KEY_ID > meerkat-agent-apt-ci-secret.asc
```

Set these repository secrets:

```sh
gh secret set APT_GPG_PRIVATE_KEY < meerkat-agent-apt-ci-secret.asc
gh secret set APT_GPG_PASSPHRASE
```

If the exported CI key has no passphrase, omit `APT_GPG_PASSPHRASE`.

The publish workflow intentionally fails when `APT_GPG_PRIVATE_KEY` is missing.
That protects public users from installing a repository signed by a temporary
or rotating development key.

## Rotate only deliberately

A signing-key rotation is a release event. Before changing the key, publish the
new public key, announce that users must refresh their keyring, and keep the old
repository metadata available long enough for existing users to update cleanly.
