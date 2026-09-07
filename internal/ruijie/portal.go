package ruijie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

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

// DiscoverPortal 从微软连通性检测地址获取锐捷 ePortal 登录参数。
func (c *Client) DiscoverPortal(ctx context.Context) (string, string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		"http://www.msftconnecttest.com/redirect",
		nil,
	)
	if err != nil {
		return "", "", err
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("访问连通性检测地址失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", "", fmt.Errorf("读取 Portal 响应失败: %w", err)
	}

	text := string(body)

	/*
		典型锐捷返回：

		<script>
		location.href='http://172.16.32.240/eportal/index.jsp?...'
		</script>
	*/
	re := regexp.MustCompile(`(?i)(?:location\.href\s*=\s*['"])(https?://[^'"]+/eportal/index\.jsp\?[^'"]+)`)

	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return "", "", errors.New("没有从响应中找到锐捷 ePortal 登录地址")
	}

	indexURL := match[1]

	parsed, err := url.Parse(indexURL)
	if err != nil {
		return "", "", fmt.Errorf("解析 Portal URL 失败: %w", err)
	}

	if parsed.RawQuery == "" {
		return "", "", errors.New("Portal URL 中没有 query 参数")
	}

	/*
		浏览器提交 login 时：

		queryString=wlanuserip%253D...%2526wlanacname%253D...

		所以这里先把整个 RawQuery 编码一次，
		之后 url.Values.Encode() 会再进行一次表单编码。
	*/
	queryString := url.QueryEscape(parsed.RawQuery)

	return indexURL, queryString, nil
}

// GetCurrentSession 获取当前 Portal 会话。
func (c *Client) GetCurrentSession(ctx context.Context) (string, error) {
	target := c.PortalBase() + "/eportal/redirectortosuccess.jsp"

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
		return "", fmt.Errorf("获取当前登录状态失败: %w", err)
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if location == "" {
		return "", errors.New("当前没有找到登录成功跳转地址")
	}

	parsed, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("解析 Location 失败: %w", err)
	}

	userIndex := parsed.Query().Get("userIndex")
	if userIndex == "" {
		return "", errors.New("Location 中没有 userIndex")
	}

	return userIndex, nil
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

// GetCurrentUser 获取当前登录用户。
// 锐捷这里 result=wait 也可能已经带有完整 userId/userIp，所以不能只看 result。
func (c *Client) GetCurrentUser(ctx context.Context) (*UserInfo, bool, error) {
	userIndex, err := c.GetCurrentSession(ctx)
	if err != nil {
		return nil, false, nil
	}

	info, err := c.GetOnlineUserInfo(ctx, userIndex)
	if err != nil {
		return nil, false, err
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
		return info, true, nil
	}

	return info, false, nil
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
