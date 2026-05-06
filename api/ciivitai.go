package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"civitai-model-downloader/dto"
	"civitai-model-downloader/util"
)

const (
	baseURL        = "https://civitai.com"
	requestTimeout = 30 * time.Second
)

func doGet(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if util.AuthHeader != nil {
		if v := util.AuthHeader["Authorization"]; v != "" && v != "Bearer " {
			for k, v := range util.AuthHeader {
				req.Header.Set(k, v)
			}
		}
	}
	req.Header.Set("User-Agent", "cvtcli/2.0")
	req.Header.Set("Accept", "application/json")

	resp, err := util.GetHttpClient().GetRawClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		limit := io.LimitReader(resp.Body, 512)
		body, _ := io.ReadAll(limit)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}

func GetModelInfo(ctx context.Context, req *dto.ModelRequest) (*dto.ModelsResponse, error) {
	if req == nil {
		req = &dto.ModelRequest{}
	}
	u := baseURL + "/api/v1/models" + req.QueryString()
	data, err := doGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp dto.ModelsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &resp, nil
}

func GetModelById(ctx context.Context, modelId string) (*dto.ModelItem, error) {
	u := baseURL + "/api/v1/models/" + modelId
	data, err := doGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var model dto.ModelItem
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &model, nil
}

func GetModelByVersionId(ctx context.Context, versionId string) (*dto.ModelVersionFull, error) {
	u := baseURL + "/api/v1/model-versions/" + versionId
	data, err := doGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var model dto.ModelVersionFull
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &model, nil
}

func GetModelByHash(ctx context.Context, hash string) (*dto.ModelVersionFull, error) {
	u := baseURL + "/api/v1/model-versions/by-hash/" + hash
	data, err := doGet(ctx, u)
	if err != nil {
		return nil, err
	}
	var model dto.ModelVersionFull
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &model, nil
}
