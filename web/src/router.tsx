// Minimal hash-free client router. The console is served as a static SPA
// by the gateway, which falls back to index.html for unknown paths, so
// history-based routing works without server-side route tables.

import {
  createContext,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from 'react';

type Listener = () => void;
const listeners = new Set<Listener>();

function emit() {
  listeners.forEach((l) => l());
}

export function navigate(path: string) {
  if (window.location.pathname === path) return;
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

const ParamContext = createContext<Record<string, string>>({});

// Routes renders the first matching child <Route>; a path="*" child acts
// as the fallback. Matching is reactive: the current location is read via
// useRoute(), so navigation re-renders the route table.
export function Routes({ children }: { children: ReactNode }) {
  const current = useRoute();
  const routes = Array.isArray(children) ? children : [children];
  let matched: { params: Record<string, string>; element: ReactNode } | null =
    null;
  for (const route of routes) {
    if (!route) continue;
    const props = route.props as { path: string; element: ReactNode };
    const params = matchPath(props.path, current);
    if (params !== null) {
      matched = { params, element: props.element };
      break;
    }
  }
  if (!matched) return null;
  return (
    <ParamContext.Provider value={matched.params}>
      {matched.element}
    </ParamContext.Provider>
  );
}

// Route is a declarative marker consumed by <Routes>; it renders nothing
// on its own so sibling routes never double-render.
export function Route({
  element,
}: {
  path: string;
  element: ReactNode;
}) {
  void element;
  return null;
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
