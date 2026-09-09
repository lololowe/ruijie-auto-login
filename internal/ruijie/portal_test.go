package ruijie

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 模拟锐捷网关拦截 http://119.29.29.29/ 后的典型响应。
const samplePortalQuery = "wlanuserip=172.16.26.11&wlanacname=AC01&ssid=&nasip=172.16.32.1" +
	"&snmpagentip=&mac=ec4255cc00c1&t=wireless-v2&url=http%3A%2F%2Fexample.com%2F" +
	"&apmac=&nasid=NAS01&vid=100&port=1&nasportid=eth0"

func TestExtractPortalURLSingleQuote(t *testing.T) {
	body := "<script>top.self.location.href='http://172.16.32.240/eportal/index.jsp?" +
		samplePortalQuery + "'</script>"

	parsed, err := extractPortalURL(body)
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}

	if parsed.Scheme != "http" {
		t.Errorf("scheme = %q, 期望 http", parsed.Scheme)
	}

	if parsed.Host != "172.16.32.240" {
		t.Errorf("host = %q, 期望 172.16.32.240", parsed.Host)
	}

	// query 中的 & 不能被截断，必须保留完整参数。
	query := parsed.Query()

	if got := query.Get("wlanuserip"); got != "172.16.26.11" {
		t.Errorf("wlanuserip = %q", got)
	}

	if got := query.Get("mac"); got != "ec4255cc00c1" {
		t.Errorf("mac = %q", got)
	}

	if got := query.Get("nasportid"); got != "eth0" {
		t.Errorf("nasportid = %q, query 可能被 & 截断", got)
	}

	// URL 编码字符必须正确解码。
	if got := query.Get("url"); got != "http://example.com/" {
		t.Errorf("url = %q, URL 编码解析错误", got)
	}
}

func TestExtractPortalURLDoubleQuote(t *testing.T) {
	body := "<script>location.href=\"http://172.16.32.240/eportal/index.jsp?" +
		"wlanuserip=1.2.3.4&wlanacname=AC01\"</script>"

	parsed, err := extractPortalURL(body)
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}

	if got := parsed.Query().Get("wlanacname"); got != "AC01" {
		t.Errorf("wlanacname = %q", got)
	}
}

func TestExtractPortalURLHTMLEscaped(t *testing.T) {
	// 部分网关会把 & 编码成 &amp; 返回。
	body := "<script>top.self.location.href='http://172.16.32.240/eportal/index.jsp?" +
		"wlanuserip=1.2.3.4&amp;wlanacname=AC01'</script>"

	parsed, err := extractPortalURL(body)
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}

	if got := parsed.Query().Get("wlanacname"); got != "AC01" {
		t.Errorf("HTML 实体未还原, wlanacname = %q", got)
	}
}

func TestExtractPortalURLNotFound(t *testing.T) {
	cases := []string{
		"",
		"<html><body>ok</body></html>",
		"<script>location.href='http://example.com/redirect'</script>",
	}

	for _, body := range cases {
		if _, err := extractPortalURL(body); err == nil {
			t.Errorf("正文 %q 应该返回错误", body)
		}
	}
}

func TestExtractPortalURLNoQuery(t *testing.T) {
	body := "<script>location.href='http://172.16.32.240/eportal/index.jsp'</script>"

	if _, err := extractPortalURL(body); err == nil {
		t.Error("缺少 query 参数时应该返回错误")
	}
}

func TestExtractPortalURLKeepsRawQuery(t *testing.T) {
	body := "<script>top.self.location.href='http://172.16.32.240/eportal/index.jsp?" +
		samplePortalQuery + "'</script>"

	parsed, err := extractPortalURL(body)
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}

	// RawQuery 必须完整，包含所有参数分隔符。
	if count := strings.Count(parsed.RawQuery, "&"); count != 12 {
		t.Errorf("RawQuery 参数分隔符数量 = %d, 期望 12", count)
	}
}

// newTestClient 基于模拟的 Portal 服务器创建客户端。
func newTestClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := &Config{PortalBase: srv.URL, RequestTimeout: 5}

	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	return client
}

// TestGetCurrentUserOffline
// 实测：离线时 getOnlineUserInfo 以空 userIndex 查询，
// 服务器按来源 IP 找不到会话，明确返回 result=fail 且 userId/userIp 为 null。
// 这必须判定为 OFFLINE。
func TestGetCurrentUserOffline(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("method") != "getOnlineUserInfo" {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(
				w,
				`{"userIndex":null,"result":"fail",`+
					`"message":"获取用户信息失败，用户可能已经下线",`+
					`"userId":null,"userIp":null}`,
			)
		},
	)

	client := newTestClient(t, mux)

	user, state, err := client.GetCurrentUser(context.Background())

	if state != PortalOffline {
		t.Errorf("state = %v, 期望 OFFLINE（result=fail 是明确未登录信号）", state)
	}

	if user != nil {
		t.Errorf("OFFLINE 时 user 应为 nil, got %+v", user)
	}

	// 明确未登录不是错误，err 必须为 nil。
	if err != nil {
		t.Errorf("OFFLINE 时 err 应为 nil, got %v", err)
	}
}

