# open-ads

自研广告投放栈原型:SSP(供给)) → DSP(竞价+预算租约) → DMP(受众/隐私),配套专用数据面与控制台 UI。

## 服务与端口(本地 127.0.0.1)

| 服务 | 端口 | 说明 |
|---|---|---|
| SSP Client/API | `8080` | 广告请求、OpenRTB auction、同源 render 代理 |
| DSP | `8081` | 决策器、发布管理 API(`/admin/v1`)、render 端点 |
| DMP | `8082` | 受众 resolve、身份撤回 |
| Console | `8083` | 独立沙箱控制台(静态服务,带 live-reload) |
| MariaDB | `3306` | SSP(`ssp_db`)/ DSP(`dsp_db`) |
| Valkey | `6379` | 租约/发布的跨实例协调,ACL 按 `ssp:` `dsp:` `dmp:` 前缀隔离 |
| ClickHouse | `9000` / `8123` | DMP 事件面 |

凭据与卷:见 `deployments/docker-compose.yml`(沙箱明文,勿用于生产)。

## 代码布局

多模块仓库,每个服务独立 go.mod:

```
ssp/      供给平台(go run ./cmd/ssp)
dsp/      需求平台 + 决策器 + 租约(lease/)+ 内容寻址素材库(assets/)
dmp/      数据平台(ClickHouse migrations 见 dmp/migrations)
console/  沙箱控制台 UI(console/web)
deployments/  docker-compose + valkey ACL + mariadb init
.github/workflows/go.yml  CI:fmt/vet/test(-race) + compose 集成冒烟
```

## 本地开发

```sh
docker compose -f deployments/docker-compose.yml up -d   # 数据面 + 服务( dev target 带 air 热重载)
docker compose -f deployments/docker-compose.yml up -d --wait mariadb valkey clickhouse   # 仅数据面
make -C console run        # 单独起控制台(默认 :8083)
```

控制台 <http://127.0.0.1:8083>;投放冒烟:`POST /sdk/v1/ad-requests`(728x90 / `publisher.test`/`demo`)。

Go 版本:模块统一要求 1.27.3+(标准库安全修复 GO-2026-6809)。
