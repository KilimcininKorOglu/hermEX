import { useRef, useEffect, useMemo, forwardRef, useImperativeHandle } from "react"
import { Bold, Italic, Underline, Link } from "lucide-react"
import { Button } from "@/components/ui/button"
import { isSafeLinkURL, sanitizeClipboard, sanitizeHTML } from "@/utils/sanitize"
import { linkifyHTML, linkifyNode } from "@/utils/linkify"

// Collapses the selection at the given viewport point. Chromium and WebKit expose
// caretRangeFromPoint, Gecko caretPositionFromPoint; with neither, the insert
// falls back to the current selection.
function placeCaret(x: number, y: number) {
  const doc = document as Document & {
    caretRangeFromPoint?: (x: number, y: number) => Range | null
    caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null
  }
  const selection = window.getSelection()
  if (!selection) return

  const range = doc.caretRangeFromPoint?.(x, y) ?? null
  if (range) {
    selection.removeAllRanges()
    selection.addRange(range)
    return
  }

  const position = doc.caretPositionFromPoint?.(x, y)
  if (!position) return
  const fromPosition = document.createRange()
  fromPosition.setStart(position.offsetNode, position.offset)
  fromPosition.collapse(true)
  selection.removeAllRanges()
  selection.addRange(fromPosition)
}

export interface RichTextEditorHandle {
  getHTML: () => string
  setHTML: (html: string) => void
}

interface RichTextEditorProps {
  value: string
  onChange: (html: string) => void
  placeholder?: string
  className?: string
}

export const RichTextEditor = forwardRef<RichTextEditorHandle, RichTextEditorProps>(
  ({ value, onChange, placeholder, className }, ref) => {
    const editorRef = useRef<HTMLDivElement>(null)

    // Everything this component renders goes into a live contentEditable, so it
    // sanitizes here rather than trusting each caller: the value arrives from the
    // server (a quoted reply, a stored signature or template) and a sink whose
    // safety depends on every call site remembering regenerates the hole the next
    // time someone adds one.
    const safeValue = useMemo(() => sanitizeHTML(value), [value])

    useImperativeHandle(ref, () => ({
      getHTML: () => editorRef.current?.innerHTML ?? "",
      setHTML: (html: string) => {
        if (editorRef.current) {
          editorRef.current.innerHTML = sanitizeHTML(html)
        }
      },
    }))

    // Sync external value changes (e.g., when editing a different signature)
    useEffect(() => {
      if (editorRef.current && editorRef.current.innerHTML !== safeValue) {
        editorRef.current.innerHTML = safeValue
      }
    }, [safeValue])

    const execCmd = (cmd: string, val?: string) => {
      document.execCommand(cmd, false, val)
      editorRef.current?.focus()
      onChange(editorRef.current?.innerHTML ?? "")
    }

    const handleLink = () => {
      const url = window.prompt("URL:")?.trim()
      // A javascript: href typed here would survive into the sent body, so the
      // scheme is checked before it ever reaches the document.
      if (url && isSafeLinkURL(url)) execCmd("createLink", url)
    }

    const handleInput = () => {
      onChange(editorRef.current?.innerHTML ?? "")
    }

    // Bare URLs and addresses become links when the field is left, not while it is
    // typed in: replacing the text node the caret sits in would move the caret, and
    // the user would lose their place mid-word.
    const handleBlur = () => {
      const el = editorRef.current
      if (!el) return
      const before = el.innerHTML
      linkifyNode(el)
      if (el.innerHTML !== before) onChange(el.innerHTML)
    }

    const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
      // Prevent formatting shortcuts from being swallowed
      e.stopPropagation()
    }

    // Insert a transferred payload ourselves instead of letting the browser drop
    // the raw fragment into the live DOM, which would run any handler it carries.
    const insertTransfer = (data: DataTransfer) => {
      // Linkified after sanitizing, so a pasted address is a link on arrival and
      // the anchors this adds are the only ones the sanitizer never saw.
      const html = linkifyHTML(sanitizeClipboard(data.getData("text/html"), data.getData("text/plain")))
      editorRef.current?.focus()
      document.execCommand("insertHTML", false, html)
      onChange(editorRef.current?.innerHTML ?? "")
    }

    const handlePaste = (e: React.ClipboardEvent<HTMLDivElement>) => {
      e.preventDefault()
      insertTransfer(e.clipboardData)
    }

    // Move the caret to the drop point first, so intercepting the drop does not
    // move the content to wherever the previous selection happened to be.
    const handleDrop = (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault()
      placeCaret(e.clientX, e.clientY)
      insertTransfer(e.dataTransfer)
    }

    return (
      <div className="space-y-1">
        {/* Toolbar */}
        <div className="flex items-center gap-1 border rounded-md p-1 bg-muted/50">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 px-2"
            onMouseDown={(e) => { e.preventDefault(); execCmd("bold") }}
            title="Bold"
          >
            <Bold className="h-3.5 w-3.5" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 px-2"
            onMouseDown={(e) => { e.preventDefault(); execCmd("italic") }}
            title="Italic"
          >
            <Italic className="h-3.5 w-3.5" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 px-2"
            onMouseDown={(e) => { e.preventDefault(); execCmd("underline") }}
            title="Underline"
          >
            <Underline className="h-3.5 w-3.5" />
          </Button>
          <div className="w-px h-5 bg-border mx-1" />
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 px-2"
            onMouseDown={(e) => { e.preventDefault(); handleLink() }}
            title="Insert link"
          >
            <Link className="h-3.5 w-3.5" />
          </Button>
        </div>
        {/* Editor */}
        <div
          ref={editorRef}
          contentEditable
          className={`min-h-[100px] max-h-[300px] overflow-y-auto rounded-md border bg-background px-3 py-2 text-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring ${className ?? ""}`}
          style={{ whiteSpace: "pre-wrap" }}
          onInput={handleInput}
          onBlur={handleBlur}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          onDrop={handleDrop}
          suppressContentEditableWarning
          dangerouslySetInnerHTML={{ __html: safeValue }}
          data-placeholder={placeholder}
        />
        <style>{`
          [contenteditable][data-placeholder]:empty::before {
            color: hsl(var(--muted-foreground));
            pointer-events: none;
            content: attr(data-placeholder);
          }
        `}</style>
      </div>
    )
  }
)

RichTextEditor.displayName = "RichTextEditor"
