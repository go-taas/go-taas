// Minimal hash-free client router. The console is served as a static SPA
// by the gateway, which falls back to index.html for unknown paths, so
// history-based routing works without server-side route tables.

import { useEffect, useState, type ReactNode } from 'react';

type Listener = () => void;
const listeners = new Set<Listener>();

function emit() {
  listeners.forEach((l) => l());
}

export function navigate(path: string) {
  window.history.pushState({}, '', path);
  emit();
}

export function useRoute(): string {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    const listener = () => setPath(window.location.pathname);
    listeners.add(listener);
    window.addEventListener('popstate', listener);
    return () => {
      listeners.delete(listener);
      window.removeEventListener('popstate', listener);
    };
  }, []);
  return path;
}

// Router renders its children; children use <Route> to match segments.
export function Router({ children }: { children: ReactNode }) {
  useRoute(); // subscribe to navigation so the tree re-renders
  return <>{children}</>;
}

Router.subscribe = function (listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
};

// Route matches an exact path or a path with a single :param segment.
// On match it renders element with the param injected via context.
import { createContext, useContext } from 'react';

const ParamContext = createContext<Record<string, string>>({});

export function Route({
  path,
  element,
}: {
  path: string;
  element: ReactNode;
}) {
  const current = window.location.pathname;
  const params = matchPath(path, current);
  if (params === null) return null;
  return <ParamContext.Provider value={params}>{element}</ParamContext.Provider>;
}

export function useParams(): Record<string, string> {
  return useContext(ParamContext);
}

function matchPath(pattern: string, actual: string): Record<string, string> | null {
  if (pattern === '*') return {};
  const pat = pattern.split('/').filter(Boolean);
  const act = actual.split('/').filter(Boolean);
  if (pat.length !== act.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < pat.length; i++) {
    if (pat[i].startsWith(':')) {
      params[pat[i].slice(1)] = decodeURIComponent(act[i]);
    } else if (pat[i] !== act[i]) {
      return null;
    }
  }
  return params;
}
