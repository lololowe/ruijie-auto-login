package ruijie

import (
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
