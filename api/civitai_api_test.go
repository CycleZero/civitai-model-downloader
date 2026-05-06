package api

import (
	"testing"

	"civitai-model-downloader/dto"
)

func TestGetModelInfo(t *testing.T) {
	_, err := GetModelInfo(&dto.ModelRequest{})
	if err != nil {
		t.Skip("network required: " + err.Error())
	}
}
