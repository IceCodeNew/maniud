# maniud

[![检查](https://github.com/IceCodeNew/maniud/actions/workflows/checks.yml/badge.svg?branch=master)](https://github.com/IceCodeNew/maniud/actions/workflows/checks.yml?query=branch%3Amaster)
[![Codecov](https://codecov.io/gh/IceCodeNew/maniud/branch/master/graph/badge.svg)](https://codecov.io/gh/IceCodeNew/maniud)
[![发布版本](https://img.shields.io/github/v/release/IceCodeNew/maniud?display_name=tag&sort=semver)](https://github.com/IceCodeNew/maniud/releases)
[![下载量](https://img.shields.io/github/downloads/IceCodeNew/maniud/total)](https://github.com/IceCodeNew/maniud/releases)
[![Go 版本](https://img.shields.io/github/go-mod/go-version/IceCodeNew/maniud)](go.mod)
[![Go 文档](https://pkg.go.dev/badge/github.com/IceCodeNew/maniud/containerconfig.svg)](https://pkg.go.dev/github.com/IceCodeNew/maniud/containerconfig)
[![Go Report Card](https://goreportcard.com/badge/github.com/IceCodeNew/maniud)](https://goreportcard.com/report/github.com/IceCodeNew/maniud)

[English](README.md) | [简体中文](README.zh-CN.md)

该项目旨在让容器镜像真正获得像 `.exe` 文件那样「一键运行」的体验。

是否遇到过以下问题：

- 上游只发布镜像，没有提供可用的启动方法。
- 容器挂载宿主机目录后，因为 UID 或 GID 与目录所有者不一致而无法读写。
- 每次镜像发布新版本，都要手工跟进并替换正在运行的容器。
- 为了自动升级而使用 `latest`，结果未经审阅的新镜像导致服务中断。

有人在 Twitter 开源算法的仓库里直接发了一句：[「I DONT GIVE A FUCK ABOUT THE FUCKING CODE!」](https://github.com/twitter/the-algorithm/issues/1999)。
很多人只想把应用跑起来，没兴趣先研究容器项目的代码。
maniud 可以把复制来的容器命令或已经拉取的镜像转换成便于审阅的 Compose 文件，准备宿主机路径，并安全地应用和升级服务。

## 安装

第一次安装时，可以把下面的提示词交给能够操作当前机器的 agent：

```text
请在这台机器上安装 https://github.com/IceCodeNew/maniud 的最新稳定版本。
请先阅读 https://github.com/IceCodeNew/maniud/blob/master/docs/release-verification.zh-CN.md，识别当前操作系统和 CPU 架构，并按文档下载和核验对应的发布文件。
请把程序安装到 PATH 中现有且无需 sudo 即可写入的用户目录，再运行 maniud --version。
任何核验失败都应停止安装并报告原始错误，不得绕过核验，也不得改动无关文件。
```

熟悉命令行的用户可以通过 [mise](https://mise.jdx.dev/) 安装并更新 maniud：

```sh
mise use --global 'github:IceCodeNew/maniud[asset_pattern=maniud_{{ version }}_{{ os(macos="darwin") }}_{{ arch(x64="amd64") }},bin=maniud]@latest'
maniud --version
```

## Day 0：创建并启动服务

先创建包含初始提交的 GitOps 工作区，再进入新建的空 `services` 目录：

```sh
maniud gitops init "$HOME/maniud-desired-state"
cd "$HOME/maniud-desired-state/services"
```

镜像项目提供的使用说明可能只有下面两条命令：

```sh
docker pull registry.example.com/team/photos:1.4.2
docker run --name photos --restart unless-stopped --mount type=bind,src=/srv/photos,dst=/var/lib/photos registry.example.com/team/photos:1.4.2
```

先拉取固定版本的镜像，再把复制来的启动命令交给 `gen`：

```sh
docker pull registry.example.com/team/photos:1.4.2
maniud gen -- docker run --name photos --restart unless-stopped --mount type=bind,src=/srv/photos,dst=/var/lib/photos registry.example.com/team/photos:1.4.2
```

如果上游没有提供可用的启动命令，需要在镜像前标明本地运行时：

```sh
docker pull registry.example.com/team/photos:1.4.2
maniud gen docker://registry.example.com/team/photos:1.4.2
```

maniud 还支持 `podman://` 和 `containerd://` 运行时。
不推荐使用 `latest` 等可变标签，因为同一份配置以后可能指向另一份镜像；Day 1 的 GitOps 流程会自动应用经过审阅的升级提交，并允许通过 revert 回滚。
如果本地没有该镜像，`gen` 会停止，并在标准错误中打印对应的拉取命令。
镜像无法声明当前站点使用的凭据、公开 URL 或全部存储路径，因此 `gen` 也会提醒补齐缺少的应用设置后再提交文件。

`gen` 默认写入 `photos.yaml`，服务使用宿主机 bind mount 时还会生成 `photos.prepare.sh`。
生成准备脚本后，程序会在标准错误中提醒先审阅并执行脚本，再运行 `apply`。

```sh
cat photos.yaml
cat photos.prepare.sh
```

执行脚本前应检查其中的每个路径和账号。
脚本把缺失的 bind mount 来源标为 `directory`；如果容器需要文件，请把对应行的这个单词改成 `file`，再使用处理这些宿主机路径所需的权限运行修改后的脚本。

```sh
sudo sh photos.prepare.sh
```

`apply` 只接受干净 Git 工作区中已经提交的 Compose 文件，因此审阅生成文件后要先提交：

```sh
git add photos.yaml photos.prepare.sh
git commit -m 'Add photos service'
```

允许 maniud 修改运行时和状态前，先预览部署计划：

```sh
maniud apply --dry-run photos.yaml
```

预览通过时，命令以状态码 0 退出，并用简短文本说明接下来会做什么：

```text
Dry run passed for photos/photos.
Action: create a new workload (bootstrap).
Runtime: docker on linux/amd64.
Image: registry.example.com/team/photos:1.4.2@sha256:….
Ready to apply. No changes were made.
```

输出以 `Ready to apply` 结束并返回状态码 0 时，可以执行同一份计划。
非零退出状态表示预览失败，命令会直接说明失败原因。
`maniud apply --help` 列出了每种动作，以及可选详细 JSON 模式返回的字段。

预览结果符合预期后，应用同一份文件：

```sh
maniud apply photos.yaml
```

## Day 1：转为 GitOps 运维

通过常用的 Git 托管流程创建一个空的专用远端仓库，再把 Day 0 的提交推送到 `origin`：

```sh
cd "$HOME/maniud-desired-state"
git remote add origin YOUR_REPOSITORY_URL
git push -u origin master
```

通过服务管理器运行 daemon：

```sh
maniud daemon start --interval 300
```

daemon 会在启动时立即协调一次，之后按设定的间隔继续检查。
它会先确认本地分支和 `origin` 可以安全快进，再接受后续更新；修改任何工作负载前，daemon 会验证完整的 `services/` 快照，并先恢复未完成事务。
以后升级镜像时，请在 `services/` 中修改到新的固定版本，审阅并执行有变化的准备脚本，再提交和推送改动。
daemon 会在下一轮自动应用该提交。

## 恢复或回滚

如果独立运行的 `apply` 中途停止，请保持 Compose 文件、状态目录、运行时工作负载和备份不变，再次执行同一条命令。

```sh
maniud apply photos.yaml
```

如果 GitOps 协调中途停止，请保持已注册的检出目录和状态不变，再重启由服务管理器托管的 `maniud daemon start` 进程。
daemon 会立即协调，并在获取更新的提交前恢复已有事务。

如果升级已经完成，但新版本无法正常工作，请 revert 对应的 Git 提交并推送，让已注册分支继续向前推进：

```sh
git -C "$HOME/maniud-desired-state" revert UPGRADE_COMMIT
git -C "$HOME/maniud-desired-state" push
```

手工修改状态文件、运行时所有权标签或备份前，请先阅读[恢复说明](docs/recovery.zh-CN.md)。

## 运行时连接

Docker 读取 `DOCKER_HOST`，默认连接 `unix:///var/run/docker.sock`。
Podman 先读取 `CONTAINER_HOST`，未设置时再检查标准的用户和 root socket。
containerd 要求 `CONTAINERD_ADDRESS` 指向本地 Unix socket，并通过 `CONTAINERD_NAMESPACE` 指定 namespace。
maniud 把状态保存在 `${XDG_STATE_HOME:-$HOME/.local/state}/maniud`。
`DOCKER_CONFIG` 用于选择镜像仓库凭据。

运行 `maniud COMMAND --help` 可以查看确切语法；遇到命令失败时可查阅[错误参考](docs/errors.zh-CN.md)，操作中断后可查阅[恢复说明](docs/recovery.zh-CN.md)。
