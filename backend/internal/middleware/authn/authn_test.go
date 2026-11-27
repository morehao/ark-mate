package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-iam/sdk/rp"
)

const (
	testIssuer   = "http://localhost:8081/oidc"
	testAudience = "ark_mate_web"
	testKID      = "test-key-1"
)

// newTestKeys 生成一把 RSA 密钥并包成 rp.Keys（零网络），返回签发用私钥与验签用 KeySource。
func newTestKeys(t *testing.T) (*rsa.PrivateKey, rp.KeySource) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败：%v", err)
	}
	ks := rp.NewKeysFromSet(map[string]*rsa.PublicKey{testKID: &priv.PublicKey})
	return priv, ks
}

// signToken 按 ark-iam 契约签一枚人令牌（sub=person:<id>，带 tenant_id/user_id/aud/iss/exp）。
func signToken(t *testing.T, priv *rsa.PrivateKey, mut func(*contract.TokenClaims)) string {
	t.Helper()
	now := time.Now()
	claims := &contract.TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    testIssuer,
			Audience:  jwt.ClaimStrings{testAudience},
			Subject:   contract.BuildPersonSubject("person-1"),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
		TenantID:      "tenant-1",
		UserID:        "user-1",
		ClientID:      testAudience,
		PersonIDClaim: "person-1",
		Scope:         "openid profile",
	}
	if mut != nil {
		mut(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKID
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("签发测试令牌失败：%v", err)
	}
	return signed
}

func newAuth(t *testing.T, ks rp.KeySource) *Authenticator {
	t.Helper()
	a, err := New(context.Background(), Options{Issuer: testIssuer, Audience: testAudience, Keys: ks})
	if err != nil {
		t.Fatalf("构造 Authenticator 失败：%v", err)
	}
	return a
}

// TestVerify_HappyPath 证明真实 RS256 验签能如实投影出 ark-iam 的身份字段。
func TestVerify_HappyPath(t *testing.T) {
	priv, ks := newTestKeys(t)
	a := newAuth(t, ks)
	id, err := a.Verify(context.Background(), signToken(t, priv, nil))
	if err != nil {
		t.Fatalf("验签应通过，got err=%v", err)
	}
	if id.PersonID != "person-1" || id.TenantID != "tenant-1" || id.UserID != "user-1" {
		t.Fatalf("身份字段不符：%+v", id)
	}
	if id.IsMachine {
		t.Fatalf("人令牌不应标为机器凭证")
	}
}

// TestVerify_WrongIssuer 拒绝 iss 不匹配的令牌。
func TestVerify_WrongIssuer(t *testing.T) {
	priv, ks := newTestKeys(t)
	a := newAuth(t, ks)
	tok := signToken(t, priv, func(c *contract.TokenClaims) { c.Issuer = "http://evil/oidc" })
	if _, err := a.Verify(context.Background(), tok); err == nil {
		t.Fatalf("iss 不匹配应拒绝")
	}
}

// TestVerify_WrongAudience 拒绝 aud 不含本应用 client_id 的令牌。
func TestVerify_WrongAudience(t *testing.T) {
	priv, ks := newTestKeys(t)
	a := newAuth(t, ks)
	tok := signToken(t, priv, func(c *contract.TokenClaims) { c.Audience = jwt.ClaimStrings{"other_app"} })
	if _, err := a.Verify(context.Background(), tok); err == nil {
		t.Fatalf("aud 不匹配应拒绝")
	}
}

// TestVerify_Expired 拒绝过期令牌。
func TestVerify_Expired(t *testing.T) {
	priv, ks := newTestKeys(t)
	a := newAuth(t, ks)
	tok := signToken(t, priv, func(c *contract.TokenClaims) {
		c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	})
	if _, err := a.Verify(context.Background(), tok); err == nil {
		t.Fatalf("过期令牌应拒绝")
	}
}

// TestNew_RequiresIssuerAudience 缺 issuer/audience 直接报错（fail-closed 的构造期）。
func TestNew_RequiresIssuerAudience(t *testing.T) {
	if _, err := New(context.Background(), Options{Audience: "x", Keys: rp.NewKeysFromSet(nil)}); err == nil {
		t.Fatalf("缺 issuer 应报错")
	}
	if _, err := New(context.Background(), Options{Issuer: "x", Keys: rp.NewKeysFromSet(nil)}); err == nil {
		t.Fatalf("缺 audience 应报错")
	}
}

// errKeySource 模拟「密钥集不可用」。
type errKeySource struct{}

func (errKeySource) PublicKey(context.Context, string) (*rsa.PublicKey, error) {
	return nil, contract.ErrKeySourceUnavailable
}

// TestMiddleware_Status 覆盖中间件三条出口：缺令牌 401、验签失败 401、密钥集不可用 503、好令牌 200。
func TestMiddleware_Status(t *testing.T) {
	gin.SetMode(gin.TestMode)
	priv, ks := newTestKeys(t)
	a := newAuth(t, ks)
	good := signToken(t, priv, nil)

	// 密钥集不可用的中间件
	badAuth, err := New(context.Background(), Options{Issuer: testIssuer, Audience: testAudience, Keys: errKeySource{}})
	if err != nil {
		t.Fatalf("构造 badAuth 失败：%v", err)
	}

	cases := []struct {
		name     string
		mw       gin.HandlerFunc
		authz    string
		wantCode int
	}{
		{"缺令牌", a.Middleware(), "", http.StatusUnauthorized},
		{"坏令牌", a.Middleware(), "Bearer not-a-jwt", http.StatusUnauthorized},
		// 用结构合法（三段式、kid 命中）但密钥集取不出的令牌：解析会走到 keyFunc → 命中
		// ErrKeySourceUnavailable → 503。若用乱串，解析器先在格式/签名前就判 401，
		// 测不到「密钥集不可用」这条分支。
		{"密钥集不可用", badAuth.Middleware(), "Bearer " + good, http.StatusServiceUnavailable},
		{"好令牌", a.Middleware(), "Bearer " + good, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(tc.mw)
			var gotID *contract.Identity
			r.GET("/x", func(c *gin.Context) {
				gotID = IdentityFromGin(c)
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("状态码期望 %d，得到 %d（body=%s）", tc.wantCode, w.Code, w.Body.String())
			}
			if tc.wantCode == http.StatusOK {
				if gotID == nil || gotID.TenantID != "tenant-1" {
					t.Fatalf("放行时未注入正确身份：%+v", gotID)
				}
			}
		})
	}
}

// TestBearerToken 覆盖 Authorization 头解析。
func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":  "abc",
		"bearer abc":  "abc",
		"BEARER abc":  "abc",
		"Bearer  abc": "abc",
		"":            "",
		"Basic abc":   "",
		"Bearer":      "",
	}
	for in, want := range cases {
		if got := BearerToken(in); got != want {
			t.Errorf("BearerToken(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// 保证 json import 不被优化掉（契约结构可序列化，未来 SLO 用）。
var _ = json.Marshal
