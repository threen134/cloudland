package common

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

var (
	privateKey   *rsa.PrivateKey
	publicKey    *rsa.PublicKey
	publicKeyPEM string
	keyOnce      sync.Once
)

type AccessTokenClaims struct {
	jwt.RegisteredClaims
	Email   string `json:"email"`
	OrgID   string `json:"org_id"`
	OrgName string `json:"org_name"`
	Region  string `json:"region"`
	SR      int    `json:"sr"` // SystemRole
	OR      int    `json:"or"` // OrgRole
	ST      int    `json:"st"` // UserStatus
	IsOwner bool   `json:"is_owner"`
	Type    string `json:"type"`
}

func loadKeys() {
	privPath := viper.GetString("auth.rsa_private_key_path")
	pubPath := viper.GetString("auth.rsa_public_key_path")
	if privPath == "" {
		privPath = "keys/private.pem"
	}
	if pubPath == "" {
		pubPath = "keys/public.pem"
	}

	// Load private key
	privBytes, err := os.ReadFile(privPath)
	if err != nil {
		log.Warnf("Failed to read RSA private key from %s: %v", privPath, err)
		return
	}
	block, _ := pem.Decode(privBytes)
	if block == nil {
		log.Warn("Failed to decode RSA private key PEM block")
		return
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS1 format
		key2, err2 := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err2 != nil {
			log.Warnf("Failed to parse RSA private key: %v / %v", err, err2)
			return
		}
		privateKey = key2
	} else {
		var ok bool
		privateKey, ok = key.(*rsa.PrivateKey)
		if !ok {
			log.Warn("Private key is not RSA")
			return
		}
	}

	// Load public key
	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		log.Warnf("Failed to read RSA public key from %s: %v", pubPath, err)
		return
	}
	publicKeyPEM = string(pubBytes)
	block, _ = pem.Decode(pubBytes)
	if block == nil {
		log.Warn("Failed to decode RSA public key PEM block")
		return
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		log.Warnf("Failed to parse RSA public key: %v", err)
		return
	}
	var ok bool
	publicKey, ok = pub.(*rsa.PublicKey)
	if !ok {
		log.Warn("Public key is not RSA")
		return
	}

	log.Info("RSA key pair loaded successfully")
}

func ensureKeys() {
	keyOnce.Do(loadKeys)
}

func GetPublicKeyPEM() string {
	ensureKeys()
	return publicKeyPEM
}

func CreateAccessToken(userUUID, email, orgUUID, orgName, region string,
	systemRole, orgRole, userStatus int, isOwner bool) (string, string, error) {

	ensureKeys()
	if privateKey == nil {
		return "", "", fmt.Errorf("RSA private key not loaded")
	}

	expireMinutes := viper.GetInt("auth.access_token_expire_minutes")
	if expireMinutes == 0 {
		expireMinutes = 120
	}

	jti := uuid.New().String()
	now := time.Now()
	claims := AccessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userUUID,
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(expireMinutes) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti,
			Issuer:    "CloudlandCPGateway",
		},
		Email:   email,
		OrgID:   orgUUID,
		OrgName: orgName,
		Region:  region,
		SR:      systemRole,
		OR:      orgRole,
		ST:      userStatus,
		IsOwner: isOwner,
		Type:    "access",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenStr, err := token.SignedString(privateKey)
	if err != nil {
		return "", "", fmt.Errorf("failed to sign access token: %w", err)
	}
	return tokenStr, jti, nil
}

func DecodeAccessToken(tokenStr string) (*AccessTokenClaims, error) {
	ensureKeys()
	if publicKey == nil {
		return nil, fmt.Errorf("RSA public key not loaded")
	}

	claims := &AccessTokenClaims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return publicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to parse access token: %w", err)
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid access token")
	}
	if claims.Type != "access" {
		return nil, fmt.Errorf("token type is not 'access'")
	}
	return claims, nil
}

// --- Activation Token (HS256) ---

func CreateActivationToken(userUUID string) (string, error) {
	secretKey := viper.GetString("auth.secret_key")
	if secretKey == "" {
		return "", fmt.Errorf("secret_key not configured")
	}
	expireHours := viper.GetInt("auth.activation_token_expire_hours")
	if expireHours == 0 {
		expireHours = 24
	}

	claims := jwt.MapClaims{
		"sub":  userUUID,
		"type": "activation",
		"exp":  time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}

func VerifyActivationToken(tokenStr string) (string, error) {
	secretKey := viper.GetString("auth.secret_key")
	if secretKey == "" {
		return "", fmt.Errorf("secret_key not configured")
	}

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secretKey), nil
	})
	if err != nil {
		return "", fmt.Errorf("invalid activation token: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("invalid activation token claims")
	}
	if claims["type"] != "activation" {
		return "", fmt.Errorf("token type is not 'activation'")
	}
	userUUID, ok := claims["sub"].(string)
	if !ok || userUUID == "" {
		return "", fmt.Errorf("missing sub in activation token")
	}
	return userUUID, nil
}

// --- Invitation Token (HS256) ---

func CreateInvitationToken(email, orgUUID string) (string, error) {
	secretKey := viper.GetString("auth.secret_key")
	if secretKey == "" {
		return "", fmt.Errorf("secret_key not configured")
	}
	expireHours := viper.GetInt("auth.activation_token_expire_hours")
	if expireHours == 0 {
		expireHours = 24
	}

	claims := jwt.MapClaims{
		"sub":  email,
		"org":  orgUUID,
		"type": "invitation",
		"jti":  uuid.New().String(),
		"exp":  time.Now().Add(time.Duration(expireHours) * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}

func VerifyInvitationToken(tokenStr string) (email, orgUUID string, err error) {
	secretKey := viper.GetString("auth.secret_key")
	if secretKey == "" {
		return "", "", fmt.Errorf("secret_key not configured")
	}

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secretKey), nil
	})
	if err != nil {
		return "", "", fmt.Errorf("invalid invitation token: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", "", fmt.Errorf("invalid invitation token claims")
	}
	if claims["type"] != "invitation" {
		return "", "", fmt.Errorf("token type is not 'invitation'")
	}
	email, _ = claims["sub"].(string)
	orgUUID, _ = claims["org"].(string)
	if email == "" || orgUUID == "" {
		return "", "", fmt.Errorf("missing sub or org in invitation token")
	}
	return email, orgUUID, nil
}
