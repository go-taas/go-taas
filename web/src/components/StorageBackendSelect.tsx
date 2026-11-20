// Storage backend picker shared by the model download, upload and register
// flows. Every one of them writes weights somewhere, so the operator names
// the backend explicitly instead of silently inheriting the platform
// default. The default backend sorts first, matching the server's fallback.

import { useEffect, useMemo, useState } from 'react';
import { api, type StorageBackend } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { FieldLabel } from './FormField';

interface BackendsResponse {
  backends: StorageBackend[];
}

// useStorageBackends loads the configured backends and sorts the default
// first.
export function useStorageBackends(): StorageBackend[] {
  const { orgId } = useOrg();
  const [backends, setBackends] = useState<StorageBackend[]>([]);

  useEffect(() => {
    let cancelled = false;
    void api
      .get<BackendsResponse>('/api/v1/admin/storage-backends', orgId)
      .then((data) => {
        if (cancelled) return;
        const sorted = [...(data.backends || [])].sort((a, b) => {
          if (a.isDefault === b.isDefault) return a.name.localeCompare(b.name);
          return a.isDefault ? -1 : 1;
        });
        setBackends(sorted);
      })
      .catch(() => {
        // The form still works: an empty list means the platform default,
        // which is what an installation with one backend has anyway.
        if (!cancelled) setBackends([]);
      });
    return () => {
      cancelled = true;
    };
  }, [orgId]);

  return backends;
}

export function StorageBackendSelect({
  id,
  value,
  onChange,
  testId = 'storage-backend',
  disabled = false,
}: {
  id?: string;
  value: string;
  onChange: (value: string) => void;
  testId?: string;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const backends = useStorageBackends();

  // Default to the first (the platform default) backend once the list is
  // known, so the form never submits an empty choice by accident.
  useEffect(() => {
    if (!value && backends.length > 0) onChange(backends[0].name);
  }, [backends, value, onChange]);

  const selected = useMemo(
    () => backends.find((backend) => backend.name === value),
    [backends, value],
  );

  return (
    <div className="form-field">
      <FieldLabel
        htmlFor={id}
        text={t('common.storageBackend')}
        requirement="required"
        flag={selected?.isDefault ? t('storageBackends.default') : undefined}
      />
      <select
        id={id}
        data-testid={testId}
        value={value}
        disabled={disabled || backends.length === 0}
        onChange={(e) => onChange(e.target.value)}
      >
        {backends.length === 0 && <option value="">{t('common.storageBackendDefaultOnly')}</option>}
        {backends.map((backend) => (
          <option key={backend.name} value={backend.name}>
            {backend.name}
            {backend.isDefault ? ` (${t('storageBackends.default')})` : ''}
            {` · ${t(`storageBackends.type.${backend.type}`)}`}
          </option>
        ))}
      </select>
      <div className="field-hint">{t('common.storageBackendHint')}</div>
    </div>
  );
}
