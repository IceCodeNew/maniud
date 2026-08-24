# shellcheck shell=bash
# ShellCheck analyzes this sourced file in isolation and cannot see its consumers.
# shellcheck disable=SC2034

# Shared versions for Orb setup, resume checks, and CI policy tools.
# Renovate metadata is consumed by the custom regex manager in renovate.json.

# go.mod is the single Go toolchain version source once the Foundation layer
# introduces it. PR #2 installs current Go because that isolated layer has no
# module yet; no duplicate pinned version survives in the final repository.
GO_MOD_PATH="$(dirname "${BASH_SOURCE[0]}")/../go.mod"
readonly GO_MOD_PATH
GO_VERSION=''
if [[ -f "$GO_MOD_PATH" ]]; then
  GO_VERSION="$(sed -n 's/^toolchain go//p' "$GO_MOD_PATH")"
  if [[ ! "$GO_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf '%s\n' 'go.mod must declare an exact Go toolchain version.' >&2
    return 1
  fi
fi
readonly GO_VERSION
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
# renovate: datasource=github-releases depName=rillig/gobco extractVersion=^v(?<version>.*)$
readonly GOBCO_VERSION='1.3.4'
# Gitleaks is installed only by CI and prek, not by Orb setup.
# renovate: datasource=github-releases depName=gitleaks/gitleaks
readonly GITLEAKS_VERSION='v8.30.1'

# The setup script verifies nerdctl-full against the release manager's signed
# SHA256SUMS. A signer change requires an explicit fingerprint review.
# renovate: datasource=github-releases depName=containerd/nerdctl extractVersion=^v(?<version>.*)$
readonly NERDCTL_VERSION='2.3.5'
