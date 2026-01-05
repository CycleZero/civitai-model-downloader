package api

import (
	"civitai-model-downloader/dto"
	"civitai-model-downloader/util"
	"encoding/json"
)

const CivitaiUrl = "https://civitai.com"

func GetModelInfo(req *dto.ModelRequest) (*dto.ModelResponse, error) {
	url := CivitaiUrl + "/api/v1/models" + req.GetQuery()
	data, err := util.GetHttpClient().Get(url, nil)
	if err != nil {
		return nil, err
	}
	var model dto.ModelResponse
	err = json.Unmarshal(data, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func GetModelById(modelId string) (*dto.ModelIdResponse, error) {
	url := CivitaiUrl + "/api/v1/models/" + modelId
	data, err := util.GetHttpClient().Get(url, util.AuthHeader)
	if err != nil {
		return nil, err
	}
	var model dto.ModelIdResponse
	err = json.Unmarshal(data, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func GetModelByVersionId(modelVersionId string) (*dto.ModelVersionIdResponse, error) {
	url := CivitaiUrl + "/api/v1/model-versions/" + modelVersionId
	data, err := util.GetHttpClient().Get(url, util.AuthHeader)
	if err != nil {
		return nil, err
	}
	var model dto.ModelVersionIdResponse
	err = json.Unmarshal(data, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

func GetModelByHash(modelHash string) (*dto.ModelVersionIdResponse, error) {
	url := CivitaiUrl + "/api/v1/model-versions/by-hash/" + modelHash
	data, err := util.GetHttpClient().Get(url, util.AuthHeader)
	if err != nil {
		return nil, err
	}
	var model dto.ModelVersionIdResponse
	err = json.Unmarshal(data, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}
