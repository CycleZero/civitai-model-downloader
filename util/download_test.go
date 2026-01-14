package util

import "testing"

func TestDownloadFile(t *testing.T) {
	url := "http://data.server.poyuan233.cn:8088/data/mm.m"
	fileSize, err := getFileSize(url)
	if err != nil {
		panic(err)
	}
	err = StartDownloadFile(url, "miaomiaoRealskin_epsV13.safetensors", fileSize, 8, 1024*1024*1024)
	if err != nil {
		panic(err)
	}
}
