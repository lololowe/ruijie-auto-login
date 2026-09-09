#!/bin/sh
# ============================================================
# 锐捷校园网自动登录 Shell 版
#
# 适用环境: iOS + iSH / Android + Termux / Linux
# 依赖: /bin/sh + curl + sed + grep + awk + sleep
# 不依赖: Go / Python / Node.js / jq / systemd
#
# 用法:
#   ./autologin.sh            持续监控，掉线自动重新登录
#   ./autologin.sh --status   查询当前在线状态
#   ./autologin.sh --logout   注销当前登录
#   ./autologin.sh --once     单次检查并登录，成功后退出
#   ./autologin.sh --help     显示帮助
#
# 配置文件: 与本脚本同目录下的 config.sh
#   首次使用请先执行: cp config.sh.example config.sh
# ============================================================

set -u

# ---------------- 全局变量 ----------------

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
CONFIG_FILE="$SCRIPT_DIR/config.sh"

PORTAL_BASE=""
PROBE_URL="http://119.29.29.29/"
CHECK_INTERVAL=10
RETRY_INTERVAL=5
REQUEST_TIMEOUT=10
ACCOUNTS=""

ACCOUNT_COUNT=0
CURRENT_INDEX=0

# 会话 Cookie 文件（模拟 Go 版本的 cookie jar）
COOKIE_FILE="${TMPDIR:-/tmp}/ruijie_autologin.$$"

# 可中断 sleep 的后台进程号
SLEEP_PID=""

# 在线用户信息（get_online_user_info 填充）
INFO_RESULT=""
INFO_MESSAGE=""
INFO_USER_ID=""
INFO_USER_NAME=""
INFO_USER_IP=""
INFO_USER_MAC=""
INFO_SERVICE=""
INFO_LOGIN_TYPE=""

# 参数发现结果（discover_login_params 填充）
PORTAL_URL=""
QUERY_STRING=""

# get_account 结果
ACCOUNT_USER=""
ACCOUNT_PASS=""

# ---------------- 基础工具函数 ----------------

print_help() {
    cat <<'EOF'
Usage:
  autologin.sh [option]

Options:
  --status    查询当前在线状态
  --logout    注销当前登录
  --once      单次检查并登录，成功后退出
  --help      显示帮助

Without options:
  持续监控在线状态，掉线后自动重新登录

Environment:
  iSH / Termux / Linux

Config:
  配置文件位于脚本同目录下的 config.sh
  首次使用请先执行: cp config.sh.example config.sh
EOF
}

check_deps() {
    for cmd in curl sed grep awk sleep; do
        if ! command -v "$cmd" >/dev/null 2>&1; then
            printf '缺少依赖命令: %s\n' "$cmd"
            exit 1
        fi
    done
}

load_config() {
    if [ ! -f "$CONFIG_FILE" ]; then
        printf '未找到配置文件: %s\n' "$CONFIG_FILE"
        if [ -f "$CONFIG_FILE.example" ]; then
            printf '请先复制模板并填写账号:\n'
            printf '  cp %s %s\n' "$CONFIG_FILE.example" "$CONFIG_FILE"
        fi
        exit 1
    fi

    # shellcheck source=/dev/null
    . "$CONFIG_FILE"

    # 未设置的配置项使用默认值
    PORTAL_BASE=${PORTAL_BASE:-http://172.16.32.240}
    PROBE_URL=${PROBE_URL:-http://119.29.29.29/}
    CHECK_INTERVAL=${CHECK_INTERVAL:-10}
    RETRY_INTERVAL=${RETRY_INTERVAL:-5}
    REQUEST_TIMEOUT=${REQUEST_TIMEOUT:-10}
    ACCOUNTS=${ACCOUNTS:-}

    # 数字配置项校验，非法时回退默认值
    case "$CHECK_INTERVAL" in ''|*[!0-9]*) CHECK_INTERVAL=10 ;; esac
    case "$RETRY_INTERVAL" in ''|*[!0-9]*) RETRY_INTERVAL=5 ;; esac
    case "$REQUEST_TIMEOUT" in ''|*[!0-9]*) REQUEST_TIMEOUT=10 ;; esac

    # 去掉 PORTAL_BASE 末尾多余的 /
    PORTAL_BASE=${PORTAL_BASE%/}
}

