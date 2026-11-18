// ThemeToggle is the console's light/dark switch. The chosen theme is
// reflected as `data-theme` on <html> (the CSS skins read that attribute)
// and persisted so the next visit boots in the same theme. An inline
// script in index.html applies the stored choice before the bundle loads,
// which avoids a flash of the default theme.

import { useEffect, useState } from 'react';
import { Moon, Sun } from '@phosphor-icons/react';
import { useI18n } from '../i18n';

export type Theme = 'dark' | 'light';

const STORAGE_KEY = 'go-taas.theme';

function currentTheme(): Theme {
  if (typeof document === 'undefined') return 'dark';
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
}

function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
  try {
    localStorage.setItem(STORAGE_KEY, theme);
  } catch {
    // Storage can be unavailable in a locked-down browser; the current
    // page still follows the toggle.
  }
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', theme === 'light' ? '#f5f7fa' : '#0b0b0e');
}

export default function ThemeToggle({
  testId,
  className,
}: {
  testId?: string;
  className?: string;
}) {
  const { t } = useI18n();
  const [theme, setTheme] = useState<Theme>(currentTheme);
  const next: Theme = theme === 'dark' ? 'light' : 'dark';
  const label = next === 'light' ? t('theme.switchToLight') : t('theme.switchToDark');

  // Keep the document and the persisted preference in sync with the
  // button. The inline boot script already applied the stored value, so
  // this only runs on an explicit toggle.
  useEffect(() => {
    applyTheme(theme);
  }, [theme]);

  return (
    <button
      type="button"
      className={className ? `theme-toggle ${className}` : 'theme-toggle'}
      data-testid={testId ?? 'theme-toggle'}
      aria-label={label}
      aria-pressed={theme === 'light'}
      title={label}
      onClick={() => setTheme(next)}
    >
      {theme === 'dark' ? (
        <Sun size={18} weight="regular" aria-hidden="true" />
      ) : (
        <Moon size={18} weight="regular" aria-hidden="true" />
      )}
    </button>
  );
}
