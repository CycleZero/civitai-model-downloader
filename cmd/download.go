package cmd

import (
	"civitai-model-downloader/api"
	"civitai-model-downloader/log"
	"civitai-model-downloader/util"
	"fmt"
	"github.com/spf13/cobra"
	"net/http"
	"strings"
)

var DownloadUrl string

var ModelId string

var ModelVersionId string

var ModelName string

var ModelHash string

var DownloadDir string

var NumThreads int

var MaxChunkSize int64
var downloadCommand = &cobra.Command{

	Use: "download",

	Run: func(cmd *cobra.Command, args []string) {
		var fileSize int64
		if DownloadDir == "" {
			DownloadDir = "./"
		}
		if DownloadUrl != "" {
			//TODO 直接下载
		} else if ModelHash != "" {
			model, err := api.GetModelByHash(ModelHash)
			if err != nil {
				log.Logger().Sugar().Error(err)
				return
			}
			ModelName, err = GetModelNameByDownloadUrl(model.DownloadURL)
			if err != nil {
				log.Logger().Sugar().Error(err)
				return
			}
			DownloadUrl = model.DownloadURL
			fileSize = int64(model.Files[0].SizeKb * 1024)
		} else if ModelVersionId != "" {
			model, err := api.GetModelByVersionId(ModelVersionId)
			if err != nil {
				log.Logger().Sugar().Error(err)
				return
			}
			ModelName = model.Name
			DownloadUrl = model.DownloadURL

		} else if ModelId != "" {
			log.Logger().Error("不支持")
			return
			//model, err := api.GetModelById(ModelId)
			//if err != nil {
			//	log.Logger().Sugar().Error(err)
			//	return
			//}
			//ModelName = model.Name
			//DownloadUrl = model.DownloadURL
			//
		} else {
			log.Logger().Error("请选择下载方式")
			return
		}

		err := util.StartDownloadFile(DownloadUrl, DownloadDir+"/"+ModelName, fileSize, NumThreads, MaxChunkSize)
		//err := util.DownloadDirect(DownloadUrl, DownloadDir+"/"+ModelName, fileSize)
		if err != nil {
			log.Logger().Sugar().Error(err)
			return
		}

		if err != nil {

			log.Logger().Sugar().Error(err)
			return
		}
		log.Logger().Sugar().Info("下载完成")
		return
	},
}

func GetModelNameByDownloadUrl(downloadUrl string) (string, error) {
	rangeHeader := fmt.Sprintf("bytes=%d-%d", 0, 1)
	req, err := http.NewRequest("GET", downloadUrl, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Range", rangeHeader)
	res, err := util.GetHttpClient().GetRawClient().Do(req)
	if err != nil {
		return "", err
	}
	h := res.Header.Get("Content-Disposition")
	f := h[strings.Index(h, "filename=")+9:]
	if f == "" {
		return "", fmt.Errorf("no filename")
	}
	f = strings.Trim(f, "\"")
	return f, nil
}
func init() {
	downloadCommand.PersistentFlags().StringVarP(&DownloadUrl, "url", "u", "", "download url")
	downloadCommand.PersistentFlags().StringVarP(&ModelId, "modelId", "m", "", "model id")
	downloadCommand.PersistentFlags().StringVar(&ModelHash, "hash", "", "model hash")
	downloadCommand.PersistentFlags().StringVarP(&DownloadDir, "downloadDir", "o", "", "download dir")
	downloadCommand.PersistentFlags().StringVarP(&ModelVersionId, "modelVersionId", "v", "", "model version id")
	downloadCommand.PersistentFlags().IntVarP(&NumThreads, "numThreads", "t", 8, "num threads")
	downloadCommand.PersistentFlags().Int64VarP(&MaxChunkSize, "maxChunkSize", "s", 1024*1024*1024, "max chunk size")
	rootCmd.AddCommand(downloadCommand)
}
