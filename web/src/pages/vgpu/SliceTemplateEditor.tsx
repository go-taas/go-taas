// SliceTemplateEditor: the reusable slice-template editor (feature #52,
// FR2.2). It manages a list of template rows (name, memory GiB, compute
// %, description) with inline validation, used by both the create and
// edit dialogs.

import type { SliceTemplate } from '../../api';
import { useI18n } from '../../i18n';

export interface TemplateRow {
  name: string;
  memoryGib: string;
  computePct: string;
  description: string;
}

export function rowsToTemplates(rows: TemplateRow[]): SliceTemplate[] {
  return rows
    .filter((r) => r.name.trim() !== '')
    .map((r) => ({
      name: r.name.trim(),
      memoryGib: String(parseInt(r.memoryGib, 10) || 0),
      computePct: String(parseInt(r.computePct, 10) || 0),
      description: r.description.trim(),
    }));
}

export function templatesToRows(templates: SliceTemplate[]): TemplateRow[] {
  return (templates || []).map((t) => ({
    name: t.name,
    memoryGib: String(parseInt(t.memoryGib, 10) || 0),
    computePct: String(parseInt(t.computePct, 10) || 0),
    description: t.description || '',
  }));
}

export default function SliceTemplateEditor({
  rows,
  onChange,
  errors,
}: {
  rows: TemplateRow[];
  onChange: (rows: TemplateRow[]) => void;
  errors?: Record<number, string>;
}) {
  const { t } = useI18n();
  const update = (index: number, patch: Partial<TemplateRow>) => {
    const next = rows.map((r, i) => (i === index ? { ...r, ...patch } : r));
    onChange(next);
  };

  return (
    <div className="form-field full">
      <label>{t('vgpu.templates')}</label>
      {rows.map((row, i) => (
        <div
          key={i}
          className="template-row"
          data-testid={`vgpu-pool-template-row-${i}`}
          style={{ display: 'flex', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}
        >
          <input
            data-testid={`vgpu-pool-template-name-${i}`}
            placeholder={t('vgpu.templateName')}
            value={row.name}
            onChange={(e) => update(i, { name: e.target.value })}
            style={{ width: 120 }}
          />
          <input
            data-testid={`vgpu-pool-template-memory-${i}`}
            placeholder={t('vgpu.templateMemory')}
            type="number"
            min={1}
            value={row.memoryGib}
            onChange={(e) => update(i, { memoryGib: e.target.value })}
            style={{ width: 100 }}
          />
          <input
            data-testid={`vgpu-pool-template-compute-${i}`}
            placeholder={t('vgpu.templateCompute')}
            type="number"
            min={5}
            max={100}
            value={row.computePct}
            onChange={(e) => update(i, { computePct: e.target.value })}
            style={{ width: 100 }}
          />
          <input
            data-testid={`vgpu-pool-template-desc-${i}`}
            placeholder={t('vgpu.templateDesc')}
            value={row.description}
            onChange={(e) => update(i, { description: e.target.value })}
            style={{ flex: 1 }}
          />
          <button
            type="button"
            className="secondary"
            data-testid={`vgpu-pool-template-remove-${i}`}
            onClick={() => onChange(rows.filter((_, idx) => idx !== i))}
          >
            {t('common.remove')}
          </button>
          {errors?.[i] && (
            <div className="field-error" data-testid={`vgpu-pool-template-error-${i}`}>
              {errors[i]}
            </div>
          )}
        </div>
      ))}
      <button
        type="button"
        className="secondary"
        data-testid="vgpu-pool-template-add"
        onClick={() =>
          onChange([...rows, { name: '', memoryGib: '', computePct: '', description: '' }])
        }
      >
        {t('vgpu.templateAdd')}
      </button>
    </div>
  );
}