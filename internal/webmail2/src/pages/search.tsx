import { useState, useEffect, useRef, useCallback } from "react"
import { useNavigate, useSearchParams } from "react-router-dom"
import {
  Search,
  Mail,
  X,
  Clock,
  ArrowRight,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useI18n } from "@/hooks/useI18n"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import API from "@/utils/api"
import { getCookie, setCookie, deleteCookie } from "@/utils/cookies"

interface SearchEmail {
  id: string
  from: string
  fromEmail: string
  subject: string
  preview: string
  date: string
  folder: string
  read: boolean
}

const RECENT_SEARCHES_COOKIE = 'hermex_recent_searches'
const MAX_RECENT_SEARCHES = 5

export function SearchPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const [query, setQuery] = useState(searchParams.get("q") || "")
  const [loading, setLoading] = useState(false)
  const [hasSearched, setHasSearched] = useState(false)
  const [results, setResults] = useState<SearchEmail[]>([])
  const [totalResults, setTotalResults] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [recentSearches, setRecentSearches] = useState<string[]>([])
  const inputRef = useRef<HTMLInputElement>(null)

  // Load recent searches from the cookie
  useEffect(() => {
    const saved = getCookie(RECENT_SEARCHES_COOKIE)
    if (saved) {
      try {
        setRecentSearches(JSON.parse(saved))
      } catch {
        // Ignore parse errors
      }
    }
  }, [])

  // Save a search term to recent searches
  const saveRecentSearch = useCallback((term: string) => {
    if (!term.trim()) return
    setRecentSearches(prev => {
      const filtered = prev.filter(s => s.toLowerCase() !== term.toLowerCase())
      const updated = [term, ...filtered].slice(0, MAX_RECENT_SEARCHES)
      setCookie(RECENT_SEARCHES_COOKIE, JSON.stringify(updated))
      return updated
    })
  }, [])

  // Clear all recent searches
  const clearRecentSearches = useCallback(() => {
    setRecentSearches([])
    deleteCookie(RECENT_SEARCHES_COOKIE)
  }, [])

  // Perform actual search
  const performSearch = useCallback(async (searchQuery: string) => {
    if (!searchQuery.trim()) return

    setLoading(true)
    setError(null)
    setHasSearched(true)

    try {
      const response = await API.search(searchQuery)
      if (response.emails) {
        const mapped = response.emails.map(email => ({
          id: email.id,
          from: email.fromName || email.from,
          fromEmail: email.from,
          subject: email.subject,
          preview: email.preview || email.body?.substring(0, 100) || '',
          date: email.date,
          folder: email.folder,
          read: email.read,
        }))
        setResults(mapped)
        setTotalResults(response.total || mapped.length)
        saveRecentSearch(searchQuery)
      } else {
        setResults([])
        setTotalResults(0)
      }
    } catch (err) {
      console.error('Search error:', err)
      setError(t('search.searchFailed'))
      setResults([])
      setTotalResults(0)
    } finally {
      setLoading(false)
    }
  }, [saveRecentSearch, t])

  // Handle search from URL params (initial load)
  useEffect(() => {
    const q = searchParams.get("q")
    if (q && q !== query) {
      setQuery(q)
      performSearch(q)
    }
  }, [searchParams])

  // Focus input on mount
  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  const handleSearch = (e?: React.FormEvent) => {
    e?.preventDefault()
    if (!query.trim()) return
    performSearch(query)
  }

  const handleClear = () => {
    setQuery("")
    setHasSearched(false)
    setResults([])
    setError(null)
    inputRef.current?.focus()
  }

  const handleRecentSearch = (term: string) => {
    setQuery(term)
    performSearch(term)
  }

  return (
    <div className="space-y-4">
      <div className="space-y-4">
        <form onSubmit={handleSearch} className="flex gap-2">
          <div className="relative flex-1">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              ref={inputRef}
              className="pl-9 pr-20"
              placeholder={t("search.placeholder")}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            {query && (
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7"
                onClick={handleClear}
              >
                <X className="h-4 w-4" />
              </Button>
            )}
          </div>
          <Button type="submit" disabled={loading || !query.trim()}>
            {loading ? t("search.searching") : t("common.search")}
          </Button>
        </form>
      </div>

      {error && (
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-4 text-destructive">
          {error}
        </div>
      )}

      {loading ? (
        <div className="space-y-4">
          {[1, 2, 3].map((i) => (
            <div key={i} className="flex items-start gap-4 p-4 rounded-lg border">
              <Skeleton className="h-4 w-4" />
              <div className="flex-1 space-y-2">
                <Skeleton className="h-4 w-64" />
                <Skeleton className="h-3 w-full" />
              </div>
            </div>
          ))}
        </div>
      ) : !hasSearched ? (
        <div className="space-y-6">
          {/* Initial State */}
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <div className="rounded-full bg-primary/10 p-6">
              <Search className="h-12 w-12 text-primary" />
            </div>
            <h3 className="mt-6 text-xl font-semibold">{t("search.title")}</h3>
            <p className="mt-2 text-muted-foreground max-w-md">
              {t("search.subtitle")}
            </p>
          </div>

          {/* Recent Searches */}
          {recentSearches.length > 0 && (
            <div>
              <div className="flex items-center justify-between mb-3">
                <h4 className="text-sm font-medium text-muted-foreground flex items-center gap-2">
                  <Clock className="h-4 w-4" />
                  {t("search.recentSearches")}
                </h4>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={clearRecentSearches}
                  className="text-xs"
                >
                  {t("search.clear")}
                </Button>
              </div>
              <div className="space-y-1">
                {recentSearches.map((term) => (
                  <Button
                    key={term}
                    variant="ghost"
                    className="w-full justify-start text-muted-foreground hover:text-foreground"
                    onClick={() => handleRecentSearch(term)}
                  >
                    <ArrowRight className="h-4 w-4 mr-2" />
                    {term}
                  </Button>
                ))}
              </div>
            </div>
          )}
        </div>
      ) : results.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <div className="rounded-full bg-muted p-4">
            <Mail className="h-8 w-8 text-muted-foreground" />
          </div>
          <h3 className="mt-4 text-lg font-semibold">{t("common.noResults")}</h3>
          <p className="text-sm text-muted-foreground mt-1">
            {t("search.noResultsFor", { query })}
            <br />
            {t("search.tryDifferentKeywords")}
          </p>
          <Button variant="link" className="mt-4" onClick={handleClear}>
            {t("search.clearSearch")}
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          <div className="text-sm text-muted-foreground px-2">
            {totalResults === 1
              ? t("search.resultCount", { count: String(totalResults), query })
              : t("search.resultCountPlural", { count: String(totalResults), query })}
          </div>
          <div className="rounded-lg border bg-card divide-y">
            {results.map((email) => (
              <div
                key={email.id}
                className={cn(
                  "flex items-start gap-3 p-4 cursor-pointer transition-colors hover:bg-accent/50",
                  !email.read && "bg-accent/10"
                )}
                onClick={() => navigate(`/email/${email.id}`)}
              >
                <Checkbox className="mt-1" />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    {!email.read && (
                      <span className="h-2 w-2 rounded-full bg-primary shrink-0" />
                    )}
                    <span className="font-medium">{email.from}</span>
                    <Badge variant="outline" className="text-[10px]">
                      {email.folder}
                    </Badge>
                  </div>
                  <div className="text-sm">
                    <span className="font-medium">{email.subject}</span>
                    <span className="text-muted-foreground"> — {email.preview}</span>
                  </div>
                  <div className="text-xs text-muted-foreground mt-1">
                    {email.date}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
