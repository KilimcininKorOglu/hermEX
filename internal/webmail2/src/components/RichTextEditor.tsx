import { useRef, useEffect, forwardRef, useImperativeHandle } from "react"
import { Bold, Italic, Underline, Link } from "lucide-react"
import { Button } from "@/components/ui/button"
import { sanitizeClipboard } from "@/utils/sanitize"

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

    useImperativeHandle(ref, () => ({
      getHTML: () => editorRef.current?.innerHTML ?? "",
      setHTML: (html: string) => {
        if (editorRef.current) {
          editorRef.current.innerHTML = html
        }
      },
    }))

    // Sync external value changes (e.g., when editing a different signature)
    useEffect(() => {
      if (editorRef.current && editorRef.current.innerHTML !== value) {
        editorRef.current.innerHTML = value
      }
    }, [value])

    const execCmd = (cmd: string, val?: string) => {
      document.execCommand(cmd, false, val)
      editorRef.current?.focus()
      onChange(editorRef.current?.innerHTML ?? "")
    }

    const handleLink = () => {
      const url = window.prompt("URL:")
      if (url) execCmd("createLink", url)
    }

    const handleInput = () => {
      onChange(editorRef.current?.innerHTML ?? "")
    }

    const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
      // Prevent formatting shortcuts from being swallowed
      e.stopPropagation()
    }

    // Insert a transferred payload ourselves instead of letting the browser drop
    // the raw fragment into the live DOM, which would run any handler it carries.
    const insertTransfer = (data: DataTransfer) => {
      const html = sanitizeClipboard(data.getData("text/html"), data.getData("text/plain"))
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
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          onDrop={handleDrop}
          suppressContentEditableWarning
          dangerouslySetInnerHTML={{ __html: value }}
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
