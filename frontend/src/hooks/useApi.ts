import { useCallback, useEffect, useRef, useState } from "react";
import { GET } from "@/api/client";

/** Fetch-on-mount + manual refetch + optional polling. */
export function useApi<T>(path: string | null, opts?: { pollMs?: number }) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(!!path);
  const timer = useRef<number | null>(null);

  const refetch = useCallback(async () => {
    if (!path) return;
    try {
      const d = await GET<T>(path);
      setData(d);
      setError(null);
    } catch (e: any) {
      setError(e.message || String(e));
    } finally {
      setLoading(false);
    }
  }, [path]);

  useEffect(() => {
    if (!path) return;
    setLoading(true);
    refetch();
    if (opts?.pollMs) {
      timer.current = window.setInterval(refetch, opts.pollMs);
    }
    return () => {
      if (timer.current) clearInterval(timer.current);
    };
  }, [path, refetch]);

  return { data, error, loading, refetch, setData };
}