# 统计账号数量
count_accounts() {
    ACCOUNT_COUNT=0
    while IFS= read -r line; do
        case "$line" in
            *'|'*) ACCOUNT_COUNT=$((ACCOUNT_COUNT + 1)) ;;
        esac
    done <<EOF
$ACCOUNTS
EOF
}

# 获取第 $1 个账号（从 1 开始），结果写入 ACCOUNT_USER / ACCOUNT_PASS
# 注意：POSIX sh 函数没有局部变量，这里用 _ga 前缀避免与调用方变量冲突
get_account() {
    _ga_target=$1
    _ga_idx=0
    while IFS= read -r _ga_line; do
        # 跳过不含 | 的行（空行、注释等）
        case "$_ga_line" in
            *'|'*) ;;
            *) continue ;;
        esac
        _ga_idx=$((_ga_idx + 1))
        if [ "$_ga_idx" -eq "$_ga_target" ]; then
            # 用参数展开按第一个 | 拆分，不使用 eval，密码特殊字符安全
            ACCOUNT_USER=${_ga_line%%|*}
            ACCOUNT_PASS=${_ga_line#*|}
            return 0
        fi
    done <<EOF
$ACCOUNTS
EOF
    return 1
}

# 随机选择账号起点
random_start() {
    if [ "$ACCOUNT_COUNT" -eq 0 ]; then
        return 1
    fi
    CURRENT_INDEX=$(awk -v n="$ACCOUNT_COUNT" 'BEGIN { srand(); print int(rand() * n) }')
    # awk 输出异常时回退到第一个账号
    case "$CURRENT_INDEX" in
        ''|*[!0-9]*) CURRENT_INDEX=0 ;;
    esac
    printf '随机选择账号起点: %d/%d\n' "$((CURRENT_INDEX + 1))" "$ACCOUNT_COUNT"
}

# 移动到下一个账号（循环轮询）
next_account() {
    CURRENT_INDEX=$(( (CURRENT_INDEX + 1) % ACCOUNT_COUNT ))
}

# 把当前在线账号与配置列表对应起来
# 注意：POSIX sh 函数没有局部变量，这里用 _mu 前缀避免与调用方变量冲突
match_current_user() {
    _mu_target=$1
    _mu_i=1
    while [ "$_mu_i" -le "$ACCOUNT_COUNT" ]; do
        if get_account "$_mu_i"; then
            if [ "$ACCOUNT_USER" = "$_mu_target" ]; then
                CURRENT_INDEX=$((_mu_i - 1))
                return 0
            fi
        fi
        _mu_i=$((_mu_i + 1))
    done
    return 1
}

# 可被信号中断的 sleep
interruptible_sleep() {
    sleep "$1" &
    SLEEP_PID=$!
    wait "$SLEEP_PID" 2>/dev/null
    SLEEP_PID=""
}

cleanup() {
    if [ -n "$SLEEP_PID" ]; then
        kill "$SLEEP_PID" 2>/dev/null
    fi
    rm -f "$COOKIE_FILE" 2>/dev/null
}

on_signal() {
    printf '\n收到退出信号，正在停止...\n'
    exit 0
}

# ---------------- URL / JSON 解析 ----------------

