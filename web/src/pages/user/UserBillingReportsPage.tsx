// End-user Billing Reports page (feature-25): tenant-scoped report
// builder, report history with CSV download, and scheduled reports with
// a run history. End-user surface: route /billing/reports, API prefix
// /api/v1/billing/reports/*. No organization dropdown (the caller's org
// is implicit, AD6). Implements docs/design/billing-reports.md §5.2 and
// docs/architecture/billing-reports.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import {
  formatTime,
  type CreateReportResponse,
  type CreateScheduleResponse,
  type DownloadReportResponse,
  type GetReportResponse,
  type ListReportsResponse,
  type ListScheduleRunsResponse,
  type ListSchedulesResponse,
  type Report,
  type ReportSchedule,
} from '../../api';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, StateBadge, usePolling } from '../../components';

const PAGE_SIZE = 20;
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'reports.range24h', hours: 24 },
  { id: '7d', labelKey: 'reports.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'reports.range30d', hours: 30 * 24 },
  { id: '90d', labelKey: 'reports.range90d', hours: 90 * 24 },
  { id: 'custom', labelKey: 'reports.rangeCustom', hours: 0 },
];

const DIMENSIONS = [
  { id: 'organization', labelKey: 'reports.dimOrganization' },
  { id: 'api_key', labelKey: 'reports.dimApiKey' },
  { id: 'model', labelKey: 'reports.dimModel' },
];

const GRANULARITIES = [
  { id: 'daily', labelKey: 'reports.granDaily' },
  { id: 'hourly', labelKey: 'reports.granHourly' },
];

const FREQUENCIES = [
  { id: 'daily', labelKey: 'reports.freqDaily' },
  { id: 'weekly', labelKey: 'reports.freqWeekly' },
  { id: 'monthly', labelKey: 'reports.freqMonthly' },
];

const RELATIVE_RANGES = [
  { id: 'last_7_days', labelKey: 'reports.relLast7Days' },
  { id: 'last_30_days', labelKey: 'reports.relLast30Days' },
  { id: 'last_month', labelKey: 'reports.relLastMonth' },
];

const TIMEZONES = ['UTC', 'Asia/Shanghai', 'America/New_York', 'Europe/London', 'Asia/Tokyo'];

function rangeFor(preset: string, customSince: string, customUntil: string): { since: number; until: number } {
  const now = Math.floor(Date.now() / 1000);
  if (preset === 'custom') {
    const until = customUntil ? Math.floor(new Date(customUntil).getTime() / 1000) : now;
    const since = customSince
      ? Math.floor(new Date(customSince).getTime() / 1000)
      : until - 24 * 3600;
    return { since, until };
  }
  const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
  return { since: now - hours * 3600, until: now };
}

