// LanguageSwitcher renders a globe icon in the shell header that opens a
// dropdown listing the supported languages (Simplified Chinese and
// English). Selecting one switches the console language.

import { useEffect, useRef, useState } from 'react';
import { CaretDown, Globe } from '@phosphor-icons/react';
import { useI18n, type Lang } from '../i18n';

// LANGUAGES is the ordered list of languages the dropdown offers. The
// label is the language's own name, so it reads correctly in either
// locale.
const LANGUAGES: { code: Lang; label: string }[] = [
  { code: 'zh', label: '简体中文' },
  { code: 'en', label: 'English' },
];

export default function LanguageSwitcher({ testId }: { testId?: string }) {
  const { lang, setLang } = useI18n();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close the dropdown on an outside click or Escape.
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

  return (
    <div className="lang-switcher" ref={ref} data-testid={testId ?? 'lang-switcher'}>
      <button
        className="lang-switch"
        data-testid="lang-switch"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label="Language"
        onClick={() => setOpen((v) => !v)}
      >
        <Globe size={20} weight="duotone" aria-hidden="true" />
        <CaretDown size={12} weight="bold" aria-hidden="true" />
      </button>
      {open && (
        <ul className="lang-menu" role="listbox" data-testid="lang-menu">
          {LANGUAGES.map((l) => (
            <li key={l.code} role="option" aria-selected={l.code === lang}>
              <button
                className={l.code === lang ? 'lang-menu-item active' : 'lang-menu-item'}
                data-testid={`lang-option-${l.code}`}
                onClick={() => {
                  setLang(l.code);
                  setOpen(false);
                }}
              >
                {l.label}
                {l.code === lang && <span className="lang-menu-check">✓</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}