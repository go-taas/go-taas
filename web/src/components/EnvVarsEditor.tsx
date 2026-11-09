import { useI18n } from '../i18n';
import { FieldLabel } from './FormField';

// EnvRow is one environment variable as the editors hold it: an ordered row
// rather than a map, so renaming a variable does not collide with another row
// or reorder the list under the operator's cursor.
export interface EnvRow {
  name: string;
  value: string;
}

// envToRows turns a wire environment map into editor rows, sorted by name so
// the form is stable across reloads.
export function envToRows(env?: Record<string, string>): EnvRow[] {
  return Object.entries(env || {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => ({ name, value }));
}

// rowsToEnv turns editor rows into the wire map. A row without a name is
// dropped (an unfinished row is not a variable); a row with a name and an
// empty value is kept, because `FOO=` is a real setting.
export function rowsToEnv(rows: EnvRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (!name) continue;
    out[name] = row.value;
  }
  return out;
}

// EnvVarsEditor edits a set of environment variables as name/value rows.
//
// Environment variables are the settings an engine reads from its process
// environment rather than from its command line (VLLM_USE_V1, HF_HOME,
// NCCL_DEBUG, ...). They apply to every engine container of the service, for
// every serving topology; a per-role section can override one for a single
// role.
export function EnvVarsEditor({
  rows,
  onChange,
  testIdPrefix = 'env',
  hint,
}: {
  rows: EnvRow[];
  onChange: (next: EnvRow[]) => void;
  testIdPrefix?: string;
  hint?: string;
}) {
  const { t } = useI18n();

  const setRow = (index: number, field: 'name' | 'value', value: string) => {
    onChange(rows.map((row, i) => (i === index ? { ...row, [field]: value } : row)));
  };

  return (
    <div className="form-field full" data-testid={`${testIdPrefix}-vars`}>
      {/* Variables are always optional: an unset variable is simply not
          passed to the engine, which keeps its own default. */}
      <FieldLabel text={t('deploy.envVars')} requirement="optional" />
      <div className="field-hint">{hint ?? t('deploy.envVarsHint')}</div>
      {rows.map((row, i) => (
        <div key={i} className="toolbar" style={{ marginTop: 6 }}>
          <input
            className="mono"
            data-testid={`${testIdPrefix}-name-${i}`}
            placeholder="VLLM_USE_V1"
            value={row.name}
            onChange={(e) => setRow(i, 'name', e.target.value)}
          />
          <input
            className="mono"
            data-testid={`${testIdPrefix}-value-${i}`}
            placeholder="1"
            value={row.value}
            onChange={(e) => setRow(i, 'value', e.target.value)}
          />
          <button
            type="button"
            className="secondary"
            data-testid={`${testIdPrefix}-remove-${i}`}
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
          >
            {t('common.delete')}
          </button>
        </div>
      ))}
      <button
        type="button"
        className="secondary"
        data-testid={`${testIdPrefix}-add`}
        onClick={() => onChange([...rows, { name: '', value: '' }])}
      >
        {t('deploy.addEnvVar')}
      </button>
    </div>
  );
}
