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

// ErrPortalNoSession 表示明确确认当前没有登录的 Portal 会话。
// 仅用于“没有登录跳转地址”等确定性信号；
// 网络失败、超时等错误不能包装成该错误，否则会被误判为离线。
var ErrPortalNoSession = errors.New("当前没有登录的 Portal 会话")

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

// localIP 通过 UDP dial 获取本机出口 IP。
// UDP 是无连接的，Dial 只做路由选择、不会真正发包，
// 因此可以安全地使用外部地址，且无需任何网络连通性。
func localIP() string {
	conn, err := net.Dial("udp", "119.29.29.29:80")
	if err != nil {
		return ""
	}
	defer conn.Close()

	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}

	return ""
}

// GetCurrentSession 获取当前 Portal 会话。
//
// 实测（2026-09-09，湖南工业大学 ePortal）：
// 这台 ePortal 的 redirectortosuccess.jsp 无参数请求时无法定位会话，
// 即使已经在线也只返回空跳转 Location: http:// ，导致误判 OFFLINE；
// 必须携带 ?wlanuserip=<本机出口IP> 参数才能查到会话：
//
//	GET /eportal/redirectortosuccess.jsp?wlanuserip=172.28.130.45
//	→ 302 Location: http://172.16.32.240/eportal/./success.jsp?userIndex=...
//
// 未登录时该接口返回登录页跳转（无 userIndex），是明确的离线信号。
func (c *Client) GetCurrentSession(ctx context.Context) (string, error) {
	// 优先携带 wlanuserip 参数查询；出口 IP 获取失败时回退为无参数查询。
	if ip := localIP(); ip != "" {
		return c.querySession(ctx, ip)
	}

	return c.querySession(ctx, "")
}

// querySession 请求 redirectortosuccess.jsp 并解析当前会话的 userIndex。
// wlanUserIP 为空时表示无参数请求。
func (c *Client) querySession(ctx context.Context, wlanUserIP string) (string, error) {
	target := c.PortalBase() + "/eportal/redirectortosuccess.jsp"

	if wlanUserIP != "" {
		target += "?wlanuserip=" + url.QueryEscape(wlanUserIP)
	}

	client := *c.HTTP

	// 这里必须禁止自动跟随 302，因为我们需要读取 Location。
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		target,
		nil,
	)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		// 网络失败/超时不能当成“没有登录”，调用方据此应判定 UNKNOWN。
		if isTimeoutErr(err) {
			return "", fmt.Errorf("Portal 状态查询超时: %w", err)
		}
		return "", fmt.Errorf("获取当前登录状态失败: %w", err)
	}
	defer resp.Body.Close()

	/*
		实测的两种明确“无会话”形态：
		  1. Location 指向登录页（如 /eportal/index.jsp?wlanuserip=...），无 userIndex；
		  2. Location 为空或空地址 http:// 。
		请求本身成功却拿不到 userIndex，均视为明确未登录。
	*/
	userIndex := locationUserIndex(resp.Header.Get("Location"))
	if userIndex == "" {
		return "", ErrPortalNoSession
	}

	return userIndex, nil
}

// locationUserIndex 从 302 跳转地址中提取 userIndex 参数。
// 地址为空、空地址（http://）、或不含 userIndex（如登录页）时返回空。
func locationUserIndex(location string) string {
	if location == "" {
		return ""
	}

	parsed, err := url.Parse(location)
	if err != nil {
		return ""
	}

	return parsed.Query().Get("userIndex")
}

// GetOnlineUserInfo 查询当前在线用户。
func (c *Client) GetOnlineUserInfo(ctx context.Context, userIndex string) (*UserInfo, error) {
	target := c.PortalBase() + "/eportal/InterFace.do?method=getOnlineUserInfo"

	form := url.Values{}
	form.Set("userIndex", userIndex)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

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

	return &UserInfo{
		UserIndex: userIndex,
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
// 返回值语义：
//   - PortalOnline:  明确在线，user 为在线用户信息
//   - PortalOffline: 明确没有登录会话，err 为 nil
//   - PortalUnknown: 查询失败、超时或信息不完整，err 说明原因
//
// 关键约束：网络请求失败绝不能返回 PortalOffline，错误也不能被吞掉。
func (c *Client) GetCurrentUser(ctx context.Context) (*UserInfo, PortalState, error) {
	perRequest := time.Duration(c.Config.RequestTimeout) * time.Second

	/*
		会话查询与用户信息查询各自使用独立的超时上下文，
		避免前一个请求耗时吃掉后一个请求的超时预算。
	*/
	sessCtx, sessCancel := context.WithTimeout(ctx, perRequest)
	userIndex, err := c.GetCurrentSession(sessCtx)
	sessCancel()

	if err != nil {
		// 只有“明确没有登录会话”才是 OFFLINE，其余一律 UNKNOWN。
		if errors.Is(err, ErrPortalNoSession) {
			return nil, PortalOffline, nil
		}
		return nil, PortalUnknown, err
	}

	infoCtx, infoCancel := context.WithTimeout(ctx, perRequest)
	info, err := c.GetOnlineUserInfo(infoCtx, userIndex)
	infoCancel()

	if err != nil {
		return nil, PortalUnknown, err
	}

	/*
		你的环境实际出现过：

		result = wait
		message = 用户信息不完整，请稍后重试

		但同时已经有：

		userId
		userName
		userIp
		userMac

		所以实际判断依据是 UserID + UserIP。
	*/
	if info.UserID != "" && info.UserIP != "" {
		return info, PortalOnline, nil
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
// 先获取当前会话的 userIndex，再调用 logout 接口。
func (c *Client) Logout(ctx context.Context) error {
	userIndex, err := c.GetCurrentSession(ctx)
	if err != nil {
		return fmt.Errorf("获取当前登录会话失败: %w", err)
	}

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
