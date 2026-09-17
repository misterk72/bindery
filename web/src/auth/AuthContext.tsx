import { createContext, useContext, useEffect, useState, ReactNode, useCallback } from 'react'
import { api, BINDERY_BASE, AuthStatus, initCSRF } from '../api/client'

// The three user roles the backend knows (internal/auth/roles.go).
export type UserRole = 'admin' | 'user' | 'requester'

interface AuthContextValue {
  status: AuthStatus | null
  loading: boolean
  isAdmin: boolean
  // role is null until the status has loaded, or when the backend reported
  // a role this build does not know.
  role: UserRole | null
  // A requester browses a read only library and asks for books; every other
  // screen is closed to them server side, so the UI does not offer it.
  isRequester: boolean
  refresh: () => Promise<void>
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

function parseRole(role: string | undefined): UserRole | null {
  return role === 'admin' || role === 'user' || role === 'requester' ? role : null
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = useCallback(async () => {
    try {
      const s = await api.authStatus()
      setStatus(s)
      // Re-hydrate CSRF token after page reload: authLogin() calls initCSRF,
      // but a subsequent reload keeps the session cookie without the token
      // in JS memory, so mutating requests would 403 until the next login.
      if (s.authenticated) {
        await initCSRF()
      }
    } catch {
      setStatus(null)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { refresh() }, [refresh])

  useEffect(() => {
    const onVisible = () => { if (document.visibilityState === 'visible') refresh() }
    document.addEventListener('visibilitychange', onVisible)
    return () => document.removeEventListener('visibilitychange', onVisible)
  }, [refresh])

  const logout = useCallback(async () => {
    try { await api.authLogout() } catch { /* ignore — we're clearing state anyway */ }
    await refresh()
    window.location.href = `${BINDERY_BASE}/login`
  }, [refresh])

  const role = parseRole(status?.role)
  const isAdmin = role === 'admin'
  const isRequester = role === 'requester'

  return (
    <AuthContext.Provider value={{ status, loading, isAdmin, role, isRequester, refresh, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}

// useIsRequester is for components that also render outside the provider
// (in isolated tests, for one): with no provider the answer is false, which
// keeps the full add flow they were written for.
export function useIsRequester(): boolean {
  return useContext(AuthContext)?.isRequester ?? false
}
