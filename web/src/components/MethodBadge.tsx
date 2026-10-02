// MethodBadge renders the color-coded HTTP method badge for an API
// endpoint (feature #38, AD5).

const METHOD_COLORS: Record<string, string> = {
  GET: '#2E7D32',
  POST: '#1565C0',
  PUT: '#E65100',
  DELETE: '#C62828',
  PATCH: '#6A1B9A',
};

export default function MethodBadge({ method }: { method: string }) {
  const color = METHOD_COLORS[method.toUpperCase()] || '#455A64';
  return (
    <span
      className="method-badge"
      data-testid={`method-badge-${method.toLowerCase()}`}
      style={{ backgroundColor: color, color: '#fff', padding: '2px 8px', borderRadius: 4, fontSize: 12, fontWeight: 600 }}
    >
      {method.toUpperCase()}
    </span>
  );
}