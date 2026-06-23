import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { Search, Bell, Sun, Moon, Menu, User, LogOut, ChevronDown, Keyboard, Languages, Check } from "lucide-react"
import { useTheme } from "@/components/theme-provider"
import { useI18n } from "@/hooks/useI18n"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { useAuth } from "@/contexts/AuthContext"
import { useMailbox } from "@/contexts/MailboxContext"
import api from "@/utils/api"

interface Notification {
  id: string
  from: string
  subject: string
  date: string
}

interface HeaderProps {
  onMenuToggle: () => void
  sidebarCollapsed: boolean
}

// Human-readable names for the supported locales (shown in the language menu).
const localeNames: Record<string, string> = {
  en: "English",
  tr: "Türkçe",
}

export function Header({ onMenuToggle, sidebarCollapsed }: HeaderProps) {
  const { setTheme, resolvedTheme } = useTheme()
  const { logout, user } = useAuth()
  const { t, locale, changeLocale, supportedLocales } = useI18n()
  const navigate = useNavigate()
  const [searchQuery, setSearchQuery] = useState("")
  const { inboxEmails } = useMailbox()

  // Surface unread inbox messages as notifications from the shared inbox state
  // (no separate fetch; stays in sync as messages are read/deleted).
  const notifications: Notification[] = inboxEmails
    .filter((m) => !m.read)
    .slice(0, 5)
    .map((m) => ({ id: m.id, from: m.from, subject: m.subject, date: m.date }))

  const email = user?.email ?? ""
  const displayName = email ? email.split("@")[0] : t("header.account")
  const initials = (email ? email.slice(0, 2) : "?").toUpperCase()

  const handleSignOut = async () => {
    await logout()
    navigate("/login")
  }

  const handleSearch = (e: React.FormEvent) => {
    e.preventDefault()
    if (searchQuery.trim()) {
      navigate(`/search?q=${encodeURIComponent(searchQuery.trim())}`)
      setSearchQuery("")
    }
  }

  return (
    <header
      className={cn(
        "fixed top-0 right-0 z-30 h-16 border-b bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60 transition-all duration-300 left-0",
        sidebarCollapsed ? "lg:left-16" : "lg:left-64"
      )}
    >
      <div className="flex h-full items-center justify-between gap-4 px-4 lg:px-6">
        {/* Left: Menu Toggle & Search */}
        <div className="flex items-center gap-4 flex-1">
          <Button
            variant="ghost"
            size="icon"
            className="lg:hidden"
            onClick={onMenuToggle}
          >
            <Menu className="h-5 w-5" />
          </Button>

          <form onSubmit={handleSearch} className="relative max-w-md flex-1 hidden md:block">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="search"
              placeholder={t("header.searchPlaceholder")}
              className="pl-10 bg-muted/50 border-0 focus:bg-background focus:ring-2 focus:ring-primary/20"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
            />
          </form>
        </div>

        {/* Right: Actions */}
        <div className="flex items-center gap-2">
          {/* Language Selector */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="relative" title={t("sidebar.selectLanguage")}>
                <Languages className="h-5 w-5" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-44">
              <DropdownMenuLabel>{t("common.language")}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              {supportedLocales.map((code) => (
                <DropdownMenuItem
                  key={code}
                  onClick={() => changeLocale(code)}
                  className="flex items-center justify-between cursor-pointer"
                >
                  <span>{localeNames[code] ?? code.toUpperCase()}</span>
                  {locale === code && <Check className="h-4 w-4" />}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          {/* Theme Toggle */}
          <Button
            variant="ghost"
            size="icon"
            className="relative"
            onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}
          >
            {resolvedTheme === "dark" ? (
              <Sun className="h-5 w-5" />
            ) : (
              <Moon className="h-5 w-5" />
            )}
          </Button>

          {/* Keyboard Shortcuts */}
          <Button
            variant="ghost"
            size="icon"
            className="relative"
            onClick={() => document.dispatchEvent(new CustomEvent("toggle-shortcuts"))}
            title={t("header.keyboardShortcuts")}
          >
            <Keyboard className="h-5 w-5" />
          </Button>

          {/* Notifications */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" className="relative">
                <Bell className="h-5 w-5" />
                {notifications.length > 0 && (
                  <Badge className="absolute -right-1 -top-1 h-5 w-5 p-0 flex items-center justify-center text-[10px]">
                    {notifications.length}
                  </Badge>
                )}
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-80">
              <DropdownMenuLabel>{t("header.notifications")}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <div className="max-h-80 overflow-y-auto">
                {notifications.length === 0 ? (
                  <div className="p-4 text-center text-sm text-muted-foreground">
                    {t("header.noNotifications")}
                  </div>
                ) : (
                  notifications.map((n) => (
                    <DropdownMenuItem
                      key={n.id}
                      className="flex flex-col items-start gap-1 p-3 cursor-pointer"
                      onClick={() => navigate(`/email/${n.id}`)}
                    >
                      <span className="font-medium text-sm truncate w-full">{n.subject || t("header.noSubject")}</span>
                      <span className="text-xs text-muted-foreground truncate w-full">{t("header.fromLabel", { from: n.from })}</span>
                      <span className="text-xs text-muted-foreground">{n.date}</span>
                    </DropdownMenuItem>
                  ))
                )}
              </div>
            </DropdownMenuContent>
          </DropdownMenu>

          {/* User Profile */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" className="relative h-9 w-9 rounded-full">
                <Avatar className="h-9 w-9 ring-2 ring-primary/20">
                  <AvatarImage src={user?.hasAvatar && email ? api.avatarUrl(email) : ""} alt={email} />
                  <AvatarFallback className="bg-gradient-to-br from-primary to-primary/80 text-primary-foreground font-semibold">
                    {initials}
                  </AvatarFallback>
                </Avatar>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-56">
              <DropdownMenuLabel>
                <div className="flex flex-col">
                  <span className="font-semibold">{displayName}</span>
                  <span className="text-xs text-muted-foreground">{email}</span>
                </div>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => navigate("/settings")}>
                <User className="mr-2 h-4 w-4" />
                {t("header.profile")}
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => navigate("/settings")}>
                <ChevronDown className="mr-2 h-4 w-4" />
                {t("header.accountSettings")}
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem className="text-destructive" onClick={handleSignOut}>
                <LogOut className="mr-2 h-4 w-4" />
                {t("header.signOut")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </header>
  )
}
