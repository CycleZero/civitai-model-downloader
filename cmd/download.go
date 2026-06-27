package cmd

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"civitai-model-downloader/api"
	"civitai-model-downloader/log"
	"civitai-model-downloader/util"

	"github.com/CycleZero/downloader"
	"github.com/spf13/cobra"
)

var (
	flagUrl          string
	flagModelId      string
	flagVersionId    string
	flagHash         string
	flagOutputDir    string
	flagThreads      int
	flagChunkSizeStr string
	flagMaxChunkSize int64
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
			ChunkSize:   parseChunkSize(flagChunkSizeStr),
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
	// Drain the body so the TCP connection returns to the idle pool
	// for reuse by the downloader's probe/chunk requests.
	defer func() {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	// Surface auth/permission/not-found errors with a clear message
	// instead of the misleading "no Content-Disposition header".
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d when resolving filename (check api-key)", resp.StatusCode)
	}

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
	downloadCommand.PersistentFlags().StringVarP(&flagChunkSizeStr, "chunkSize", "c", "", "chunk size for dynamic worker pool (e.g. 16M, 1G, 16777216; empty=auto)")
	downloadCommand.PersistentFlags().Int64VarP(&flagMaxChunkSize, "maxChunkSize", "s", 1024*1024*1024, "(deprecated, unused) kept for backward compatibility")
	rootCmd.AddCommand(downloadCommand)
}

// parseChunkSize parses a human-friendly size string into bytes.
// Accepts plain integers ("16777216"), or numbers with a unit
// suffix: B, K/KB, M/MB, G/GB (case-insensitive). Returns 0 for an
// empty string, which signals "auto-select" to the downloader.
func parseChunkSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}

	numStr := s
	unit := ""
	for i, c := range s {
		if (c >= '0' && c <= '9') || c == '.' {
			continue
		}
		numStr = s[:i]
		unit = strings.ToLower(strings.TrimSpace(s[i:]))
		break
	}
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	var mult int64
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kb":
		mult = 1024
	case "m", "mb":
		mult = 1024 * 1024
	case "g", "gb":
		mult = 1024 * 1024 * 1024
	default:
		return 0
	}
	return int64(n * float64(mult))
}
