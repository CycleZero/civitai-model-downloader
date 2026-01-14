package cmd

import (
	"civitai-model-downloader/log"
	"civitai-model-downloader/util"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"os"
	"os/user"
)

var ConfigFilePath string
var rootCmd = cobra.Command{
	Use: "cvtcli",
	Run: func(cmd *cobra.Command, args []string) {

	},
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		vc, err := initConfig()
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

func initConfig() (*viper.Viper, error) {
	vc := viper.New()
	if ConfigFilePath != "" {
		vc.SetConfigFile(ConfigFilePath)
		err := vc.ReadInConfig()
		if err != nil {
			return nil, err
		}
		log.Logger().Info("已加载配置文件：" + vc.ConfigFileUsed())
		return vc, nil
	} else {
		_, err := os.Stat(DefaultConfigPath())
		if err != nil {
			if os.IsNotExist(err) {
				file, err := util.CreatFile(DefaultConfigPath())
				if err != nil {
					return nil, err
				}
				file.Close()
				vc.Set("api-key", "")
				err = vc.WriteConfigAs(DefaultConfigPath())
				if err != nil {
					return nil, err
				}
				log.Logger().Info("配置文件已初始化，请前往 " + DefaultConfigPath() + " 修改api-key")
				return nil, fmt.Errorf("请修改配置文件")
			} else {
				log.Logger().Error("无法初始化配置文件")
				panic(err)
			}
		}

		vc.SetConfigFile(DefaultConfigPath())
		err = vc.ReadInConfig()
		if err != nil {
			return nil, err
		}
		log.Logger().Info("已加载配置文件：" + vc.ConfigFileUsed())
		return vc, nil
	}
}

func DefaultConfigPath() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.HomeDir + "/.cvtcli/config.yaml"
}