// TestGetCurrentUserOnline 验证在线时返回 ONLINE 及完整用户信息。
// 实测：在线响应自带 userIndex（logout 复用），必须正确提取到 UserInfo。
func TestGetCurrentUserOnline(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("method") != "getOnlineUserInfo" {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(
				w,
				`{"userIndex":"idx-from-response","result":"success",`+
					`"userId":"24412030205","userName":"test",`+
					`"userIp":"172.28.130.45","userMac":"fed29afd7067"}`,
			)
		},
	)

	client := newTestClient(t, mux)

	user, state, err := client.GetCurrentUser(context.Background())

	if err != nil {
		t.Fatalf("在线查询不应返回错误: %v", err)
	}

	if state != PortalOnline {
		t.Errorf("state = %v, 期望 ONLINE", state)
	}

	if user == nil || user.UserID != "24412030205" || user.UserIP != "172.28.130.45" {
		t.Fatalf("用户信息不完整, got %+v", user)
	}

	// userIndex 来自响应体，供 logout 使用。
	if user.UserIndex != "idx-from-response" {
		t.Errorf("UserIndex = %q, 期望 idx-from-response", user.UserIndex)
	}
}

// TestGetCurrentUserOnlineWithWaitResult
// 实测（抓包验证）：登录后瞬间查询可能返回 result=wait，
// 但已带完整 userId/userIp，必须判定为 ONLINE。
func TestGetCurrentUserOnlineWithWaitResult(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("method") != "getOnlineUserInfo" {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(
				w,
				`{"userIndex":"idx-wait","result":"wait",`+
					`"message":"用户信息不完整，请稍后重试",`+
					`"userId":"21412080406","userIp":"172.28.130.45"}`,
			)
		},
	)

	client := newTestClient(t, mux)

	user, state, err := client.GetCurrentUser(context.Background())

	if err != nil {
		t.Fatalf("查询不应返回错误: %v", err)
	}

	if state != PortalOnline {
		t.Errorf("state = %v, 期望 ONLINE（result=wait 但 userId/userIp 完整）", state)
	}

	if user == nil || user.UserID != "21412080406" {
		t.Errorf("用户信息不完整, got %+v", user)
	}
}

// TestGetCurrentUserIncompleteUserInfo
// result=wait 且 userId/userIp 为空：信息不完整只能算 UNKNOWN，不能当成未登录。
func TestGetCurrentUserIncompleteUserInfo(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("method") != "getOnlineUserInfo" {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"userIndex":null,"result":"wait","message":"用户信息不完整，请稍后重试"}`)
		},
	)

	client := newTestClient(t, mux)

	_, state, err := client.GetCurrentUser(context.Background())

	if state != PortalUnknown {
		t.Errorf("state = %v, 期望 UNKNOWN（信息不完整不能判定为 OFFLINE）", state)
	}

	if err == nil {
		t.Error("UNKNOWN 时 err 应携带原因说明")
	}
}

// TestLogoutUsesUserIndexFromOnlineUserInfo
// 验证 Logout 从 getOnlineUserInfo 响应中提取 userIndex 并提交给 logout 接口。
// 实测：该流程在真实 ePortal 上返回 {"result":"success","message":"下线成功！"}。
func TestLogoutUsesUserIndexFromOnlineUserInfo(t *testing.T) {
	var gotUserIndex string

	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("method") {
			case "getOnlineUserInfo":
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(
					w,
					`{"userIndex":"idx-logout-1","result":"success",`+
						`"userId":"24412030205","userIp":"172.28.130.45"}`,
				)
			case "logout":
				if err := r.ParseForm(); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				gotUserIndex = r.PostFormValue("userIndex")
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"result":"success","message":"下线成功！"}`)
			default:
				http.NotFound(w, r)
			}
		},
	)

	client := newTestClient(t, mux)

	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("注销不应失败: %v", err)
	}

	if gotUserIndex != "idx-logout-1" {
		t.Errorf("logout 提交的 userIndex = %q, 期望 idx-logout-1", gotUserIndex)
	}
}

// TestLogoutWhenOffline
// 离线时（result=fail，userIndex 为 null）注销应返回明确错误，而不是误报成功。
func TestLogoutWhenOffline(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("method") != "getOnlineUserInfo" {
				http.NotFound(w, r)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(
				w,
				`{"userIndex":null,"result":"fail",`+
					`"message":"获取用户信息失败，用户可能已经下线",`+
					`"userId":null,"userIp":null}`,
			)
		},
	)

	client := newTestClient(t, mux)

	if err := client.Logout(context.Background()); err == nil {
		t.Error("离线时注销应返回错误（当前没有登录会话）")
	}
}
