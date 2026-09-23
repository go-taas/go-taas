// Shared presentational components used across console pages.

import { useEffect, useState, type ReactNode } from 'react';
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
