package ruijie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// PortalState 表示当前 Portal 登录状态（三态）。
// 网络请求失败、超时等只能得到 PortalUnknown，绝不能当成 PortalOffline。
type PortalState int

const (
	// PortalUnknown: Portal 查询失败、超时或信息不完整，无法确定状态。
	PortalUnknown PortalState = iota
	// PortalOnline: 明确确认当前账号在线。
	PortalOnline
	// PortalOffline: 明确确认当前没有登录账号。
	PortalOffline
)

// String 返回状态的可读表示。
func (s PortalState) String() string {
	switch s {
	case PortalOnline:
		return "ONLINE"
	case PortalOffline:
		return "OFFLINE"
	default:
		return "UNKNOWN"
	}
}

// isTimeoutErr 判断请求错误是否为超时（含 context deadline exceeded）。
func isTimeoutErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	return errors.Is(err, context.DeadlineExceeded)
}

type Client struct {
	Config *Config
	HTTP   *http.Client
}

type UserInfo struct {
	UserIndex string

	UserID   string
	UserName string
	UserIP   string
	UserMAC  string
	Service  string

	LoginType string
	PortalIP  string
	PortalURL string

	Result  string
	Message string
}

type onlineUserResponse struct {
	Result  string `json:"result"`
	Message string `json:"message"`

	UserIndex string `json:"userIndex"`
	UserID    string `json:"userId"`
	UserName  string `json:"userName"`
	UserIP    string `json:"userIp"`
	UserMAC   string `json:"userMac"`

	Service   string `json:"service"`
	LoginType string `json:"loginType"`

	PortalIP  string `json:"portalIp"`
	PortalURL string `json:"portalUrl"`

	IsAutoLogin     string `json:"isAutoLogin"`
	CheckUserLogout string `json:"checkUserLogout"`
}

func NewClient(cfg *Config) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Cookie Jar 失败: %w", err)
	}

	client := &http.Client{
		Jar:     jar,
		Timeout: time.Duration(cfg.RequestTimeout) * time.Second,
	}

	return &Client{
		Config: cfg,
		HTTP:   client,
	}, nil
}

func (c *Client) PortalBase() string {
	return strings.TrimRight(c.Config.PortalBase, "/")
}

// portalProbeURL 是用于触发锐捷网关拦截的参数探测地址。
// 未认证时锐捷网关会拦截该 HTTP 请求，
// 并在响应正文中返回真正的 ePortal 登录地址。
const portalProbeURL = "http://119.29.29.29/"

/*
典型锐捷返回：

<script>
top.self.location.href='http://172.16.32.240/eportal/index.jsp?...'
</script>

这里只匹配被引号包裹的 ePortal 登录地址本身，
兼容单引号、双引号以及不同的赋值写法。
[^'"]+ 保证 query 中的 & 不会被截断。
*/
var portalURLPattern = regexp.MustCompile(
	`['"](https?://[^'"]+/eportal/index\.jsp\?[^'"]+)['"]`,
)

// extractPortalURL 从锐捷网关拦截响应的正文中提取 ePortal 登录 URL。
func extractPortalURL(body string) (*url.URL, error) {
	match := portalURLPattern.FindStringSubmatch(body)
	if len(match) < 2 {
		/*
			注意：这只表示探测请求本身成功了，
			但响应内容中没有预期的锐捷 Portal 跳转地址，
			不能据此判定账号密码错误。
		*/
		return nil, errors.New("登录参数获取失败：当前请求未返回锐捷 ePortal URL")
	}

	/*
		URL 中的 & 等字符可能被 HTML 实体编码，
		例如 &amp; 需要先还原成 &，否则 query 会解析错误。
	*/
	raw := html.UnescapeString(match[1])

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析 ePortal URL 失败: %w", err)
	}

	if parsed.RawQuery == "" {
		return nil, errors.New("ePortal URL 中没有 query 参数")
	}

	return parsed, nil
}

// DiscoverPortal 请求参数探测地址，从锐捷网关的拦截响应中提取登录参数。
func (c *Client) DiscoverPortal(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		portalProbeURL,
		nil,
	)
	if err != nil {
		return "", "", err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// 超时属于网络/参数获取失败，与账号密码无关，日志需明确区分。
		if isTimeoutErr(err) {
			return "", "", fmt.Errorf("登录参数探测请求超时: %w", err)
		}
		return "", "", fmt.Errorf("请求参数探测地址失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", "", fmt.Errorf("读取参数探测响应失败: %w", err)
	}

	parsed, err := extractPortalURL(string(body))
	if err != nil {
		return "", "", err
	}

	/*
		浏览器提交 login 时：

		queryString=wlanuserip%253D...%2526wlanacname%253D...

		所以这里先把整个 RawQuery 编码一次，
		之后 url.Values.Encode() 会再进行一次表单编码。
	*/
	queryString := url.QueryEscape(parsed.RawQuery)

	return parsed.String(), queryString, nil
}

