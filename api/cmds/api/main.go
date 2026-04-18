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

	// S3 镜像仓库（MinIO 或外部 S3）配置绑定
	viper.BindEnv("s3.endpoint", "S3_ENDPOINT")
	viper.BindEnv("s3.access_key", "S3_ACCESS_KEY")
	viper.BindEnv("s3.secret_key", "S3_SECRET_KEY")
	viper.BindEnv("s3.bucket", "S3_BUCKET")
	viper.BindEnv("s3.region", "S3_REGION")
	viper.BindEnv("s3.use_ssl", "S3_USE_SSL")
	viper.BindEnv("s3.upload_timeout_minutes", "S3_UPLOAD_TIMEOUT_MINUTES")

	// DNS 注册域名（clapi 启动时写入 hyper-hosts）
	viper.BindEnv("minio.hostname", "MINIO_HOSTNAME")
	viper.BindEnv("clapi.hostname", "CLAPI_HOSTNAME")

	// capture 上传链路：compute → clapi → MinIO
	viper.BindEnv("clapi.internal_url", "CLAPI_INTERNAL_URL")
	viper.BindEnv("sci.shared_secret", "SCI_SHARED_SECRET")

	// management_vip 用于 DNS 注册（MinIO/clapi 域名指向控制节点 VIP）
	viper.BindEnv("management_vip", "MANAGEMENT_VIP")

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
