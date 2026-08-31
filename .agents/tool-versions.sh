# shellcheck shell=bash
# ShellCheck analyzes this sourced file in isolation and cannot see its consumers.
# shellcheck disable=SC2034

# Shared versions for Orb setup, resume checks, and CI policy tools.
# Renovate metadata is consumed by the custom regex manager in renovate.json.

# go.mod is the single Go toolchain version source. Go 1.27 removes a redundant
# toolchain directive when it matches the language version, so accept the exact
# root go directive as the installed toolchain version.
GO_MOD_PATH="$(dirname "${BASH_SOURCE[0]}")/../go.mod"
readonly GO_MOD_PATH
GO_VERSION=''
if [[ -f "$GO_MOD_PATH" ]]; then
  GO_VERSION="$(sed -n 's/^toolchain go//p' "$GO_MOD_PATH")"
  if [[ -z "$GO_VERSION" ]]; then
    GO_VERSION="$(sed -n 's/^go //p' "$GO_MOD_PATH")"
  fi
  if [[ ! "$GO_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    printf '%s\n' 'go.mod must declare an exact Go language or toolchain version.' >&2
    return 1
  fi
fi
readonly GO_VERSION
# renovate: datasource=github-releases depName=golangci/golangci-lint extractVersion=^v(?<version>.*)$
readonly GOLANGCI_LINT_VERSION='2.13.2'
# renovate: datasource=github-releases depName=google/capslock
readonly CAPSLOCK_VERSION='v0.3.2'
# Capslock v0.3.2 depends on x/tools v0.43.0, which cannot build SSA for Go
# 1.27 promoted literal fields or generic methods. Keep the fixed transitive
# dependency explicit until Capslock publishes a release containing it.
# renovate: datasource=go depName=golang.org/x/tools
readonly CAPSLOCK_X_TOOLS_VERSION='v0.49.0'
# NilAway and depaware do not publish stable releases; the Go datasource follows their latest pseudo-versions.
# renovate: datasource=go depName=go.uber.org/nilaway
readonly NILAWAY_VERSION='v0.0.0-20260808063849-8649a03c818a'
# renovate: datasource=go depName=github.com/tailscale/depaware
readonly DEPAWARE_VERSION='v0.0.0-20260720165112-f20f66241ec6'
# renovate: datasource=go depName=golang.org/x/vuln
readonly GOVULNCHECK_VERSION='v1.7.0'
# renovate: datasource=github-releases depName=rillig/gobco extractVersion=^v(?<version>.*)$
readonly GOBCO_VERSION='1.3.4'
# renovate: datasource=github-releases depName=JetBrains/go-modern-guidelines
readonly GO_MODERN_GUIDELINES_VERSION='v0.1.1'
# searching-with-fff loads fff-mcp as a long-lived stdio server. Keep the
# reviewed skill revision and binary checksum together when updating either.
readonly SEARCHING_WITH_FFF_REVISION='e915d903e58d28f12291b5c6c2c8001b12fafdc3'
readonly FFF_MCP_VERSION='v0.10.5'
readonly FFF_MCP_X86_64_LINUX_SHA256='6ab4f411eeee83e7e3900450b23bb50172e4f93d24dc720265126f0c3a1b1f23'
readonly FFF_MCP_AARCH64_LINUX_SHA256='e5e315a57dc9b282e2105172fb299883e1cf9bb63c2a36dd493f23763c8fdd4d'
# Gitleaks is installed only by CI and prek, not by Orb setup.
# renovate: datasource=github-releases depName=gitleaks/gitleaks
readonly GITLEAKS_VERSION='v8.30.1'

# The setup script verifies nerdctl-full against the release manager's signed
# SHA256SUMS. A signer change requires an explicit fingerprint review.
# renovate: datasource=github-releases depName=containerd/nerdctl extractVersion=^v(?<version>.*)$
readonly NERDCTL_VERSION='2.3.5'