// GetOnlineUserInfo 查询当前在线用户。
//
// 实测（抓包验证）：userIndex 传空时服务器按来源 IP 自动定位会话，
// 这正是浏览器门户页的行为：
//
//	GET /eportal/InterFace.do?method=getOnlineUserInfo&userIndex=
//
//	在线 → {"userIndex":"...","result":"success","userId":"...","userIp":"..."}
//	离线 → {"userIndex":null,"result":"fail","userId":null,"userIp":null,
//	        "message":"获取用户信息失败，用户可能已经下线"}
//
// 在线响应中自带 userIndex，logout 可直接复用。
//
// 最初使用 POST 提交 userIndex，但部分锐捷网关对 POST 请求超时
// （context deadline exceeded），同一地址改用 GET 后正常返回，
// 因此改为 GET。
//
// 旧方案依赖 redirectortosuccess.jsp 的 302 Location 提取 userIndex，
// 但部分网关即使在线也只返回空 Location，导致在线被误判为离线，
// 已彻底弃用。
func (c *Client) GetOnlineUserInfo(ctx context.Context, userIndex string) (*UserInfo, error) {
	target := c.PortalBase() + "/eportal/InterFace.do?method=getOnlineUserInfo"

	// userIndex 作为 query 参数附加到 URL，GET 请求无需请求体
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("userIndex", userIndex)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		u.String(),
		nil,
	)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 getOnlineUserInfo 失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("读取用户信息失败: %w", err)
	}

	var result onlineUserResponse

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析用户信息失败: %w\n响应: %s", err, string(body))
	}

	// 在线时响应自带 userIndex；离线时为 null，回退到入参值
	resolvedIndex := result.UserIndex
	if resolvedIndex == "" {
		resolvedIndex = userIndex
	}

	return &UserInfo{
		UserIndex: resolvedIndex,
		UserID:    result.UserID,
		UserName:  result.UserName,
		UserIP:    result.UserIP,
		UserMAC:   result.UserMAC,
		Service:   result.Service,
		LoginType: result.LoginType,
		PortalIP:  result.PortalIP,
		PortalURL: result.PortalURL,
		Result:    result.Result,
		Message:   result.Message,
	}, nil
}

// GetCurrentUser 获取当前登录用户及三态登录状态。
// 锐捷这里 result=wait 也可能已经带有完整 userId/userIp，所以不能只看 result。
//
// 直接以空 userIndex 调用 getOnlineUserInfo，服务器按来源 IP 定位会话，
// 不依赖 redirectortosuccess.jsp（部分网关对该接口返回异常，曾导致在线被误判为离线）。
//
// 返回值语义：
//   - PortalOnline:  明确在线，user 为在线用户信息
//   - PortalOffline: 服务器明确返回 result=fail（用户已下线），err 为 nil
//   - PortalUnknown: 查询失败、超时或信息不完整，err 说明原因
//
// 关键约束：网络请求失败绝不能返回 PortalOffline，错误也不能被吞掉。
func (c *Client) GetCurrentUser(ctx context.Context) (*UserInfo, PortalState, error) {
	perRequest := time.Duration(c.Config.RequestTimeout) * time.Second

	infoCtx, infoCancel := context.WithTimeout(ctx, perRequest)
	info, err := c.GetOnlineUserInfo(infoCtx, "")
	infoCancel()

	if err != nil {
		return nil, PortalUnknown, err
	}

	/*
		实际判断依据是 UserID + UserIP：
		result = wait 也可能带完整 userId/userIp（已在生产环境观测到），
		此时按在线处理。
	*/
	if info.UserID != "" && info.UserIP != "" {
		return info, PortalOnline, nil
	}

	/*
		服务器明确回答“用户已下线”（result=fail 且 userId/userIp 为空），
		这是确定性离线信号。
	*/
	if info.Result == "fail" {
		return nil, PortalOffline, nil
	}

	// 信息不完整只能算 UNKNOWN，不能当成未登录。
	return info, PortalUnknown, errors.New("在线用户信息不完整，暂无法确认登录状态")
}

// CheckInternet 使用 Apple 连通性检测页判断互联网是否可用。
func (c *Client) CheckInternet(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"https://www.apple.com/library/test/success.html",
		nil,
	)
	if err != nil {
		return false
	}

	client := *c.HTTP

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return false
	}

	return strings.Contains(string(body), "Success")
}

type logoutResponse struct {
	Result  string `json:"result"`
	Message string `json:"message"`
}

// Logout 注销当前锐捷登录账号。
// 先以空 userIndex 查询在线用户（服务器按来源 IP 定位会话），
// 从响应中取出 userIndex，再调用 logout 接口。
func (c *Client) Logout(ctx context.Context) error {
	info, err := c.GetOnlineUserInfo(ctx, "")
	if err != nil {
		return fmt.Errorf("获取当前登录会话失败: %w", err)
	}

	if info.UserIndex == "" {
		return errors.New("当前没有登录的 Portal 会话（可能已经下线）")
	}

	userIndex := info.UserIndex

	target := c.PortalBase() + "/eportal/InterFace.do?method=logout"

	form := url.Values{}
	form.Set("userIndex", userIndex)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return fmt.Errorf("创建注销请求失败: %w", err)
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded; charset=UTF-8",
	)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("发送注销请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("读取注销响应失败: %w", err)
	}

	var result logoutResponse

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf(
			"解析注销响应失败: %w\n响应: %s",
			err,
			string(body),
		)
	}

	if result.Result != "success" {
		return fmt.Errorf("锐捷返回注销失败: %s", result.Message)
	}

	return nil
}
