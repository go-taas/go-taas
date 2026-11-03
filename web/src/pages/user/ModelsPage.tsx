// End-user Models page: the masked user-realm model catalog (feature #16,
// FR4.1) presented as a rate card — every inference service the caller can
// use, with its model id, what a million input / output / cache-hit tokens
// cost, and whether it is self-hosted or provider-backed.
//
// The pricing columns are the reason the page exists: a caller picking a model
// needs to know what a call costs before sending it, and the catalog already
// carries the current rate (the billing module resolves it, the model module
// projects it), so the page makes one request.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
import { formatRate } from '../../display';
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
      <div className="subtitle">{t('umodels.pricingNote')}</div>
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
              <th>{t('umodels.colModelId')}</th>
              <th>{t('umodels.colInput')}</th>
              <th>{t('umodels.colOutput')}</th>
              <th>{t('umodels.colCached')}</th>
              <th>{t('umodels.colKind')}</th>
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
                  {m.virtual && (
                    <span className="badge" data-testid="models-auto-badge">
                      {t('umodels.auto')}
                    </span>
                  )}
                </td>
                <td className="mono muted" data-testid={`model-id-${m.name}`}>{m.modelId}</td>
                <td data-testid={`model-price-input-${m.name}`}>
                  {priceCell(m, 'input')}
                </td>
                <td data-testid={`model-price-output-${m.name}`}>
                  {priceCell(m, 'output')}
                </td>
                <td data-testid={`model-price-cached-${m.name}`}>
                  {priceCell(m, 'cached')}
                </td>
                <td>
                  {m.external ? (
                    <span className="badge external" data-testid={`model-kind-${m.name}`}>
                      {t('umodels.kindExternal', { provider: m.providerName || '—' })}
                    </span>
                  ) : m.virtual ? (
                    <span className="badge" data-testid={`model-kind-${m.name}`}>
                      {t('umodels.kindVirtual')}
                    </span>
                  ) : (
                    <span className="badge fixed" data-testid={`model-kind-${m.name}`}>
                      {t('umodels.kindSelfHosted')}
                    </span>
                  )}
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

/** priceCell renders one rate column, or a dash when the model is unpriced. */
function priceCell(model: AvailableModel, kind: 'input' | 'output' | 'cached'): string {
  const pricing = model.pricing;
  if (!pricing) return '—';
  const rate =
    kind === 'input'
      ? pricing.inputPricePerMillion
      : kind === 'output'
        ? pricing.outputPricePerMillion
        : pricing.cachedPricePerMillion;
  // A cache-hit rate of zero means "no discount", not "free": showing a
  // currency amount there would read as a price column of 0.00.
  if (kind === 'cached' && rate === 0) return '—';
  return formatRate(rate, pricing.currency);
}
