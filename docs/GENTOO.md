# Rootless Gentoo chroot builds

Anneal can maintain Gentoo build roots and publish binary packages to a LAN
binhost **without running anneald as root**. This matters: the agent has shell,
file-write, web-fetch, MCP, and scheduled-task tools, so an autonomous root
session would make every prompt-injection path a host-root compromise.

The design uses:

- **bubblewrap + user namespaces** — the agent gets uid 0 only inside a
  namespace mapped to the unprivileged anneal user; no host root.
- **overlayfs** — the real chroot is a read-only lower; build changes go to a
  user-owned upper directory under `~/.anneal/gentoo/`.
- **a user-owned writable tree** — either a reflink clone/snapshot of the
  chroot or ACLs granting the anneal user write access. Overlayfs needs to be
  able to copy up lower files, so the lower must be writable by the *mapped*
  user.
- **a shared binpkg directory** — only `.gpkg.tar` output is published;
  nothing is merged into the real chroot by default.
- **a validated tool** — package atoms are passed as argv, never through a
  shell; no arbitrary `emerge` options are exposed.

## One-time setup

### Option A — user-owned clone (recommended)

A clone keeps the real chroot untouched. A btrfs snapshot created once by root
is the fastest way to get a writable tree:

```sh
# run once as root
btrfs subvolume snapshot -r /mnt/muslos /mnt/anneal-build/muslos
btrfs property set -ts /mnt/anneal-build/muslos ro false
chown -R anneal:anneal /mnt/anneal-build/muslos
```

If the tree is not a btrfs subvolume, copy it as the unprivileged user:

```sh
cp -a --reflink=auto --no-preserve=ownership /mnt/muslos /mnt/anneal-build/muslos
```

`cp` may skip files the user cannot read (for example `/etc/shadow`). That is
acceptable for a build root; a snapshot preserves everything. Refresh the
clone on your own schedule (delete/recreate the snapshot, then re-chown), or
keep the build root persistent and let the overlay upper cache build
dependencies.

### Option B — ACL mode

If you want the tool to operate directly on the real chroot, grant the anneal
user write access with default ACLs so newly created files stay writable:

```sh
# run once as root; replace "anneal" with the service user
setfacl -R  -m u:anneal:rwX /mnt/muslos
setfacl -R -d -m u:anneal:rwX /mnt/muslos
```

This is broader than a clone: the anneal user (and therefore an agent tool
running as that user) can modify the real tree. The overlay still isolates
each build's temporary changes, but `bash` is intentionally available to the
agent; use job-level permission rules and review recurring prompts.

Do **not** run `anneald` as root as a shortcut. `bwrap` is designed to make the
rootless path work.

### Verify user namespaces

```sh
unshare -Ur sh -c 'id'      # uid=0(root) inside the namespace
bwrap --ro-bind / / true    # exit 0
```

If these fail, the kernel/container has unprivileged user namespaces disabled.
Fix that policy; do not fall back to running emerge as host root.

## Configuration

```json
{
  "gentoo": {
    "enabled": true,
    "timeout_s": 7200,
    "tmpfs_mb": 16384,
    "roots": [
      {
        "name": "muslos",
        "source": "/mnt/muslos",
        "base": "/mnt/anneal-build/muslos",
        "overlay_dir": "/home/anneal/.anneal/gentoo/muslos",
        "pkgdir": "/usr/portage/packages",
        "distdir": "/usr/portage/distfiles",
        "binpkg_dir": "/mnt/binhost/muslos",
        "distfiles_dir": "/mnt/binhost/distfiles",
        "emerge_args": ["--update", "--deep", "--newuse"]
      },
      {
        "name": "musloszen2",
        "source": "/mnt/musloszen2",
        "base": "/mnt/anneal-build/musloszen2",
        "overlay_dir": "/home/anneal/.anneal/gentoo/musloszen2",
        "pkgdir": "/usr/portage/packages",
        "distdir": "/usr/portage/distfiles",
        "binpkg_dir": "/mnt/binhost/musloszen2",
        "distfiles_dir": "/mnt/binhost/distfiles"
      }
    ]
  }
}
```

| key | meaning |
|-----|---------|
| `source` | real chroot, used as the read-only lower |
| `base` | optional user-owned clone/snapshot used as the overlay lower |
| `overlay_dir` | host directory for `upper/` and `work/` |
| `pkgdir` | path inside the chroot where the binpkg output is bind-mounted |
| `distdir` | path inside the chroot for the shared distfiles cache |
| `binpkg_dir` | host directory served to LAN clients |
| `distfiles_dir` | shared download cache |
| `no_network` | unshare the network (only with cached distfiles) |
| `features` | Portage FEATURES override (default `-userpriv -usersandbox buildpkg`) |
| `emerge_args` | config-trusted extra emerge flags, e.g. `--update --deep --newuse` |
| `env` | extra environment variables inside the sandbox |

Find the chroot's actual `PKGDIR`/`DISTDIR` with:

