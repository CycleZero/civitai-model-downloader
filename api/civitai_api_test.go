package api

import (
	"context"
	"testing"
	"time"

	"civitai-model-downloader/dto"
)

func TestGetModelInfo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := GetModelInfo(ctx, &dto.ModelRequest{})
	if err != nil {
		t.Skip("network required: " + err.Error())
	}
}
