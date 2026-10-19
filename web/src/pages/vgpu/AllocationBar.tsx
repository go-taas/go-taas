// AllocationBar: the pool utilization bar (feature #52, FR3.2). Amber
// at >= 80%, red at 100%.

export default function AllocationBar({ pct }: { pct: number }) {
  const clamped = Math.max(0, Math.min(100, pct));
  const cls = clamped >= 100 ? 'badge failed' : clamped >= 80 ? 'badge steady' : 'badge running';
  return (
    <div
      className="allocation-bar"
      data-testid="vgpu-pool-utilization-bar"
      style={{ display: 'flex', alignItems: 'center', gap: 8 }}
    >
      <div
        style={{
          width: 80,
          height: 8,
          borderRadius: 4,
          background: '#e5e7eb',
          overflow: 'hidden',
        }}
      >
        <div
          style={{
            width: `${clamped}%`,
            height: '100%',
            background: clamped >= 100 ? '#dc2626' : clamped >= 80 ? '#d97706' : '#16a34a',
          }}
        />
      </div>
      <span className={cls}>{clamped}%</span>
    </div>
  );
}