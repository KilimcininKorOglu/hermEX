import { Button } from "@/components/ui/button"
import { useI18n } from "@/hooks/useI18n"
import api, { maxBulkExport } from "@/utils/api"
import { Download, X, type LucideIcon } from "lucide-react"
import { toast } from "sonner"

/** One page-specific bulk action shown in the contextual toolbar. */
export interface BulkAction {
  key: string
  label: string
  icon: LucideIcon
  onClick: () => void
  /** Render with the destructive (red) button variant. */
  destructive?: boolean
}

interface BulkActionBarProps {
  /** Selected message ids ("<folder>:<uid>"); the bar renders nothing when empty. */
  ids: string[]
  /** Page-specific actions, shown before the built-in Export as zip. */
  actions?: BulkAction[]
  /** Clear the selection. */
  onClear: () => void
  /** Shared-mailbox owner the export download targets, if any. */
  owner?: string
}

/**
 * The contextual multi-select toolbar shared by every folder view. It appears
 * only while messages are selected and always offers Export as zip, so the bulk
 * download lives in one place instead of being re-added to each page.
 */
export function BulkActionBar({ ids, actions = [], onClear, owner }: BulkActionBarProps) {
  const { t } = useI18n()
  if (ids.length === 0) return null

  const handleExport = () => {
    // The server caps the export; warn rather than silently dropping the overflow.
    if (ids.length > maxBulkExport) {
      toast.info(t("bulk.exportCapped", { count: String(maxBulkExport) }))
    }
    api.exportSelected(ids, owner)
  }

  return (
    <div className="flex items-center gap-2 flex-wrap">
      <span className="text-sm text-muted-foreground whitespace-nowrap">
        {t("bulk.selectedCount", { count: String(ids.length) })}
      </span>
      {actions.map((a) => (
        <Button
          key={a.key}
          variant={a.destructive ? "destructive" : "outline"}
          size="sm"
          onClick={a.onClick}
        >
          <a.icon className="h-4 w-4 mr-1" />
          {a.label}
        </Button>
      ))}
      <Button variant="outline" size="sm" onClick={handleExport}>
        <Download className="h-4 w-4 mr-1" />
        {t("bulk.exportZip")}
      </Button>
      <Button variant="ghost" size="sm" onClick={onClear} aria-label={t("bulk.clearSelection")}>
        <X className="h-4 w-4" />
      </Button>
    </div>
  )
}
