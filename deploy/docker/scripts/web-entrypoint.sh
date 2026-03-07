#!/bin/bash
# Web 服务 (clbase/clapi) 容器启动脚本
# 负责从环境变量生成 config.toml

set -e

CONF_DIR=/opt/cloudland/web/conf
CONF_FILE=$CONF_DIR/config.toml

# 如果用户已经挂载了 config.toml，直接使用
if [ -f "$CONF_FILE" ]; then
    echo "==> 使用已挂载的 config.toml"
else
    echo "==> 从环境变量生成 config.toml"

    # 默认值
    : "${PUBLIC_IP:=127.0.0.1}"
    : "${DB_HOST:=postgres}"
    : "${DB_PORT:=5432}"
    : "${DB_USER:=postgres}"
    : "${DB_PASSWORD:=d6Passwd}"
    : "${DB_NAME:=cloudland}"
    : "${ADMIN_PASSWORD:=passw0rd}"
    : "${CLOUDLAND_HOST:=cloudland}"
    : "${MONITOR_HOST:=prometheus}"
    : "${MONITOR_PORT:=9090}"

    cat > "$CONF_FILE" <<_EOF_
[base]
listen = "0.0.0.0:5443"
key = "/certs/cland/selfsigned.key"
cert = "/certs/cland/selfsigned.crt"

[console]
host = "${PUBLIC_IP}"
port = 443

[rest]
listen = "0.0.0.0:8255"
key = "/certs/cland/selfsigned.key"
cert = "/certs/cland/selfsigned.crt"

[internal]
listen = "0.0.0.0:5005"

[sci]
endpoint = "http://${CLOUDLAND_HOST}:5006"

[db]
type = "postgres"
uri = "host=${DB_HOST} port=${DB_PORT} user=${DB_USER} password=${DB_PASSWORD} dbname=${DB_NAME} sslmode=disable"
debug = true
open = 50
lifetime = 30
idle = 1

[monitor]
host = "${MONITOR_HOST}"
port = "${MONITOR_PORT}"

[admin]
password = "${ADMIN_PASSWORD}"

[key]
_EOF_

    if [ "${COMPOSE_PROFILES:-dev}" = "dev" ] && [ ! -s "/certs/cland/jwt_private.pem" ]; then
        echo "==> 使用内置的开发模式 RSA 密钥"
        cat >> "$CONF_FILE" <<'_EOF_'
