# DMP 负载与 POC 报告归档

本目录保存 `dmp/scripts/load_poc.py` 的真实运行结果，用于 D-01 关闭后在生产拓扑上复测并逐项对比。
每次运行都会写出一个 `YYYY-MM-DD-<label>.json`，label 描述拓扑（默认 `sandbox-single-node`）。

## 复测方法

```bash
# 沙箱（单机 docker compose，DMP + ClickHouse + Valkey 同宿主）
python3 dmp/scripts/load_poc.py --rows 100000 --requests 600 --concurrency 20 \
  --label sandbox-single-node

# D-01 之后：换成生产拓扑标签，其余参数保持一致才可比
python3 dmp/scripts/load_poc.py --rows 100000 --requests 600 --concurrency 20 \
  --label prod-<topology> --report docs/plan/iteration-4/poc/<date>-prod-<topology>.json
```

脚本对以下结果做出 PASS/FAIL 判定（失败即非零退出，可直接进 CI）：

| 指标 | 目标 |
|---|---|
| resolve 错误数 | 0 |
| resolve p95 | ≤ 50 ms |
| 100k 行物理删除耗时 | ≤ 60 s |
| 删除期间服务快照可解析 | 段数不变 |

## 已归档结果

| 日期 | 拓扑 | 行数 | 导入 rows/s | p50 / p95 / p99 | 删除 | 结论 |
|---|---|---|---|---|---|---|
| 2026-10-27 | sandbox-single-node（10 vCPU，ClickHouse 24.3 单节点，Valkey 单实例，DMP 单副本） | 100k | 28,383 | 25.1 / 37.1 / 44.8 ms | 0.15 s | PASS |

### 解读与留白

- 该结果说明**单节点**已经满足 I4-07 的 p95 ≤ 50 ms 与删除 ≤ 60 s 目标，但**不覆盖**生产拓扑：
  多副本 DMP、ClickHouse 分片/副本、Valkey 哨兵或集群、跨可用区网络延迟都还没有实测。
- 删除耗时随快照所在分区的其他数据量增长；本次删除 100k 行位于独立分区，生产需按最大分区复测。
- D-01 关闭（mTLS workload identity + 生产拓扑）后必须重跑本脚本并把报告放在同一目录，用同一组参数对比，
  再决定 I4-07 是否标记为生产验证完成。
