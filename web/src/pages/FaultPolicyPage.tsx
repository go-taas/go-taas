// FaultPolicyPage: the admin auto-recovery policy page (feature #53,
// design §5.3). Per-category toggles for auto-restart and
// auto-reschedule, plus the always-on drain-confirm flag.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { type AutoRecoveryPolicy, type GetAutoRecoveryPolicyResponse } from '../api';

const ROOT_CAUSES = [
  'service_crash_loop',
  'node_not_ready',
  'accelerator_device_plugin_failure',
  'oom',
  'image_pull_failure',
];

export default function FaultPolicyPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [policy, setPolicy] = useState<AutoRecoveryPolicy | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);

  const load = useCallback(async () => {
    setError('');
    try {
      const res = await api.get<GetAutoRecoveryPolicyResponse>('/api/v1/admin/faults/policy', orgId);
      setPolicy(res.policy);
    } catch (e: any) {
      if (e?.code === 10036) setPermissionDenied(true);
      else setError(e?.message || t('fault.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const update = async (patch: Partial<AutoRecoveryPolicy>) => {
    setError('');
    try {
      const res = await api.patch<GetAutoRecoveryPolicyResponse>('/api/v1/admin/faults/policy', orgId, patch);
      setPolicy(res.policy);
    } catch (e: any) {
      setError(e?.message || t('fault.updateFailed'));
    }
  };

  if (permissionDenied) {
    return (
      <div>
        <div className="page-header">
          <h1>{t('fault.policyTitle')}</h1>
        </div>
        <div className="empty-state" data-testid="fault-permission-denied">
          {t('fault.permissionDenied')}
        </div>
      </div>
    );
  }
  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (!policy) return null;

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('fault.policyTitle')}</h1>
          <div className="subtitle">{t('fault.policySubtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('fault.autoRestart')}</h3>
        {ROOT_CAUSES.map((cause) => (
          <div key={cause} className="policy-row" data-testid={`fault-policy-restart-${cause}`} style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8 }}>
            <span style={{ flex: 1 }}>{cause}</span>
            <input
              type="checkbox"
              data-testid={`fault-policy-restart-toggle-${cause}`}
              checked={!!policy.autoRestart[cause]}
              onChange={(e) =>
                update({ autoRestart: { ...policy.autoRestart, [cause]: e.target.checked } })
              }
            />
          </div>
        ))}
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('fault.autoReschedule')}</h3>
        {ROOT_CAUSES.map((cause) => (
          <div key={cause} className="policy-row" data-testid={`fault-policy-reschedule-${cause}`} style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 8 }}>
            <span style={{ flex: 1 }}>{cause}</span>
            <input
              type="checkbox"
              data-testid={`fault-policy-reschedule-toggle-${cause}`}
              checked={!!policy.autoReschedule[cause]}
              onChange={(e) =>
                update({ autoReschedule: { ...policy.autoReschedule, [cause]: e.target.checked } })
              }
            />
          </div>
        ))}
      </div>

      <div className="panel">
        <div className="policy-row" data-testid="fault-policy-drain-confirm" style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ flex: 1 }}>{t('fault.drainAlwaysConfirm')}</span>
          <input type="checkbox" checked={policy.drainAlwaysConfirm} disabled />
        </div>
      </div>
    </div>
  );
}