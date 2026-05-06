package util

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient *HttpClient

type HttpClient struct {
	c *http.Client
}

func NewHttpClient() *HttpClient {
	return &HttpClient{
		c: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				MaxIdleConns:    100,
				IdleConnTimeout: 90 * time.Second,
			},
		},
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
	resp, err := c.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		limit := io.LimitReader(resp.Body, 512)
		body, _ := io.ReadAll(limit)
		return nil, &HTTPError{Code: resp.StatusCode, Body: string(body)}
	}
	return io.ReadAll(resp.Body)
}

func (c *HttpClient) GetRawClient() *http.Client {
	return c.c
}

func (c *HttpClient) Do(req *http.Request) (*http.Response, error) {
	if AuthHeader != nil {
		if _, ok := req.Header["Authorization"]; !ok {
			if v := AuthHeader["Authorization"]; v != "" {
				req.Header.Set("Authorization", v)
			}
		}
	}
	return c.c.Do(req)
}

type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body)
}
