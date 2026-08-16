# shellcheck shell=bash
# ShellCheck analyzes this sourced file in isolation and cannot see its consumers.
# shellcheck disable=SC2034

# Shared version pins for Orb setup, resume checks, and CI policy tools.
# Renovate metadata is consumed by the custom regex manager in renovate.json.

# PR #2 has no go.mod yet; Foundation replaces this bootstrap pin with its
# toolchain directive as the single version source.
# renovate: datasource=golang-version depName=go
readonly GO_VERSION='1.26.6'
# renovate: datasource=github-releases depName=golangci/golangci-lint extractVersion=^v(?<version>.*)$
readonly GOLANGCI_LINT_VERSION='2.12.2'
# renovate: datasource=github-releases depName=google/capslock
readonly CAPSLOCK_VERSION='v0.3.2'
# NilAway and depaware do not publish stable releases; the Go datasource follows their latest pseudo-versions.
# renovate: datasource=go depName=go.uber.org/nilaway
readonly NILAWAY_VERSION='v0.0.0-20260808063849-8649a03c818a'
# renovate: datasource=go depName=github.com/tailscale/depaware
readonly DEPAWARE_VERSION='v0.0.0-20260720165112-f20f66241ec6'
# renovate: datasource=go depName=golang.org/x/vuln
readonly GOVULNCHECK_VERSION='v1.7.0'

# The setup script verifies nerdctl-full against the release's published SHA256SUMS.
# renovate: datasource=github-releases depName=containerd/nerdctl extractVersion=^v(?<version>.*)$
readonly NERDCTL_VERSION='2.3.5'