```sh
bwrap --ro-bind /mnt/muslos / --proc /proc --dev /dev /bin/sh -c \
  'portageq envvar PKGDIR DISTDIR'
```

## How a build runs

For `gentoo action=build root=muslos packages=["app-misc/hello"]`:

```text
bwrap
  --overlay <overlay>/upper <overlay>/work /
  --uid 0 --gid 0
  --proc /proc --dev /dev
  --size 16384MiB --tmpfs /dev/shm
  --bind /mnt/binhost/muslos /usr/portage/packages
  --bind /mnt/binhost/distfiles /usr/portage/distfiles
  --unshare-all --share-net
  --die-with-parent --new-session
  --clearenv
  PATH=/usr/sbin:/usr/bin:/sbin:/bin
  PORTAGE_USERNAME=root PORTAGE_GRPNAME=root
  PORTAGE_TMPDIR=/dev/shm PORTAGE_TMPFS=/dev/shm BUILD_PREFIX=/dev/shm
  FEATURES=-userpriv -usersandbox buildpkg
  /usr/bin/emerge --buildpkgonly --oneshot app-misc/hello
```

`--buildpkgonly` builds and packages the requested package but does not merge
it into the build root. Any missing build dependencies are installed into the
overlay (or cached in the persistent upper) and do not touch the real chroot.
`PORTAGE_USERNAME=root` avoids the unmapped portage uid inside the user
namespace. `/dev/shm` is a bounded tmpfs; `make.conf` settings such as
`MAKEOPTS` still apply.

The tool actions are:

| action | behavior |
|--------|----------|
| `list` | configured roots |
| `preflight` | check bwrap, source/base, overlay, binpkg and distfile paths |
| `info` | `emerge --info` inside the sandbox |
| `pretend` | `emerge --pretend --buildpkgonly <atoms>` |
| `build` | `emerge --buildpkgonly --oneshot <atoms>` |

Atoms are validated (`app-misc/hello`, `=sys-devel/gcc-15.2.0`,
`app-misc/hello[doc]`, `@world`, `@system`); options and shell metacharacters
are rejected. Every invocation is appended to `~/.anneal/gentoo/audit.jsonl`.

## Binhost

Serve `binpkg_dir` over HTTP from the build box (or the router, via a bind
mount):

```caddyfile
binhost.example.net {
    root * /mnt/binhost
    file_server browse
}
```

On the LAN clients:

```sh
PORTAGE_BINHOST="https://binhost.example.net/muslos"
FEATURES="getbinpkg"
emerge -uDN @world
```

Portage writes the `Packages` index when `buildpkg` runs. If you ever need to
rebuild it manually, run `emaint binhost --fix` inside the same sandbox.

## Scheduled binhost job

After the first manual `pretend` and `build` succeed, create a recurring job:

```json
{
  "id": "gentoo-binhost",
  "name": "Weekly Gentoo binhost",
  "schedule": "0 3 * * 0",
  "timezone": "Europe/Amsterdam",
  "prompt": "Use the gentoo tool to preflight both roots. Then build @world for root muslos and root musloszen2 with action=build. Summarize new .gpkg.tar files and any failures. Reply with [SILENT] only if there was nothing to build and no errors.",
  "permission_mode": "dontAsk",
  "permission": {"gentoo": "allow"},
  "deliver": ["ntfy"],
  "timeout_s": 14400
}
```

```sh
annealctl schedule add -id gentoo-binhost -name "Weekly Gentoo binhost" \
  -schedule "0 3 * * 0" -timezone Europe/Amsterdam \
  -permission-mode dontAsk -allow gentoo -deliver ntfy -timeout 14400 \
  -prompt "Use the gentoo tool to preflight both roots, build @world for muslos and musloszen2, and summarize new packages and failures. Reply [SILENT] if nothing changed."
```

The job runs unattended. `dontAsk` means the model cannot invent a shell
command; the only write path is the validated `gentoo` tool. Allow `bash`
explicitly only for specific, reviewed commands.

## Security model

- Host root is never required by the agent. `bwrap` creates a user namespace;
  uid 0 inside maps to the unprivileged outside user.
- The real chroot is readable, not writable, in clone mode. In ACL mode the
  service user can write it; that is the trade-off for avoiding a clone.
- Only the overlay, the binpkg output, the distfiles cache, and `/dev`,
  `/proc`, `/dev/shm` are visible. `$HOME`, the repo, SSH keys, and the Docker
  socket are not bind-mounted.
- Ebuilds run as pseudo-root inside the namespace. They can write the overlay
  and network-fetch, but cannot chown host files or load host kernel modules.
  For untrusted or unattended jobs, pre-fetch distfiles and set
  `no_network: true`.
- No shell is involved; package atoms and options are argv elements.
- `--die-with-parent`, `--disable-userns`, and a timeout prevent orphan or
  nested-namespace builds.
- The audit log records action/root/packages/status/duration and new packages.

If a build genuinely needs host privileges (for example mounting a loop
device), run that one operation as a separate, fixed root helper with an
allowlist. Do not give the agent a root shell.
