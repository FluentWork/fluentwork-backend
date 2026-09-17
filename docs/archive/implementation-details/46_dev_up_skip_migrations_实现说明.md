# `dev-up.sh --skip-migrations`

**对应**：本地重启踩坑（2026-09-10 22:30 重启验证）。**不是**协议 / 业务行为变更。  
**代码**：`scripts/dev-up.sh`  
**门禁**：`bash -n` 通过；双向实跑验证见 §测试

## 原理与背景

**迁移是 apply-once 的,重启要能复用已有 schema。**

`0008` 起的迁移都是 `ALTER TABLE ... ADD COLUMN`,没有幂等保护。`dev-up.sh --local-mysql` / `--mysql` 每次都重放 `migrations/*.sql`,于是**第二次之后的每一次启动都会挂**：

```
Applying 0008_alter_utterances_add_llm_eval.sql to local MySQL
ERROR 1060 (42S21) at line 5: Duplicate column name 'llm_eval_json'
```

`set -euo pipefail` 让脚本当场退出,服务起不来 —— 但库其实完全没问题。这个坑与当天的代码改动无关,只是重启才暴露。

## 方案

新增 `--skip-migrations`,把两个 `apply_migrations*` 调用收进一个守卫：

```bash
run_migrations() {
  if [[ "$SKIP_MIGRATIONS" -eq 1 ]]; then
    echo "Skipping migrations (--skip-migrations); using the schema already in MySQL."
    return 0
  fi
  "$@"
}
```

调用点改为 `run_migrations apply_migrations_local` / `run_migrations apply_migrations`,两条路径都覆盖。

`CREATE DATABASE IF NOT EXISTS` / `CREATE USER IF NOT EXISTS` **不跳过** —— 它们本来就幂等,而且库不存在时仍需要。

不带 MySQL 时给一条提示,避免误以为生效：

```
note: --skip-migrations has no effect without --mysql / --local-mysql;
      the default in-memory mode runs no migrations.
```

`scripts/dev-local.sh` 走 `exec ... dev-up.sh --local-mysql "$@"`,`"$@"` 已转发参数,所以 `./scripts/dev-local.sh --skip-migrations` 直接可用,**该脚本无需改动**。

## 新方案理由

- **不改迁移本身**：把 `0008`+ 改成幂等（`ADD COLUMN IF NOT EXISTS` 之类）要碰历史迁移文件,属于高风险路径（CLAUDE.md「数据库迁移 — 不可逆操作」）,而且已应用的库里改历史迁移毫无收益。
- **不做自动探测**：判断「schema 是否已是最新」需要一个迁移版本表,现在没有;引入它是一次独立的重构,不该塞进一个 dev 脚本的参数。
- **默认仍是「跑迁移」**：新克隆的库第一次启动必须能用,不能把跳过设成默认。

## 影响面

- **代码**：只动 `scripts/dev-up.sh`,不碰任何 Go 代码、迁移、配置。
- **行为**：不加 `--skip-migrations` 时行为与之前**完全一致**。
- **风险**：带着该 flag 启动一个 schema 落后的库,服务会在运行时报错而不是在启动时。这是使用者显式选择的代价,提示文案已说明「using the schema already in MySQL」。

## 测试

外壳脚本没有单测框架（`dev-check.sh` 只跑 gofumpt / goimports / golangci-lint / go test / go build）,所以用**双向实跑**验证,并在备用端口上做,不干扰正在运行的服务。

**带 flag（期望成功）**

```
$ PORT=18080 GATEWAY_PORT=18081 AUTO_CORPUS_SEED=0 \
    ./scripts/dev-up.sh --local-mysql --skip-migrations
备用栈就绪(第 2s)
迁移执行次数: 0
Duplicate column 错误: 0
Skipping migrations (--skip-migrations); using the schema already in MySQL.
```

**不带 flag（期望复现失败）**

```
$ PORT=18080 GATEWAY_PORT=18081 AUTO_CORPUS_SEED=0 \
    ./scripts/dev-up.sh --local-mysql
退出码: 1
ERROR 1060 (42S21) at line 5: Duplicate column name 'llm_eval_json'
```

**回归**：`bash -n scripts/dev-up.sh` 通过；`--help` 列出新参数；两次实跑后 8080/8081 上的既有服务未受影响（备用端口已释放）。

## 明确不做

- 不给迁移加幂等保护或版本表
- 不改 `dev-local.sh`
- 不改默认行为（默认仍执行迁移）
