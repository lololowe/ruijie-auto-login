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

// TestGetCurrentUserOfflineByRedirectWithoutUserIndex
// 实测：注销后 redirectortosuccess.jsp 仍返回 302，
// 但 Location 指向登录页（无 userIndex 参数），必须判定为 OFFLINE。
// 之前把它当成查询异常返回 UNKNOWN，导致程序永远卡在"状态未知、不登录"。
func TestGetCurrentUserOfflineByRedirectWithoutUserIndex(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/redirectortosuccess.jsp",
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(
				w,
				r,
				"/eportal/index.jsp?wlanuserip=172.28.130.45&wlanacname=AC01",
				http.StatusFound,
			)
		},
	)

	client := newTestClient(t, mux)

	user, state, err := client.GetCurrentUser(context.Background())

	if state != PortalOffline {
		t.Errorf("state = %v, 期望 OFFLINE（Location 无 userIndex 是明确未登录信号）", state)
	}

	if user != nil {
		t.Errorf("OFFLINE 时 user 应为 nil, got %+v", user)
	}

	// 明确未登录不是错误，err 必须为 nil。
	if err != nil {
		t.Errorf("OFFLINE 时 err 应为 nil, got %v", err)
	}
}

// TestGetCurrentUserOfflineByNoLocation
// 未登录的另一种形态：响应不带 Location 头，也必须判定为 OFFLINE。
func TestGetCurrentUserOfflineByNoLocation(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/redirectortosuccess.jsp",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		},
	)

	client := newTestClient(t, mux)

	_, state, err := client.GetCurrentUser(context.Background())

	if state != PortalOffline {
		t.Errorf("state = %v, 期望 OFFLINE（无 Location 是明确未登录信号）", state)
	}

	if err != nil {
		t.Errorf("OFFLINE 时 err 应为 nil, got %v", err)
	}
}

// TestGetCurrentUserOnline 验证在线时返回 ONLINE 及完整用户信息。
func TestGetCurrentUserOnline(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/redirectortosuccess.jsp",
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(
				w,
				r,
				"/eportal/success.jsp?userIndex=idx-test-123",
				http.StatusFound,
			)
		},
	)

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(
				w,
				`{"result":"success","userId":"24412030205","userName":"test",`+
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
		t.Errorf("用户信息不完整, got %+v", user)
	}
}

// TestGetCurrentUserIncompleteUserInfo
// result=wait 且 userId/userIp 为空：信息不完整只能算 UNKNOWN，不能当成未登录。
func TestGetCurrentUserIncompleteUserInfo(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/redirectortosuccess.jsp",
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(
				w,
				r,
				"/eportal/success.jsp?userIndex=idx-test-123",
				http.StatusFound,
			)
		},
	)

	mux.HandleFunc(
		"/eportal/InterFace.do",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"result":"wait","message":"用户信息不完整，请稍后重试"}`)
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

// TestQuerySessionWithWlanuserip
// 实测：ePortal 要求 redirectortosuccess.jsp 携带 wlanuserip 参数才能定位会话。
// 验证 querySession 会把 wlanuserip 正确拼进查询串。
func TestQuerySessionWithWlanuserip(t *testing.T) {
	var gotParam string

	mux := http.NewServeMux()

	mux.HandleFunc(
		"/eportal/redirectortosuccess.jsp",
		func(w http.ResponseWriter, r *http.Request) {
			gotParam = r.URL.Query().Get("wlanuserip")

			http.Redirect(
				w,
				r,
				"/eportal/./success.jsp?userIndex=idx-param-1",
				http.StatusFound,
			)
		},
	)

	client := newTestClient(t, mux)

	// 模拟实测抓包：带 wlanuserip 时 ePortal 返回带 userIndex 的跳转。
	idx, err := client.querySession(context.Background(), "172.28.130.45")

	if err != nil {
		t.Fatalf("带 wlanuserip 查询不应失败: %v", err)
	}

	if idx != "idx-param-1" {
		t.Errorf("userIndex = %q, 期望 idx-param-1", idx)
	}

	if gotParam != "172.28.130.45" {
		t.Errorf("服务端收到的 wlanuserip = %q, 期望 172.28.130.45", gotParam)
	}
}

// TestLocationUserIndex 验证从 302 Location 提取 userIndex 的各种形态。
func TestLocationUserIndex(t *testing.T) {
	cases := []struct {
		name     string
		location string
		want     string
	}{
		{
			// 实测在线形态：带 userIndex 的 success 跳转（注意含 ./ 路径）。
			name:     "success跳转带userIndex",
			location: "http://172.16.32.240/eportal/./success.jsp?userIndex=abc123",
			want:     "abc123",
		},
		{
			// 实测无参数请求的空地址形态：解析成功但无 query。
			name:     "空地址http://",
			location: "http://",
			want:     "",
		},
		{
			name:     "空Location",
			location: "",
			want:     "",
		},
		{
			// 实测未登录形态：跳登录页，无 userIndex。
			name:     "登录页跳转",
			location: "http://172.16.32.240/eportal/index.jsp?wlanuserip=172.28.130.45",
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := locationUserIndex(tc.location); got != tc.want {
				t.Errorf("locationUserIndex(%q) = %q, 期望 %q", tc.location, got, tc.want)
			}
		})
	}
}
