module github.com/morehao/ark-mate

go 1.27.0

require (
	github.com/coder/acp-go-sdk v0.13.5
	github.com/coder/websocket v1.8.15
	github.com/gin-gonic/gin v1.12.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/morehao/ark-iam/contract v0.1.0
	github.com/morehao/ark-iam/sdk v0.0.0
	github.com/morehao/golib v1.39.2
	go.uber.org/goleak v1.3.0
	gopkg.in/yaml.v3 v3.0.1
	gorm.io/gorm v1.31.2
)

require (
	github.com/bytedance/gopkg v0.1.4 // indirect
	github.com/bytedance/sonic v1.15.5 // indirect
	github.com/bytedance/sonic/loader v0.5.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.7 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/gin-contrib/cors v1.7.11 // indirect
	github.com/gin-contrib/sse v1.2.1 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/go-playground/validator/v10 v10.30.6 // indirect
	github.com/go-redis/redis_rate/v10 v10.0.1 // indirect
	github.com/goccy/go-json v0.12.0 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/ncruces/go-sqlite3 v0.38.0 // indirect
	github.com/ncruces/go-sqlite3-wasm/v2 v2.1.35300 // indirect
	github.com/ncruces/go-sqlite3-wasm/v6 v6.7.35401 // indirect
	github.com/ncruces/go-sqlite3/gormlite v0.34.0 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.66.0 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.2 // indirect
	go.mongodb.org/mongo-driver/v2 v2.10.1 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/arch v0.33.0 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

// TODO(P1 合并前必须处理)：ark-iam 集成物当前用本地 replace 指向同机检出（开发期桥接）。
// 正式合并前替换为发布版 `github.com/morehao/ark-iam/sdk v0.4.0`（contract 为传递依赖），删除这两条 replace。
// 设计见 .dsh/docs/specs/2026-11-24-ark-iam-rp-integration-design.md。
replace github.com/morehao/ark-iam/sdk => ../../ark-iam/sdk

replace github.com/morehao/ark-iam/contract => ../../ark-iam/contract
