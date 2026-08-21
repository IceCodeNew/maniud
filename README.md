# maniud

`maniud` creates Compose services from container runtime arguments or Docker
archives, applies one service through a journaled transaction, and reconciles
desired state from Git. It talks to runtime APIs directly. The product does not
invoke Docker, Podman, nerdctl, or the Docker Compose CLI.

The phase-one CLI has five commands:

```text
gen
apply
gitops init
daemon
doctor
```

## Install

Releases provide CGO-free binaries for Linux amd64/arm64 and macOS arm64. A
macOS amd64 binary is cross-built, but CI does not run it on an Intel Mac.

Download the binary matching your host from the
[GitHub releases](https://github.com/IceCodeNew/maniud/releases), verify it as
described in [Release verification](docs/release-verification.md), then install
it on `PATH`:

```sh
chmod 0755 maniud_VERSION_OS_ARCH
install -m 0755 maniud_VERSION_OS_ARCH "$HOME/.local/bin/maniud"
maniud --version
```

## Three-step offline generation

This flow needs an installed `maniud` binary and a Docker image archive. It
does not contact a registry or runtime daemon.

1. Choose one archive member by its exact tag. Use an absolute archive path.

   ```sh
   archive="$PWD/hello.tar"
   source="docker-archive:$archive:example.org/hello:1"
   ```

2. Generate a new Compose file. `maniud` refuses to replace an existing output
   file.

   ```sh
   maniud gen --name hello --output hello.yaml "$source"
   ```

3. Review the JSON result and generated file.

   ```sh
   sed -n '1,200p' hello.yaml
   git add hello.yaml
   git commit -m 'add hello service'
   ```

The JSON result includes the exact manual `docker image load` command required
before an archive-backed service can be applied. Generation does not import or
start the image, and it does not prove that an apply will succeed.

`gen` also accepts supported `docker`, `podman`, or `nerdctl` `create` and `run`
arguments after `--`. It parses those arguments as data and never executes the
named command:

```sh
maniud gen --name web --output web.yaml -- \
  docker run --read-only --restart unless-stopped example.org/web:1
```

## Apply one service

`apply` accepts a Compose file from a clean Git worktree. The file and every
captured secondary input must be committed. Run a dry run first:

```sh
maniud apply --dry-run services/web.yaml
maniud apply services/web.yaml
```

Both commands validate the selected runtime and its observed workload. A dry
run performs no runtime effect and no durable write. It can still contact the
runtime and registry to prove current image and workload identity.

If a Compose file has multiple active services, pass the service name as the
second positional argument:

```sh
maniud apply --dry-run compose.yaml web
```

Docker uses `DOCKER_HOST`; the default is
`unix:///var/run/docker.sock`. Podman uses `CONTAINER_HOST`, then
`XDG_RUNTIME_DIR/podman/podman.sock`, and finally its rootful or user socket.
Plain `tcp://` Docker endpoints emit an `insecure_remote_engine` warning before
use and are intended only for an operator-controlled VPN and firewall. TLS from
Docker environment variables is not configured in phase one.

Runtime state defaults to `$XDG_STATE_HOME/maniud/state.db`, or
`$HOME/.local/state/maniud/state.db` when `XDG_STATE_HOME` is unset. `DOCKER_CONFIG`
selects registry credentials for registry-backed images.

## Git reconciliation

Register one clean local checkout whose selected branch can fast-forward from
its verified `origin`:

```sh
maniud gitops init --branch main /absolute/path/to/desired-state
maniud daemon --once
maniud daemon --interval 300
```

The repository stores service files in `services/*.yaml` or `services/*.yml`.
The daemon validates the complete current snapshot before applying any service.
On each cycle it recovers transactions from the registered commit before it
fetches and fast-forwards to a newer commit.

## Operations

- [Error reference](docs/errors.md)
- [Recovery runbook](docs/recovery.md)
- [Release verification](docs/release-verification.md)

Run `maniud --help` or `maniud COMMAND --help` for the exact public grammar.
