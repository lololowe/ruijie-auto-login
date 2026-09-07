package ruijie

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type loginResponse struct {
	UserIndex         string `json:"userIndex"`
	Result            string `json:"result"`
	Message           string `json:"message"`
	ForwordURL        string `json:"forwordurl"`
	KeepaliveInterval int    `json:"keepaliveInterval"`
	ValidCodeURL      string `json:"validCodeUrl"`
}

// Login 使用指定账号执行锐捷登录。
func (c *Client) Login(ctx context.Context, account Account) (*loginResponse, error) {
	_, queryString, err := c.DiscoverPortal(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取登录参数失败: %w", err)
	}

	target := c.PortalBase() + "/eportal/InterFace.do?method=login"

	form := url.Values{}

	form.Set("userId", account.Username)
	form.Set("password", account.Password)

	// 你的校园网抓包确认 service 为空。
	form.Set("service", "")

	form.Set("queryString", queryString)

	form.Set("operatorPwd", "")
	form.Set("operatorUserId", "")
	form.Set("validcode", "")
	form.Set("passwordEncrypt", "false")

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded; charset=UTF-8",
	)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求登录接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("读取登录响应失败: %w", err)
	}

	var result loginResponse

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf(
			"解析登录响应失败: %w\n响应: %s",
			err,
			string(body),
		)
	}

	return &result, nil
}

// LoginAndVerify 登录后再次检查当前在线用户。
func (c *Client) LoginAndVerify(ctx context.Context, account Account) (*UserInfo, error) {
	result, err := c.Login(ctx, account)
	if err != nil {
		return nil, err
	}

	if result.Result != "success" {
		return nil, fmt.Errorf(
			"登录失败: %s",
			result.Message,
		)
	}

	// 给 Portal 一点时间同步在线用户信息。
	for i := 0; i < 5; i++ {
		time.Sleep(500 * time.Millisecond)

		user, loggedIn, err := c.GetCurrentUser(ctx)
		if err != nil {
			continue
		}

		if loggedIn {
			return user, nil
		}
	}

	/*
		登录接口已经明确返回 success，
		但在线用户接口暂时还没有同步完成。
		这种情况也不能直接判定账号密码错误。
	*/
	return nil, fmt.Errorf("登录接口返回成功，但暂未检测到在线用户")
}
