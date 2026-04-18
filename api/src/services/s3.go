/*
Copyright <holder> All Rights Reserved.
SPDX-License-Identifier: Apache-2.0
*/

package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/spf13/viper"

	"api/src/model"
)

var (
	s3Client *minio.Client
	s3Bucket string
)

// InitS3 构造 MinIO/S3 客户端，失败时不 fatal —— S3Enabled() 返回 false，走 legacy 模式
func InitS3() {
	endpoint := viper.GetString("s3.endpoint")
	if endpoint == "" {
		logger.Info("S3_ENDPOINT not set, image S3 store disabled")
		return
	}

	accessKey := viper.GetString("s3.access_key")
	secretKey := viper.GetString("s3.secret_key")
	useSSL := viper.GetBool("s3.use_ssl")
	region := viper.GetString("s3.region")
	s3Bucket = viper.GetString("s3.bucket")
	if s3Bucket == "" {
		s3Bucket = "images"
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
		Region: region,
	})
	if err != nil {
		logger.Errorf("Failed to init S3 client: %v (falling back to legacy mode)", err)
		return
	}

	// minio.New 不发网络请求；BucketExists ping 一下验证 endpoint/凭据/bucket 均可用
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := client.BucketExists(pingCtx, s3Bucket)
	if err != nil {
		logger.Errorf("S3 bucket check failed (endpoint=%s bucket=%s): %v (falling back to legacy mode)", endpoint, s3Bucket, err)
		return
	}
	if !exists {
		logger.Errorf("S3 bucket %q does not exist on %s (falling back to legacy mode)", s3Bucket, endpoint)
		return
	}

	s3Client = client
	logger.Infof("S3 image store initialized: endpoint=%s bucket=%s ssl=%v", endpoint, s3Bucket, useSSL)
}

// S3Enabled 指示当前是否启用了 S3 镜像仓库
func S3Enabled() bool {
	return s3Client != nil
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

// detectImageFormat 读对象前 32 字节，识别 qcow2 magic 并提取 virtual size
// qcow2 失败时回退为 raw，virtual size 取对象实际大小
func detectImageFormat(ctx context.Context, bucket, objectName string) (*ImageInfo, error) {
	obj, err := s3Client.GetObject(ctx, bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()

	header := make([]byte, 32)
	if _, err := io.ReadFull(obj, header); err != nil {
		return nil, err
	}
	// qcow2 magic: 0x514649fb ("QFI\xfb")
	if binary.BigEndian.Uint32(header[0:4]) == 0x514649fb {
		virtualSize := binary.BigEndian.Uint64(header[24:32])
		return &ImageInfo{Format: "qcow2", VirtualSize: virtualSize}, nil
	}
	// 非 qcow2 视为 raw
	stat, err := obj.Stat()
	if err != nil {
		return nil, err
	}
	return &ImageInfo{Format: "raw", VirtualSize: uint64(stat.Size)}, nil
}

// GenerateDownloadURL 生成 presigned GET URL，供 compute node 下载镜像
// 有效期 2h，compute 侧不需要 S3 凭据
func GenerateDownloadURL(ctx context.Context, image *model.Image) (string, error) {
	if !S3Enabled() {
		return "", fmt.Errorf("s3 not enabled")
	}
	u, err := s3Client.PresignedGetObject(ctx, s3Bucket, s3ObjectName(image), 2*time.Hour, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// S3PutObject 将 reader 写入 S3，size=-1 触发 multipart，上限 ~640GB
func S3PutObject(ctx context.Context, objectName string, reader io.Reader) error {
	_, err := s3Client.PutObject(ctx, s3Bucket, objectName, reader, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream",
		PartSize:    64 * 1024 * 1024, // 64MB × 10000 part ≈ 640GB 上限
	})
	return err
}

// S3RemoveObject 删除 S3 中的对象；对不存在对象幂等
func S3RemoveObject(ctx context.Context, objectName string) error {
	return s3Client.RemoveObject(ctx, s3Bucket, objectName, minio.RemoveObjectOptions{})
}

// S3DetectImage 只暴露给外部调用的封装，内部走 detectImageFormat
func S3DetectImage(ctx context.Context, objectName string) (*ImageInfo, error) {
	return detectImageFormat(ctx, s3Bucket, objectName)
}

// ---- capture 上传 HMAC token ----
//
// compute 把 capture 出的镜像 POST 回 clapi 转发到 S3，为了鉴权使用
// HMAC-SHA256(secret, "<image_id>|<expiry>") 作为短期 token，secret 只存于 clapi 环境变量

// GenerateCaptureToken 派发 capture_image.sh 时调用，返回 token 和过期 unix 时间戳
func GenerateCaptureToken(imageID int64) (token string, expiry int64) {
	expiry = time.Now().Add(2 * time.Hour).Unix()
	mac := hmac.New(sha256.New, []byte(viper.GetString("sci.shared_secret")))
	fmt.Fprintf(mac, "%d|%d", imageID, expiry)
	token = hex.EncodeToString(mac.Sum(nil))
	return
}

// VerifyCaptureToken 校验 token 及是否过期
func VerifyCaptureToken(token string, imageID, expiry int64) bool {
	if time.Now().Unix() > expiry {
		return false
	}
	secret := viper.GetString("sci.shared_secret")
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
