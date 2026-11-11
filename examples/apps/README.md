# 示例应用包（examples/apps）

> 用途：产品层的**可安装应用包样板**，用于演示「运行期装卸（应用体系二期 C）」的 install 能力。
> 这些是**示例**，不是内置应用——ark-mate 启动时 `apps` 表为空，不含任何内置 app（历史没有 app，不臆造）。

## 目录结构

每个应用包是一个子目录，目录名 = `manifest.json` 里的 `id`：

```
examples/apps/<id>/
  manifest.json      # 包分发清单（唯一声明「包提供什么」）
  entrypoint.sh      # 主机启动入口（app-host 子进程按此拉起）
```

`manifest.json` 的 `integrity.sha256` 是**对 `entrypoint.sh` 文件内容求的 sha256**（小写 64 位十六进制）。
改了 `entrypoint.sh` 就必须重算并同步到清单，否则 `Scan`/`Distribute` 会以「完整性校验失败」拒绝装载。

重算命令：

```sh
shasum -a 256 examples/apps/weather-query/entrypoint.sh
```

## 现有样例

| id | 导航名 | 工具域 | impl | 声明权限 |
| --- | --- | --- | --- | --- |
| `weather-query` | 天气查询 | `weather`（forecast / current） | `weather.sync` | `net:domain` |
| `daily-digest` | 每日简报 | `digest`（build / list） | `digest.build` | `read:usage` |
| `doc-reviewer` | 文档审阅 | `docreview`（scan / summarize） | `docreview.scan` | `file:domain` |

## 怎么安装

`InstallApp` 接收的来源 `dir` 是**包的根目录**（`examples/apps/<id>`），服务端会扫描其**父目录**
（`examples/apps`）后定位到该包，再做清单/完整性/版本门槛校验并原子拷贝到本地应用目录
（`~/.dsh/mate/apps/<id>`，可用 `MATE_APPS_DIR` 覆盖）。

REST（HTTP 侧注入了运行期端口，install 真正可用）：

```sh
curl -X POST http://127.0.0.1:<port>/v1/mate/apps/install \
  -H 'Content-Type: application/json' \
  -d '{"dir":"<仓库绝对路径>/examples/apps/weather-query","enable":false}'
```

- `enable=false`（推荐先这样）：只装包 + 建最小定义，`state=off`，可随后在对话里 `app_set_enabled` 启用。
- `enable=true`：装完立即起运行期（拉起 `app-host` 子进程 + apply slot）。

卸载：`DELETE /v1/mate/apps/<id>/uninstall`（先停运行期 + 清包目录，再删定义，幂等）。

## 从应用市场一键安装（M-A7）

这三个示例包同时作为内置市场清单（`backend/internal/infra/appmarket/market.json`，`go:embed`）的首批条目，
可在前端「扩展 › 应用市场」页直接看到并一键「安装」，无需手动填本地路径。

- 市场清单里每条的 `source.dir` 是**相对来源根**的路径（如 `apps/weather-query`）。
- **来源根**来源：
  1. `MATE_APP_SOURCE_ROOTS`（冒号分隔的**绝对路径**，可多个）；
  2. 默认发行根：随发行包分发的 `apps/` 目录（可执行文件同级优先，其次当前工作目录）。

  本地开发想从市场装这些示例包，把来源根指到本目录的**上级**（它下面有 `apps/`）：

  ```sh
  export MATE_APP_SOURCE_ROOTS="<仓库绝对路径>/examples"
  ```

  这样清单 `dir=apps/weather-query` 会解析到 `<仓库>/examples/apps/weather-query`，`GET /apps/market`
  里该条 `installable=true` 且 `source.dir` 回填为**绝对路径**（前端直接透传给 `POST /apps/install {dir}` 即可装通）。

- **安全边界**：`source.dir` 解析后必须落在某个来源根内（`withinDir` 校验），否则该条 `installable=false`，
  防止市场清单指向 `/etc` 之类系统路径被当包装载。装包时仍复用 `apppkg` 的清单合法性 + 完整性 sha256 +
  min core version 门槛（与手动 install 同一条链路，市场不新开执行面）。

- **版本门**：示例包声明 `min_core_version=1.0.0`；dev 构建的核心版本是 `dev`，会被版本门 fail-closed 拒绝
  （这是正确行为）。用发布构建（`-ldflags` 注入真实版本）或测试里置版本后即可装通。

## 校验现状

这 3 个包已用 `apppkg.Scan` 实测通过：`Loaded=3 Failed=0`（清单合法 + entrypoint sha256 匹配）。
另有一条端到端测试（`backend/internal/bootstrap/assembly/app_market_test.go`）实测走「来源根 → 市场派生 →
`svcapp.InstallApp`」把 `weather-query` 装通。
