package cmd

import (
	"civitai-model-downloader/util"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
)

var ConfigFilePath string
var rootCmd = cobra.Command{
	Use: "cvtcli",
	Run: func(cmd *cobra.Command, args []string) {

	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		vc := viper.New()
		ConfigFilePath = "./config.yaml"
		vc.SetConfigFile(ConfigFilePath)
		err := vc.ReadInConfig()
		if err != nil {
			return err
		}
		token := vc.GetString("api-key")
		util.AuthHeader = map[string]string{"Authorization": "Bearer " + token}
		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

}