# URL 编码，等价于 Go 的 url.QueryEscape：
# 未保留字符 (A-Z a-z 0-9 - _ . ~) 原样保留，空格变 +，其余变 %XX
urlencode() {
    printf '%s' "$1" | awk '
    BEGIN {
        hex = "0123456789ABCDEF"
        lookup = ""
        for (i = 1; i < 128; i++) {
            lookup = lookup sprintf("%c", i)
        }
    }
    {
        out = ""
        n = length($0)
        for (i = 1; i <= n; i++) {
            ch = substr($0, i, 1)
            if (ch ~ /[A-Za-z0-9._~-]/) {
                out = out ch
            } else if (ch == " ") {
                out = out "+"
            } else {
                # lookup 从字符 1 开始，index 返回值即为 ASCII 码
                o = index(lookup, ch)
                if (o < 1) {
                    out = out ch
                } else {
                    out = out "%" substr(hex, int(o / 16) + 1, 1) substr(hex, o % 16 + 1, 1)
                }
            }
        }
        printf "%s", out
    }'
}

# 从 JSON 中提取字符串字段值（锐捷返回的是扁平 JSON，无需 jq）
# $1 = JSON 文本, $2 = 字段名
json_get() {
    printf '%s' "$1" | sed -n 's/.*"'"$2"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

# ---------------- 参数发现 ----------------

# 请求参数探测地址，从锐捷网关拦截响应中提取登录参数
# 成功后设置 PORTAL_URL 和 QUERY_STRING
discover_login_params() {
    printf '正在获取锐捷登录参数...\n'

    body=$(curl -s \
        --connect-timeout "$REQUEST_TIMEOUT" \
        --max-time "$REQUEST_TIMEOUT" \
        -c "$COOKIE_FILE" -b "$COOKIE_FILE" \
        "$PROBE_URL" 2>/dev/null)

    if [ $? -ne 0 ] || [ -z "$body" ]; then
        printf '获取登录参数失败: 无法访问参数探测地址 %s\n' "$PROBE_URL"
        return 1
    fi

    # 提取被引号包裹的 ePortal 登录 URL，兼容单引号/双引号，
    # [^"'<>空白]* 保证 query 中的 & 不会被截断
    PORTAL_URL=$(printf '%s\n' "$body" |
        grep -o "https\{0,1\}://[^\"'<>[:space:]]*/eportal/index\.jsp?[^\"'<>[:space:]]*" |
        head -n 1)

    if [ -z "$PORTAL_URL" ]; then
        printf '获取登录参数失败: 响应中未找到锐捷 ePortal URL\n'
        return 1
    fi

    # 还原 HTML 实体（部分网关会把 & 编码成 &amp; 返回）
    PORTAL_URL=$(printf '%s' "$PORTAL_URL" | sed -e 's/&amp;/\&/g' -e 's/&#38;/\&/g')

    # 提取 ? 之后的完整 query 部分
    case "$PORTAL_URL" in
        *'?'*) QUERY_RAW=${PORTAL_URL#*\?} ;;
        *) QUERY_RAW="" ;;
    esac

    if [ -z "$QUERY_RAW" ]; then
        printf '获取登录参数失败: ePortal URL 中没有 query 参数\n'
        return 1
    fi

    # 与 Go 版本一致：整个 query 先做一次 URL 编码，
    # 提交表单时 curl --data-urlencode 会再编码一次
    QUERY_STRING=$(urlencode "$QUERY_RAW")

    printf '已获取登录参数: %s\n' "${PORTAL_URL%%"?"*}"
    return 0
}

# ---------------- 在线状态检测 ----------------

# 获取本机出口 IP（等价于 Go 版本的 UDP dial 技巧）
# 成功时输出 IP 并返回 0；失败时返回 1
get_local_ip() {
    # 直接解析路由表拿默认网关所在网卡会引入平台差异，
    # 这里沿用 Go 版思路：UDP connect 只做路由选择、不真正发包，
    # LocalAddr 即为本机出口 IP。实现上用 nc/自己都不好办，
    # 改用解析路由表的通用做法会有兼容性问题，因此退化为：
    # 依次尝试常见平台的方式，全部失败时返回空（调用方回退无参数查询）。
    ip=$(ip -4 route get 1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1)
    if [ -n "$ip" ]; then
        printf '%s' "$ip"
        return 0
    fi

    ip=$(route -n get 1 2>/dev/null | sed -n 's/.*interface: //p' | head -n 1)
    if [ -n "$ip" ]; then
        # macOS: 先拿接口名再取 IP
        ip=$(ifconfig "$ip" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -n 1)
        if [ -n "$ip" ]; then
            printf '%s' "$ip"
            return 0
        fi
    fi

    # BusyBox/Termux 环境：ifconfig 直接输出 inet 地址
    ip=$(ifconfig 2>/dev/null | sed -n 's/.*inet addr:\([0-9.]*\).*/\1/p' | grep -v '127.0.0.1' | head -n 1)
    if [ -z "$ip" ]; then
        ip=$(ifconfig 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | grep -v '127.0.0.1' | head -n 1)
    fi

    if [ -n "$ip" ]; then
        printf '%s' "$ip"
        return 0
    fi

    return 1
}

