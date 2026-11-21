import { useState, useEffect, type SubmitEvent } from 'react'
import { Navigate } from 'react-router-dom'
import { useAuth } from '../contexts/AuthContext'
import { useI18n } from '@/hooks/useI18n'
import { SignInBadge, useBranding } from '@/components/sign-in-badge'

// LoginPage is the first sign-in step: email and password on the staff badge.
export function LoginPage() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const branding = useBranding()
  const { login, isAuthenticated } = useAuth()
  const { t } = useI18n()

  const appName = branding?.app_name || 'hermEX'
  useEffect(() => {
    document.title = `${appName} ${t('login.webmail')}`
  }, [appName, t])

  if (isAuthenticated) {
    return <Navigate to="/inbox" replace />
  }

  const handleSubmit = async (e: SubmitEvent<HTMLFormElement>) => {
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
    <SignInBadge branding={branding} state={loading ? 'pending' : error ? 'refused' : undefined}>
      <form onSubmit={handleSubmit} className="badge-form">
        {error && (
          <p className="badge-error" role="alert">
            {error}
          </p>
        )}
        <label htmlFor="email" className="badge-field">
          {t('login.emailAddress')}
          <input
            id="email"
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder={t('login.emailPlaceholder')}
            required
            autoFocus
          />
        </label>
        <label htmlFor="password" className="badge-field">
          {t('login.password')}
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </label>
        <button type="submit" disabled={loading} className="badge-submit">
          {loading ? t('login.signingIn') : t('login.signIn')}
        </button>
      </form>
    </SignInBadge>
  )
}
