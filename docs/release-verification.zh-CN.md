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
