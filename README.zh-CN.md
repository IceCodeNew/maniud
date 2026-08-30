# maniud

[![检查](https://github.com/IceCodeNew/maniud/actions/workflows/checks.yml/badge.svg?branch=master)](https://github.com/IceCodeNew/maniud/actions/workflows/checks.yml?query=branch%3Amaster)
[![Codecov](https://codecov.io/gh/IceCodeNew/maniud/branch/master/graph/badge.svg)](https://codecov.io/gh/IceCodeNew/maniud)
[![发布版本](https://img.shields.io/github/v/release/IceCodeNew/maniud?display_name=tag&sort=semver)](https://github.com/IceCodeNew/maniud/releases)
[![下载量](https://img.shields.io/github/downloads/IceCodeNew/maniud/total)](https://github.com/IceCodeNew/maniud/releases)

[English](README.md) | [简体中文](README.zh-CN.md)

maniud 可以把容器镜像或项目提供的 `docker run` 命令转换成便于审阅的 Compose 文件。maniud 会准备宿主机路径、预览改动、执行升级，并恢复中断的操作。支持 Docker、Podman 和 containerd。

## 两分钟上手

你需要先[安装 maniud](docs/release-verification.zh-CN.md#使用-github-cli-安装已验证的-release)，准备 Git，并启动 Docker Engine。示例镜像来自公共仓库，固定的多平台 digest 同时支持 Linux AMD64 和 ARM64。

```sh
image='registry.access.redhat.com/ubi9/ubi-micro@sha256:990002083442f6a93cd3249da32ecb7c3f6be778a1bec3a73a9c17fbc40edc15'
docker pull "$image"
maniud tui
```

首次进入时，确认或修改期望状态仓库路径，再确认创建仓库。选择 **Add service**，粘贴下面的命令：

```text
docker run --name maniud-hello --restart unless-stopped registry.access.redhat.com/ubi9/ubi-micro@sha256:990002083442f6a93cd3249da32ecb7c3f6be778a1bec3a73a9c17fbc40edc15 /usr/bin/sleep infinity
```

`maniud` 只解析这条命令，不会直接执行。检查生成的 Compose 文件和暂存区 diff。确认页默认选中 **Back**，按 Tab 选择要执行的操作，再按 Enter。提交完成后，`maniud` 会重新读取已经提交的文件并执行 dry-run，随后才提供 **Apply**。

![maniud TUI Review 页面](docs/images/tui-review.svg)

[TUI 使用指南](docs/tui.zh-CN.md)记录了完整流程、快捷键、终端要求和恢复方式。[Release 验证指南](docs/release-verification.zh-CN.md)提供带来源验证的安装步骤。

## 安装

可以把下面的提示词交给能够操作目标机器的 agent：

```text
请安装 https://github.com/IceCodeNew/maniud 的最新稳定版本。
请按 docs/release-verification.zh-CN.md 选择并核验发布文件。
请把程序安装到 PATH 中无需 sudo 即可写入的目录，再运行 maniud --version。
核验失败时立即停止，不要改动无关文件。
```

[发布版本核验说明](docs/release-verification.zh-CN.md)也提供了手动下载和核验命令。使用 [mise](https://mise.jdx.dev/) 时可以运行：

```sh
mise use --global 'github:IceCodeNew/maniud[asset_pattern=maniud_{{ version }}_{{ os(macos="darwin") }}_{{ arch(x64="amd64") }},bin=maniud]@latest'
maniud --version
```

## 使用非交互命令

先创建由 Git 管理的服务目录：

```sh
maniud gitops init "$HOME/maniud-desired-state"
cd "$HOME/maniud-desired-state/services"
```

拉取固定版本的镜像，再把项目提供的启动命令交给 `gen`：

```sh
docker pull registry.example.com/team/photos:1.4.2
maniud gen -- docker run --name photos --restart unless-stopped \
  --mount type=bind,src=/srv/photos,dst=/var/lib/photos \
  registry.example.com/team/photos:1.4.2
```

项目没有提供启动命令时，可以直接从本地镜像生成配置：

```sh
maniud gen docker://registry.example.com/team/photos:1.4.2
```

同一写法也支持 `podman://` 和 `containerd://`。固定版本便于审阅和回滚，请勿使用 `latest`。

检查生成的 Compose 文件。如果 `gen` 还创建了 `.prepare.sh` 文件，先核对其中的路径和账户，再使用所需权限运行。确认后提交这些文件：

```sh
cat photos.yaml
cat photos.prepare.sh
sudo sh photos.prepare.sh
git add photos.yaml photos.prepare.sh
git commit -m 'Add photos service'
```

先预览操作，再应用同一份文件：

```sh
maniud apply --dry-run photos.yaml
maniud apply photos.yaml
```

`apply` 只接受干净 Git 工作区中已经提交的 Compose 文件。预览失败不会修改运行时或状态。

## 从 Git 应用更新

把预期状态仓库推送到私有远端，再通过服务管理器运行 daemon：

```sh
cd "$HOME/maniud-desired-state"
git remote add origin YOUR_REPOSITORY_URL
git push -u origin master
maniud daemon start --interval 300
```

daemon 会在启动时立即检查仓库，之后按设定间隔继续检查。升级服务时，修改固定镜像版本，检查有变化的准备脚本，再提交并推送改动。

## 通知

从 [`env.example`](env.example) 复制需要的设置到进程环境。Bark 和 Telegram 可以同时启用。通知发送失败会单独报告，不会改变操作结果。

## 恢复中断的操作

保持 Compose 文件、状态目录、运行时工作负载和备份不变。再次执行同一条 `apply` 命令，或者重启由服务管理器托管的 daemon。编辑状态或备份前，先阅读[恢复说明](docs/recovery.zh-CN.md)。

运行 `maniud COMMAND --help` 可以查看命令语法，[错误参考](docs/errors.zh-CN.md)列出了失败代码。[自定义构建说明](docs/custom-builds.zh-CN.md)介绍了如何只加入需要的运行时。
