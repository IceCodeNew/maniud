# maniud

[![Checks](https://github.com/IceCodeNew/maniud/actions/workflows/checks.yml/badge.svg?branch=master)](https://github.com/IceCodeNew/maniud/actions/workflows/checks.yml?query=branch%3Amaster)
[![Codecov](https://codecov.io/gh/IceCodeNew/maniud/branch/master/graph/badge.svg)](https://codecov.io/gh/IceCodeNew/maniud)
[![Release](https://img.shields.io/github/v/release/IceCodeNew/maniud?display_name=tag&sort=semver)](https://github.com/IceCodeNew/maniud/releases)
[![Downloads](https://img.shields.io/github/downloads/IceCodeNew/maniud/total)](https://github.com/IceCodeNew/maniud/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/IceCodeNew/maniud)](go.mod)
[![Go Reference](https://pkg.go.dev/badge/github.com/IceCodeNew/maniud/containerconfig.svg)](https://pkg.go.dev/github.com/IceCodeNew/maniud/containerconfig)
[![Go Report Card](https://goreportcard.com/badge/github.com/IceCodeNew/maniud)](https://goreportcard.com/report/github.com/IceCodeNew/maniud)

[English](README.md) | [简体中文](README.zh-CN.md)

maniud aims to give container images the one-click experience people expect from an `.exe` file.

Have you run into any of these problems?

- The upstream project publishes an image but gives you no usable startup instructions.
- A container mounts a host directory, then fails because its UID or GID does not match the directory owner.
- You must watch every image release and replace the running container yourself.
- You follow `latest` to automate upgrades, then an unreviewed image stops a working service.

Someone put it bluntly in Twitter's open-source algorithm repository: [“I DONT GIVE A FUCK ABOUT THE FUCKING CODE!”](https://github.com/twitter/the-algorithm/issues/1999).
Many people just want to run the application and have no interest in studying the container project first.
maniud turns a copied container command or a pulled image into a reviewable Compose file, prepares host paths, and safely applies and upgrades the service.

## Install

For a first installation, send this prompt to an agent that can operate your machine:

```text
Install the latest stable maniud release from https://github.com/IceCodeNew/maniud on this machine.
Read https://github.com/IceCodeNew/maniud/blob/master/docs/release-verification.md first, detect the operating system and architecture, and follow that guide to download and verify the correct release artifact.
Install it into a user-writable directory already on PATH without sudo, then run maniud --version.
Stop and report the original error if any verification fails, and do not change unrelated files.
```

Power users can install and update maniud with [mise](https://mise.jdx.dev/):

```sh
mise use --global 'github:IceCodeNew/maniud[asset_pattern=maniud_{{ version }}_{{ os(macos="darwin") }}_{{ arch(x64="amd64") }},bin=maniud]@latest'
maniud --version
```

## Day 0: create and start a service

Create the GitOps workspace with its initial commit first, then enter the new empty `services` directory:

```sh
maniud gitops init "$HOME/maniud-desired-state"
cd "$HOME/maniud-desired-state/services"
```

An image project may publish instructions that stop here:

```sh
docker pull registry.example.com/team/photos:1.4.2
docker run --name photos --restart unless-stopped --mount type=bind,src=/srv/photos,dst=/var/lib/photos registry.example.com/team/photos:1.4.2
```

Pull the fixed image version first, then give the copied startup command to `gen`:

```sh
docker pull registry.example.com/team/photos:1.4.2
maniud gen -- docker run --name photos --restart unless-stopped --mount type=bind,src=/srv/photos,dst=/var/lib/photos registry.example.com/team/photos:1.4.2
```

If the upstream project gives you no usable command, tell maniud which local runtime owns the pulled image:

```sh
docker pull registry.example.com/team/photos:1.4.2
maniud gen docker://registry.example.com/team/photos:1.4.2
```

maniud also supports the `podman://` and `containerd://` runtimes.
Mutable tags such as `latest` are not recommended because the same configuration can later select another image; the Day 1 GitOps flow automatically applies reviewed upgrade commits and lets you roll them back with `git revert`.
If the image is absent locally, `gen` stops and prints the pull command to standard error.
An image cannot declare site-specific credentials, public URLs, or every required storage path, so `gen` also tells you to add any missing application settings before committing the file.

`gen` writes `photos.yaml` by default and also writes `photos.prepare.sh` when the service uses host bind mounts.
It warns on standard error that the preparation script must be reviewed and run before `apply`.

```sh
cat photos.yaml
cat photos.prepare.sh
```

Review every path and account in the script before running it.
Each missing bind source is marked as `directory`; change that word to `file` on the corresponding line when the container expects a file, then run the edited script with the privileges required for those host paths.

```sh
sudo sh photos.prepare.sh
```

`apply` accepts only committed Compose files from a clean Git worktree, so commit the generated files after reviewing them:

```sh
git add photos.yaml photos.prepare.sh
git commit -m 'Add photos service'
```

Preview the deployment before allowing runtime or state changes:

```sh
maniud apply --dry-run photos.yaml
```

A successful preview exits with status 0 and explains the planned action in a short result:

```text
Dry run passed for photos/photos.
Action: create a new workload (bootstrap).
Runtime: docker on linux/amd64.
Image: registry.example.com/team/photos:1.4.2@sha256:….
Ready to apply. No changes were made.
```

When the output ends with `Ready to apply` and exits with status 0, you can run the same plan.
A nonzero exit means the preview failed, and the command states the reason directly.
Use `maniud apply --help` to see every action and the fields returned by the optional detailed JSON mode.

Apply the same file after the preview matches your intent:

```sh
maniud apply photos.yaml
```

## Day 1: put the service under GitOps

Create an empty dedicated remote repository through your usual Git hosting workflow, then push the Day 0 commit to `origin`:

```sh
cd "$HOME/maniud-desired-state"
git remote add origin YOUR_REPOSITORY_URL
git push -u origin master
```

Run the daemon under your service manager:

```sh
maniud daemon start --interval 300
```

The daemon reconciles immediately at startup and checks again after each interval.
It first proves that the local branch and `origin` can fast-forward safely, then accepts later updates; before changing any workload, the daemon validates the complete `services/` snapshot and recovers unfinished transactions.
To upgrade an image later, change it to the new fixed version under `services/`, review and run any changed preparation script, then commit and push the change.
The daemon applies that commit on its next cycle.

## Recover or roll back

If a standalone `apply` stops midway, keep its Compose file, state directory, runtime workload, and backups unchanged, then run the same `apply` again.

```sh
maniud apply photos.yaml
```

If a GitOps cycle stops midway, keep the registered checkout and state unchanged, then restart the supervised `maniud daemon start` process.
The daemon reconciles immediately and recovers the recorded transaction before fetching a newer commit.

If an upgrade completed but the new version is unhealthy, revert its Git commit and push the revert so the registered branch still moves forward:

```sh
git -C "$HOME/maniud-desired-state" revert UPGRADE_COMMIT
git -C "$HOME/maniud-desired-state" push
```

Read the [recovery guide](docs/recovery.md) before changing state files, runtime ownership labels, or backups by hand.

## Runtime connections

Docker reads `DOCKER_HOST` and defaults to `unix:///var/run/docker.sock`.
Podman reads `CONTAINER_HOST` before checking its standard user and root sockets.
containerd requires a local Unix socket in `CONTAINERD_ADDRESS` and a namespace in `CONTAINERD_NAMESPACE`.
maniud stores state under `${XDG_STATE_HOME:-$HOME/.local/state}/maniud`.
`DOCKER_CONFIG` selects registry credentials.

Use `maniud COMMAND --help` for exact command syntax, the [error reference](docs/errors.md) for failure codes, and the [recovery guide](docs/recovery.md) when an operation was interrupted.
