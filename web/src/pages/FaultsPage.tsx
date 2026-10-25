// FaultsPage: the admin incident list (feature #53, design §5.1). The
// incident table with severity, status, root cause, target,
// auto-recovered, started/resolved; a filter bar (status, severity,
// root-cause, target-type, search, pagination).

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner, Pagination, StateBadge } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import { formatTime, type Incident, type ListIncidentsResponse } from '../api';
import SeverityBadge from './faults/SeverityBadge';

const PAGE_SIZE = 20;
const STATUSES = ['detected', 'diagnosing', 'recovering', 'resolved', 'failed'];
const SEVERITIES = ['critical', 'warning', 'info'];
const ROOT_CAUSES = [
  'service_crash_loop',
  'node_not_ready',
  'accelerator_device_plugin',
  'oom',
  'image_pull_failure',
  'unknown',
];
const TARGET_TYPES = ['service', 'node', 'accelerator'];

export default function FaultsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [status, setStatus] = useState('');
  const [severity, setSeverity] = useState('');
  const [rootCause, setRootCause] = useState('');
  const [targetType, setTargetType] = useState('');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      if (status) params.set('status', status);
      if (severity) params.set('severity', severity);
      if (rootCause) params.set('root_cause', rootCause);
      if (targetType) params.set('target_type', targetType);
      if (search) params.set('search', search);
      const data = await api.get<ListIncidentsResponse>(
        `/api/v1/admin/faults?${params.toString()}`,
        orgId,
      );
      setIncidents(data.incidents || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e: any) {
      if (e?.code === 10036) setPermissionDenied(true);
      else setError(e?.message || t('fault.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, status, severity, rootCause, targetType, search, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (permissionDenied) {
    return (
      <div>
        <div className="page-header">
          <h1>{t('fault.title')}</h1>
        </div>
        <div className="empty-state" data-testid="fault-permission-denied">
          {t('fault.permissionDenied')}
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('fault.title')}</h1>
          <div className="subtitle">{t('fault.subtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="filter-bar">
        <input
          data-testid="fault-search"
          placeholder={t('fault.search')}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setOffset(0);
          }}
        />
        <select data-testid="fault-filter-status" value={status} onChange={(e) => { setStatus(e.target.value); setOffset(0); }}>
          <option value="">{t('fault.allStatuses')}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>{tEnum('state', s)}</option>
          ))}
        </select>
        <select data-testid="fault-filter-severity" value={severity} onChange={(e) => { setSeverity(e.target.value); setOffset(0); }}>
          <option value="">{t('fault.allSeverities')}</option>
          {SEVERITIES.map((s) => (
            <option key={s} value={s}>{tEnum('state', s)}</option>
          ))}
        </select>
        <select data-testid="fault-filter-root-cause" value={rootCause} onChange={(e) => { setRootCause(e.target.value); setOffset(0); }}>
          <option value="">{t('fault.allRootCauses')}</option>
          {ROOT_CAUSES.map((r) => (
            <option key={r} value={r}>{tEnum('state', r)}</option>
          ))}
        </select>
        <select data-testid="fault-filter-target-type" value={targetType} onChange={(e) => { setTargetType(e.target.value); setOffset(0); }}>
          <option value="">{t('fault.allTargetTypes')}</option>
          {TARGET_TYPES.map((tt) => (
            <option key={tt} value={tt}>{tEnum('state', tt)}</option>
          ))}
        </select>
      </div>

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : incidents.length === 0 ? (
        <div className="empty-state" data-testid="fault-empty">
          {t('fault.empty')}
        </div>
      ) : (
        <table className="data-table" data-testid="fault-table">
          <thead>
            <tr>
              <th>{t('fault.severity')}</th>
              <th>{t('fault.status')}</th>
              <th>{t('fault.rootCause')}</th>
              <th>{t('fault.target')}</th>
              <th>{t('fault.autoRecovered')}</th>
              <th>{t('fault.started')}</th>
              <th>{t('fault.resolved')}</th>
            </tr>
          </thead>
          <tbody>
            {incidents.map((inc) => (
              <tr
                key={inc.incidentId}
                data-testid={`fault-row-${inc.incidentId}`}
                onClick={() => navigate(`/admin/faults/${inc.incidentId}`)}
                style={{ cursor: 'pointer' }}
              >
                <td><SeverityBadge severity={inc.severity} /></td>
                <td><StateBadge state={inc.status} /></td>
                <td><span className="badge">{inc.rootCause}</span></td>
                <td>{inc.targetType}: {inc.targetName}</td>
                <td>{inc.autoRecovered ? '✓' : '—'}</td>
                <td>{formatTime(inc.startedAt)}</td>
                <td>{inc.resolvedAt ? formatTime(inc.resolvedAt) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Pagination total={total} offset={offset} limit={PAGE_SIZE} onPageChange={setOffset} />
    </div>
  );
}