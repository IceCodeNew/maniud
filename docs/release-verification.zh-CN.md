# 校验发布版本

[English](release-verification.md) | [简体中文](release-verification.zh-CN.md)

请从 [GitHub Releases](https://github.com/IceCodeNew/maniud/releases) 下载适合当前主机的二进制文件、`SHA256SUMS` 和对应的 `maniud_X.Y.Z.sigstore.json` bundle。

| 文件名后缀 | 发布版本验证方式 |
| --- | --- |
| `linux_amd64` | 在 Linux amd64 上构建并测试。 |
| `linux_arm64` | 在 Linux arm64 上构建并执行基本运行检查。 |
| `darwin_arm64` | 在 macOS arm64 上构建并测试。 |
| `darwin_amd64` | 只进行交叉编译，没有 Intel Mac 原生测试。 |

所有发布二进制文件都使用 `CGO_ENABLED=0`。

## 使用 GitHub CLI 安装已验证的 Release

请先安装 [GitHub CLI](https://cli.github.com/)。下面的命令会选择当前稳定版本和本机架构，使用 release bundle 核验 binary 与 `SHA256SUMS`，检查唯一匹配的 checksum，再把程序安装到 `$HOME/.local/bin`。

```sh
set -eu

repo='IceCodeNew/maniud'
tag="$(gh release view --repo "$repo" --json tagName --jq .tagName)"
version="${tag#v}"

case "$(uname -s):$(uname -m)" in
  Darwin:arm64)  platform='darwin_arm64' ;;
  Darwin:x86_64) platform='darwin_amd64' ;;
  Linux:aarch64 | Linux:arm64) platform='linux_arm64' ;;
  Linux:x86_64) platform='linux_amd64' ;;
  *) printf 'unsupported host: %s %s\n' "$(uname -s)" "$(uname -m)" >&2; exit 1 ;;
esac

artifact="maniud_${version}_${platform}"
bundle="maniud_${version}.sigstore.json"
workdir="$(mktemp -d "${TMPDIR:-/tmp}/maniud-install.XXXXXX")"
trap 'rm -rf "$workdir"' EXIT

gh release download "$tag" --repo "$repo" --dir "$workdir" \
  --pattern "$artifact" --pattern SHA256SUMS --pattern "$bundle"
cd "$workdir"

for subject in "$artifact" SHA256SUMS; do
  gh attestation verify "$subject" \
    --repo "$repo" \
    --bundle "$bundle" \
    --signer-workflow "$repo/.github/workflows/release.yml" \
    --source-ref refs/heads/master \
    --deny-self-hosted-runners
done

expected="$(awk -v file="$artifact" '
  $2 == file { digest = $1; count++ }
  END { if (count != 1) exit 1; print digest }
' SHA256SUMS)"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$artifact" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$artifact" | awk '{print $1}')"
fi
test "$actual" = "$expected"

install -d "$HOME/.local/bin"
install -m 0755 "$artifact" "$HOME/.local/bin/maniud"
"$HOME/.local/bin/maniud" --version
```

如果 shell 尚未从 `$HOME/.local/bin` 查找命令，请把这个目录添加到 `PATH`。任一步骤失败都应立即停止。

## 检查 SHA-256 摘要

先设置下载的文件名，再与 `SHA256SUMS` 中的确切记录比较：

```sh
artifact=maniud_X.Y.Z_linux_amd64
expected="$(awk -v file="$artifact" '$2 == file { print $1 }' SHA256SUMS)"
actual="$(shasum -a 256 "$artifact" | awk '{ print $1 }')"
test -n "$expected" && test "$actual" = "$expected"
```

如果找不到对应记录或摘要不一致，请停止安装。

## 核验 Sigstore attestation

核对二进制文件对应的仓库、Release workflow、master 分支和 bundle：

```sh
gh attestation verify "$artifact" \
  --repo IceCodeNew/maniud \
  --signer-workflow IceCodeNew/maniud/.github/workflows/release.yml \
  --source-ref refs/heads/master \
  --deny-self-hosted-runners \
  --bundle maniud_X.Y.Z.sigstore.json
```

信任任何校验和记录前，请先核验 `SHA256SUMS`：

```sh
gh attestation verify SHA256SUMS \
  --repo IceCodeNew/maniud \
  --signer-workflow IceCodeNew/maniud/.github/workflows/release.yml \
  --source-ref refs/heads/master \
  --deny-self-hosted-runners \
  --bundle maniud_X.Y.Z.sigstore.json
```

仓库、工作流、来源分支、产物摘要或校验和有一项不符时，应拒绝该下载。
完成核验并安装二进制文件后，请运行 `maniud --version` 确认发布版本。
