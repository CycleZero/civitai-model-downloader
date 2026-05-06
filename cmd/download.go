package cmd

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"civitai-model-downloader/api"
	"civitai-model-downloader/log"
	"civitai-model-downloader/util"
	"civitai-model-downloader/util/downloader"

	"github.com/spf13/cobra"
)

var (
	flagUrl       string
	flagModelId   string
	flagVersionId string
	flagHash      string
	flagOutputDir string
	flagThreads   int
	flagChunkSize int64
)

var downloadCommand = &cobra.Command{
	Use: "download",
	Run: func(cmd *cobra.Command, args []string) {
		var (
			downloadUrl string
			modelName   string
		)
		outputDir := flagOutputDir
		if outputDir == "" {
			outputDir = "."
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		switch {
		case flagUrl != "":
			downloadUrl = flagUrl
			var err error
			modelName, err = resolveFilename(downloadUrl)
			if err != nil {
				log.Logger().Sugar().Errorf("resolve filename: %v", err)
				return
			}
		case flagHash != "":
			model, err := api.GetModelByHash(ctx, flagHash)
			if err != nil {
				log.Logger().Sugar().Errorf("api: %v", err)
				return
			}
			downloadUrl = model.DownloadURL
			modelName, err = resolveFilename(downloadUrl)
			if err != nil && len(model.Files) > 0 && model.Files[0].Name != "" {
				modelName = model.Files[0].Name
			} else if err != nil {
				log.Logger().Sugar().Errorf("resolve filename: %v", err)
				return
			}
		case flagVersionId != "":
			model, err := api.GetModelByVersionId(ctx, flagVersionId)
			if err != nil {
				log.Logger().Sugar().Errorf("api: %v", err)
				return
			}
			downloadUrl = model.DownloadURL
			modelName = model.Name
		case flagModelId != "":
			log.Logger().Error("model-id download not yet implemented, use --hash or --modelVersionId")
			return
		default:
			log.Logger().Error("specify --url, --hash, --modelVersionId, or --modelId")
			return
		}

		outPath := filepath.Join(outputDir, modelName)
		log.Logger().Sugar().Infof("downloading %s -> %s", downloadUrl, outPath)

		cfg := &downloader.Config{
			Concurrency: flagThreads,
			MaxRetries:  3,
			HTTPTimeout: 0,
			Headers:     util.AuthHeader,
			Resume:      true,
			Logger:      log.Logger(),
		}

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			log.Logger().Sugar().Info("interrupted, saving state...")
			cancel()
		}()

		dl := downloader.New(downloadUrl, outPath, cfg)
		err := dl.Download(ctx)
		signal.Stop(sigCh)
		close(sigCh)

		if err != nil {
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
	if cd == "" {
		return "", fmt.Errorf("no Content-Disposition header")
	}

	if name := parseRFC5987(cd); name != "" {
		return name, nil
	}

	if name := parseSimpleFilename(cd); name != "" {
		return name, nil
	}

	return "", fmt.Errorf("cannot parse filename from Content-Disposition: %s", cd)
}

func parseSimpleFilename(cd string) string {
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		idx := strings.Index(cd, "filename=")
		if idx < 0 {
			return ""
		}
		f := cd[idx+9:]
		f = strings.Trim(f, `"`)
		if i := strings.IndexByte(f, ';'); i > 0 {
			f = f[:i]
		}
		return f
	}
	return params["filename"]
}

func parseRFC5987(cd string) string {
	idx := strings.Index(cd, "filename*=")
	if idx < 0 {
		return ""
	}
	rest := cd[idx+10:]
	end := strings.IndexByte(rest, ';')
	if end > 0 {
		rest = rest[:end]
	}
	rest = strings.Trim(rest, `"`)

	encEnd := strings.IndexByte(rest, '\'')
	if encEnd < 0 {
		return ""
	}
	langEnd := strings.IndexByte(rest[encEnd+1:], '\'')
	if langEnd < 0 {
		return ""
	}
	encoded := rest[encEnd+langEnd+2:]
	encoding := strings.ToLower(rest[:encEnd])

	var decoded string
	switch encoding {
	case "utf-8":
		var err error
		decoded, err = urlDecode(encoded)
		if err != nil {
			return ""
		}
	default:
		return ""
	}
	return decoded
}

func urlDecode(s string) (string, error) {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '%' && i+2 < len(s):
			hi, lo := unhex(s[i+1]), unhex(s[i+2])
			if hi < 0 || lo < 0 {
				buf.WriteByte(s[i])
			} else {
				buf.WriteByte(byte(hi<<4) | byte(lo))
				i += 2
			}
		case s[i] == '+':
			buf.WriteByte(' ')
		default:
			buf.WriteByte(s[i])
		}
	}
	return buf.String(), nil
}

func unhex(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'a' <= c && c <= 'f':
		return int(c - 'a' + 10)
	case 'A' <= c && c <= 'F':
		return int(c - 'A' + 10)
	}
	return -1
}

func init() {
	downloadCommand.PersistentFlags().StringVarP(&flagUrl, "url", "u", "", "direct download URL")
	downloadCommand.PersistentFlags().StringVarP(&flagModelId, "modelId", "m", "", "model ID")
	downloadCommand.PersistentFlags().StringVar(&flagHash, "hash", "", "model hash")
	downloadCommand.PersistentFlags().StringVarP(&flagOutputDir, "downloadDir", "o", "", "output directory")
	downloadCommand.PersistentFlags().StringVarP(&flagVersionId, "modelVersionId", "v", "", "model version ID")
	downloadCommand.PersistentFlags().IntVarP(&flagThreads, "numThreads", "t", 8, "number of concurrent download threads")
	downloadCommand.PersistentFlags().Int64VarP(&flagChunkSize, "maxChunkSize", "s", 1024*1024*1024, "max chunk size (unused, kept for compat)")
	rootCmd.AddCommand(downloadCommand)
}
