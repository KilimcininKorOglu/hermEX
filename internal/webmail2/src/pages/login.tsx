import { useState, useEffect, type FormEvent } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from '../contexts/AuthContext'
import { useI18n } from '@/hooks/useI18n'

interface Branding {
  app_name: string
  logo_url: string
  primary_color: string
  tagline: string
  footer_text: string
}

export function LoginPage() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [branding, setBranding] = useState<Branding | null>(null)
  const { login, isAuthenticated } = useAuth()
  const { t } = useI18n()

  // Resolve per-tenant branding from the address-bar hostname, so the login screen
  // reflects the tenant immediately, before the user types anything. The server maps
  // the accessed hostname to a registered domain.
  const host = window.location.hostname.toLowerCase()
  useEffect(() => {
    let cancelled = false
    fetch(`${window.location.origin}/api/v1/branding?domain=${encodeURIComponent(host)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((b: Branding | null) => {
        // Apply branding when the tenant set ANY customization, not just an
        // app name — a tenant may override only the logo, tagline, or footer.
        const hasBranding =
          !!b && (!!b.app_name || !!b.logo_url || !!b.primary_color || !!b.tagline || !!b.footer_text)
        if (!cancelled) setBranding(hasBranding ? b : null)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [host])

  const appName = branding?.app_name || 'hermEX'
  useEffect(() => {
    document.title = `${appName} ${t('login.webmail')}`
  }, [appName, t])

  if (isAuthenticated) {
    return <Navigate to="/inbox" replace />
  }

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      const success = await login(email, password)
      if (!success) {
        setError(t('login.invalidCredentials'))
      }
    } catch {
      setError(t('login.connectionError'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gradient-to-br from-blue-50 to-indigo-100">
      <div className="max-w-md w-full mx-4">
        <div className="bg-white rounded-2xl shadow-xl p-8">
          <div className="text-center mb-8">
            {branding?.logo_url ? (
              <img
                src={branding.logo_url}
                alt={appName}
                className="w-16 h-16 rounded-2xl object-contain mx-auto mb-4"
              />
            ) : (
              <div
                className="w-16 h-16 bg-indigo-600 rounded-2xl flex items-center justify-center mx-auto mb-4"
                style={branding?.primary_color ? { backgroundColor: branding.primary_color } : undefined}
              >
                <svg className="w-8 h-8 text-white" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M3 8l7.89 5.26a2 2 0 002.22 0L21 8M5 19h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z" />
                </svg>
              </div>
            )}
            <h1 className="text-2xl font-bold text-gray-900">{appName}</h1>
            <p className="text-gray-500 mt-1">{branding?.tagline || t('login.subtitle')}</p>
          </div>

          <form onSubmit={handleSubmit} className="space-y-6">
            {error && (
              <div className="bg-red-50 border border-red-200 text-red-600 px-4 py-3 rounded-lg text-sm">
                {error}
              </div>
            )}

            <div>
              <label htmlFor="email" className="block text-sm font-medium text-gray-700 mb-2">
                {t('login.emailAddress')}
              </label>
              <input
                id="email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="you@example.com"
                required
                className="w-full px-4 py-3 border border-gray-300 rounded-lg focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500 transition-colors"
              />
            </div>

            <div>
              <label htmlFor="password" className="block text-sm font-medium text-gray-700 mb-2">
                {t('login.password')}
              </label>
              <input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('login.passwordPlaceholder')}
                required
                className="w-full px-4 py-3 border border-gray-300 rounded-lg focus:ring-2 focus:ring-indigo-500 focus:border-indigo-500 transition-colors"
              />
            </div>

            <button
              type="submit"
              disabled={loading}
              style={branding?.primary_color ? { backgroundColor: branding.primary_color } : undefined}
              className="w-full bg-indigo-600 text-white py-3 px-4 rounded-lg font-medium hover:bg-indigo-700 focus:ring-4 focus:ring-indigo-200 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {loading ? t('login.signingIn') : t('login.signIn')}
            </button>
          </form>

          <div className="mt-8 pt-6 border-t border-gray-200">
            <p className="text-center text-sm text-gray-500">
              {t('login.demoAccounts')} <span className="font-mono text-xs">demo@localhost / demo1234</span>
            </p>
          </div>
        </div>

        <p className="text-center text-xs text-gray-400 mt-4">
          {branding?.footer_text || `hermEX v1.0 - ${t('login.tagline')}`}
        </p>
      </div>
    </div>
  )
}

export default LoginPage
