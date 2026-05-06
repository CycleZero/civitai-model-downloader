package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"civitai-model-downloader/api"
	"civitai-model-downloader/log"
	"civitai-model-downloader/util"
	"civitai-model-downloader/util/downloader"

	"github.com/spf13/cobra"
)

var (
	DownloadUrl     string
	ModelId         string
	ModelVersionId  string
	ModelName       string
	ModelHash       string
	DownloadDir     string
	NumThreads      int
	MaxChunkSize    int64
)

var downloadCommand = &cobra.Command{
	Use: "download",
	Run: func(cmd *cobra.Command, args []string) {
		if DownloadDir == "" {
			DownloadDir = "."
		}

		if DownloadUrl != "" {
			if ModelName == "" {
				var err error
				ModelName, err = resolveFilename(DownloadUrl)
				if err != nil {
					log.Logger().Sugar().Errorf("resolve filename: %v", err)
					return
				}
			}
		} else if ModelHash != "" {
			model, err := api.GetModelByHash(ModelHash)
			if err != nil {
				log.Logger().Sugar().Errorf("api: %v", err)
				return
			}
			DownloadUrl = model.DownloadURL
			if ModelName == "" {
				var err error
				ModelName, err = resolveFilename(DownloadUrl)
				if err != nil && len(model.Files) > 0 && model.Files[0].Name != "" {
					ModelName = model.Files[0].Name
				} else if err != nil {
					log.Logger().Sugar().Errorf("resolve filename: %v", err)
					return
				}
			}
		} else if ModelVersionId != "" {
			model, err := api.GetModelByVersionId(ModelVersionId)
			if err != nil {
				log.Logger().Sugar().Errorf("api: %v", err)
				return
			}
			ModelName = model.Name
			DownloadUrl = model.DownloadURL
		} else if ModelId != "" {
			log.Logger().Error("model-id download not yet implemented, use --hash or --modelVersionId")
			return
		} else {
			log.Logger().Error("specify --url, --hash, --modelVersionId, or --modelId")
			return
		}

		outPath := DownloadDir + "/" + ModelName

		log.Logger().Sugar().Infof("downloading %s -> %s", DownloadUrl, outPath)

		cfg := &downloader.Config{
			Concurrency: NumThreads,
			MaxRetries:  3,
			HTTPTimeout: 0,
			Headers:     util.AuthHeader,
			Resume:      true,
			Logger:      log.Logger(),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// graceful shutdown on SIGINT/SIGTERM
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			log.Logger().Sugar().Info("interrupted, saving state...")
			cancel()
		}()

		dl := downloader.New(DownloadUrl, outPath, cfg)
		if err := dl.Download(ctx); err != nil {
			log.Logger().Sugar().Errorf("download: %v", err)
			return
		}
		log.Logger().Sugar().Infof("download complete: %s", outPath)
	},
}

func resolveFilename(url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Range", "bytes=0-1")
	if util.AuthHeader != nil {
		for k, v := range util.AuthHeader {
			req.Header.Set(k, v)
		}
	}
	resp, err := util.GetHttpClient().GetRawClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	cd := resp.Header.Get("Content-Disposition")
	idx := strings.Index(cd, "filename=")
	if idx < 0 {
		return "", fmt.Errorf("no filename in Content-Disposition: %s", cd)
	}
	f := cd[idx+9:]
	f = strings.Trim(f, `"`)
	if idx2 := strings.Index(f, `";`); idx2 > 0 {
		f = f[:idx2]
	}
	if f == "" {
		return "", fmt.Errorf("empty filename")
	}
	return f, nil
}

func init() {
	downloadCommand.PersistentFlags().StringVarP(&DownloadUrl, "url", "u", "", "direct download URL")
	downloadCommand.PersistentFlags().StringVarP(&ModelId, "modelId", "m", "", "model ID")
	downloadCommand.PersistentFlags().StringVar(&ModelHash, "hash", "", "model hash")
	downloadCommand.PersistentFlags().StringVarP(&DownloadDir, "downloadDir", "o", "", "output directory")
	downloadCommand.PersistentFlags().StringVarP(&ModelVersionId, "modelVersionId", "v", "", "model version ID")
	downloadCommand.PersistentFlags().IntVarP(&NumThreads, "numThreads", "t", 8, "number of concurrent download threads")
	downloadCommand.PersistentFlags().Int64VarP(&MaxChunkSize, "maxChunkSize", "s", 1024*1024*1024, "max chunk size (unused, kept for compat)")
	rootCmd.AddCommand(downloadCommand)
}
