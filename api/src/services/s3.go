/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"api/src/model"
	"api/src/utils/tracing"
)

var (
	// 启动后由重试 goroutine 写入、请求并发读取，用原子指针
	s3Client atomic.Pointer[minio.Client]
	s3Bucket string
)

// errS3Config 表示 S3 配置本身有误（如 endpoint 格式非法），重试无意义
var errS3Config = errors.New("invalid S3 config")

// InitS3 构造 MinIO/S3 客户端并校验 bucket。未配置 S3_ENDPOINT 时返回 nil（不启用 S3）；
// endpoint/凭据/bucket 暂不可用时返回错误，由调用方重试；配置错误返回包装了 errS3Config 的错误
func InitS3() error {
	endpoint := viper.GetString("s3.endpoint")
	if endpoint == "" {
		logger.Info("S3_ENDPOINT not set, image S3 store disabled")
		return nil
	}

	accessKey := viper.GetString("s3.access_key")
	secretKey := viper.GetString("s3.secret_key")
	useSSL := viper.GetBool("s3.use_ssl")
	// 兼容带 scheme 的写法（minio-go 只接受 host:port，否则报 fully qualified paths），并据此决定是否使用 SSL
	if strings.HasPrefix(endpoint, "https://") {
		endpoint, useSSL = strings.TrimPrefix(endpoint, "https://"), true
	} else if strings.HasPrefix(endpoint, "http://") {
		endpoint, useSSL = strings.TrimPrefix(endpoint, "http://"), false
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	region := viper.GetString("s3.region")
	bucket := viper.GetString("s3.bucket")
	if bucket == "" {
		bucket = "images"
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", errS3Config, err)
	}

	// minio.New 不发网络请求；BucketExists ping 一下验证 endpoint/凭据/bucket 均可用
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := client.BucketExists(pingCtx, bucket)
	if err != nil {
		return fmt.Errorf("S3 bucket check failed (endpoint=%s bucket=%s): %w", endpoint, bucket, err)
	}
	if !exists {
		return fmt.Errorf("S3 bucket %q does not exist on %s", bucket, endpoint)
	}

	s3Bucket = bucket
	s3Client.Store(client)
	logger.Infof("S3 image store initialized: endpoint=%s bucket=%s ssl=%v", endpoint, bucket, useSSL)
	return nil
}

// S3Configured 指示是否配置了 S3 镜像仓库（不代表已连通）
func S3Configured() bool {
	return viper.GetString("s3.endpoint") != ""
}

// S3Enabled 指示 S3 镜像仓库是否已初始化可用
func S3Enabled() bool {
	return s3Client.Load() != nil
}

// S3Bucket 返回已启用的 bucket 名称（外部调用者用）
func S3Bucket() string {
	return s3Bucket
}

// s3ObjectName 按 deterministic 规则由 image 构造对象名，避免 DB 新增字段
//
//	<ID>-<UUID 前缀> 是全局唯一的（DB id + UUID 去重）
func s3ObjectName(image *model.Image) string {
	prefix := strings.Split(image.UUID, "-")[0]
	return fmt.Sprintf("image-%d-%s", image.ID, prefix)
}

// S3UploadTimeout 返回镜像上传的总超时，默认 120min；可通过 S3_UPLOAD_TIMEOUT_MINUTES 调整
func S3UploadTimeout() time.Duration {
	m := viper.GetInt("s3.upload_timeout_minutes")
	if m <= 0 {
		m = 120
	}
	return time.Duration(m) * time.Minute
}

// ImageInfo 是 qcow2 头部解析的结果
type ImageInfo struct {
	Format      string // "qcow2" 或 "raw"
	VirtualSize uint64
}

// detectImageFormat 读对象头部 512 字节识别镜像格式，规则见 classifyImageHeader
func detectImageFormat(ctx context.Context, bucket, objectName string) (*ImageInfo, error) {
	obj, err := s3Client.Load().GetObject(ctx, bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	header := make([]byte, 512)
	n, err := io.ReadFull(obj, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	stat, err := obj.Stat()
	if err != nil {
		return nil, err
	}
	return classifyImageHeader(header[:n], stat.Size)
}

// unsupportedImageMagics 常见的非磁盘镜像/需先转换的格式，给出明确报错
var unsupportedImageMagics = []struct {
	magic []byte
	desc  string
}{
	{[]byte{0x1f, 0x8b}, "gzip compressed, decompress it first"},
	{[]byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, "xz compressed, decompress it first"},
	{[]byte("BZh"), "bzip2 compressed, decompress it first"},
	{[]byte{0x28, 0xb5, 0x2f, 0xfd}, "zstd compressed, decompress it first"},
	{[]byte("PK\x03\x04"), "zip archive"},
	{[]byte("KDMV"), "vmdk, convert it to qcow2 first"},
	{[]byte("# Disk DescriptorFile"), "vmdk descriptor, convert it to qcow2 first"},
	{[]byte("vhdxfile"), "vhdx, convert it to qcow2 first"},
	{[]byte("conectix"), "vhd, convert it to qcow2 first"},
}

// classifyImageHeader 按文件头部判断镜像格式：qcow2 取头部中的 virtual size；
// raw 必须在 510-511 字节带 MBR 引导签名 0x55AA（GPT 的保护性 MBR、hybrid ISO 同样有），virtual size 取文件大小。
// HTML 错误页、压缩包、vmdk/vhd 等一律报错，避免被当成 raw 登记为可用镜像
func classifyImageHeader(header []byte, size int64) (*ImageInfo, error) {
	// qcow2 magic: 0x514649fb ("QFI\xfb")
	if len(header) >= 32 && binary.BigEndian.Uint32(header[0:4]) == 0x514649fb {
		return &ImageInfo{Format: "qcow2", VirtualSize: binary.BigEndian.Uint64(header[24:32])}, nil
	}
	for _, m := range unsupportedImageMagics {
		if bytes.HasPrefix(header, m.magic) {
			return nil, fmt.Errorf("unsupported image format: %s", m.desc)
		}
	}
	if len(header) < 512 || header[510] != 0x55 || header[511] != 0xAA {
		return nil, fmt.Errorf("not a qcow2 or bootable raw disk image (no MBR/GPT boot signature)")
	}
	return &ImageInfo{Format: "raw", VirtualSize: uint64(size)}, nil
}

// GenerateDownloadURL 生成 presigned GET URL，供 compute node 下载镜像
// 有效期 2h，compute 侧不需要 S3 凭据
func GenerateDownloadURL(ctx context.Context, image *model.Image) (string, error) {
	client := s3Client.Load()
	if client == nil {
		return "", fmt.Errorf("s3 not enabled")
	}
	u, err := client.PresignedGetObject(ctx, s3Bucket, s3ObjectName(image), 2*time.Hour, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// S3PutObject 将 reader 写入 S3，size=-1 触发 multipart，上限 ~640GB
func S3PutObject(ctx context.Context, objectName string, reader io.Reader) (err error) {
	ctx, span := startS3Span(ctx, "PutObject", objectName)
	defer func() { tracing.EndSpan(span, err) }()
	_, err = s3Client.Load().PutObject(ctx, s3Bucket, objectName, reader, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		PartSize:    64 * 1024 * 1024, // 64MB × 10000 part ≈ 640GB 上限
	})
	return err
}

// S3RemoveObject 删除 S3 中的对象；对不存在对象幂等
func S3RemoveObject(ctx context.Context, objectName string) (err error) {
	ctx, span := startS3Span(ctx, "RemoveObject", objectName)
	defer func() { tracing.EndSpan(span, err) }()
	return s3Client.Load().RemoveObject(ctx, s3Bucket, objectName, minio.RemoveObjectOptions{})
}

// S3DetectImage 只暴露给外部调用的封装，内部走 detectImageFormat
func S3DetectImage(ctx context.Context, objectName string) (info *ImageInfo, err error) {
	ctx, span := startS3Span(ctx, "GetObject", objectName)
	defer func() { tracing.EndSpan(span, err) }()
	return detectImageFormat(ctx, s3Bucket, objectName)
}

// startS3Span 为 S3 操作创建子 span（仅在 ctx 带上游 span 时）；不在 HTTP 层注入 trace 头，避免外发到第三方对象存储
func startS3Span(ctx context.Context, op, objectName string) (context.Context, trace.Span) {
	return tracing.StartChild(ctx, "s3."+op,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("aws.s3.bucket", s3Bucket),
			attribute.String("aws.s3.key", objectName),
		))
}

// ---- capture 上传 HMAC token ----
//
// compute 把 capture 出的镜像 POST 回 clapi 转发到 S3，为了鉴权使用
// HMAC-SHA256(secret, "<image_id>|<expiry>") 作为短期 token，secret 只存于 clapi 环境变量

// GenerateCaptureToken 派发 capture_image.sh 时调用，返回 token 和过期 unix 时间戳
func GenerateCaptureToken(imageID int64) (token string, expiry int64) {
	expiry = time.Now().Add(2 * time.Hour).Unix()
	mac := hmac.New(sha256.New, []byte(viper.GetString("capture.upload_secret")))
	fmt.Fprintf(mac, "%d|%d", imageID, expiry)
	token = hex.EncodeToString(mac.Sum(nil))
	return
}

// VerifyCaptureToken 校验 token 及是否过期
func VerifyCaptureToken(token string, imageID, expiry int64) bool {
	if time.Now().Unix() > expiry {
		return false
	}
	secret := viper.GetString("capture.upload_secret")
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d|%d", imageID, expiry)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(token), []byte(expected))
}

// BuildCaptureUploadURL 返回 compute 访问 clapi 的上传 URL，含 expiry 查询参数
func BuildCaptureUploadURL(imageID, expiry int64) string {
	base := strings.TrimRight(viper.GetString("clapi.internal_url"), "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/internal/images/%d/upload?expiry=%d", base, imageID, expiry)
}

// ParseS3Host 从 S3_ENDPOINT 抽取 host（用于 DNS 注册判断是否是 `.cloudland.internal` 域名）
func ParseS3Host() string {
	ep := viper.GetString("s3.endpoint")
	if ep == "" {
		return ""
	}
	if !strings.Contains(ep, "://") {
		ep = "http://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
