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
- 运行过程中持续检测网络状态
- 网络断开后自动重新认证
- 当前账号认证失败后自动尝试下一个账号
- 所有账号均失败后自动等待并重新尝试
- 独立的注销工具
- 配置文件修改账号后无需重新编译

## 工作原理

本项目针对锐捷 ePortal Web 认证流程实现。

未认证状态下，访问：

```text
http://www.msftconnecttest.com/redirect
```

锐捷网关会将请求重定向到类似：

```text
http://172.16.32.240/eportal/index.jsp?wlanuserip=...&wlanacname=...&mac=...
```

程序从这个地址中提取当前网络环境对应的动态参数，然后调用：

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

随后程序可以通过：

```text
POST /eportal/InterFace.do?method=getOnlineUserInfo
```

查询当前认证用户。

## 登录流程

```text
程序启动
    │
    ▼
检测当前是否已经登录
    │
    ├── 已登录
    │     │
    │     ▼
    │   获取当前用户信息
    │     │
    │     ▼
    │   开始网络监控
    │
    └── 未登录
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
│       ├── auth.go
│       └── monitor.go
│
└── cmd/
    ├── autologin/
    │   └── main.go
    │
    └── logout/
        └── main.go
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
- 获取当前 `userIndex`
- 查询当前登录用户
- 检测互联网连接状态

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

自动登录主程序入口。

### `cmd/logout/main.go`

独立注销程序，不参与自动登录主程序。

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

编译自动登录程序：

```bash
go build -o ruijie-autologin ./cmd/autologin
```

编译注销程序：

```bash
go build -o ruijie-logout ./cmd/logout
```

Windows 下生成：

```text
ruijie-autologin.exe
ruijie-logout.exe
```

## 运行

### 自动登录

```bash
ruijie-autologin.exe
```

开发阶段也可以直接：

```bash
go run ./cmd/autologin
```

### 注销

```bash
ruijie-logout.exe
```

或者：

```bash
go run ./cmd/logout
```

## 当前登录用户检测

程序启动时会调用：

```text
/eportal/redirectortosuccess.jsp
```

获取当前认证会话的 `userIndex`，然后调用：

```text
/eportal/InterFace.do?method=getOnlineUserInfo
```

获取用户信息。

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

## 注销原理

注销工具首先获取当前会话：

```text
GET /eportal/redirectortosuccess.jsp
```

得到：

```text
success.jsp?userIndex=...
```

然后调用：

```text
POST /eportal/InterFace.do?method=logout
```

提交：

```text
userIndex=...
```

注销不需要账号密码。

## 网络监控

程序运行过程中会周期性检查互联网连接。

默认：

```json
"checkInterval": 10
```

即每 10 秒检查一次。

发现当前连接异常后：

```text
当前账号掉线
    ↓
切换到下一个账号
    ↓
获取新的 Portal 参数
    ↓
重新认证
    ↓
登录成功
    ↓
继续监控
```

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
GET  /eportal/redirectortosuccess.jsp

POST /eportal/InterFace.do?method=getOnlineUserInfo

POST /eportal/InterFace.do?method=login

POST /eportal/InterFace.do?method=logout
```

认证流程中的动态参数来自：

```text
http://www.msftconnecttest.com/redirect
```

## 当前状态

目前项目已经验证：

- 锐捷 Portal 自动发现
- 动态 `queryString` 获取
- HTTP 直接登录
- 登录成功状态确认
- 当前登录用户检测
- 多账号顺序轮询
- 启动随机轮询起点
- 掉线重新认证
- 独立注销

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
