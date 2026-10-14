// End-user Models page: the masked user-realm model catalog with a
// read-only autoscaling indicator (feature #16, FR4.1).

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
import { type AvailableModel, type PageMeta } from '../../api';

interface ListResponse {
  response: { code: number; message: string };
  models: AvailableModel[];
  pageMeta?: PageMeta;
}

export default function ModelsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<ListResponse>('/api/v1/models?page.limit=100', orgId)
      .then((data) => setModels(data.models || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('umodels.loadFailed')))
      .finally(() => setLoading(false));
  }, [api, orgId, t]);

  return (
    <div className="page">
      <h1>{t('umodels.title')}</h1>
      {error && <div className="error">{error}</div>}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : models.length === 0 ? (
        <div className="empty" data-testid="models-empty">{t('umodels.empty')}</div>
      ) : (
        <table className="data" data-testid="models-table">
          <thead>
            <tr>
              <th>{t('umodels.colModel')}</th>
              <th>{t('umodels.colVersion')}</th>
              <th>{t('umodels.colAutoscaling')}</th>
            </tr>
          </thead>
          <tbody>
            {models.map((m) => (
              <tr
                key={m.modelId}
                data-testid={`model-row-${m.name}`}
                style={{ cursor: 'pointer' }}
                onClick={() => navigate(`/models/${m.modelId}`)}
              >
                <td>
                  <strong>{m.name}</strong>
                  {m.external && (
                    <span className="badge external" data-testid="models-external-badge">
                      External
                    </span>
                  )}
                  {m.virtual && (
                    <span className="badge" data-testid="models-auto-badge">
                      {t('umodels.auto')}
                    </span>
                  )}
                </td>
                <td className="muted">
                  {m.external || m.virtual ? '—' : m.latestVersion}
                </td>
                <td>
                  <AutoscalingIndicator autoscaling={m.autoscaling} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function AutoscalingIndicator({ autoscaling }: { autoscaling?: AvailableModel['autoscaling'] }) {
  const { t } = useI18n();
  if (!autoscaling) {
    return <span className="badge fixed" data-testid="autoscaling-fixed">{t('umodels.fixed')}</span>;
  }
  if (!autoscaling.autoscaled) {
    return <span className="badge fixed" data-testid="autoscaling-fixed">{t('umodels.fixed')}</span>;
  }
  if (autoscaling.state === 'scaled-to-zero') {
    return <span className="badge scaled-to-zero" data-testid="autoscaling-scaled-to-zero">{t('umodels.scaledToZero')}</span>;
  }
  if (autoscaling.state === 'warming-up') {
    return <span className="badge cold-starting" data-testid="autoscaling-warming-up">{t('umodels.warmingUp')}</span>;
  }
  return (
    <span className="badge autoscaled" data-testid="autoscaling-autoscaled">
      {t('umodels.autoscaled', { n: autoscaling.currentReplicas })}
    </span>
  );
}
