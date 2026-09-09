# Ruijie Auto Login

一个基于 Go 实现的锐捷校园网自动认证工具。

无需打开浏览器，通过 HTTP 请求直接完成锐捷 ePortal 认证，并持续检测网络状态。断线后会自动重新获取当前 Portal 参数，并从账号列表中轮询账号进行重新认证。

## 功能

- 无需浏览器即可完成锐捷校园网认证
- 自动发现锐捷 ePortal
- 自动获取动态认证参数
- 支持多个账号
- 程序启动时随机选择账号作为轮询起点
- 后续按照账号列表顺序轮询
- 启动时自动检测当前是否已经存在登录用户
- 已登录时不会重复认证
- 运行过程中持续检测 Portal 登录状态与互联网连通性
- **三态状态机**：Portal 状态区分 `ONLINE` / `OFFLINE` / `UNKNOWN`，网络抖动不会被误判为掉线
- **掉线连续确认**：连续 3 次 `OFFLINE` 才确认掉线并触发账号轮换，避免瞬时抖动导致无意义重登
- **互联网检测与登录状态解耦**：`CheckInternet` 失败只记录日志，绝不触发重新登录
- 网络断开后自动重新认证
- 当前账号认证失败后自动尝试下一个账号
- 所有账号均失败后自动等待并重新尝试
- 支持 `--status`、`--logout`、`--once`、`--help` 命令行参数
- 配置文件修改账号后无需重新编译

## 工作原理

本项目针对锐捷 ePortal Web 认证流程实现。

未认证状态下，程序请求参数探测地址：

```text
http://119.29.29.29/
```

锐捷网关会拦截该 HTTP 请求，并在响应正文中返回类似：

```html
<script>
  top.self.location.href =
    "http://172.16.32.240/eportal/index.jsp?wlanuserip=...&wlanacname=...&mac=...";
</script>
```

程序从响应正文中提取完整的 ePortal 登录 URL，解析其 query 作为登录参数，然后调用：

```text
POST /eportal/InterFace.do?method=login
```

提交：

```text
userId
password
service
queryString
operatorPwd
operatorUserId
validcode
passwordEncrypt
```

其中 `service` 根据当前校园网环境为空。

登录成功后，锐捷返回：

```json
{
  "userIndex": "...",
  "result": "success"
}
```

随后程序通过：

```text
GET /eportal/InterFace.do?method=getOnlineUserInfo&userIndex=
```

查询当前认证用户（`userIndex` 为空时服务器按来源 IP 自动定位会话）。

## 登录流程

```text
程序启动
    │
    ▼
检测当前是否已经登录（三态）
    │
    ├── ONLINE（已登录）
    │     │
    │     ▼
    │   获取当前用户信息
    │     │
    │     ▼
    │   开始网络监控
    │
    ├── UNKNOWN（查询失败/超时）
    │     │
    │     ▼
    │   不贸然登录，进入监控模式继续检测
    │   （由监控状态机在状态明确后再决定是否登录）
    │
    └── OFFLINE（未登录）
          │
          ▼
      随机选择账号起点
          │
          ▼
      按顺序轮询账号
          │
          ├── 登录成功
          │      │
          │      ▼
          │    开始网络监控
          │
          └── 登录失败
                 │
                 ▼
              下一个账号
                 │
                 ▼
              轮完一圈
                 │
                 ▼
             等待后重新尝试
```

## 账号轮询规则

账号列表保持配置文件中的顺序。

例如：

```text
账号 A
账号 B
账号 C
账号 D
```

程序启动时随机选择起点。

如果随机到 `C`：

```text
C → D → A → B → C → ...
```

如果随机到 `A`：

```text
A → B → C → D → A → ...
```

当当前账号掉线后，从当前账号的下一个账号开始尝试。

这种方式可以避免程序每次启动都优先使用第一个账号，同时保持实现简单。

## 项目结构

```text
ruijie-auto-login/
│
├── go.mod
├── config.json
│
├── internal/
│   └── ruijie/
│       ├── config.go
│       ├── portal.go
│       ├── portal_test.go
│       ├── auth.go
│       └── monitor.go
│
├── cmd/
│   └── autologin/
│       └── main.go
│
└── shell/
    ├── autologin.sh
    ├── config.sh.example
    └── config.sh          # 本地配置，不提交到仓库
```

### `internal/ruijie/config.go`

负责：

- 读取 `config.json`
- 解析账号列表
- 加载程序运行参数

### `internal/ruijie/portal.go`

负责：

- 自动发现 ePortal
- 获取动态 `queryString`
- 查询当前在线用户（`getOnlineUserInfo`，GET 请求）
- 检测互联网连接状态
- 注销当前登录

