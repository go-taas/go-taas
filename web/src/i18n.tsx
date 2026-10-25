// Lightweight i18n for the console (no external dependency). Provides a
// language context, a t() translation function, and a language switch.
// Translations live in ./locales/{en,zh}.ts.

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { en } from './locales/en';
import { zh } from './locales/zh';

export type Lang = 'en' | 'zh';

const STORAGE_KEY = 'go-taas.lang';

const dictionaries: Record<Lang, Record<string, string>> = { en, zh };

export function getStoredLang(): Lang {
  const stored = localStorage.getItem(STORAGE_KEY);
  if (stored === 'zh' || stored === 'en') return stored;
  // Default to the browser language when it is Chinese, else English.
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

export function setStoredLang(lang: Lang): void {
  localStorage.setItem(STORAGE_KEY, lang);
}

interface I18nContextValue {
  lang: Lang;
  setLang: (lang: Lang) => void;
  t: (key: string, vars?: Record<string, string | number>) => string;
  // tOr translates a key and returns the fallback when the key is not
  // translated, so enum values coming from the API stay readable.
  tOr: (key: string, fallback: string) => string;
  // tEnum translates a namespaced enum value (e.g. tEnum('state',
  // 'running') reads "state.running") and falls back to the raw value.
  tEnum: (namespace: string, value: string) => string;
}

const I18nContext = createContext<I18nContextValue>({
  lang: 'en',
  setLang: () => {},
  t: (key) => key,
  tOr: (_key, fallback) => fallback,
  tEnum: (_namespace, value) => value,
});

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(getStoredLang);

  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);

  const setLang = (next: Lang) => {
    setStoredLang(next);
    setLangState(next);
  };

  const t = (key: string, vars?: Record<string, string | number>): string => {
    let text = dictionaries[lang][key] ?? dictionaries.en[key] ?? key;
    if (vars) {
      for (const [k, v] of Object.entries(vars)) {
        text = text.replace(new RegExp(`\\{${k}\\}`, 'g'), String(v));
      }
    }
    return text;
  };

  const tOr = (key: string, fallback: string): string => {
    const text = dictionaries[lang][key] ?? dictionaries.en[key];
    return text ?? fallback;
  };

  const tEnum = (namespace: string, value: string): string => {
    if (!value) return '';
    const direct = dictionaries[lang][`${namespace}.${value}`] ?? dictionaries.en[`${namespace}.${value}`];
    if (direct) return direct;
    // Proto enum names arrive as CONSTANT_CASE (AUDIT_RESULT_SUCCESS,
    // AUTOSCALING_STATE_SCALING_UP). Try progressively shorter suffixes so
    // state.success and state.scaling-up resolve without per-page mapping.
    if (/^[A-Z][A-Z0-9_]*$/.test(value) && value.includes('_')) {
      const parts = value.toLowerCase().split('_');
      for (let i = 0; i < parts.length; i += 1) {
        const key = `${namespace}.${parts.slice(i).join('-')}`;
        const hit = dictionaries[lang][key] ?? dictionaries.en[key];
        if (hit) return hit;
      }
    }
    return value;
  };

  return (
    <I18nContext.Provider value={{ lang, setLang, t, tOr, tEnum }}>
      {children}
    </I18nContext.Provider>
  );
}

export function useI18n(): I18nContextValue {
  return useContext(I18nContext);
}