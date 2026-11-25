/**
 * 控制面登录门禁（决策 v38，设计 §C2 / DM-3）。
 *
 * 只在 `/meta.auth.enabled === true` 时启用（云端 SaaS 形态）；`enabled=false`（桌面本地态降级 /
 * 单机自用）时**完全不门禁**，直接渲染子节点——保持旧行为。门禁的登录协议完全交给 ark-iam 的
 * `@morehao/ark-iam-react`（PKCE 授权码，public client，无 secret），本文件只做三件装配事：
 *
 *  1. 用 `meta.auth` 的 issuer + clientId 构造 `createAuthProvider`；
 *  2. 把当前 access_token 注入 `@ark-mate/api` 的请求头（`setAccessTokenProvider`），
 *     静默续期后的新 token 每请求现取（不缓存旧值）；
 *  3. 未认证时 `signinRedirect()` 跳 OP，认证后渲染界面。
 *
 * **依赖前置**：本模块 `import '@morehao/ark-iam-react/auth'`，前端构建/typecheck 前必须先
 * `pnpm add @morehao/ark-iam-react@^0.4.0`（peer: antd ^6.7.0 / react&react-dom ^19.3.1 /
 * react-router-dom ^7.15.0 / @ant-design/icons ^6.3.4）。
 * 未装包 → 该 specifier 无法解析（这是集成本身的前置，不是 bug）。
 */
import { createContext, useContext, useEffect, type ReactNode } from 'react'
import { createAuthProvider, useAuthGuard, FullPageSpinner } from '@morehao/ark-iam-react/auth'
import { setAccessTokenProvider } from '@ark-mate/api'
import type { AuthInfo } from '@ark-mate/types'

/** 把登录态里的 access_token 桥接到 api 请求头（每请求现取，支持静默续期）。 */
function TokenBridge({ children }: { children: ReactNode }) {
  const auth = useAuthGuard()
  useEffect(() => {
    setAccessTokenProvider(() => auth.user?.access_token ?? null)
    return () => setAccessTokenProvider(null)
  }, [auth.user?.access_token])
  return <>{children}</>
}

/** 认证守卫：加载中 → spinner；未认证 → 跳 OP 并 spinner；已认证 → TokenBridge + children。 */
function RequireAuth({ children }: { children: ReactNode }) {
  const auth = useAuthGuard()
  useEffect(() => {
    if (!auth.isLoading && !auth.isAuthenticated) {
      void auth.signinRedirect()
    }
  }, [auth.isLoading, auth.isAuthenticated, auth])
  if (auth.isLoading) return <FullPageSpinner />
  if (!auth.isAuthenticated) return <FullPageSpinner />
  return <TokenBridge>{children}</TokenBridge>
}

/**
 * 认证是否启用（决策 v38）。默认 `false`。只在 `auth.enabled=true` 时由 AuthGate 置 `true`。
 *
 * 用途：左栏 footer 的「登出 / 租户切换」UI 必须**只在认证启用时**渲染——`enabled=false`
 * 的桌面本地态没有 `AuthProvider`（也就没有 `useAuthGuard` 的 context），此时若无条件调用
 * SDK 的 hooks 会抛错。消费方先用 `useAuthEnabled()` 判断，未启用直接返回 null（不挂载
 * 调用 SDK hooks 的子组件），从而保证本地态与旧行为完全一致。
 */
const AuthEnabledContext = createContext(false)

/** 认证是否启用（AuthGate 之外恒为 false）。 */
export function useAuthEnabled(): boolean {
  return useContext(AuthEnabledContext)
}

/**
 * AuthGate：按 `meta.auth` 决定是否包登录门禁。
 *
 * `auth.enabled=false` → 直透 children（桌面本地态，不门禁、不引 OP）。
 * `auth.enabled=true` → 构造 AuthProvider + RequireAuth。
 *
 * `useRuntimeConfig:false`：配置已由后端 `/meta.auth` 显式给出，不再拉 `/config.json`（避免与
 * 核心同源部署时多一次无谓请求、也避免 config.json 缺失导致的抖动）。
 */
export function AuthGate({ auth, children }: { auth: AuthInfo; children: ReactNode }) {
  if (!auth.enabled) return <>{children}</>
  const AuthProvider = createAuthProvider({
    clientID: auth.clientId,
    issuer: auth.issuer,
    useRuntimeConfig: false,
    redirectPath: '/auth/callback',
    postLogoutRedirectPath: '/',
  })
  return (
    <AuthEnabledContext.Provider value={true}>
      <AuthProvider>
        <RequireAuth>{children}</RequireAuth>
      </AuthProvider>
    </AuthEnabledContext.Provider>
  )
}

/** 登录回调页路径（AuthProvider 的 redirectPath 必须与 IAM 侧 client 注册的 redirect_uri 一致）。 */
export const AUTH_CALLBACK_PATH = '/auth/callback'