# 获取当前 Portal 会话的 userIndex
# 成功时输出 userIndex 并返回 0
#
# 实测（与 Go 版本一致）：这台 ePortal 的 redirectortosuccess.jsp
# 无参数请求时无法定位会话，即使在线也只返回空跳转 Location: http:// ，
# 必须携带 ?wlanuserip=<本机出口IP> 才能查到会话；
# 获取不到出口 IP 时回退为无参数请求（兼容其他锐捷部署）。
get_current_session() {
    session_url="$PORTAL_BASE/eportal/redirectortosuccess.jsp"

    if wlan_ip=$(get_local_ip); then
        session_url="$session_url?wlanuserip=$(urlencode "$wlan_ip")"
    fi

    headers=$(curl -s \
        --connect-timeout "$REQUEST_TIMEOUT" \
        --max-time "$REQUEST_TIMEOUT" \
        -c "$COOKIE_FILE" -b "$COOKIE_FILE" \
        -o /dev/null -D - \
        "$session_url" 2>/dev/null) || return 1

    location=$(printf '%s\n' "$headers" | grep -i '^Location:' | head -n 1 | tr -d '\r')
    location=${location#*:}
    # 去掉前导空白
    location=$(printf '%s' "$location" | sed 's/^[[:space:]]*//')

    # 请求成功但没有 Location：视为查询失败（UNKNOWN），交由调用方判断
    if [ -z "$location" ]; then
        return 1
    fi

    # Location 是空地址（http://）或登录页跳转（无 userIndex），
    # 都是“明确没有登录会话”的确定性信号
    case "$location" in
        *userIndex=*) ;;
        *) return 2 ;;
    esac

    user_index=$(printf '%s' "$location" | sed -n 's/.*[?&]userIndex=\([^&]*\).*/\1/p')
    if [ -z "$user_index" ]; then
        return 2
    fi

    printf '%s' "$user_index"
    return 0
}

# 查询在线用户信息，结果写入 INFO_* 全局变量
get_online_user_info() {
    user_index=$1

    resp=$(curl -s \
        --connect-timeout "$REQUEST_TIMEOUT" \
        --max-time "$REQUEST_TIMEOUT" \
        -c "$COOKIE_FILE" -b "$COOKIE_FILE" \
        -H 'Content-Type: application/x-www-form-urlencoded; charset=UTF-8' \
        --data-urlencode "userIndex=$user_index" \
        "$PORTAL_BASE/eportal/InterFace.do?method=getOnlineUserInfo" 2>/dev/null) || return 1

    if [ -z "$resp" ]; then
        return 1
    fi

    INFO_RESULT=$(json_get "$resp" "result")
    INFO_MESSAGE=$(json_get "$resp" "message")
    INFO_USER_ID=$(json_get "$resp" "userId")
    INFO_USER_NAME=$(json_get "$resp" "userName")
    INFO_USER_IP=$(json_get "$resp" "userIp")
    INFO_USER_MAC=$(json_get "$resp" "userMac")
    INFO_SERVICE=$(json_get "$resp" "service")
    INFO_LOGIN_TYPE=$(json_get "$resp" "loginType")
    return 0
}