function downloadCSV(csv: string, filename: string) {
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

function dimensionLabel(d: string, t: (k: string) => string): string {
  switch (d) {
    case 'organization':
      return t('reports.dimOrganization');
    case 'api_key':
      return t('reports.dimApiKey');
    case 'model':
      return t('reports.dimModel');
    default:
      return d;
  }
}

function rangeLabel(r: Report): string {
  const since = parseInt(r.since || '0', 10);
  const until = parseInt(r.until || '0', 10);
  if (!since || !until) return '—';
  return `${new Date(since * 1000).toLocaleDateString()} – ${new Date(until * 1000).toLocaleDateString()}`;
}

export default function UserBillingReportsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [tab, setTab] = useState<'reports' | 'schedules'>('reports');

  const [reports, setReports] = useState<Report[]>([]);
  const [reportsTotal, setReportsTotal] = useState(0);
  const [reportsOffset, setReportsOffset] = useState(0);
  const [reportsLoading, setReportsLoading] = useState(true);
  const [reportsError, setReportsError] = useState('');
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);

  const [builderOpen, setBuilderOpen] = useState(true);
  const [reportName, setReportName] = useState('');
  const [reportDimension, setReportDimension] = useState('model');
  const [reportPreset, setReportPreset] = useState('30d');
  const [reportCustomSince, setReportCustomSince] = useState('');
  const [reportCustomUntil, setReportCustomUntil] = useState('');
  const [reportGranularity, setReportGranularity] = useState('daily');
  const [reportTimezone, setReportTimezone] = useState('UTC');
  const [generating, setGenerating] = useState(false);
  const [generatingId, setGeneratingId] = useState<string | null>(null);

  const [schedules, setSchedules] = useState<ReportSchedule[]>([]);
  const [schedulesTotal, setSchedulesTotal] = useState(0);
  const [schedulesOffset, setSchedulesOffset] = useState(0);
  const [schedulesLoading, setSchedulesLoading] = useState(true);
  const [schedulesError, setSchedulesError] = useState('');

  const [scheduleName, setScheduleName] = useState('');
  const [scheduleDimension, setScheduleDimension] = useState('model');
  const [scheduleRelativeRange, setScheduleRelativeRange] = useState('last_7_days');
  const [scheduleGranularity, setScheduleGranularity] = useState('daily');
  const [scheduleFrequency, setScheduleFrequency] = useState('weekly');
  const [scheduleTimezone, setScheduleTimezone] = useState('UTC');
  const [creatingSchedule, setCreatingSchedule] = useState(false);

  const [runsSchedule, setRunsSchedule] = useState<ReportSchedule | null>(null);
  const [runs, setRuns] = useState<Report[]>([]);
  const [runsLoading, setRunsLoading] = useState(false);
  const [runsError, setRunsError] = useState('');

  const [deleteTarget, setDeleteTarget] = useState<ReportSchedule | null>(null);
  const [deleting, setDeleting] = useState(false);

  const loadReports = useCallback(
    async (offset: number) => {
      setReportsLoading(true);
      setReportsError('');
      try {
        const params = new URLSearchParams({ 'page.offset': String(offset), 'page.limit': String(PAGE_SIZE) });
        const data = await api.get<ListReportsResponse>(`/api/v1/billing/reports?${params}`, orgId);
        setReports(data.reports || []);
        setReportsTotal(parseInt(data.pageMeta?.total || '0', 10));
        setLastUpdated(Date.now());
      } catch (e) {
        setReportsError(e instanceof Error ? e.message : t('reports.loadFailed'));
      } finally {
        setReportsLoading(false);
      }
    },
    [orgId, t],
  );

  const loadSchedules = useCallback(
    async (offset: number) => {
      setSchedulesLoading(true);
      setSchedulesError('');
      try {
        const params = new URLSearchParams({ 'page.offset': String(offset), 'page.limit': String(PAGE_SIZE) });
        const data = await api.get<ListSchedulesResponse>(`/api/v1/billing/reports/schedules?${params}`, orgId);
        setSchedules(data.schedules || []);
        setSchedulesTotal(parseInt(data.pageMeta?.total || '0', 10));
      } catch (e) {
        setSchedulesError(e instanceof Error ? e.message : t('reports.schedulesLoadFailed'));
      } finally {
        setSchedulesLoading(false);
      }
    },
    [orgId, t],
  );

  useEffect(() => {
    void loadReports(0);
    void loadSchedules(0);
  }, [loadReports, loadSchedules]);

  const pollGenerating = useCallback(() => {
    if (!generatingId) return;
    api
      .get<GetReportResponse>(`/api/v1/billing/reports/${generatingId}`, orgId)
      .then((data) => {
        const status = data.report?.status;
        if (status === 'ready' || status === 'failed') {
          setGeneratingId(null);
          setGenerating(false);
          void loadReports(reportsOffset);
        }
      })
      .catch(() => {
        // Keep polling; a transient failure is not terminal.
      });
  }, [generatingId, orgId, loadReports, reportsOffset]);

  usePolling(pollGenerating, 3000, generatingId !== null);

  const generate = async () => {
    if (!reportName.trim()) {
      setReportsError(t('reports.nameRequired'));
      return;
    }
    const range = rangeFor(reportPreset, reportCustomSince, reportCustomUntil);
    if (range.since > range.until) {
      setReportsError(t('reports.rangeInvalid'));
      return;
    }
    setGenerating(true);
    setReportsError('');
    try {
      const body: Record<string, unknown> = {
        name: reportName.trim(),
        dimension: `REPORT_DIMENSION_${reportDimension.toUpperCase()}`,
        since: range.since,
        until: range.until,
        granularity: `REPORT_GRANULARITY_${reportGranularity.toUpperCase()}`,
        timezone: reportTimezone,
      };
      const data = await api.post<CreateReportResponse>('/api/v1/billing/reports', orgId, body);
      setGeneratingId(data.report.reportId);
      setReportName('');
      setBuilderOpen(false);
      void loadReports(0);
    } catch (e) {
      setReportsError(e instanceof Error ? e.message : t('reports.generateFailed'));
      setGenerating(false);
    }
  };

  const download = async (reportId: string) => {
    try {
      const data = await api.get<DownloadReportResponse>(
        `/api/v1/billing/reports/${reportId}/download`,
        orgId,
      );
      downloadCSV(data.csv, data.filename || `billing-report-${reportId}.csv`);
    } catch (e) {
      setReportsError(e instanceof Error ? e.message : t('reports.downloadFailed'));
    }
  };

  const createSchedule = async () => {
    if (!scheduleName.trim()) {
      setSchedulesError(t('reports.nameRequired'));
      return;
    }
    setCreatingSchedule(true);
    setSchedulesError('');
    try {
      const body: Record<string, unknown> = {
        name: scheduleName.trim(),
        dimension: `REPORT_DIMENSION_${scheduleDimension.toUpperCase()}`,
        relativeRange: `RELATIVE_RANGE_${scheduleRelativeRange.toUpperCase()}`,
        granularity: `REPORT_GRANULARITY_${scheduleGranularity.toUpperCase()}`,
        frequency: `REPORT_FREQUENCY_${scheduleFrequency.toUpperCase()}`,
        timezone: scheduleTimezone,
      };
      await api.post<CreateScheduleResponse>('/api/v1/billing/reports/schedules', orgId, body);
      setScheduleName('');
      void loadSchedules(0);
    } catch (e) {
      setSchedulesError(e instanceof Error ? e.message : t('reports.scheduleCreateFailed'));
    } finally {
      setCreatingSchedule(false);
    }
  };

  const openRuns = async (schedule: ReportSchedule) => {
    setRunsSchedule(schedule);
    setRuns([]);
    setRunsError('');
    setRunsLoading(true);
    try {
      const data = await api.get<ListScheduleRunsResponse>(
        `/api/v1/billing/reports/schedules/${schedule.scheduleId}/runs?page.limit=100`,
        orgId,
      );
      setRuns(data.runs || []);
    } catch (e) {
      setRunsError(e instanceof Error ? e.message : t('reports.runsLoadFailed'));
    } finally {
      setRunsLoading(false);
    }
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    setSchedulesError('');
    try {
      await api.del(`/api/v1/billing/reports/schedules/${deleteTarget.scheduleId}`, orgId);
      setDeleteTarget(null);
      void loadSchedules(0);
    } catch (e) {
      setSchedulesError(e instanceof Error ? e.message : t('reports.scheduleDeleteFailed'));
    } finally {
      setDeleting(false);
    }
  };

  const editSchedule = (schedule: ReportSchedule) => {
    setScheduleName(schedule.name);
    setScheduleDimension(schedule.dimension);
    setScheduleRelativeRange(schedule.relativeRange);
    setScheduleGranularity(schedule.granularity);
    setScheduleFrequency(schedule.frequency);
    setScheduleTimezone(schedule.timezone);
    setTab('schedules');
  };

  return (
    <div className="page" data-testid="billing-reports-page">
      <div className="page-header">
        <div>
          <h1>{t('reports.title')}</h1>
          <div className="subtitle">{t('reports.userSubtitle')}</div>
        </div>
        <button className="primary" data-testid="new-report" onClick={() => setBuilderOpen(true)}>
          {t('reports.newReport')}
        </button>
      </div>

      <div className="segmented" data-testid="reports-tabs">
        <button
          className={tab === 'reports' ? 'active' : ''}
          data-testid="reports-tab"
          onClick={() => setTab('reports')}
        >
          {t('reports.tabReports')}
        </button>
        <button
          className={tab === 'schedules' ? 'active' : ''}
          data-testid="schedules-tab"
          onClick={() => setTab('schedules')}
        >
          {t('reports.tabSchedules')}
        </button>
      </div>

      {tab === 'reports' && (
        <>
          {builderOpen && (
            <div className="panel" data-testid="report-builder">
              <h3>{t('reports.builderTitle')}</h3>
              <div className="form-row">
                <label>{t('reports.fieldName')}</label>
                <input
                  data-testid="report-name"
                  value={reportName}
                  onChange={(e) => setReportName(e.target.value)}
                  placeholder={t('reports.fieldName')}
                />
              </div>
              <div className="form-row">
                <label>{t('reports.fieldDimension')}</label>
                <div>
                  {DIMENSIONS.map((d) => (
                    <label key={d.id} className="radio">
                      <input
                        type="radio"
                        name="report-dimension"
                        data-testid={`report-dimension-${d.id}`}
                        checked={reportDimension === d.id}
                        onChange={() => setReportDimension(d.id)}
                      />
                      {t(d.labelKey)}
                    </label>
                  ))}
                </div>
              </div>
              <div className="form-row">
                <label>{t('reports.fieldRange')}</label>
                <div>
                  {RANGE_PRESETS.map((p) => (
                    <label key={p.id} className="radio">
                      <input
                        type="radio"
                        name="report-range"
                        data-testid={`report-range-${p.id}`}
                        checked={reportPreset === p.id}
                        onChange={() => setReportPreset(p.id)}
                      />
                      {t(p.labelKey)}
                    </label>
                  ))}
                </div>
                {reportPreset === 'custom' && (
                  <div>
                    <input
                      type="datetime-local"
                      data-testid="report-custom-since"
                      value={reportCustomSince}
                      onChange={(e) => setReportCustomSince(e.target.value)}
                    />
                    <input
                      type="datetime-local"
                      data-testid="report-custom-until"
                      value={reportCustomUntil}
                      onChange={(e) => setReportCustomUntil(e.target.value)}
                    />
                  </div>
                )}
              </div>
              <div className="form-row">
                <label>{t('reports.fieldGranularity')}</label>
                <div>
                  {GRANULARITIES.map((g) => (
                    <label key={g.id} className="radio">
                      <input
                        type="radio"
                        name="report-granularity"
                        data-testid={`report-granularity-${g.id}`}
                        checked={reportGranularity === g.id}
                        onChange={() => setReportGranularity(g.id)}
                      />
                      {t(g.labelKey)}
                    </label>
                  ))}
                </div>
              </div>
              <div className="form-row">
                <label>{t('reports.fieldTimezone')}</label>
                <select
                  data-testid="report-timezone"
                  value={reportTimezone}
                  onChange={(e) => setReportTimezone(e.target.value)}
                >
                  {TIMEZONES.map((tz) => (
                    <option key={tz} value={tz}>
                      {tz}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <button
                  className="primary"
                  data-testid="generate-report"
                  disabled={generating}
                  onClick={() => void generate()}
                >
                  {generating ? t('reports.generating') : t('reports.generate')}
                </button>
                <button className="secondary" data-testid="report-builder-cancel" onClick={() => setBuilderOpen(false)}>
                  {t('reports.cancel')}
                </button>
              </div>
            </div>
          )}

          {reportsError && <ErrorBanner message={reportsError} />}

          <div className="page-header" style={{ marginTop: 16 }}>
            <div>
              <h2>{t('reports.tabReports')}</h2>
              {lastUpdated && <div className="muted">{t('common.lastUpdated', { time: new Date(lastUpdated).toLocaleTimeString() })}</div>}
            </div>
            <button className="secondary" data-testid="reports-refresh" onClick={() => void loadReports(reportsOffset)}>
              {t('reports.refresh')}
            </button>
          </div>

          {reportsLoading ? (
            <div className="loading" data-testid="reports-loading">{t('common.loading')}</div>
          ) : reports.length === 0 ? (
            <div className="empty" data-testid="reports-empty">
              <div>{t('reports.empty')}</div>
              <div className="muted">{t('reports.emptyHint')}</div>
            </div>
          ) : (
            <>
              <table className="data" data-testid="reports-table">
                <thead>
                  <tr>
                    <th>{t('reports.colName')}</th>
                    <th>{t('reports.colDimension')}</th>
                    <th>{t('reports.colRange')}</th>
                    <th>{t('reports.colGranularity')}</th>
                    <th>{t('reports.colStatus')}</th>
                    <th>{t('reports.colRows')}</th>
                    <th>{t('reports.colCreated')}</th>
                    <th>{t('reports.colActions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {reports.map((r) => (
                    <tr key={r.reportId} data-testid={`report-row-${r.reportId}`}>
                      <td>{r.name}</td>
                      <td>{dimensionLabel(r.dimension, t)}</td>
                      <td>{rangeLabel(r)}</td>
                      <td>{r.granularity}</td>
                      <td data-testid={`report-status-${r.reportId}`}>
                        <StateBadge state={r.status} />
                        {generatingId === r.reportId && (
                          <span className="muted" data-testid={`report-generating-${r.reportId}`}>
                            {' '}
                            {t('reports.generating')}
                          </span>
                        )}
                      </td>
                      <td data-testid={`report-rows-${r.reportId}`}>{r.rowCount}</td>
                      <td>{formatTime(r.createdAt)}</td>
                      <td>
                        <button
                          className="link"
                          data-testid={`report-download-${r.reportId}`}
                          disabled={r.status !== 'ready'}
                          onClick={() => void download(r.reportId)}
                        >
                          {t('reports.download')}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <Pagination
                offset={reportsOffset}
                limit={PAGE_SIZE}
                total={reportsTotal}
                onPageChange={(o) => {
                  setReportsOffset(o);
                  void loadReports(o);
                }}
              />
            </>
          )}
        </>
      )}

      {tab === 'schedules' && (
        <>
          <div className="panel" data-testid="schedule-builder">
            <h3>{t('reports.scheduleBuilderTitle')}</h3>
            <div className="form-row">
              <label>{t('reports.fieldName')}</label>
              <input
                data-testid="schedule-name"
                value={scheduleName}
                onChange={(e) => setScheduleName(e.target.value)}
                placeholder={t('reports.fieldName')}
              />
            </div>
            <div className="form-row">
              <label>{t('reports.fieldDimension')}</label>
              <div>
                {DIMENSIONS.map((d) => (
                  <label key={d.id} className="radio">
                    <input
                      type="radio"
                      name="schedule-dimension"
                      data-testid={`schedule-dimension-${d.id}`}
                      checked={scheduleDimension === d.id}
                      onChange={() => setScheduleDimension(d.id)}
                    />
                    {t(d.labelKey)}
                  </label>
                ))}
              </div>
            </div>
            <div className="form-row">
              <label>{t('reports.fieldRelativeRange')}</label>
              <select
                data-testid="schedule-relative-range"
                value={scheduleRelativeRange}
                onChange={(e) => setScheduleRelativeRange(e.target.value)}
              >
                {RELATIVE_RANGES.map((rr) => (
                  <option key={rr.id} value={rr.id}>
                    {t(rr.labelKey)}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-row">
              <label>{t('reports.fieldGranularity')}</label>
              <div>
                {GRANULARITIES.map((g) => (
                  <label key={g.id} className="radio">
                    <input
                      type="radio"
                      name="schedule-granularity"
                      data-testid={`schedule-granularity-${g.id}`}
                      checked={scheduleGranularity === g.id}
                      onChange={() => setScheduleGranularity(g.id)}
                    />
                    {t(g.labelKey)}
                  </label>
                ))}
              </div>
            </div>
            <div className="form-row">
              <label>{t('reports.fieldFrequency')}</label>
              <div>
                {FREQUENCIES.map((f) => (
                  <label key={f.id} className="radio">
                    <input
                      type="radio"
                      name="schedule-frequency"
                      data-testid={`schedule-frequency-${f.id}`}
                      checked={scheduleFrequency === f.id}
                      onChange={() => setScheduleFrequency(f.id)}
                    />
                    {t(f.labelKey)}
                  </label>
                ))}
              </div>
            </div>
            <div className="form-row">
              <label>{t('reports.fieldTimezone')}</label>
              <select
                data-testid="schedule-timezone"
                value={scheduleTimezone}
                onChange={(e) => setScheduleTimezone(e.target.value)}
              >
                {TIMEZONES.map((tz) => (
                  <option key={tz} value={tz}>
                    {tz}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <button
                className="primary"
                data-testid="create-schedule"
                disabled={creatingSchedule}
                onClick={() => void createSchedule()}
              >
                {creatingSchedule ? t('reports.creatingSchedule') : t('reports.createSchedule')}
              </button>
            </div>
          </div>

          {schedulesError && <ErrorBanner message={schedulesError} />}

          {schedulesLoading ? (
            <div className="loading" data-testid="schedules-loading">{t('common.loading')}</div>
          ) : schedules.length === 0 ? (
            <div className="empty" data-testid="schedules-empty">
              <div>{t('reports.noSchedules')}</div>
              <div className="muted">{t('reports.noSchedulesHint')}</div>
            </div>
          ) : (
            <>
              <table className="data" data-testid="schedules-table">
                <thead>
                  <tr>
                    <th>{t('reports.scheduleColName')}</th>
                    <th>{t('reports.scheduleColDimension')}</th>
                    <th>{t('reports.scheduleColRelativeRange')}</th>
                    <th>{t('reports.scheduleColFrequency')}</th>
                    <th>{t('reports.scheduleColStatus')}</th>
                    <th>{t('reports.scheduleColLastRun')}</th>
                    <th className="col-actions">{t('reports.scheduleColActions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {schedules.map((sc) => (
                    <tr key={sc.scheduleId} data-testid={`schedule-row-${sc.scheduleId}`}>
                      <td>{sc.name}</td>
                      <td>{dimensionLabel(sc.dimension, t)}</td>
                      <td>{t(`reports.rel${sc.relativeRange.replace(/_/g, '')}`)}</td>
                      <td>{sc.frequency}</td>
                      <td>
                        <StateBadge state={sc.status} />
                      </td>
                      <td>{formatTime(sc.lastRunAt)}</td>
                      <td>
                        <RowActionsMenu
                          label={t('common.actionsFor', { name: sc.name || sc.scheduleId })}
                          testId={`schedule-actions-${sc.scheduleId}`}
                          actions={[
                            {
                              key: `schedule-runs-${sc.scheduleId}`,
                              label: t('reports.viewRuns'),
                              onClick: () => void openRuns(sc),
                            },
                            {
                              key: `schedule-edit-${sc.scheduleId}`,
                              label: t('reports.edit'),
                              onClick: () => editSchedule(sc),
                            },
                            {
                              key: `schedule-delete-${sc.scheduleId}`,
                              label: t('reports.delete'),
                              danger: true,
                              onClick: () => setDeleteTarget(sc),
                            },
                          ]}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <Pagination
                offset={schedulesOffset}
                limit={PAGE_SIZE}
                total={schedulesTotal}
                onPageChange={(o) => {
                  setSchedulesOffset(o);
                  void loadSchedules(o);
                }}
              />
            </>
          )}
        </>
      )}

      {deleteTarget && (
        <Dialog title={t('reports.deleteTitle')} onClose={() => setDeleteTarget(null)} testId="delete-schedule-dialog">
          <p dangerouslySetInnerHTML={{ __html: t('reports.deleteBody', { name: deleteTarget.name }) }} />
          <div>
            <button className="secondary" onClick={() => setDeleteTarget(null)}>
              {t('common.cancel')}
            </button>
            <button className="primary danger" data-testid="confirm-delete-schedule" disabled={deleting} onClick={() => void confirmDelete()}>
              {deleting ? t('common.deleting') : t('common.delete')}
            </button>
          </div>
        </Dialog>
      )}

      {runsSchedule && (
        <Dialog title={t('reports.runsTitle')} onClose={() => setRunsSchedule(null)} testId="schedule-runs-drawer">
          <p className="muted">{runsSchedule.name}</p>
          {runsError && <ErrorBanner message={runsError} />}
          {runsLoading ? (
            <div className="loading">{t('common.loading')}</div>
          ) : runs.length === 0 ? (
            <div className="empty" data-testid="runs-empty">{t('reports.runsEmpty')}</div>
          ) : (
            <table className="data" data-testid="schedule-runs-table">
              <thead>
                <tr>
                  <th>{t('reports.colCreated')}</th>
                  <th>{t('reports.colStatus')}</th>
                  <th>{t('reports.colRows')}</th>
                  <th>{t('reports.colActions')}</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => (
                  <tr key={r.reportId} data-testid={`run-row-${r.reportId}`}>
                    <td>{formatTime(r.createdAt)}</td>
                    <td>
                      <StateBadge state={r.status} />
                    </td>
                    <td>{r.rowCount}</td>
                    <td>
                      <button
                        className="link"
                        data-testid={`run-download-${r.reportId}`}
                        disabled={r.status !== 'ready'}
                        onClick={() => void download(r.reportId)}
                      >
                        {t('reports.download')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Dialog>
      )}
    </div>
  );
}