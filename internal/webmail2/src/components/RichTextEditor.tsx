import { useRef, useEffect, forwardRef, useImperativeHandle } from "react"
import { Bold, Italic, Underline, Link } from "lucide-react"
import { Button } from "@/components/ui/button"

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