# 检查当前是否在线（等价于 Go 版本 GetCurrentUser，三态）
# 返回 0 = 在线(ONLINE), 1 = 离线(OFFLINE), 2 = 未知(UNKNOWN)
#
# 三态判定（与 Go 版本一致）：
#   - OFFLINE 只来自 Portal 的“明确否定”（登录页跳转/空地址），
#     网络失败、超时等一律是 UNKNOWN，绝不猜测为离线
#   - 注意: result=wait 也可能带完整 userId/userIp，所以按 userId + userIp 判断
check_status() {
    user_index=$(get_current_session)
    st=$?

    if [ "$st" -eq 2 ]; then
        # Portal 明确表示没有登录会话
        return 1
    fi

    if [ "$st" -ne 0 ] || [ -z "$user_index" ]; then
        # 请求失败/超时：状态未知
        return 2
    fi

    if ! get_online_user_info "$user_index"; then
        return 2
    fi

    if [ -n "$INFO_USER_ID" ] && [ -n "$INFO_USER_IP" ]; then
        return 0
    fi

    # 信息不完整：只能算未知，不能当成未登录
    return 2
}

# 检查互联网连通性（等价于 Go 版本 CheckInternet）
# 返回 0 = 可用, 1 = 不可用
# 注意：它只反映互联网可达性，绝不参与登录状态判断
check_internet() {
    body=$(curl -s \
        --connect-timeout "$REQUEST_TIMEOUT" \
        --max-time "$REQUEST_TIMEOUT" \
        "https://www.apple.com/library/test/success.html" 2>/dev/null) || return 1

    case "$body" in
        *Success*) return 0 ;;
        *) return 1 ;;
    esac
}

print_user_info() {
    printf '\n========== 当前登录状态 ==========\n'
    printf '状态: 已登录\n'
    printf '账号: %s\n' "$INFO_USER_ID"
    printf '用户名: %s\n' "$INFO_USER_NAME"
    printf 'IP: %s\n' "$INFO_USER_IP"
    printf 'MAC: %s\n' "$INFO_USER_MAC"
    printf '服务: %s\n' "$INFO_SERVICE"
    printf '登录类型: %s\n' "$INFO_LOGIN_TYPE"
    printf '==================================\n'
}

# ---------------- 登录 / 注销 ----------------

# 使用指定账号登录（含参数发现与登录后验证）
# $1 = 账号, $2 = 密码
do_login() {
    username=$1
    password=$2

    discover_login_params || return 1

    printf '正在提交登录请求...\n'

    resp=$(curl -s \
        --connect-timeout "$REQUEST_TIMEOUT" \
        --max-time "$REQUEST_TIMEOUT" \
        -c "$COOKIE_FILE" -b "$COOKIE_FILE" \
        -H 'Content-Type: application/x-www-form-urlencoded; charset=UTF-8' \
        --data-urlencode "userId=$username" \
        --data-urlencode "password=$password" \
        --data-urlencode "service=" \
        --data-urlencode "queryString=$QUERY_STRING" \
        --data-urlencode "operatorPwd=" \
        --data-urlencode "operatorUserId=" \
        --data-urlencode "validcode=" \
        --data-urlencode "passwordEncrypt=false" \
        "$PORTAL_BASE/eportal/InterFace.do?method=login" 2>/dev/null)

    if [ $? -ne 0 ] || [ -z "$resp" ]; then
        printf '登录失败: HTTP 请求失败\n'
        return 1
    fi

    result=$(json_get "$resp" "result")
    message=$(json_get "$resp" "message")

    if [ "$result" != "success" ]; then
        printf '登录失败: 锐捷返回失败: %s\n' "${message:-未知错误}"
        return 1
    fi

    printf '登录接口返回成功，正在验证在线状态...\n'

    # 给 Portal 一点时间同步在线用户信息（与 Go 版本一致，最多重试 5 次）
    i=0
    while [ "$i" -lt 5 ]; do
        sleep 1
        check_status
        if [ "$?" -eq 0 ]; then
            return 0
        fi
        i=$((i + 1))
    done

    printf '登录验证失败: 登录接口返回成功，但暂未检测到在线用户\n'
    return 1
}

