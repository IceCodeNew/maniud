# 恢复说明

[English](recovery.md) | [简体中文](recovery.zh-CN.md)

保持完整的状态目录及其 `backups` 目录不变。
不要修改其中的文件，也不要在备份目录之间移动文件。

默认状态路径如下：

```text
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/state.db
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/backups/
```

## 恢复中断的独立 apply

1. 保留中断命令使用的 Compose 文件。
2. 恢复对同一运行时、状态目录、宿主机 bind mount 路径和镜像仓库凭据的访问。
3. 运行 `maniud apply photos.yaml`，让 maniud 继续已记录的操作。

如果上次操作的结果不明确，maniud 会先检查运行时，再选择可以安全继续的操作。
运行时无法访问或结果仍不明确时，maniud 会停止并保留操作记录。

第一次收到 SIGINT 或 SIGTERM 时，maniud 会停止新的操作，保存可恢复状态，再以状态码 130 退出。
再次收到信号时，进程会立即退出，并可能中断这次清理。

## 恢复中断的 GitOps 协调

保持已注册的检出目录、状态目录、运行时工作负载和备份不变。
运行时和镜像仓库恢复后，重启由服务管理器托管的 `maniud daemon start` 进程。
daemon 会先恢复未完成的操作，再检查更新的提交。
maniud 会先验证每个服务文件，其中任何一个无效时都不会开始应用。

## 恢复失败的升级

maniud 只会使用中断操作创建的备份进行恢复。
保持该备份目录不变，恢复运行时连接和宿主机文件系统容量，再重新执行独立 `apply` 或重启由服务管理器托管的 `maniud daemon start` 进程。
不要把其他备份复制进该目录。

## 重建备份索引

先扫描备份目录，不修改状态：

```sh
maniud doctor --reindex-backups
```

确认列出的每份备份都属于预期操作后，替换索引：

```sh
maniud doctor --reindex-backups --confirm
```

确认命令会等待正在执行的服务操作，再替换完整索引。
发现备份不完整、无效或与其他记录冲突时，命令会停止。

只有在操作其他状态目录时才需要显式指定状态数据库：

```sh
maniud doctor --reindex-backups --state /srv/maniud/state.db
```

只有相同路径的只读扫描成功后，才能运行 `--confirm`。

## 保留矛盾证据

当 `apply_failed` 的 `retryable` 为 false 时，停止重试。
保留预期 Compose 文件或 Git commit、maniud 版本、完整的状态和备份目录，以及通过现有运维工具取得的只读运行时检查结果。
不要删除或重命名已观测到的工作负载，否则可能失去恢复未知操作所需的证据。