private = """-----BEGIN RSA PRIVATE KEY-----
MIIEpQIBAAKCAQEAv2TkunvS/lUZm7oH4cpHvy/QyT72kwoVdEzBwfBNZoXWmC2h
P/+qoTSXiq6FGwvVLSaiOSIR1WDDuihNXmR4zXDAeObHicVlldmmG8NzgCW5ZjO7
1fnmGXgv76VtrB6ccu5K2JxWSzvLrrH9/PmibukyRyO4hBhxeBhipeaLi2HM2SJS
eu867NzBKbQqbsaXbb+D+Ko4T4C5ouJATYe6+0ZyhqyFJmz6ARoDnNCE5DvPXniA
K0b3Mm/AJbEBX0p8OA6m5xeeRFggueaA3BBw1NOcZoVLDo2XNH5vWrj1eyMZHac7
tirTDOE2VxR14xGhjuYSaGO/yc0VjjtUX8N7VQIDAQABAoIBAHUO7UINR5/cRpxT
LEzxne4V/ZmIU+DcswB9jafjJEPHKdfLWKs+4IpWEzVzxd8j3o8N6PwOlV+vHia2
TZOk2am1A1Muuu3NeHMtOgYTBYpkCD+09nZJsGz1cEQfJrO1yTQWAFr5S2IaQVoo
bNKTj8BMCj8uXsUT+hpct8EF/2UQK37VhfkDOvrRtTVDiw88DJNlcVf9Ugo0GG2h
TZT1RT/h6JeEA1+AomHsSfEJ4XKlmRaHqstHl3T9JPoeNlUr4KgFeVg+cH0ta3EM
8lQsv1z5jVI/v9vBuUN2tgq4OQ/fGl8KWlKZa8XyIhz3sKusfquCFx1vWah3Jm+h
w/UhCSUCgYEA/qbQkISXX3l6QcFq/ojjuH4+YmeGaRmu/F2+oLNB35Xr1AqBrPLH
vT8WE2b+bAahw+my353x1iQygABO3U1KQP+RzKi6CMwrDSNn6y/xyGxIPKR/MdiE
m3Yj+LxsWrTIYfhzPchP5L2qB3V7Dt8WUHhGI3kIsOpm0i5dx9NgVlcCgYEAwGhU
+/T3/6NBY3wUd0ngXlKiDNHt34ZoCoAY0NLY/lsiWUtwYlgC9Qs91cX9aLCyO6mZ
LF8AJ7aaVMyRa0r5ycem9uIzJUhDZo+9v0Zb9fbkjoQ86/BGezJA+79Sy3Q54s+D
jJ9ElaCJ4n6/FIP/IkK+9Mgp5G8Ts8ufEE7C+DMCgYEAt3iUuCrvrRgm341teypB
d8FtTRTtoHeiva0FFV9RzLeFi+Zt+5+IDW+QhjYkhMxabH7KI5b2kKTPxa1zJLr1
DtOTxnKiZohDVFn4G1kVyKNLgHW8Nrua/y8lR6bqIogx/3Q0A4V5GoMUJ/+aw+Iz
f5LIZfJkMqMPpctGQhynQk0CgYEAjE7Iwl92Rc4YTeLazc5qtn3NvEmODHVoA1g8
QHOxV3K/zpwLnTuPFICZG/3geGp53rYjg87XPx6S1onC9ZncI3/bSqfTIjnbJLxn
Y0d8ohXjv+XAw1EZJJeV+b8gMktUNwiaZn5yNia2xhslXmGPOL2xoLEik3lIxdET
8oFs/i8CgYEA8PguaOdYUDyumAwtiq59D8Zs0ZpmWOO59eyvIqalZlNdG04Mmpg1
rNb9zpL+Jy5lj+4NAWjgEaxJWQuWF+Gc8epZk8vw0GNf3VCoZiHSEM4F2u8V7IoN
gTqcfiuPwIa74zsLJnyCsuERYXmkz/almnTrJx0xkPj55CzRfyZX31w=
-----END RSA PRIVATE KEY-----
"""
public = """-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAv2TkunvS/lUZm7oH4cpH
vy/QyT72kwoVdEzBwfBNZoXWmC2hP/+qoTSXiq6FGwvVLSaiOSIR1WDDuihNXmR4
zXDAeObHicVlldmmG8NzgCW5ZjO71fnmGXgv76VtrB6ccu5K2JxWSzvLrrH9/Pmi
bukyRyO4hBhxeBhipeaLi2HM2SJSeu867NzBKbQqbsaXbb+D+Ko4T4C5ouJATYe6
+0ZyhqyFJmz6ARoDnNCE5DvPXniAK0b3Mm/AJbEBX0p8OA6m5xeeRFggueaA3BBw
1NOcZoVLDo2XNH5vWrj1eyMZHac7tirTDOE2VxR14xGhjuYSaGO/yc0VjjtUX8N7
VQIDAQAB
-----END PUBLIC KEY-----
"""
_EOF_
    else
        echo "==> 读取 /certs/cland 下初始化的 RSA 密钥"
        if [ ! -s "/certs/cland/jwt_private.pem" ] || [ ! -s "/certs/cland/jwt_public.pem" ]; then
            echo "ERROR: 找不到有效的 jwt_private.pem 或 jwt_public.pem，请先执行 init-certs.sh"
            exit 1
        fi
        
        echo "private = \"\"\"" >> "$CONF_FILE"
        cat "/certs/cland/jwt_private.pem" >> "$CONF_FILE"
        echo "\"\"\"" >> "$CONF_FILE"
        
        echo "public = \"\"\"" >> "$CONF_FILE"
        cat "/certs/cland/jwt_public.pem" >> "$CONF_FILE"
        echo "\"\"\"" >> "$CONF_FILE"
    fi
fi


LOG_DIR=/opt/cloudland/log
mkdir -p "$LOG_DIR"

echo "==> 启动: $@"
exec "$@" &
CHILD_PID=$!

# 根据服务名自动推断日志文件名 (clapi → clapi.log, clbase → clbase.log)
BIN_NAME=$(basename "$1")
LOG_FILE="$LOG_DIR/${BIN_NAME}.log"
for i in $(seq 1 20); do
    [ -f "$LOG_FILE" ] && break
    sleep 0.5
done
[ -f "$LOG_FILE" ] && tail -f "$LOG_FILE" &

wait $CHILD_PID
