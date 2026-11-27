package ctrcontrol_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/morehao/ark-iam/contract"
	"github.com/morehao/ark-iam/sdk/rp"
	"github.com/morehao/ark-mate/internal/control/identitymap"
	"github.com/morehao/ark-mate/internal/controller/ctrcontrol"
	"github.com/morehao/ark-mate/internal/middleware/authn"
	"github.com/morehao/ark-mate/internal/persistence/store"

	// dbclient.Open → glog 需要 zap driver（test 二进制不含装配根的匿名引入）。
	_ "github.com/morehao/golib/glog/driver/zap"
)

const (
	itIssuer   = "http://localhost:8081/oidc"
	itAudience = "ark_mate_web"
	itKID      = "it-key-1"
)

// TestE2E_LoginOnboardsAndProjects 端到端闭环（决策 v38）：
// 真实 RS256 令牌 → authn 中间件验签 → ctrcontrol.Me → identitymap 幂等开通 → 落 control_users，
// 且响应投影出正确身份。
func TestE2E_LoginOnboardsAndProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 真库（临时 sqlite，Open 自动迁移；含 v44 的 control 四表）。
	st, err := store.Open(filepath.Join(t.TempDir(), "e2e.db"))
	if err != nil {
		t.Fatalf("打开库: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// 真实验签器（零网络：注入公钥集）。
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成密钥: %v", err)
	}
	auth, err := authn.New(ctx, authn.Options{
		Issuer:   itIssuer,
		Audience: itAudience,
		Keys:     rp.NewKeysFromSet(map[string]*rsa.PublicKey{itKID: &priv.PublicKey}),
	})
	if err != nil {
		t.Fatalf("构造 authenticator: %v", err)
	}

	// 编排：真 identitymap（接真 store）。
	ctr := ctrcontrol.NewControlCtr(identitymap.New(st, nil))

	// 组装：authn 中间件 + /control/me。
	r := gin.New()
	g := r.Group("/control", auth.Middleware())
	g.GET("/me", ctr.Me)

	// 签一枚人令牌。
	now := time.Now()
	claims := &contract.TokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    itIssuer,
			Audience:  jwt.ClaimStrings{itAudience},
			Subject:   contract.BuildPersonSubject("p-e2e"),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(5 * time.Minute)),
		},
		TenantID: "t-e2e", UserID: "u-e2e", ClientID: itAudience, PersonIDClaim: "p-e2e",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = itKID
	signed, err := tok.SignedString(priv)
	if err != nil {
		t.Fatalf("签发: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/control/me", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d（body=%s）", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			UserID   string `json:"userId"`
			TenantID string `json:"tenantId"`
			PersonID string `json:"personId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if env.Data.UserID != "u-e2e" || env.Data.TenantID != "t-e2e" || env.Data.PersonID != "p-e2e" {
		t.Fatalf("投影不符：%+v", env.Data)
	}

	// 断言：control_users 已幂等开通一行；control_tenants 已 ensure（默认 sandboxed）。
	row, ok, err := st.GetControlUser(ctx, "t-e2e", "u-e2e")
	if err != nil || !ok {
		t.Fatalf("应已开通 control_users 行：ok=%v err=%v", ok, err)
	}
	if row.PersonID != "p-e2e" || row.UserType != "normal" {
		t.Fatalf("开通行不符：%+v", row)
	}
	var iso string
	if err := st.DB().WithContext(ctx).Raw(
		`SELECT isolation_level FROM control_tenants WHERE tenant_id='t-e2e'`).Row().Scan(&iso); err != nil {
		t.Fatalf("读 control_tenants: %v", err)
	}
	if iso != "sandboxed" {
		t.Fatalf("租户默认隔离档应为 sandboxed，得到 %q", iso)
	}

	// 再请求一次：幂等（仍一行）。
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/control/me", nil).WithContext(req.Context()))
	var cnt int64
	if err := st.DB().WithContext(ctx).Raw(`SELECT count(*) FROM control_users WHERE tenant_id='t-e2e'`).Scan(&cnt).Error; err != nil {
		t.Fatalf("计数: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("重复登录应仍 1 行，得到 %d", cnt)
	}
}

// TestE2E_NoTokenRejected 无令牌 → 中间件 401。
func TestE2E_NoTokenRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth, err := authn.New(context.Background(), authn.Options{
		Issuer: itIssuer, Audience: itAudience,
		Keys: rp.NewKeysFromSet(map[string]*rsa.PublicKey{itKID: &priv.PublicKey}),
	})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	r := gin.New()
	g := r.Group("/control", auth.Middleware())
	g.GET("/me", ctrcontrol.NewControlCtr(nil).Me)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/control/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，得到 %d", w.Code)
	}
}