# 从 CURRENT_INDEX 开始按顺序尝试一整圈账号
login_cycle() {
    if [ "$ACCOUNT_COUNT" -eq 0 ]; then
        printf '配置中没有可用账号\n'
        return 1
    fi

    tried=0
    while [ "$tried" -lt "$ACCOUNT_COUNT" ]; do
        idx=$((CURRENT_INDEX + 1))
        if ! get_account "$idx"; then
            printf '读取第 %d 个账号失败\n' "$idx"
            return 1
        fi

        printf '\n[%s] 正在尝试账号 [%d/%d]: %s\n' "$(timestamp)" "$idx" "$ACCOUNT_COUNT" "$ACCOUNT_USER"

        if do_login "$ACCOUNT_USER" "$ACCOUNT_PASS"; then
            printf '[%s] 登录成功\n' "$(timestamp)"
            printf '[%s] 账号: %s\n' "$(timestamp)" "$INFO_USER_ID"
            printf '[%s] IP: %s\n' "$(timestamp)" "$INFO_USER_IP"
            match_current_user "$INFO_USER_ID"
            return 0
        fi

        next_account
        tried=$((tried + 1))
    done

    return 1
}

# 注销当前登录（独立逻辑，不与自动登录循环耦合）
do_logout() {
    user_index=$(get_current_session)
    session_st=$?

    if [ "$session_st" -eq 2 ]; then
        printf '注销失败: 当前没有登录会话（无需注销）\n'
        return 1
    fi

    if [ "$session_st" -ne 0 ]; then
        printf '注销失败: 获取当前登录会话失败（网络错误或超时）\n'
        return 1
    fi

    resp=$(curl -s \
        --connect-timeout "$REQUEST_TIMEOUT" \
        --max-time "$REQUEST_TIMEOUT" \
        -c "$COOKIE_FILE" -b "$COOKIE_FILE" \
        -H 'Content-Type: application/x-www-form-urlencoded; charset=UTF-8' \
        --data-urlencode "userIndex=$user_index" \
        "$PORTAL_BASE/eportal/InterFace.do?method=logout" 2>/dev/null)

    if [ $? -ne 0 ] || [ -z "$resp" ]; then
        printf '注销失败: HTTP 请求失败\n'
        return 1
    fi

    result=$(json_get "$resp" "result")
    message=$(json_get "$resp" "message")

    if [ "$result" != "success" ]; then
        printf '注销失败: 锐捷返回失败: %s\n' "${message:-未知错误}"
        return 1
    fi

    printf '注销成功。\n'
    return 0
}

# ---------------- 各命令入口 ----------------

# 日志时间戳（等价于 Go 版本的 ts()）
timestamp() {
    date '+%Y-%m-%d %H:%M:%S'
}

# 互联网状态的可读文本
internet_text() {
    if [ "$1" -eq 0 ]; then
        printf '正常'
    else
        printf '不可用'
    fi
}

cmd_status() {
    printf '正在查询当前认证状态...\n'
    check_status
    st=$?
    case "$st" in
        0)
            printf '当前状态: 在线\n'
            print_user_info
            exit 0
            ;;
        1)
            printf '当前状态: 离线\n'
            exit 0
            ;;
        *)
            printf '当前状态: 未知（无法确认是否在线）\n'
            exit 1
            ;;
    esac
}

cmd_logout() {
    printf '正在注销当前登录账号...\n'
    if do_logout; then
        exit 0
    fi
    exit 1
}

# 单次认证模式：登录成功后立即退出，绝不进入持续监控
cmd_once() {
    printf '正在检测当前登录状态...\n'
    check_status
    st=$?

    if [ "$st" -eq 0 ]; then
        printf '当前已经在线，无需重复登录。\n'
        print_user_info
        exit 0
    fi

    if [ "$st" -eq 2 ]; then
        # 无法确定状态时不贸然登录，避免重复上线
        printf '当前状态未知，无法确认是否在线，本次不执行登录。\n'
        exit 1
    fi

    printf '当前离线，开始尝试登录。\n'

    random_start

    if login_cycle; then
        printf '\n登录成功，本次认证结束。\n'
        exit 0
    fi

    printf '\n全部账号登录失败。\n'
    exit 1
}

