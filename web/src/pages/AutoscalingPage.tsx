// Admin Autoscaling page: the global default autoscaling policy editor.
// Implements docs/design/inference-autoscaling.md FR1.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  ApiError,
  type AutoscalingPolicy,
  type GetAutoscalingPolicyResponse,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { FieldLabel, FormLegend } from '../components/FormField';
import { ErrorBanner } from '../components';

const DEFAULTS: AutoscalingPolicy = {
  enabled: true,
  minReplicas: 1,
  maxReplicas: 10,
  targetConcurrency: 32,
  scaleToZero: false,
  cooldownSeconds: 300,
};

export default function AutoscalingPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [policy, setPolicy] = useState<AutoscalingPolicy>(DEFAULTS);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [toast, setToast] = useState('');

  const load = useCallback(async () => {
    setError('');
    try {
      const data = await api.get<GetAutoscalingPolicyResponse>(
        '/api/v1/admin/autoscaling/policy',
        orgId,
      );
      if (data.policy) setPolicy(data.policy);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('autoscaling.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const validate = (p: AutoscalingPolicy, t: (key: string, vars?: Record<string, string | number>) => string): Record<string, string> => {
    const errs: Record<string, string> = {};
    if (p.minReplicas > p.maxReplicas) {
      errs.maxReplicas = t('autoscaling.validationMax');
    }
    if (p.minReplicas === 0 && !p.scaleToZero) {
      errs.minReplicas = t('autoscaling.validationMinZero');
    }
    if (p.scaleToZero && p.minReplicas !== 0) {
      errs.scaleToZero = t('autoscaling.validationSetMinZero');
    }
    if (p.targetConcurrency < 1 || p.targetConcurrency > 1000) {
      errs.targetConcurrency = t('autoscaling.validationConcurrency');
    }
    if (p.cooldownSeconds < 0 || p.cooldownSeconds > 3600) {
      errs.cooldownSeconds = t('autoscaling.validationCooldown');
    }
    if (p.maxReplicas < 1 || p.maxReplicas > 100) {
      errs.maxReplicas = t('autoscaling.validationMax');
    }
    return errs;
  };

  const save = async () => {
    const errs = validate(policy, t);
    setFieldErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSaving(true);
    setError('');
    setToast('');
    try {
      const data = await api.put<GetAutoscalingPolicyResponse>(
        '/api/v1/admin/autoscaling/policy',
        orgId,
        { policy },
      );
      if (data.policy) setPolicy(data.policy);
      setToast(t('autoscaling.saved'));
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('autoscaling.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    setPolicy(DEFAULTS);
    setFieldErrors({});
  };

  const set = (patch: Partial<AutoscalingPolicy>) => {
    setPolicy((p) => ({ ...p, ...patch }));
    setFieldErrors({});
  };

  if (loading) return <div className="loading">{t('common.loading')}</div>;

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('autoscaling.title')}</h1>
          <div className="subtitle">{t('autoscaling.subtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {toast && (
        <div className="success-banner" data-testid="autoscaling-saved-toast">
          {toast}
        </div>
      )}

      <div className="panel" style={{ maxWidth: 560 }}>
        <FormLegend testId="autoscaling-legend" />
        <div className="form-grid">
          <div className="form-field">
            <FieldLabel
              htmlFor="as-enabled"
              text={t('autoscaling.enabled')}
              requirement="defaulted"
              flag={t('form.reqDefaultValue', {
                value: policy.enabled ? t('common.enabled') : t('common.disabled'),
              })}
            />
            <input
              id="as-enabled"
              data-testid="as-enabled"
              type="checkbox"
              checked={policy.enabled}
              onChange={(e) => set({ enabled: e.target.checked })}
            />
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="as-min"
              text={t('autoscaling.minReplicas')}
              requirement="defaulted"
              flag={t('form.reqDefaultValue', { value: String(policy.minReplicas) })}
            />
            <input
              id="as-min"
              data-testid="as-min"
              type="number"
              min={0}
              max={100}
              disabled={!policy.enabled}
              value={policy.minReplicas}
              onChange={(e) => set({ minReplicas: parseInt(e.target.value, 10) || 0 })}
            />
            {fieldErrors.minReplicas && (
              <div className="field-error" data-testid="as-min-error">{fieldErrors.minReplicas}</div>
            )}
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="as-max"
              text={t('autoscaling.maxReplicas')}
              requirement="defaulted"
              flag={t('form.reqDefaultValue', { value: String(policy.maxReplicas) })}
            />
            <input
              id="as-max"
              data-testid="as-max"
              type="number"
              min={1}
              max={100}
              disabled={!policy.enabled}
              value={policy.maxReplicas}
              onChange={(e) => set({ maxReplicas: parseInt(e.target.value, 10) || 0 })}
            />
            {fieldErrors.maxReplicas && (
              <div className="field-error" data-testid="as-max-error">{fieldErrors.maxReplicas}</div>
            )}
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="as-target"
              text={t('autoscaling.targetConcurrency')}
              requirement="defaulted"
              flag={t('form.reqDefaultValue', { value: String(policy.targetConcurrency) })}
            />
            <input
              id="as-target"
              data-testid="as-target"
              type="number"
              min={1}
              max={1000}
              disabled={!policy.enabled}
              value={policy.targetConcurrency}
              onChange={(e) => set({ targetConcurrency: parseInt(e.target.value, 10) || 0 })}
            />
            {fieldErrors.targetConcurrency && (
              <div className="field-error" data-testid="as-target-error">{fieldErrors.targetConcurrency}</div>
            )}
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="as-scale-to-zero"
              text={t('autoscaling.scaleToZero')}
              requirement="optional"
            />
            <input
              id="as-scale-to-zero"
              data-testid="as-scale-to-zero"
              type="checkbox"
              disabled={!policy.enabled || policy.minReplicas !== 0}
              checked={policy.scaleToZero}
              onChange={(e) => set({ scaleToZero: e.target.checked })}
            />
            {fieldErrors.scaleToZero && (
              <div className="field-error" data-testid="as-scale-to-zero-error">{fieldErrors.scaleToZero}</div>
            )}
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="as-cooldown"
              text={t('autoscaling.cooldown')}
              requirement="defaulted"
              flag={t('form.reqDefaultValue', { value: String(policy.cooldownSeconds) })}
            />
            <input
              id="as-cooldown"
              data-testid="as-cooldown"
              type="number"
              min={0}
              max={3600}
              disabled={!policy.enabled}
              value={policy.cooldownSeconds}
              onChange={(e) => set({ cooldownSeconds: parseInt(e.target.value, 10) || 0 })}
            />
            {fieldErrors.cooldownSeconds && (
              <div className="field-error" data-testid="as-cooldown-error">{fieldErrors.cooldownSeconds}</div>
            )}
          </div>
        </div>
        <div className="dialog-actions">
          <button className="secondary" data-testid="as-reset" onClick={reset}>
            {t('autoscaling.reset')}
          </button>
          <button data-testid="as-save" disabled={saving} onClick={() => void save()}>
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </div>
    </div>
  );
}
