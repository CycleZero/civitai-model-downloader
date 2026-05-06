package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"civitai-model-downloader/dto"
	"civitai-model-downloader/util"
)

const baseURL = "https://civitai.com"

func doGet(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if util.AuthHeader != nil {
		for k, v := range util.AuthHeader {
			req.Header.Set(k, v)
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

func GetModelInfo(req *dto.ModelRequest) (*dto.ModelsResponse, error) {
	u := baseURL + "/api/v1/models" + req.QueryString()
	data, err := doGet(u)
	if err != nil {
		return nil, err
	}
	var resp dto.ModelsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &resp, nil
}

func GetModelById(modelId string) (*dto.ModelItem, error) {
	u := baseURL + "/api/v1/models/" + modelId
	data, err := doGet(u)
	if err != nil {
		return nil, err
	}
	var model dto.ModelItem
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &model, nil
}

func GetModelByVersionId(versionId string) (*dto.ModelVersionFull, error) {
	u := baseURL + "/api/v1/model-versions/" + versionId
	data, err := doGet(u)
	if err != nil {
		return nil, err
	}
	var model dto.ModelVersionFull
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &model, nil
}

func GetModelByHash(hash string) (*dto.ModelVersionFull, error) {
	u := baseURL + "/api/v1/model-versions/by-hash/" + hash
	data, err := doGet(u)
	if err != nil {
		return nil, err
	}
	var model dto.ModelVersionFull
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &model, nil
}
