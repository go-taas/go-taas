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
      setError(e instanceof Error ? e.message : 'failed to load policy');
    } finally {
      setLoading(false);
    }
  }, [orgId]);

  useEffect(() => {
    void load();
  }, [load]);

  const validate = (p: AutoscalingPolicy): Record<string, string> => {
    const errs: Record<string, string> = {};
    if (p.minReplicas > p.maxReplicas) {
      errs.maxReplicas = 'Max replicas must be ≥ min replicas and ≤ 100.';
    }
    if (p.minReplicas === 0 && !p.scaleToZero) {
      errs.minReplicas = 'Min replicas must be 0 to enable scale-to-zero.';
    }
    if (p.scaleToZero && p.minReplicas !== 0) {
      errs.scaleToZero = 'Set min replicas to 0 to enable scale-to-zero.';
    }
    if (p.targetConcurrency < 1 || p.targetConcurrency > 1000) {
      errs.targetConcurrency = 'Target concurrency must be between 1 and 1000.';
    }
    if (p.cooldownSeconds < 0 || p.cooldownSeconds > 3600) {
      errs.cooldownSeconds = 'Cooldown must be between 0 and 3600 seconds.';
    }
    if (p.maxReplicas < 1 || p.maxReplicas > 100) {
      errs.maxReplicas = 'Max replicas must be ≥ min replicas and ≤ 100.';
    }
    return errs;
  };

  const save = async () => {
    const errs = validate(policy);
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
      setToast('Default policy saved.');
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to save policy');
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

  if (loading) return <div className="loading">Loading…</div>;

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Default autoscaling policy</h1>
          <div className="subtitle">
            Applied to new inference services. Existing services keep their own
            policy unless you edit them.
          </div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {toast && (
        <div className="success-banner" data-testid="autoscaling-saved-toast">
          {toast}
        </div>
      )}

      <div className="panel" style={{ maxWidth: 560 }}>
        <div className="form-grid">
          <div className="form-field">
            <label htmlFor="as-enabled">Enabled</label>
            <input
              id="as-enabled"
              data-testid="as-enabled"
              type="checkbox"
              checked={policy.enabled}
              onChange={(e) => set({ enabled: e.target.checked })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="as-min">Min replicas</label>
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
            <label htmlFor="as-max">Max replicas</label>
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
            <label htmlFor="as-target">Target concurrency</label>
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
            <label htmlFor="as-scale-to-zero">Scale to zero</label>
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
            <label htmlFor="as-cooldown">Cooldown seconds</label>
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
            Reset to defaults
          </button>
          <button data-testid="as-save" disabled={saving} onClick={() => void save()}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  );
}