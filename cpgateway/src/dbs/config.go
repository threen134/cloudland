package dbs

import "github.com/spf13/viper"

func getDBType() string {
	v := viper.GetString("db.type")
	if v == "" {
		return "sqlite3"
	}
	return v
}

func getDBUri() string {
	v := viper.GetString("db.uri")
	if v != "" {
		return v
	}
	v = viper.GetString("db.url")
	if v != "" {
		return v
	}
	return ""
}

func getDBIdle() int {
	v := viper.GetInt("db.idle")
	if v == 0 {
		return 5
	}
	return v
}

func getDBOpen() int {
	v := viper.GetInt("db.open")
	if v == 0 {
		return 50
	}
	return v
}

func getDBLifetime() int {
	v := viper.GetInt("db.lifetime")
	if v == 0 {
		return 30
	}
	return v
}

func getDBDebug() bool {
	return viper.GetBool("db.debug")
}