### `internal/ruijie/auth.go`

负责：

- 锐捷登录
- 登录结果解析
- 登录后的状态确认

### `internal/ruijie/monitor.go`

负责：

- 账号轮询
- 当前账号管理
- 网络状态监控
- 掉线自动重新认证

### `cmd/autologin/main.go`

自动登录主程序入口，支持 `--status`、`--logout`、`--once`、`--help` 参数。

## 配置

创建 `config.json`：

```json
{
  "portalBase": "http://172.16.32.240",
  "checkInterval": 10,
  "retryInterval": 5,
  "requestTimeout": 10,
  "accounts": [
    {
      "username": "账号A",
      "password": "密码A"
    },
    {
      "username": "账号B",
      "password": "密码B"
    },
    {
      "username": "账号C",
      "password": "密码C"
    }
  ]
}
```

### 配置项

| 配置项           | 说明                                     |
| ---------------- | ---------------------------------------- |
| `portalBase`     | 锐捷 ePortal 地址                        |
| `checkInterval`  | 正常运行时网络检测间隔，单位秒           |
| `retryInterval`  | 所有账号登录失败后的重试等待时间，单位秒 |
| `requestTimeout` | HTTP 请求超时时间，单位秒                |
| `accounts`       | 账号列表                                 |

修改账号密码只需要修改 `config.json`，不需要重新编译程序。

## 编译

首先初始化依赖：

```bash
go mod tidy
```

项目只使用 Go 标准库，交叉编译不需要任何额外工具链。以下命令在 Windows PowerShell 中执行（Linux/macOS 下把 `$env:XXX="yyy"` 换成 `export XXX=yyy` 即可）。

### Windows（amd64）

```powershell
$env:GOOS="windows"; $env:GOARCH="amd64"; go build -o ruijie-autologin.exe ./cmd/autologin
```

生成：

```text
ruijie-autologin.exe
```

### Android / Termux / NetHunter（aarch64）

对应 `uname -a` 显示 `aarch64` 的 Android 环境，统一使用 `GOOS=linux`：

```powershell
$env:GOOS="linux"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"; go build -o ruijie-autologin-android ./cmd/autologin
```

生成的 `ruijie-autologin-android` 传入手机后：

```bash
chmod +x ruijie-autologin-android
./ruijie-autologin-android --once
```

注意：不要使用 `GOOS=android`。它会把 ELF 解释器设为 `/system/bin/linker64`，在 Termux 中可以直接运行，但在 NetHunter 等 chroot 环境里看不到 `/system`，执行时会报 `没有那个文件或目录`。`GOOS=linux` 编译出的是静态链接二进制，Termux 和 NetHunter chroot 都能直接运行。

### iOS / iSH（i686）

iSH 是 iOS 上的 i686（x86 32 位）Linux 模拟环境，对应 `uname -a` 显示 `i686 Linux`，因此使用 `GOOS=linux GOARCH=386`：

```powershell
$env:GOOS="linux"; $env:GOARCH="386"; $env:CGO_ENABLED="0"; go build -o ruijie-autologin-ish ./cmd/autologin
```

生成的 `ruijie-autologin-ish` 传入 iSH 后：

```bash
chmod +x ruijie-autologin-ish
./ruijie-autologin-ish --once
```

iSH 不适合长期后台运行，建议使用 `--once` 配合 iOS 快捷指令完成单次认证。

## 运行

### 命令行参数

```text
Usage:
  autologin [options]

不带参数时：
  持续监控在线状态，掉线后自动重新登录

Options:
  --status    查询当前在线状态
  --logout    注销当前登录
  --once      单次检查并登录，成功后退出
  --help      显示帮助
```

### 默认模式（持续监控）

```bash
ruijie-autologin.exe
```

持续检查校园网在线状态，检测到掉线后自动按账号轮询策略重新登录。

### 查询状态

```bash
ruijie-autologin.exe --status
```

只查询当前认证状态并输出在线/离线结果，不进行登录，查询完成后立即退出。

### 注销

```bash
ruijie-autologin.exe --logout
```

注销当前锐捷登录账号后退出，不进入监控，不自动重新登录。

### 单次认证（iOS / iSH 场景）

```bash
ruijie-autologin.exe --once
```

检查状态 → 已在线则直接退出；离线则获取登录参数、按账号轮询策略登录、验证成功后立即退出。不进入持续监控，适合在 iSH 中由快捷指令启动。所有账号都失败时返回非 0 退出码。

开发阶段也可以直接：

```bash
go run ./cmd/autologin
```

## Shell 版本

