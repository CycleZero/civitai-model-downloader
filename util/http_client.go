package util

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"time"
)

var httpClient *HttpClient

type HttpClient struct {
	c *http.Client
}

func NewHttpClient() *HttpClient {
	c := &http.Client{
		Timeout: 0 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			Proxy: func(req *http.Request) (*url.URL, error) {
				// 在这里指定您的代理地址
				proxyURL, err := url.Parse("http://127.0.0.1:7890") // 示例代理地址
				if err != nil {
					return nil, err
				}
				return proxyURL, nil
			},
		},
	}
	return &HttpClient{
		c: c,
	}
}

func GetHttpClient() *HttpClient {
	if httpClient == nil {
		httpClient = NewHttpClient()
	}
	return httpClient
}

func (c *HttpClient) Get(url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	r, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (c *HttpClient) Post(url string, body []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	r, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (c *HttpClient) GetRawClient() *http.Client {
	return c.c
}

func (c *HttpClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", AuthHeader["Authorization"])
	return c.c.Do(req)
}
