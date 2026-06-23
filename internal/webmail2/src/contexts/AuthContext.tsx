import { createContext, useContext, useState, useCallback, useEffect } from 'react'
import api from '../utils/api'
import { setDisplayTimeZone } from '../utils/date'
import { getCookie, setCookie, deleteCookie } from '../utils/cookies'

interface UserPrefs {
  onboarded?: boolean
  timezone?: string
  locale?: string
  theme?: string
}

interface AuthUser extends UserPrefs {
  email: string
  hasAvatar?: boolean
  isAdmin?: boolean
}

interface AuthContextType {
  user: AuthUser | null
  isAuthenticated: boolean
  isLoading: boolean
  loading: boolean
  error: string | null
  login: (email: string, password: string) => Promise<boolean>
  logout: () => Promise<void>
  // updatePrefs merges presentation prefs into the live user (after onboarding or
  // a settings change) and re-applies the display timezone immediately so dates
  // re-render in the chosen zone without a reload.
  updatePrefs: (prefs: UserPrefs) => void
}

const AuthContext = createContext<AuthContextType | null>(null)

// Marker cookie set on a successful login and cleared on logout / a stale-session
// probe. Without it, the mount-time `api.me()` probe runs on a fresh or
// logged-out browser and logs a 401 on the login screen before the user has
// done anything. (The JWT itself lives in a separate unreadable HttpOnly cookie;
// this readable marker only gates whether to probe.)
const sessionMarkerKey = 'hermex-webmail-authed'

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  // hydrating gates routing until we know whether a valid session cookie exists.
  const [hydrating, setHydrating] = useState(true)

  // applyMe maps a /auth/me payload into the user state and applies the chosen
  // display timezone so every date formatter renders in the user's zone.
  const applyMe = useCallback((me: Awaited<ReturnType<typeof api.me>>) => {
    setUser({
      email: me.email ?? '',
      hasAvatar: me.has_avatar,
      isAdmin: me.isAdmin,
      onboarded: me.onboarded,
      timezone: me.timezone,
      locale: me.locale,
      theme: me.theme,
    })
    setIsAuthenticated(true)
    setDisplayTimeZone(me.timezone || '')
  }, [])

  // On mount, ask the server who we are. The JWT lives in an HttpOnly cookie the
  // client cannot read, so this is the only way to restore the session after a
  // page reload instead of bouncing the user to /login.
  useEffect(() => {
    let active = true
    // Only probe for a session if this browser previously logged in; otherwise
    // skip the request so the login screen does not log a 401.
    if (!getCookie(sessionMarkerKey)) {
      setHydrating(false)
      return
    }
    api.me()
      .then((me) => {
        if (!active) return
        if (me?.authenticated && me.email) {
          applyMe(me)
        } else {
          // Soft 200 with authenticated:false — the stored session is no longer
          // valid. Clear the marker so we stop probing on future loads.
          deleteCookie(sessionMarkerKey)
        }
      })
      .catch(() => {
        // Network/other failure: clear the marker so we stop probing on future
        // loads (the soft check itself no longer returns 401).
        deleteCookie(sessionMarkerKey)
      })
      .finally(() => {
        if (active) setHydrating(false)
      })
    return () => {
      active = false
    }
  }, [applyMe])

  const login = useCallback(async (email: string, password: string): Promise<boolean> => {
    setLoading(true)
    setError(null)
    try {
      // Token is now in HttpOnly cookie - no need to store in memory
      await api.post<{ expiresIn?: number }>('/auth/login', { email, password })
      setCookie(sessionMarkerKey, '1')
      // Pull the full identity + presentation prefs so the onboarding gate and
      // chosen timezone are known immediately after login.
      try {
        const me = await api.me()
        if (me?.authenticated && me.email) {
          applyMe(me)
        } else {
          setUser({ email })
          setIsAuthenticated(true)
        }
      } catch {
        setUser({ email })
        setIsAuthenticated(true)
      }
      return true
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Login failed')
      return false
    } finally {
      setLoading(false)
    }
  }, [applyMe])

  const logout = useCallback(async () => {
    // Invalidate the HttpOnly session cookie server-side; clear local state
    // regardless of the request outcome so the UI never gets stuck signed in.
    try {
      await api.logout()
    } catch (err) {
      console.error('Logout request failed:', err)
    }
    setUser(null)
    setIsAuthenticated(false)
    api.setToken(null)
    deleteCookie(sessionMarkerKey)
  }, [])

  const updatePrefs = useCallback((prefs: UserPrefs) => {
    setUser((prev) => (prev ? { ...prev, ...prefs } : prev))
    if (prefs.timezone !== undefined) {
      setDisplayTimeZone(prefs.timezone || '')
    }
  }, [])

  const value: AuthContextType = {
    user,
    isAuthenticated,
    isLoading: hydrating,
    loading,
    error,
    login,
    logout,
    updatePrefs,
  }

  return (
    <AuthContext.Provider value={value}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const context = useContext(AuthContext)
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider')
  }
  return context
}