# 持续监控（与 Go 版本 Monitor 状态机一致）
#
# 状态机规则：
#   - ONLINE:  清零掉线计数；互联网检测失败只记录，不重新登录
#   - UNKNOWN: 查询失败/超时，不累计为 OFFLINE，也不重新登录
#   - OFFLINE: 累计确认，连续达到阈值（3 次）才确认掉线并轮换账号重新登录
#
# 日志规则：
#   - Portal 状态发生变化时打印详细横幅（时间戳、变化前后状态、账号、处理动作）
#   - 状态无变化时只打印单行心跳日志
monitor() {
    # 确认掉线所需的连续 OFFLINE 次数
    OFFLINE_THRESHOLD=3

    printf '\n开始监控，检测间隔: %d 秒（连续 %d 次 OFFLINE 才确认掉线）\n' \
        "$CHECK_INTERVAL" "$OFFLINE_THRESHOLD"

    offline_count=0
    # 上一轮 Portal 状态: online / offline / unknown / boot（首轮）
    last_state="boot"
    # 最后一次确认 ONLINE 的账号信息
    last_account=""
    last_ip=""

    while true; do
        interruptible_sleep "$CHECK_INTERVAL"

        check_status
        st=$?

        check_internet
        internet_st=$?

        case "$st" in
            0) state="online" ;;
            1) state="offline" ;;
            *) state="unknown" ;;
        esac

        if [ "$state" != "$last_state" ]; then
            state_changed=1
        else
            state_changed=0
        fi

        case "$state" in
            online)
                prev_offline=$offline_count
                offline_count=0
                match_current_user "$INFO_USER_ID"
                last_account=$INFO_USER_ID
                last_ip=$INFO_USER_IP

                if [ "$state_changed" -eq 1 ]; then
                    printf '\n[%s] ========== 状态变化: %s → ONLINE ==========\n' \
                        "$(timestamp)" "$last_state"
                    if [ "$prev_offline" -gt 0 ]; then
                        printf '[%s] 掉线计数清零（此前连续确认: %d/%d）\n' \
                            "$(timestamp)" "$prev_offline" "$OFFLINE_THRESHOLD"
                    fi
                    printf '[%s] 账号: %s | 用户名: %s | IP: %s\n' \
                        "$(timestamp)" "$INFO_USER_ID" "$INFO_USER_NAME" "$INFO_USER_IP"
                    printf '[%s] 互联网状态: %s\n' "$(timestamp)" "$(internet_text "$internet_st")"
                    if [ "$internet_st" -ne 0 ]; then
                        printf '[%s] 注意：Portal 会话有效，互联网检测失败不触发重新登录\n' "$(timestamp)"
                    fi
                    printf '==================================================\n'
                else
                    printf '[%s] 心跳: ONLINE | 账号: %s | IP: %s | 互联网: %s\n' \
                        "$(timestamp)" "$INFO_USER_ID" "$INFO_USER_IP" "$(internet_text "$internet_st")"
                fi
                ;;

            unknown)
                if [ "$state_changed" -eq 1 ]; then
                    printf '\n[%s] ========== 状态变化: %s → UNKNOWN ==========\n' \
                        "$(timestamp)" "$last_state"
                    if [ -n "$last_account" ]; then
                        printf '[%s] 最后确认在线: 账号 %s | IP: %s\n' \
                            "$(timestamp)" "$last_account" "$last_ip"
                    fi
                    printf '[%s] 原因: Portal 状态查询失败或超时\n' "$(timestamp)"
                    printf '[%s] 处理: 暂不重新登录，不累计掉线，等待下一轮检测\n' "$(timestamp)"
                    printf '==================================================\n'
                else
                    printf '[%s] 心跳: UNKNOWN | 原因: Portal 状态查询失败或超时\n' "$(timestamp)"
                fi
                ;;

            offline)
                offline_count=$((offline_count + 1))

                if [ "$state_changed" -eq 1 ]; then
                    printf '\n[%s] ========== 状态变化: %s → OFFLINE ==========\n' \
                        "$(timestamp)" "$last_state"
                    if [ -n "$last_account" ]; then
                        printf '[%s] 掉线账号: %s | IP: %s\n' \
                            "$(timestamp)" "$last_account" "$last_ip"
                    fi
                fi

                if [ "$offline_count" -lt "$OFFLINE_THRESHOLD" ]; then
                    printf '[%s] 当前状态: OFFLINE | 连续确认: %d/%d | 互联网: %s | 尚未确认掉线，等待下一轮检测\n' \
                        "$(timestamp)" "$offline_count" "$OFFLINE_THRESHOLD" "$(internet_text "$internet_st")"
                    continue
                fi

                # 连续确认达到阈值，正式判定掉线
                printf '\n[%s] ========== 确认掉线（连续 %d/%d 次 OFFLINE） ==========\n' \
                    "$(timestamp)" "$offline_count" "$OFFLINE_THRESHOLD"
                if [ -n "$last_account" ]; then
                    printf '[%s] 失效账号: %s | IP: %s\n' \
                        "$(timestamp)" "$last_account" "$last_ip"
                fi
                printf '[%s] 互联网状态: %s\n' "$(timestamp)" "$(internet_text "$internet_st")"
                printf '[%s] 开始账号轮换并重新登录...\n' "$(timestamp)"
                printf '==================================================\n'

                # 确认掉线后清零计数，重新登录后从头开始统计
                offline_count=0

                # 当前账号掉线后，从下一个账号开始
                next_account

                if login_cycle; then
                    continue
                fi

                printf '[%s] 全部账号尝试失败，%d 秒后再次尝试\n' \
                    "$(timestamp)" "$RETRY_INTERVAL"
                interruptible_sleep "$RETRY_INTERVAL"
                ;;
        esac

        last_state=$state
    done
}