项目额外提供了一个纯 POSIX Shell 实现，位于 `shell/autologin.sh`。

它不依赖 Go、Python、Node.js、jq，只依赖 `sh`、`curl`、`sed`、`grep`、`awk`、`sleep`，可以直接在以下环境运行：

- iOS + iSH
- Android + Termux
- 常见 Linux

### 准备配置

Shell 版本不复用 `config.json`（避免依赖 jq），使用独立的 `shell/config.sh`：

```bash
cd shell
cp config.sh.example config.sh
# 编辑 config.sh，填写 PORTAL_BASE 和账号列表
```

账号格式为每行一个 `账号|密码`：

```text
ACCOUNTS="
账号A|密码A
账号B|密码B
"
```

### 运行

```bash
chmod +x shell/autologin.sh

./shell/autologin.sh            # 持续监控，掉线自动重新登录
./shell/autologin.sh --status   # 查询当前在线状态
./shell/autologin.sh --logout   # 注销当前登录
./shell/autologin.sh --once     # 单次检查并登录，成功后退出
./shell/autologin.sh --help     # 显示帮助
```

`--once` 专为 iSH / Termux 设计：登录成功后立即退出，不进入持续监控，适合由系统快捷指令启动。

Shell 版本与 Go 版本使用完全相同的锐捷认证流程和监控状态机：

- 通过 `http://119.29.29.29/` 发现 ePortal 登录参数
- `GET /eportal/InterFace.do?method=getOnlineUserInfo` 查询在线状态（空 `userIndex`，服务器按来源 IP 定位）
- `POST /eportal/InterFace.do?method=login` 登录
- `result=wait` 但带 `userId`/`userIp` 也视为在线
- 随机账号起点 + 顺序轮询
- **三态状态机**（`check_status` 返回 0=在线 / 1=离线 / 2=未知）
- **连续 3 次 OFFLINE 才确认掉线**，互联网检测（Apple success.html）只记日志不触发重登
- 状态变化横幅 + 时间戳日志，与 Go 版本格式一致

## 当前登录用户检测

程序启动时直接以空 `userIndex` 调用：

```text
GET /eportal/InterFace.do?method=getOnlineUserInfo&userIndex=
```

服务器按来源 IP 自动定位会话，返回当前认证用户信息：

```text
在线 → {"userIndex":"...","result":"success","userId":"...","userIp":"..."}
离线 → {"userIndex":null,"result":"fail","userId":null,"userIp":null}
```

在线响应中自带 `userIndex`，注销时直接复用。

> **实测说明**：
> 最初使用 `redirectortosuccess.jsp` 的 302 跳转提取 `userIndex`，
> 但部分网关即使在线也只返回空 `Location: http://`，导致在线被误判为离线。
> 旧方案已彻底弃用，改为直接调用 `getOnlineUserInfo`（GET），服务器按来源 IP 定位会话，
> 与浏览器门户页的行为一致。

另外，最初使用 POST 提交 `userIndex`，但部分锐捷网关对 POST 请求超时
（`context deadline exceeded`），同一地址改用 GET 后正常返回，因此改为 GET。

例如：

```text
状态: 已登录
账号: 12345678910
IP: 172.16.26.11
MAC: ec4255cc00c1
```

需要注意的是，部分锐捷 ePortal 在认证状态刚建立时可能返回：

```json
{
  "result": "wait",
  "message": "用户信息不完整，请稍后重试",
  "userId": "12345678910",
  "userIp": "172.16.26.11"
}
```

因此本项目不会仅根据 `result` 判断是否登录，而会结合用户信息判断当前认证状态。

## 状态三态与掉线确认

监控中 Portal 登录状态区分三态：

| 状态      | 含义       | 判定依据                                                                |
| --------- | ---------- | ----------------------------------------------------------------------- |
| `ONLINE`  | 明确在线   | `getOnlineUserInfo` 返回 `userId` + `userIp` 完整                      |
| `OFFLINE` | 明确未登录 | `getOnlineUserInfo` 返回 `result=fail`，`userId`/`userIp` 为空         |
| `UNKNOWN` | 无法确定   | 查询超时、网络失败、信息不完整等一切"没问明白"的情况                    |

核心原则：

- **网络请求失败绝不等于 OFFLINE**。超时、断网等情况一律归为 `UNKNOWN`，等待下一轮检测
- **互联网连通性检测（CheckInternet）与登录状态完全解耦**。`CheckInternet` 失败只打印日志（"Portal 会话仍然有效，不重新登录"），绝不触发账号轮换
- **掉线需要连续 3 次确认**。单次 `OFFLINE` 可能只是 Portal 侧瞬时抖动；连续 3 次 `OFFLINE` 才确认掉线并开始账号轮换。期间恢复 `ONLINE` 立即清零计数；`UNKNOWN` 不累计也不清零

