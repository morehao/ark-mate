package slo_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-iam/sdk/rp"
	"github.com/morehao/ark-iam/sdk/rp/logout"
	"github.com/morehao/ark-mate/internal/control/slo"
	"github.com/morehao/ark-mate/internal/persistence/store"

	_ "github.com/morehao/golib/glog/driver/zap"
)

const (
	sloIssuer = "http://localhost:8081/oidc"
	sloClient = "ark_mate_web"
	sloKID    = "slo-key-1"
)

// signLogoutToken 按 OIDC Back-Channel Logout 1.0 §2.6 签一枚合法 logout_token。
func signLogoutToken(t *testing.T, priv *rsa.PrivateKey, jti, sid string) string {
	t.Helper()
	now := time.Now()
	claims := &contract.LogoutTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    sloIssuer,
			Audience:  jwt.ClaimStrings{sloClient},
			Subject:   contract.BuildPersonSubject("p1"),
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
		SessionID: sid,
		Events:    map[string]any{"http://schemas.openid.net/event/backchannel-logout": map[string]any{}},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = sloKID
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("签 logout_token: %v", err)
	}
	return signed
}

// TestSLO_HandleAndDedup 端到端：合法 logout_token → 200 + jti 落表；重复投递 → 200 幂等（不再副作用）。
func TestSLO_HandleAndDedup(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "slo.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	defer st.Close()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	keys := rp.NewKeysFromSet(map[string]*rsa.PublicKey{sloKID: &priv.PublicKey})

	var revoked []string
	rcvr := slo.New(slo.Options{
		KeySource: keys,
		Issuer:    sloIssuer,
		ClientID:  sloClient,
		JTIStore:  slo.NewDBJTIStore(st, nil),
		Revoker:   slo.RevokerFrom(func(_ context.Context, sid string) error { revoked = append(revoked, sid); return nil }),
	})

	token := signLogoutToken(t, priv, "jti-1", "sid-1")
	status, msg := rcvr.Handle(context.Background(), token)
	if status != 200 {
		t.Fatalf("首次应 200，得到 %d (%s)", status, msg)
	}
	if len(revoked) != 1 || revoked[0] != "sid-1" {
		t.Fatalf("应撤销 sid-1：%+v", revoked)
	}
	// jti 落表。
	seen, err := st.HasControlLogoutJTI(context.Background(), rpHash(t, "jti-1"), time.Now().UnixMilli())
	if err != nil || !seen {
		t.Fatalf("jti 应落表：seen=%v err=%v", seen, err)
	}

	// 重复投递：幂等 200，且不再触发撤销（去重短路）。
	status, msg = rcvr.Handle(context.Background(), token)
	if status != 200 {
		t.Fatalf("重复投递应 200，得到 %d (%s)", status, msg)
	}
	if len(revoked) != 1 {
		t.Fatalf("重复投递不应再次撤销，revoked=%+v", revoked)
	}
}

// TestSLO_InvalidTokenRejected 非法 logout_token（缺 events）→ 400。
func TestSLO_InvalidTokenRejected(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	keys := rp.NewKeysFromSet(map[string]*rsa.PublicKey{sloKID: &priv.PublicKey})
	rcvr := slo.New(slo.Options{KeySource: keys, Issuer: sloIssuer, ClientID: sloClient})

	now := time.Now()
	claims := &contract.LogoutTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: sloIssuer, Audience: jwt.ClaimStrings{sloClient},
			Subject: contract.BuildPersonSubject("p1"), ID: "jti-x",
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
		// 缺 Events
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = sloKID
	signed, _ := tok.SignedString(priv)
	if status, _ := rcvr.Handle(context.Background(), signed); status != 400 {
		t.Fatalf("缺 events 应 400，得到 %d", status)
	}
}

func rpHash(t *testing.T, jti string) string {
	t.Helper()
	// 与 slo/receiver 使用同一 HashJTI（经 sdk 别名）。
	return logout.HashJTI(jti)
}
