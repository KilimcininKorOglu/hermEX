import { useState } from "react"
import { useNavigate } from "react-router-dom"
import { Mail, ArrowRight, CheckCircle2, X, ExternalLink } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { useI18n } from "@/hooks/useI18n"

interface WelcomeBannerProps {
  onDismiss?: () => void
}

export function WelcomeBanner({ onDismiss }: WelcomeBannerProps) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const [dismissed, setDismissed] = useState(false)

  if (dismissed) return null

  const features = [
    t("welcome.feature1"),
    t("welcome.feature2"),
    t("welcome.feature3"),
    t("welcome.feature4"),
  ]

  return (
    <div className="rounded-lg border bg-gradient-to-r from-primary/5 via-primary/10 to-primary/5 p-6">
      <div className="flex items-start justify-between gap-4">
        <div className="flex gap-4">
          <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-primary to-primary/80 shadow-lg shadow-primary/25">
            <Mail className="h-6 w-6 text-primary-foreground" />
          </div>
          <div>
            <h2 className="text-xl font-bold">{t("welcome.title")}</h2>
            <p className="text-muted-foreground mt-1">
              {t("welcome.subtitle")}
            </p>
            <div className="mt-4 grid gap-2 sm:grid-cols-2">
              {features.map((feature, index) => (
                <div key={index} className="flex items-center gap-2 text-sm">
                  <CheckCircle2 className="h-4 w-4 text-primary shrink-0" />
                  <span>{feature}</span>
                </div>
              ))}
            </div>
            <div className="flex gap-2 mt-4">
              <Button onClick={() => navigate("/compose")}>
                <Mail className="h-4 w-4 mr-2" />
                {t("welcome.composeEmail")}
              </Button>
              <Button variant="outline" onClick={() => navigate("/settings")}>
                {t("welcome.customize")}
              </Button>
            </div>
          </div>
        </div>
        <Button
          variant="ghost"
          size="icon"
          className="shrink-0"
          onClick={() => {
            setDismissed(true)
            onDismiss?.()
          }}
        >
          <X className="h-4 w-4" />
        </Button>
      </div>
    </div>
  )
}

export function SetupGuide() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const [dismissed, setDismissed] = useState(false)

  if (dismissed) return null

  const steps = [
    { num: 1, title: t("welcome.step1Title"), desc: t("welcome.step1Desc"), done: true },
    { num: 2, title: t("welcome.step2Title"), desc: t("welcome.step2Desc"), done: true },
    { num: 3, title: t("welcome.step3Title"), desc: t("welcome.step3Desc"), done: false },
    { num: 4, title: t("welcome.step4Title"), desc: t("welcome.step4Desc"), done: false },
  ]

  return (
    <div className="rounded-lg border bg-card p-6">
      <div className="flex items-center justify-between mb-4">
        <h3 className="font-semibold flex items-center gap-2">
          {t("welcome.setupTitle")}
          <Badge variant="secondary">{t("welcome.gettingStarted")}</Badge>
        </h3>
        <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => setDismissed(true)}>
          <X className="h-4 w-4" />
        </Button>
      </div>
      <div className="space-y-3">
        {steps.map((step) => (
          <div key={step.num} className="flex items-center gap-3">
            <div className={cn(
              "flex h-8 w-8 items-center justify-center rounded-full text-sm font-medium",
              step.done ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"
            )}>
              {step.done ? <CheckCircle2 className="h-4 w-4" /> : step.num}
            </div>
            <div className="flex-1">
              <p className={cn("text-sm", step.done && "text-muted-foreground line-through")}>
                {step.title}
              </p>
              <p className="text-xs text-muted-foreground">{step.desc}</p>
            </div>
          </div>
        ))}
      </div>
      <div className="mt-4 pt-4 border-t flex gap-2">
        <Button variant="outline" size="sm" className="gap-2" onClick={() => navigate("/settings")}>
          {t("welcome.documentation")}
          <ExternalLink className="h-3 w-3" />
        </Button>
        <Button variant="outline" size="sm" className="gap-2">
          {t("welcome.adminPanel")}
          <ArrowRight className="h-3 w-3" />
        </Button>
      </div>
    </div>
  )
}

function cn(...classes: (string | boolean | undefined)[]) {
  return classes.filter(Boolean).join(" ")
}
