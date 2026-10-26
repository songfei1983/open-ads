# Open Ads Sandbox Portal 原型

这是一个无依赖的静态交互原型，用于评审 Test Portal 的广告请求流程和 SSP、DSP、DMP 服务控制台。所有控制台默认使用演示数据；创建、发布、校验、撤回和复核操作仅在当前页面的内存中模拟，不会调用真实服务或产生真实账务。

## 独立服务

Console 是 open-ads 栈的独立服务（无第三方依赖的静态 UI 服务器），对 `web/` 目录做指纹监控并自动向浏览器推送刷新（live reload，约 1.5 秒）。

### 本地 Go 服务（推荐）

```sh
make run        # go run . 默认 127.0.0.1:8083
make test
```

### Docker（compose 已内置）

```sh
docker compose up -d console   # http://127.0.0.1:8083
```

### 仅静态预览（无 live reload）

### Python

```sh
make run
```

也可以不使用 Make：

```sh
python3 -m http.server 4173 --bind 127.0.0.1 --directory console/web
```

### Docker

需要本机已安装并启动 Docker：

```sh
make docker
```

该目标会构建本地镜像并以前台方式运行容器，容器退出后自动移除。也可以分开执行 `make docker-build` 和 `make docker-run`。默认地址为 <http://localhost:8083>；使用 `make run PORT=8083` 或 `make docker PORT=8083` 修改端口，使用 `IMAGE=your-image:tag` 修改镜像名。

也可直接打开 `index.html`；部分浏览器会限制 `file:` 页面使用模块脚本。

## 服务控制台原型

- [SSP 供给平台](http://localhost:8083/ssp.html)：Publisher、Placement、Supply、DSP endpoints、配置发布。
- [DSP 投放平台](http://localhost:8083/dsp.html)：Campaign、Line Item、Creative、预算与账本、账务隔离。
- [DMP 受众平台](http://localhost:8083/dmp.html)：Data Provider、Segments、Membership Snapshots、撤回与隐私门禁。

Portal 顶栏的“服务控制台”入口进入 SSP；控制台侧栏可以切换 SSP/DSP/DMP。

## 交互

- **模拟响应**：选择 filled、no-fill 或服务错误场景，观察原型状态与诊断。
- **连接 API**：输入 SSP Sandbox base URL，向其 `/sdk/v1/ad-requests` 发送请求。要求 Sandbox 服务允许该静态页面的 Origin 发起 CORS 请求；连接失败会显示安全提示。
- 诊断摘要省略 render/click/event token，不显示用户标识或完整 OpenRTB payload。

该原型不是生产 SDK 或已接通的广告服务，不包含管理端配置、真实身份数据或真实结算能力。
