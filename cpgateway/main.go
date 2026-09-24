package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"cpgateway/src/apis"
	"cpgateway/src/dbs"
	"cpgateway/src/services"
	"cpgateway/src/tracing"
)

var Version = "dev"

func initConfig() {
	configFile := os.Getenv("CPGATEWAY_CONFIG")
	if configFile == "" {
		configFile = "conf/config.toml"
	}

	viper.SetConfigFile(configFile)
	viper.SetConfigType("toml")
	// Environment variables override config keys: db.uri → DB_URI, auth.secret_key → AUTH_SECRET_KEY.
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		log.Warnf("Failed to read config file %s: %v, using defaults", configFile, err)
	}
}

func main() {
	initConfig()
	log.AddHook(tracing.LogrusHook{})
	flushTracing := tracing.Init(context.Background(), "cpgateway", Version)
	defer flushTracing()

	log.Infof("CPGateway-Go version %s starting...", Version)

	// Initialize database (triggers AutoMigrate + AutoUpgrade)
	_ = dbs.DB()

	// Run startup initialization (create root user, admin org, etc.)
	services.Init()

	// Start background heartbeat service
	services.StartHeartbeat()

	// Setup and start HTTP server
	addr := viper.GetString("server.listen_addr")
	port := viper.GetInt("server.listen_port")
	if addr == "" {
		addr = "0.0.0.0"
	}
	if port == 0 {
		port = 8000
	}

	router := apis.Routes()
	listenAddr := fmt.Sprintf("%s:%d", addr, port)
	log.Infof("Listening on %s", listenAddr)
	if err := router.Run(listenAddr); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
