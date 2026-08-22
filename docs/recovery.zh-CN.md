# 恢复说明

[English](recovery.md) | [简体中文](recovery.zh-CN.md)

请把状态数据库、sidecar、锁文件和 `backups` 目录作为一个整体保留。
不要手工修改数据库记录、所有权标签、备份清单或 journal 文件。

默认状态路径如下：

```text
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/state.db
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/backups/
```

## 恢复中断的独立 apply

1. 保留中断命令使用的 Compose 文件。
2. 恢复对同一运行时、状态目录、宿主机 bind mount 路径和镜像仓库凭据的访问。
3. 运行 `maniud apply photos.yaml`，让 maniud 继续已记录的事务。

如果上次操作的结果不明确，maniud 会先检查运行时，再选择可以安全继续的操作。
运行时无法访问或证据存在歧义时，事务会保持未解决状态。

第一次收到 SIGINT 或 SIGTERM 时，maniud 会停止新的操作，保存可恢复状态，再以状态码 130 退出。
再次收到 signal 时，进程会立即退出，并可能中断这次清理。

## 恢复中断的 GitOps 协调

请保持已注册的检出目录、状态目录、运行时工作负载和备份不变。
运行时和镜像仓库恢复后，重启由服务管理器托管的 `maniud daemon start` 进程。
daemon 会立即协调，先恢复未完成事务，再获取更新的提交。
它会验证完整的预期状态快照，任一服务文件无效时都不会只应用其中一部分。

## 恢复失败的升级

maniud 只会使用当前事务拥有的备份进行恢复。
请保持该备份目录不变，恢复运行时连接和宿主机文件系统容量，再重新执行独立 `apply` 或重启由服务管理器托管的 `maniud daemon start` 进程。
不要把其他备份复制进事务目录，否则身份和内容检查会拒绝它。

## 重建备份索引

先扫描备份目录，不修改状态：

```sh
maniud doctor --reindex-backups
```

确认列出的每份备份都属于预期事务后，替换索引：

```sh
maniud doctor --reindex-backups --confirm
```

确认命令会等待正在执行的服务操作，扫描完整清单，再原子替换整个索引。
发现格式错误、内容不完整或证据互相矛盾时，命令会停止。

只有在操作其他状态目录时才需要显式指定状态数据库：

```sh
maniud doctor --reindex-backups --state /srv/maniud/state.db
```

只有相同路径的只读扫描成功后，才能运行 `--confirm`。

## 保留矛盾证据

当 `apply_failed` 的 `retryable` 为 false 时，请停止重试。
请保留预期 Compose 文件或 Git commit、maniud 版本、完整的状态和备份目录，以及通过现有运维工具取得的只读运行时检查结果。
不要删除或重命名已观测到的工作负载，否则可能失去恢复未知操作所需的证据。
