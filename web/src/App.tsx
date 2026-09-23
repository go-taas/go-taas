import { useEffect, useState } from 'react';
import { Router, navigate, Route } from './router';
import { OrgProvider, OrgSwitcher } from './org';
import ApiKeysPage from './pages/ApiKeysPage';
import ModelsPage from './pages/ModelsPage';
import ModelDetailPage from './pages/ModelDetailPage';
import InferenceServicesPage from './pages/InferenceServicesPage';
import ServiceDetailPage from './pages/ServiceDetailPage';
import NotFoundPage from './pages/NotFoundPage';

export default function App() {
  return (
    <OrgProvider>
      <Router>
        <Layout>
          <Route path="/" element={<NavigateToModels />} />
          <Route path="/api-keys" element={<ApiKeysPage />} />
          <Route path="/models" element={<ModelsPage />} />
          <Route path="/models/:id" element={<ModelDetailPage />} />
          <Route path="/inference-services" element={<InferenceServicesPage />} />
          <Route path="/inference-services/:id" element={<ServiceDetailPage />} />
          <Route path="*" element={<NotFoundPage />} />
        </Layout>
      </Router>
    </OrgProvider>
  );
}

// The entry route redirects to the models catalog (the console's home).
function NavigateToModels() {
  useEffect(() => {
    navigate('/models');
  }, []);
  return null;
}

function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="app">
      <Sidebar />
      <main className="main">{children}</main>
    </div>
  );
}

const NAV_ITEMS = [
  { path: '/models', label: 'Models' },
  { path: '/inference-services', label: 'Inference Services' },
  { path: '/api-keys', label: 'API Keys' },
];

function Sidebar() {
  const [path, setPath] = useState(window.location.pathname);
  useEffect(() => {
    return Router.subscribe(() => setPath(window.location.pathname));
  }, []);
  return (
    <aside className="sidebar" data-testid="sidebar">
      <div className="brand">go-taas</div>
      <nav>
        {NAV_ITEMS.map((item) => (
          <a
            key={item.path}
            href={item.path}
            className={path.startsWith(item.path) ? 'nav-item active' : 'nav-item'}
            data-testid={`nav-${item.label.toLowerCase().replace(/\s+/g, '-')}`}
            onClick={(e) => {
              e.preventDefault();
              navigate(item.path);
            }}
          >
            {item.label}
          </a>
        ))}
      </nav>
      <OrgSwitcher />
    </aside>
  );
}