```text
监控循环
    │
    ├── ONLINE
    │     清零掉线计数，打印心跳日志
    │     （互联网检测失败只记录，不重登）
    │
    ├── UNKNOWN
    │     打印原因（查询超时/网络失败），暂不重新登录
    │
    └── OFFLINE
          连续确认 1/3, 2/3 → 等待下一轮
          连续确认 3/3     → 确认掉线
                             → 账号轮换 + 重新登录
                             → 成功后恢复监控
```

> **为什么这样设计**：登录成功后刚建立会话的窗口期内，
> 网关对新连接的放行存在传播延迟，此时互联网检测可能临时失败，
> 但 Portal 会话实际有效。如果用 `CheckInternet` 判断掉线，
> 会触发无意义的账号轮换（且在线状态下参数探测地址返回 404，
> 拿不到 ePortal URL，轮换必然失败）。实测抓包已验证该场景真实存在。

## 注销原理

`--logout` 先以空 `userIndex` 调用：

```text
GET /eportal/InterFace.do?method=getOnlineUserInfo&userIndex=
```

服务器按来源 IP 定位会话，在线响应中自带 `userIndex`，然后调用：

```text
POST /eportal/InterFace.do?method=logout
```

提交：

```text
userIndex=...
```

注销不需要账号密码。实测响应：`{"result":"success","message":"下线成功！"}`

## 网络监控

程序运行过程中会周期性（默认每 10 秒，`checkInterval` 配置）检查 Portal 登录状态与互联网连通性。

监控基于三态状态机（详见上文"状态三态与掉线确认"）：

- 状态无变化时打印单行心跳日志
- Portal 状态发生变化时打印详细横幅（时间戳、变化前后状态、账号、处理动作）
- 互联网状态单独跟踪，变化时也打印横幅，但绝不触发重新登录

确认掉线（连续 3 次 `OFFLINE`）后的处理流程：

```text
当前账号掉线（连续 3 次确认）
    ↓
切换到下一个账号
    ↓
获取新的 Portal 参数（请求 http://119.29.29.29/）
    ↓
重新认证
    ↓
登录成功
    ↓
继续监控
```

> 注意：参数探测地址 `http://119.29.29.29/` **只在确认需要重新登录时才请求**，
> 正常监控状态下不会反复请求它。在线状态下该地址会返回 404（网关直接放行），
> 拿不到 ePortal URL——这正是掉线误判会导致登录必然失败的原因。

## 安全说明

`config.json` 中保存的是校园网账号密码，请妥善保护该文件。

不要将包含真实账号密码的：

```text
config.json
```

提交到公开 Git 仓库。

建议在 `.gitignore` 中加入：

```gitignore
config.json
```

## 已验证的锐捷接口

当前实现基于实际抓包验证的 ePortal 接口：

```text
GET  /eportal/InterFace.do?method=getOnlineUserInfo

POST /eportal/InterFace.do?method=login

POST /eportal/InterFace.do?method=logout
```

> `redirectortosuccess.jsp` 曾用于提取 `userIndex`，但部分网关对该接口返回异常
> （即使在线也返回空 Location），已彻底弃用。

认证流程中的动态参数来自：

```text
http://119.29.29.29/
```

## 当前状态

目前项目已经验证：

- 锐捷 Portal 自动发现
- 动态 `queryString` 获取
- HTTP 直接登录
- 登录成功状态确认
- 当前登录用户检测（`getOnlineUserInfo` GET 请求，空 `userIndex` 按来源 IP 定位）
- 多账号顺序轮询
- 启动随机轮询起点
- 掉线重新认证（三态状态机 + 连续 3 次确认）
- `--logout` 注销当前登录
- 浏览器完整认证流程抓包比对：登录/注销表单字段、`passwordEncrypt=false`、`keepaliveInterval=0`（无需客户端保活）均与程序实现一致
- `getOnlineUserInfo` 由 POST 改为 GET（部分网关 POST 超时，GET 正常）
- 监控状态机 `lastState` 修复（掉线确认期间状态变化横幅不再重复打印）

后续可以继续扩展：

- Windows 后台运行
- Windows 系统托盘
- 开机自动启动
- 日志文件
- 更完善的网络状态判断
- Android 后台服务
- 配置热加载
- 状态 API

## License

本项目仅供学习、研究以及在拥有合法网络使用权限的环境中使用。

使用者需要遵守所在学校、网络运营商以及所在地区的相关规定。
