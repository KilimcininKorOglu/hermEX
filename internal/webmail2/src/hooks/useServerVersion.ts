import { useEffect, useState } from "react"
import { fetchServerVersion } from "@/utils/serverVersion"

// useServerVersion returns the version the server reports, or "" until it arrives
// or when the server has none to show. A failed request is logged rather than
// shown, since the version is informational and the page works without it.
export function useServerVersion(): string {
  const [version, setVersion] = useState("")
  useEffect(() => {
    let cancelled = false
    fetchServerVersion()
      .then((v) => {
        if (!cancelled) setVersion(v)
      })
      .catch((err: unknown) => {
        console.error("could not read the server version", err)
      })
    return () => {
      cancelled = true
    }
  }, [])
  return version
}