# 默认模式：持续监控 + 掉线自动登录
cmd_monitor() {
    printf '正在检测当前登录状态...\n'

    check_status
    st=$?

    if [ "$st" -eq 0 ]; then
        printf '\n检测到当前已经登录。\n'
        print_user_info

        if match_current_user "$INFO_USER_ID"; then
            printf '当前账号位于账号列表第 %d 个\n' "$((CURRENT_INDEX + 1))"
        else
            printf '当前账号不在配置列表中。\n'
        fi

        monitor
        return
    fi

    if [ "$st" -eq 2 ]; then
        # 启动时无法确定状态（查询失败/超时）：
        # 不贸然登录，进入监控模式继续检测，
        # 由监控状态机在状态明确后再决定是否登录
        printf '当前登录状态未知，进入监控模式继续检测。\n'
        monitor
        return
    fi

    printf '当前未检测到登录账号。\n'

    # 首次启动随机选择账号起点
    random_start

    while true; do
        if login_cycle; then
            break
        fi

        printf '\n全部账号登录失败，%d 秒后重新尝试。\n' "$RETRY_INTERVAL"
        interruptible_sleep "$RETRY_INTERVAL"
    done

    monitor
}

# ---------------- 主入口 ----------------

main() {
    case "${1:-}" in
        --help|-h)
            print_help
            exit 0
            ;;
        --status|--logout|--once|'')
            ;;
        *)
            printf '未知参数: %s\n\n' "$1"
            print_help
            exit 1
            ;;
    esac

    check_deps
    load_config
    count_accounts

    trap on_signal INT TERM
    trap cleanup EXIT

    case "${1:-}" in
        --status) cmd_status ;;
        --logout) cmd_logout ;;
        --once)   cmd_once ;;
        *)        cmd_monitor ;;
    esac
}

main "$@"
