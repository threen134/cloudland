/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"

	"api/src/apis"
	"api/src/rpcs"
	"api/src/services"
	rlog "api/src/utils/log"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

var (
	Version = "0.0.1"
)

var (
	clientCmd = &cobra.Command{
		Use: "clapi",
	}
)

func RunDaemon(cmd *cobra.Command, args []string) (err error) {
	g, _ := errgroup.WithContext(context.Background())
	g.Go(apis.Run)
	g.Go(rpcs.Run)
	return g.Wait()
}

func RootCmd() (cmd *cobra.Command) {
	for _, arg := range os.Args {
		if arg == "--daemon" {
			daemonCmd := &cobra.Command{
				Use:  "CloudlandAPI",
				RunE: RunDaemon,
			}
			daemonCmd.Flags().Bool("daemon", false, "daemon")
			return daemonCmd
		}
	}
	return clientCmd
}

func init() {
	viper.Set("AppVersion", Version)
	viper.Set("GoVersion", strings.Title(runtime.Version()))
	viper.SetConfigFile("conf/config.toml")
	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("Failed to load configuration file %+v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Configuration loaded successfully from conf/config.toml\n")

	// 绑定环境变量: 允许通过 CONSOLE_HOST / CONSOLE_PORT 覆盖配置文件中的 console.host / console.port
	// 用于多 Region 部署时指定 Region Gateway 地址
	viper.BindEnv("console.host", "CONSOLE_HOST")
	viper.BindEnv("console.port", "CONSOLE_PORT")

	rlog.InitLogger("clapi.log")
	fmt.Printf("Logger initialized, logs are being written to clapi.log\n")
	
	// Initialize services (creates admin user, org, default security group)
	services.Init()
}

func main() {
	rootCmd := RootCmd()
	if err := rootCmd.Execute(); err != nil {
		rootCmd.Println(err)
	}
}
