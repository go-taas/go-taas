// Shared presentational components used across console pages.

import { Component, useEffect, useState, type ErrorInfo, type ReactNode } from 'react';
import { navigate } from './router';

export function Dialog({
  title,
  onClose,
  children,
  testId,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <div
      className="dialog-backdrop"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="dialog" data-testid={testId}>
        <h2>{title}</h2>
        {children}
      </div>
    </div>
  );
}

// Avatar renders the user's real avatar image, falling back to the
// username's initial when no avatar is set. It is used anywhere the
// console shows who the caller is.
export function Avatar({
  avatar,
  name,
  size = 32,
  testId,
}: {
  avatar?: string;
  name?: string;
  size?: number;
  testId?: string;
}) {
  const initial = (name || '?').trim().charAt(0).toUpperCase() || '?';
  const style = { width: size, height: size };
  if (avatar) {
    return (
      <img
        src={avatar}
        alt={name || 'avatar'}
        className="avatar"
        data-testid={testId}
        style={{ ...style, borderRadius: '50%', objectFit: 'cover' }}
      />
    );
  }
  return (
    <span
      className="avatar avatar-initial"
      data-testid={testId}
      aria-hidden="true"
      style={{ ...style, borderRadius: '50%' }}
    >
      {initial}
    </span>
  );
}

export function StateBadge({ state }: { state: string }) {
  return (
    <span className={`badge ${state}`} data-testid={`state-badge-${state}`}>
      {state}
    </span>
  );
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      className="secondary"
      data-testid="copy-button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // Clipboard API may be unavailable (insecure context); the text
          // remains selectable as a fallback.
        }
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? 'Copied!' : label}
    </button>
  );
}

export function ErrorBanner({ message }: { message: string }) {
  return (
    <div className="error-banner" data-testid="error-banner" role="alert">
      {message}
    </div>
  );
}

export function Pagination({
  offset,
  limit,
  total,
  onPageChange,
}: {
  offset: number;
  limit: number;
  total: number;
  onPageChange: (offset: number) => void;
}) {
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  return (
    <div className="pagination" data-testid="pagination">
      <span>
        Page {page} of {pages} · {total} total
      </span>
      <button
        className="secondary"
        disabled={offset <= 0}
        onClick={() => onPageChange(Math.max(0, offset - limit))}
        data-testid="prev-page"
      >
        Previous
      </button>
      <button
        className="secondary"
        disabled={offset + limit >= total}
        onClick={() => onPageChange(offset + limit)}
        data-testid="next-page"
      >
        Next
      </button>
    </div>
  );
}

export function BackLink({ to, label }: { to: string; label: string }) {
  return (
    <a
      href={to}
      className="back-link"
      onClick={(e) => {
        e.preventDefault();
        navigate(to);
      }}
    >
      ← {label}
    </a>
  );
}

// usePolling re-runs an async callback on an interval until the component
// unmounts or stop() is called. Used by the service detail page to follow
// pending → deploying → running transitions (FR3.3).
export function usePolling(callback: () => void, intervalMs: number, active: boolean) {
  useEffect(() => {
    if (!active) return;
    const id = setInterval(callback, intervalMs);
    return () => clearInterval(id);
  }, [callback, intervalMs, active]);
}

// ErrorBoundary keeps a single failing page from blanking the whole
// console: React unmounts the entire root when a render throws with no
// boundary above it, which used to leave an empty page. The fallback keeps
// the shell usable (navigation still works) and offers a reload.
export class ErrorBoundary extends Component<
  { children: ReactNode; title?: string },
  { error: Error | null }
> {
  state: { error: Error | null } = { error: null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Surface the component stack for debugging without crashing the app.
    console.error('console page error:', error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <div className="page-error" role="alert" data-testid="page-error">
          <h2>{this.props.title || 'Something went wrong'}</h2>
          <p className="muted">{this.state.error.message}</p>
          <div className="header-actions">
            <button className="secondary" onClick={() => this.setState({ error: null })}>
              Try again
            </button>
            <button className="primary" onClick={() => window.location.reload()}>
              Reload
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
