# Build a custom binary

[English](custom-builds.md) | [简体中文](custom-builds.zh-CN.md)

The release binary includes Docker, Podman, and containerd. A source checkout and Go 1.27 can produce a smaller binary with only the runtimes needed on the target host.

Run the builder from the repository root. Repeat `--runtime` to select more than one runtime:

```sh
go run ./cmd/maniud-builder --runtime docker --output ./bin/maniud
go run ./cmd/maniud-builder --runtime docker --runtime podman --output ./bin/maniud
```

Omitting `--runtime` includes all three runtimes. To build commands that do not open a container runtime, disable the defaults:

```sh
go run ./cmd/maniud-builder --no-default-runtimes --output ./bin/maniud
```

Use `--target GOOS/GOARCH` to cross-compile for a [release platform](release-verification.md):

```sh
go run ./cmd/maniud-builder --runtime docker --target linux/arm64 --output ./bin/maniud
```

The builder verifies the selected runtime packages and the completed binary before replacing the output file. It writes a JSON manifest containing the output path, target, runtimes, Go version, and source revision.
