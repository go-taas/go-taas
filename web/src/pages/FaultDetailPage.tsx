// FaultDetailPage: the admin incident detail (feature #53, design
// §5.2). The incident header, the diagnosis timeline, the root-cause
// evidence, and the recovery-actions card with override/manual controls.

import { useCallback, useEffect, useState } from 'react';
import { BackLink, Dialog, ErrorBanner, StateBadge } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatTime, type GetIncidentResponse } from '../api';
import SeverityBadge from './faults/SeverityBadge';
import IncidentTimeline from './faults/IncidentTimeline';

export default function FaultDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<GetIncidentResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [showDrain, setShowDrain] = useState(false);
  const [actionError, setActionError] = useState('');

  const load = useCallback(async () => {
    setError('');
    try {
      const res = await api.get<GetIncidentResponse>(`/api/v1/admin/faults/${id}`, orgId);
      setData(res);
    } catch (e: any) {
      if (e?.body?.code === 10036) setPermissionDenied(true);
      else setError(e?.message || t('fault.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, id, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const trigger = async (action: string, confirm = false) => {
    setActionError('');
    try {
      await api.post<{ response: { code: number; message: string } }>(
        `/api/v1/admin/faults/${id}:recover`,
        orgId,
        { action, confirm },
      );
      setShowDrain(false);
      void load();
    } catch (e: any) {
      setActionError(e?.body?.message || e?.message || t('fault.actionFailed'));
    }
  };

  const override = async () => {
    setActionError('');
    try {
      await api.post<{ response: { code: number; message: string } }>(
        `/api/v1/admin/faults/${id}:override`,
        orgId,
        {},
      );
      void load();
    } catch (e: any) {
      setActionError(e?.body?.message || e?.message || t('fault.actionFailed'));
    }
  };

  if (permissionDenied) {
    return (
      <div>
        <BackLink to="/admin/faults" label={t('fault.back')} />
        <div className="empty-state" data-testid="fault-permission-denied">
          {t('fault.permissionDenied')}
        </div>
      </div>
    );
  }
  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (error)
    return (
      <div>
        <BackLink to="/admin/faults" label={t('fault.back')} />
        <ErrorBanner message={error} />
      </div>
    );
  if (!data) return null;

  const inc = data.incident;
  const resolved = inc.status === 'resolved';

  return (
    <div>
      <BackLink to="/admin/faults" label={t('fault.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="fault-detail-name">{inc.targetType}: {inc.targetName}</h1>
          <div className="subtitle mono">{inc.incidentId}</div>
        </div>
        <SeverityBadge severity={inc.severity} />
        <StateBadge state={inc.status} />
      </div>

      {resolved && (
        <div className="success-banner" data-testid="fault-resolved-banner">
          {t('fault.resolvedBanner')}
        </div>
      )}
      {actionError && <ErrorBanner message={actionError} />}

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('fault.overview')}</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">{t('fault.rootCause')}</div>
            <div className="value"><span className="badge">{inc.rootCause}</span></div>
          </div>
          <div className="detail-item">
            <div className="label">{t('fault.target')}</div>
            <div className="value">{inc.targetType}: {inc.targetName}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('fault.started')}</div>
            <div className="value">{formatTime(inc.startedAt)}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('fault.autoRecovered')}</div>
            <div className="value">{inc.autoRecovered ? '✓' : '—'}</div>
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('fault.timeline')}</h3>
        <IncidentTimeline steps={data.steps} />
      </div>

      {!resolved && (
        <div className="panel" style={{ marginBottom: 16 }}>
          <h3 style={{ marginTop: 0 }}>{t('fault.recoveryActions')}</h3>
          <div className="dialog-actions">
            <button className="secondary" data-testid="fault-action-restart" onClick={() => trigger('restart')}>
              {t('fault.restart')}
            </button>
            <button className="secondary" data-testid="fault-action-reschedule" onClick={() => trigger('reschedule')}>
              {t('fault.reschedule')}
            </button>
            <button className="danger" data-testid="fault-action-drain" onClick={() => setShowDrain(true)}>
              {t('fault.drain')}
            </button>
            <button className="secondary" data-testid="fault-action-override" onClick={override}>
              {t('fault.override')}
            </button>
          </div>
        </div>
      )}

      {showDrain && (
        <Dialog title={t('fault.drain')} onClose={() => setShowDrain(false)} testId="fault-drain-dialog">
          <p>{t('fault.drainConfirm')}</p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setShowDrain(false)}>
              {t('common.cancel')}
            </button>
            <button className="danger" data-testid="fault-drain-confirm" onClick={() => trigger('drain', true)}>
              {t('fault.drain')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}