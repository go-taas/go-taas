// Shared presentational components used across console pages.

import { Component, useEffect, useRef, useState, type ErrorInfo, type ReactNode } from 'react';
import { Check, Copy, DotsThreeVertical } from '@phosphor-icons/react';
import { navigate } from './router';
import { useI18n } from './i18n';

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
  const { tEnum } = useI18n();
  return (
    <span className={`badge ${state}`} data-testid={`state-badge-${state}`}>
      {tEnum('state', state)}
    </span>
  );
}

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useI18n();
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
      {copied ? t('common.copied') : (label ?? t('common.copy'))}
    </button>
  );
}

/**
 * InlineCopyButton is the icon-sized sibling of CopyButton: a small copy
 * affordance for a table cell, where a labelled button would dominate the
 * column. It shows a check for a moment after copying and carries the label as
 * both its tooltip and its accessible name.
 */
export function InlineCopyButton({
  text,
  label,
  testId,
}: {
  text: string;
  label: string;
  testId?: string;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  return (
    <button
      type="button"
      className="icon-button"
      data-testid={testId ?? 'inline-copy'}
      title={copied ? t('common.copied') : label}
      aria-label={copied ? t('common.copied') : label}
      onClick={async (e) => {
        // The row may itself be clickable (a model opens its detail page);
        // copying must not navigate.
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // Clipboard API may be unavailable (insecure context); the text
          // stays selectable, so the operation still has a manual fallback.
        }
        setCopied(true);
        if (timer.current) clearTimeout(timer.current);
        timer.current = setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? <Check size={14} weight="bold" aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />}
    </button>
  );
}

/** RowAction is one entry of a RowActionsMenu. */
export interface RowAction {
  /** key is the stable React key and the data-testid suffix. */
  key: string;
  label: string;
  onClick: () => void;
  /** danger marks a destructive action, rendered in the danger colour. */
  danger?: boolean;
  /**
   * group separates the menu into blocks. Actions in the same group are drawn
   * together, and a separator is inserted where the group changes — which is
   * what keeps "who is this account" actions apart from "what may it do".
   */
  group?: string;
  /**
   * disabled greys the item out. A menu entry that cannot run right now still
   * belongs in the list: hiding it makes the row look like the capability does
   * not exist, while a disabled entry plus `title` explains why.
   */
  disabled?: boolean;
  /** title is the tooltip, typically the reason a disabled item is disabled. */
  title?: string;
}

/**
 * RowActionsMenu collapses a row's actions into one overflow menu.
 *
 * A directory row carries several unrelated actions — place the account in an
 * organization, grant or revoke the administrator role, disable it, delete it —
 * and rendering them all as inline links makes the last column the widest thing
 * on the page and the destructive one indistinguishable from the rest. One
 * button per row, with the actions grouped and the destructive ones marked,
 * keeps the table scannable and puts a deliberate step in front of deletion.
 */
export function RowActionsMenu({
  actions,
  label,
  testId,
}: {
  actions: RowAction[];
  /** label is the trigger's accessible name ("Actions for alice"). */
  label: string;
  testId?: string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on an outside click or Escape, like the language switcher; the menu
  // therefore needs no "close" entry of its own.
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  if (actions.length === 0) return null;

  return (
    <div className="row-actions" ref={ref}>
      <button
        type="button"
        className="icon-button"
        data-testid={testId ?? 'row-actions'}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        title={label}
        onClick={() => setOpen((v) => !v)}
      >
        <DotsThreeVertical size={18} weight="bold" aria-hidden="true" />
      </button>
      {open && (
        <ul className="row-actions-menu" role="menu" data-testid={testId ? `${testId}-menu` : 'row-actions-menu'}>
          {actions.map((action, i) => {
            const previous = i > 0 ? actions[i - 1].group : undefined;
            const separator = i > 0 && action.group !== previous;
            return (
              <li key={action.key} role="none" className={separator ? 'row-actions-separator' : undefined}>
                <button
                  type="button"
                  role="menuitem"
                  className={action.danger ? 'row-actions-item danger' : 'row-actions-item'}
                  data-testid={`${action.key}`}
                  disabled={action.disabled}
                  title={action.title}
                  onClick={() => {
                    setOpen(false);
                    action.onClick();
                  }}
                >
                  {action.label}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
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
  const { t } = useI18n();
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  return (
    <div className="pagination" data-testid="pagination">
      <span>{t('common.pageOf', { page, pages, total })}</span>
      <button
        className="secondary"
        disabled={offset <= 0}
        onClick={() => onPageChange(Math.max(0, offset - limit))}
        data-testid="prev-page"
      >
        {t('common.previous')}
      </button>
      <button
        className="secondary"
        disabled={offset + limit >= total}
        onClick={() => onPageChange(offset + limit)}
        data-testid="next-page"
      >
        {t('common.next')}
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

// ErrorFallback is the translated fallback of the page error boundary.
function ErrorFallback({
  title,
  message,
  onRetry,
}: {
  title?: string;
  message: string;
  onRetry: () => void;
}) {
  const { t } = useI18n();
  return (
    <div className="page-error" role="alert" data-testid="page-error">
      <h2>{title || t('common.errorTitle')}</h2>
      <p className="muted">{message}</p>
      <div className="header-actions">
        <button className="secondary" onClick={onRetry}>
          {t('common.tryAgain')}
        </button>
        <button className="primary" onClick={() => window.location.reload()}>
          {t('common.reload')}
        </button>
      </div>
    </div>
  );
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
        <ErrorFallback
          title={this.props.title}
          message={this.state.error.message}
          onRetry={() => this.setState({ error: null })}
        />
      );
    }
    return this.props.children;
  }
}
